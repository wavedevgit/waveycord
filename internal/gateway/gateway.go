// Package gateway implements the documented Gateway protocol over WebSocket:
// HELLO, IDENTIFY, READY, HEARTBEAT/ACK, RESUME, INVALID_SESSION and DISPATCH.
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/klauspost/compress/zstd"

	"github.com/wavedevgit/waveycord/internal/bus"
	"github.com/wavedevgit/waveycord/internal/store"
)

// Opcodes from the gateway opcode table.
const (
	OpDispatch            = 0
	OpHeartbeat           = 1
	OpIdentify            = 2
	OpPresenceUpdate      = 3
	OpVoiceStateUpdate    = 4
	OpResume              = 6
	OpReconnect           = 7
	OpRequestGuildMembers = 8
	OpInvalidSession      = 9
	OpHello               = 10
	OpHeartbeatACK        = 11
)

// Close codes from the gateway close event codes table.
const (
	CloseUnknownError         = 4000
	CloseUnknownOpcode        = 4001
	CloseDecodeError          = 4002
	CloseNotAuthenticated     = 4003
	CloseAuthenticationFailed = 4004
	CloseAlreadyAuthenticated = 4005
	CloseInvalidSeq           = 4007
	CloseRateLimited          = 4008
	CloseSessionTimeout       = 4009
	CloseInvalidShard         = 4010
	CloseShardingRequired     = 4011
	CloseInvalidAPIVersion    = 4012
	CloseInvalidIntents       = 4013
	CloseDisallowedIntents    = 4014
)

// HeartbeatInterval is what HELLO advertises, in milliseconds.
const HeartbeatInterval = 41250

// AuthFunc resolves a bearer token to a user id.
type AuthFunc func(token string) (userID string, ok bool)

// ReadyFunc builds the READY dispatch payload for an identified session.
type ReadyFunc func(userID, sessionID string) (any, error)

// GuildsFunc lists the guild ids a session subscribes to.
type GuildsFunc func(userID string) []string

// Hub fans dispatches out to every gateway connection.
type Hub struct {
	Auth   AuthFunc
	Ready  ReadyFunc
	Guilds GuildsFunc
	Log    *slog.Logger

	mu            sync.RWMutex
	conns         map[*conn]struct{}
	bus           bus.Bus
	dispatched    atomic.Int64
	journal       *deltaJournal
	store         *store.Store
	voiceEndpoint string
	passiveNanos  atomic.Int64
	wake          chan struct{}
	stopOnce      sync.Once
	stop          chan struct{}
	// sessions keeps at most one live connection per gateway session id, which
	// is what RESUME and session takeover depend on.
	sessions map[string]*conn
}

// NewHub constructs a hub.
func NewHub(auth AuthFunc, ready ReadyFunc, guilds GuildsFunc, log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	h := &Hub{Auth: auth, Ready: ready, Guilds: guilds, Log: log,
		conns: map[*conn]struct{}{}, sessions: map[string]*conn{},
		journal: newDeltaJournal(2048), wake: make(chan struct{}, 1), stop: make(chan struct{})}
	h.passiveNanos.Store(int64(10 * time.Second))
	go h.passiveLoop()
	return h
}

// SetStore gives the hub access to voice-state persistence and guild data. It is
// called once at startup, before connections are accepted.
func (h *Hub) SetStore(st *store.Store, voiceEndpoint string) {
	if h == nil {
		return
	}
	h.store = st
	h.voiceEndpoint = voiceEndpoint
}

// SetPassiveInterval controls how often passive sessions receive a delta. The
// loop is woken immediately so a test can shorten it before connecting.
func (h *Hub) SetPassiveInterval(d time.Duration) {
	if h == nil || d <= 0 {
		return
	}
	h.passiveNanos.Store(int64(d))
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// Close stops the passive update loop.
func (h *Hub) Close() {
	if h == nil {
		return
	}
	h.stopOnce.Do(func() { close(h.stop) })
}

func (h *Hub) passiveLoop() {
	for {
		every := time.Duration(h.passiveNanos.Load())
		if every < 10*time.Millisecond {
			every = 10 * time.Millisecond
		}
		timer := time.NewTimer(every)
		select {
		case <-h.stop:
			timer.Stop()
			return
		case <-h.wake:
			timer.Stop()
			continue
		case <-timer.C:
			h.flushPassive()
		}
	}
}

// flushPassive emits one PASSIVE_UPDATE_V2 per passive guild that changed since
// that connection's cursor. Unchanged guilds produce no dispatch.
func (h *Hub) flushPassive() {
	h.mu.RLock()
	conns := make([]*conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.RUnlock()
	for _, c := range conns {
		c.stateMu.Lock()
		guilds := make([]string, 0, len(c.passive))
		for gid := range c.passive {
			guilds = append(guilds, gid)
		}
		cursors := make(map[string]uint64, len(guilds))
		for _, gid := range guilds {
			cursors[gid] = c.cursors[gid]
		}
		c.stateMu.Unlock()

		for _, gid := range guilds {
			channels, members, voice, full, head := h.journal.since(gid, cursors[gid])
			if head == cursors[gid] && !full {
				continue
			}
			if len(channels) == 0 && len(members) == 0 && len(voice) == 0 && !full {
				c.setCursor(gid, head)
				continue
			}
			payload := map[string]any{
				"guild_id":     gid,
				"channels":     channels,
				"members":      members,
				"voice_states": voice,
			}
			if full {
				payload["full"] = true
			}
			c.dispatch("PASSIVE_UPDATE_V2", payload)
			c.setCursor(gid, head)
		}
	}
}

func (c *conn) setCursor(guildID string, seq uint64) {
	c.stateMu.Lock()
	c.cursors[guildID] = seq
	c.stateMu.Unlock()
}

// activateGuild moves a guild out of the passive set (OP 14 Guild Subscriptions).
func (c *conn) activateGuild(guildID string) {
	c.stateMu.Lock()
	delete(c.passive, guildID)
	c.cursors[guildID] = c.hub.journal.head(guildID)
	c.stateMu.Unlock()
}

// Handler upgrades an HTTP request to a gateway connection.
func (h *Hub) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h == nil || h.Auth == nil {
			http.Error(w, "gateway unavailable", http.StatusServiceUnavailable)
			return
		}
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true, // the server is not behind a TLS terminator in dev
			OriginPatterns:     []string{"*"},
		})
		if err != nil {
			return
		}
		h.serve(r.Context(), ws, compressionFromRequest(r))
	})
}

// compressionFromRequest reads the documented ?compress= transport setting.
// "zstd-stream" is supported; anything else (including zlib-stream) falls back
// to uncompressed JSON rather than failing the connection.
func compressionFromRequest(r *http.Request) string {
	if strings.EqualFold(r.URL.Query().Get("compress"), "zstd-stream") {
		return "zstd-stream"
	}
	return ""
}

func (h *Hub) serve(ctx context.Context, ws *websocket.Conn, compress string) {
	c := &conn{ws: ws, hub: h, out: make(chan []byte, 64), done: make(chan struct{})}
	if compress == "zstd-stream" {
		// One streaming encoder per connection: the window survives across
		// payloads, which is the point of the blog's streaming-zstd change.
		enc, err := zstd.NewWriter(frameSink{c: c},
			zstd.WithEncoderLevel(zstd.SpeedDefault),
			zstd.WithEncoderConcurrency(1),
			zstd.WithWindowSize(1<<18),
		)
		if err != nil {
			_ = ws.Close(websocket.StatusInternalError, "compression init failed")
			return
		}
		c.encoder = enc
	}
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.conns, c)
		if c.sessionID != "" && h.sessions[c.sessionID] == c {
			delete(h.sessions, c.sessionID)
		}
		h.mu.Unlock()
		close(c.done)
		if c.encoder != nil {
			_ = c.encoder.Close()
		}
		_ = ws.CloseNow()
	}()

	go c.writeLoop()
	c.send(map[string]any{"op": OpHello, "d": map[string]any{"heartbeat_interval": HeartbeatInterval}})

	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var frame struct {
			Op int             `json:"op"`
			D  json.RawMessage `json:"d"`
			S  *int            `json:"s"`
			T  string          `json:"t"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			c.close(CloseDecodeError, "Payload not valid JSON")
			return
		}
		switch frame.Op {
		case OpHeartbeat:
			c.send(map[string]any{"op": OpHeartbeatACK})
		case OpIdentify:
			var identify struct {
				Token      string          `json:"token"`
				Properties json.RawMessage `json:"properties"`
				Intents    int             `json:"intents"`
			}
			if err := json.Unmarshal(frame.D, &identify); err != nil || identify.Token == "" {
				c.close(CloseDecodeError, "Invalid identify payload")
				return
			}
			userID, ok := h.Auth(identify.Token)
			if !ok {
				c.close(CloseAuthenticationFailed, "Authentication failed")
				return
			}
			h.mu.Lock()
			if c.sessionID != "" {
				h.mu.Unlock()
				c.close(CloseAlreadyAuthenticated, "Already authenticated")
				return
			}
			c.userID = userID
			c.sessionID = randomSessionID()
			if old, exists := h.sessions[c.sessionID]; exists && old != c {
				old.close(CloseSessionTimeout, "Session superseded")
			}
			h.sessions[c.sessionID] = c
			guildIDs := h.Guilds(userID)
			c.subscriptions = map[string]bool{}
			for _, g := range guildIDs {
				c.subscriptions[g] = true
			}
			h.mu.Unlock()
			c.stateMu.Lock()
			c.passive = map[string]bool{}
			c.cursors = map[string]uint64{}
			for _, g := range guildIDs {
				// Every guild starts passive; OP 14 promotes one to a live
				// subscription, which is how large guilds avoid the firehose.
				c.passive[g] = true
				c.cursors[g] = h.journal.head(g)
			}
			c.stateMu.Unlock()

			var payload any
			if h.Ready != nil {
				if p, err := h.Ready(userID, c.sessionID); err == nil {
					payload = p
				}
			}
			c.dispatch("READY", payload)
		case OpResume:
			var resume struct {
				Token     string `json:"token"`
				SessionID string `json:"session_id"`
				Seq       int    `json:"seq"`
			}
			if err := json.Unmarshal(frame.D, &resume); err != nil {
				c.close(CloseDecodeError, "Invalid resume payload")
				return
			}
			userID, ok := h.Auth(resume.Token)
			if !ok {
				c.close(CloseAuthenticationFailed, "Authentication failed")
				return
			}
			h.mu.Lock()
			old, exists := h.sessions[resume.SessionID]
			h.mu.Unlock()
			if !exists || old.userID != userID {
				c.send(map[string]any{"op": OpInvalidSession, "d": false})
				return
			}
			if old != c {
				h.mu.Lock()
				delete(h.sessions, resume.SessionID)
				c.userID = userID
				c.sessionID = resume.SessionID
				c.subscriptions = old.snapshotSubscriptions()
				h.sessions[c.sessionID] = c
				h.mu.Unlock()
				old.close(CloseSessionTimeout, "Session resumed elsewhere")
			}
			missing := old.replayFrom(c, resume.Seq)
			if len(missing) > 0 {
				c.send(map[string]any{"op": OpInvalidSession, "d": true})
				return
			}
			c.dispatch("RESUMED", map[string]any{})
		case OpRequestGuildMembers:
			// GUILD_MEMBERS_CHUNK is the documented answer to a member request.
			c.send(map[string]any{"op": OpDispatch, "t": "GUILD_MEMBERS_CHUNK",
				"s": c.nextSeq(), "d": map[string]any{"guild_id": "", "members": []any{}, "chunk_index": 0, "chunk_count": 1}})
		case 14: // Guild Subscriptions
			var sub struct {
				GuildID string `json:"guild_id"`
			}
			if err := json.Unmarshal(frame.D, &sub); err == nil && sub.GuildID != "" {
				c.activateGuild(sub.GuildID)
			}
		case OpPresenceUpdate:
			// Presence is accepted and acknowledged by sequence; it is not persisted.
			c.nextSeq()
		case OpVoiceStateUpdate:
			if !h.handleVoiceState(c, frame.D) {
				return
			}
		default:
			c.close(CloseUnknownOpcode, "Unknown opcode")
			return
		}
	}
}

// ConnectionCount reports live gateway connections.
func (h *Hub) ConnectionCount() int {
	if h == nil {
		return 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

// DispatchCount reports events fanned out since start.
func (h *Hub) DispatchCount() int64 {
	if h == nil {
		return 0
	}
	return h.dispatched.Load()
}

// SetBus attaches the cluster transport. Events published by other instances
// are delivered through DeliverRemote; local connections are fanned out first so
// the publishing instance does not depend on the bus round trip.
func (h *Hub) SetBus(b bus.Bus) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.bus = b
	h.mu.Unlock()
}

// DeliverRemote fans an event received from another instance out locally. It
// never republishes.
func (h *Hub) DeliverRemote(ev bus.Event) {
	if h == nil {
		return
	}
	if ev.GuildID != "" {
		h.localGuild(ev.GuildID, ev.Name, ev.Data)
	}
	for _, uid := range ev.UserIDs {
		h.localUser(uid, ev.Name, ev.Data)
	}
}

func (h *Hub) publish(ev bus.Event) {
	h.mu.RLock()
	b := h.bus
	h.mu.RUnlock()
	if b == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Publish(ctx, ev); err != nil && h.Log != nil {
		h.Log.Warn("gateway bus publish", "event", ev.Name, "err", err)
	}
}

// handleVoiceState applies an OP 4 voice state update, persists it, and emits
// VOICE_STATE_UPDATE plus a VOICE_SERVER_UPDATE to the joining user. It returns
// false when the connection must close.
func (h *Hub) handleVoiceState(c *conn, raw json.RawMessage) bool {
	if h.store == nil {
		c.close(CloseUnknownError, "Voice states unavailable")
		return false
	}
	var upd struct {
		GuildID        string  `json:"guild_id"`
		ChannelID      *string `json:"channel_id"`
		SelfMute       bool    `json:"self_mute"`
		SelfDeaf       bool    `json:"self_deaf"`
		SelfVideo      bool    `json:"self_video"`
		SelfStream     bool    `json:"self_stream"`
		RequestToSpeak *string `json:"request_to_speak_timestamp"`
	}
	if err := json.Unmarshal(raw, &upd); err != nil || upd.GuildID == "" {
		c.close(CloseDecodeError, "Invalid voice state payload")
		return false
	}
	if ok, _ := h.store.IsMember(upd.GuildID, c.userID); !ok {
		c.close(CloseUnknownError, "Not a member of the guild")
		return false
	}
	if upd.ChannelID == nil {
		previous, err := h.store.DeleteVoiceState(upd.GuildID, c.userID)
		if err != nil {
			c.close(CloseUnknownError, "Voice state update failed")
			return false
		}
		if previous != nil {
			h.DispatchGuild(upd.GuildID, "VOICE_STATE_UPDATE", store.VoiceState{
				GuildID: upd.GuildID, ChannelID: nil, UserID: c.userID, SessionID: c.sessionID,
			})
		}
		return true
	}
	ch, err := h.store.Channel(*upd.ChannelID)
	if err != nil || ch.GuildID == nil || *ch.GuildID != upd.GuildID ||
		(ch.Type != store.ChannelVoice && ch.Type != store.ChannelStage) {
		c.close(CloseUnknownError, "Invalid voice channel")
		return false
	}
	suppress := ch.Type == store.ChannelStage
	state := store.VoiceState{
		GuildID: upd.GuildID, ChannelID: upd.ChannelID, UserID: c.userID,
		SessionID: c.sessionID, SelfMute: upd.SelfMute, SelfDeaf: upd.SelfDeaf,
		SelfVideo: upd.SelfVideo, SelfStream: upd.SelfStream, Suppress: suppress,
		RequestToSpeakTimestamp: upd.RequestToSpeak,
	}
	token := randomSessionID()
	saved, err := h.store.PutVoiceState(state, token)
	if err != nil {
		c.close(CloseUnknownError, "Voice state update failed")
		return false
	}
	h.DispatchGuild(upd.GuildID, "VOICE_STATE_UPDATE", saved)
	h.DispatchUser(c.userID, "VOICE_SERVER_UPDATE", map[string]any{
		"guild_id":   upd.GuildID,
		"channel_id": *upd.ChannelID,
		"token":      token,
		"endpoint":   h.voiceEndpoint,
	})
	return true
}

// DispatchGuild sends a guild-scoped dispatch to local connections and to every
// other instance.
func (h *Hub) DispatchGuild(guildID, event string, data any) {
	if h == nil {
		return
	}
	h.localGuild(guildID, event, data)
	if raw, err := json.Marshal(data); err == nil {
		h.publish(bus.Event{GuildID: guildID, Name: event, Data: raw})
	}
}

// DispatchUser sends a dispatch to every connection of one account, locally and
// cluster-wide.
func (h *Hub) DispatchUser(userID, event string, data any) {
	if h == nil {
		return
	}
	h.localUser(userID, event, data)
	if raw, err := json.Marshal(data); err == nil {
		h.publish(bus.Event{UserIDs: []string{userID}, Name: event, Data: raw})
	}
}

func (h *Hub) localGuild(guildID, event string, data any) {
	h.dispatched.Add(1)
	if kind, id, ok := passiveEvent(event, data); ok {
		if raw, err := json.Marshal(data); err == nil {
			h.journal.record(guildID, kind, id, raw)
		}
	} else if kind, id, ok := passiveRemoval(event, data); ok {
		h.journal.remove(guildID, kind, id)
	}
	h.mu.RLock()
	targets := make([]*conn, 0, len(h.conns))
	for c := range h.conns {
		if c.sessionID == "" || !c.subscriptions[guildID] {
			continue
		}
		// Passive guilds get entity changes through PASSIVE_UPDATE_V2 only.
		if isPassiveEvent(event) {
			c.stateMu.Lock()
			passive := c.passive[guildID]
			c.stateMu.Unlock()
			if passive {
				continue
			}
		}
		targets = append(targets, c)
	}
	h.mu.RUnlock()
	for _, c := range targets {
		c.dispatch(event, data)
	}
}

func isPassiveEvent(event string) bool {
	switch event {
	case "CHANNEL_CREATE", "CHANNEL_UPDATE", "CHANNEL_DELETE",
		"GUILD_MEMBER_ADD", "GUILD_MEMBER_UPDATE", "GUILD_MEMBER_REMOVE",
		"VOICE_STATE_UPDATE":
		return true
	}
	return false
}

func (h *Hub) localUser(userID, event string, data any) {
	h.dispatched.Add(1)
	h.mu.RLock()
	targets := make([]*conn, 0, 4)
	for c := range h.conns {
		if c.sessionID != "" && c.userID == userID {
			targets = append(targets, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range targets {
		c.dispatch(event, data)
	}
}

// conn is one live gateway connection.
type conn struct {
	ws  *websocket.Conn
	hub *Hub
	out chan []byte

	writeMu       sync.Mutex
	stateMu       sync.Mutex
	seq           int
	buffer        []buffered
	userID        string
	sessionID     string
	encoder       *zstd.Encoder
	passive       map[string]bool
	cursors       map[string]uint64
	subscriptions map[string]bool
	done          chan struct{}
	closed        bool
}

type buffered struct {
	seq   int
	frame []byte
}

const bufferSize = 200

func (c *conn) snapshotSubscriptions() map[string]bool {
	out := make(map[string]bool, len(c.subscriptions))
	for k := range c.subscriptions {
		out[k] = true
	}
	return out
}

func (c *conn) nextSeq() int {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.seq++
	return c.seq
}

func (c *conn) dispatch(event string, data any) {
	if c == nil {
		return
	}
	c.stateMu.Lock()
	c.seq++
	seq := c.seq
	frame, err := json.Marshal(map[string]any{"op": OpDispatch, "t": event, "s": seq, "d": data})
	if err == nil {
		c.buffer = append(c.buffer, buffered{seq: seq, frame: frame})
		if len(c.buffer) > bufferSize {
			c.buffer = c.buffer[len(c.buffer)-bufferSize:]
		}
	}
	c.stateMu.Unlock()
	if err != nil {
		return
	}
	c.send(frame)
}

func (c *conn) send(v any) {
	var raw []byte
	switch t := v.(type) {
	case []byte:
		raw = t
	default:
		var err error
		raw, err = json.Marshal(v)
		if err != nil {
			return
		}
	}
	select {
	case c.out <- raw:
	case <-c.done:
	default:
		// Slow consumer: the documented behaviour is to drop and reconnect.
		c.close(CloseRateLimited, "Slow consumer")
	}
}

func (c *conn) writeLoop() {
	for {
		select {
		case raw := <-c.out:
			c.writeMu.Lock()
			var err error
			if c.encoder != nil {
				// Write + Flush emits only the newly compressed bytes; the frame
				// sink sends them as one binary websocket message.
				if _, err = c.encoder.Write(raw); err == nil {
					err = c.encoder.Flush()
				}
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				err = c.ws.Write(ctx, websocket.MessageText, raw)
				cancel()
			}
			c.writeMu.Unlock()
			if err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

// frameSink writes compressed bytes straight to the websocket as binary frames.
// It is only called from writeLoop while writeMu is held.
type frameSink struct{ c *conn }

func (f frameSink) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.c.ws.Write(ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// replayFrom copies buffered events after seq into c; it reports whether the
// requested sequence could be served from the buffer.
func (c *conn) replayFrom(target *conn, seq int) []int {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if seq >= c.seq {
		return nil
	}
	if len(c.buffer) == 0 || c.buffer[0].seq > seq+1 {
		return []int{seq}
	}
	for _, b := range c.buffer {
		if b.seq > seq {
			target.send(b.frame)
		}
	}
	return nil
}

func (c *conn) close(code int, reason string) {
	c.stateMu.Lock()
	if c.closed {
		c.stateMu.Unlock()
		return
	}
	c.closed = true
	c.stateMu.Unlock()
	c.writeMu.Lock()
	_ = c.ws.Close(websocket.StatusCode(code), reason)
	c.writeMu.Unlock()
}

func randomSessionID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic("gateway: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buf)
}
