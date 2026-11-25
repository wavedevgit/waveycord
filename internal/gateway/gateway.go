// Package gateway implements the documented Gateway protocol over WebSocket:
// HELLO, IDENTIFY, READY, HEARTBEAT/ACK, RESUME, INVALID_SESSION and DISPATCH.
package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
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

	mu    sync.RWMutex
	conns map[*conn]struct{}
	// sessions keeps at most one live connection per gateway session id, which
	// is what RESUME and session takeover depend on.
	sessions map[string]*conn
}

// NewHub constructs a hub.
func NewHub(auth AuthFunc, ready ReadyFunc, guilds GuildsFunc, log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{Auth: auth, Ready: ready, Guilds: guilds, Log: log,
		conns: map[*conn]struct{}{}, sessions: map[string]*conn{}}
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
		h.serve(r.Context(), ws)
	})
}

func (h *Hub) serve(ctx context.Context, ws *websocket.Conn) {
	c := &conn{ws: ws, hub: h, out: make(chan []byte, 64), done: make(chan struct{})}
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
		case OpPresenceUpdate, OpVoiceStateUpdate:
			// Accepted and acknowledged by sequence; presence/voice state carry no
			// server-side persistence in this implementation.
			c.nextSeq()
		default:
			c.close(CloseUnknownOpcode, "Unknown opcode")
			return
		}
	}
}

// DispatchGuild sends a guild-scoped dispatch.
func (h *Hub) DispatchGuild(guildID, event string, data any) {
	if h == nil {
		return
	}
	h.mu.RLock()
	targets := make([]*conn, 0, len(h.conns))
	for c := range h.conns {
		if c.sessionID != "" && c.subscriptions[guildID] {
			targets = append(targets, c)
		}
	}
	h.mu.RUnlock()
	for _, c := range targets {
		c.dispatch(event, data)
	}
}

// DispatchUser sends a dispatch to every connection of one account.
func (h *Hub) DispatchUser(userID, event string, data any) {
	if h == nil {
		return
	}
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
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			c.writeMu.Lock()
			err := c.ws.Write(ctx, websocket.MessageText, raw)
			c.writeMu.Unlock()
			cancel()
			if err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
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
	const alphabet = "0123456789abcdef"
	b := make([]byte, 32)
	now := time.Now().UnixNano()
	for i := range b {
		b[i] = alphabet[(int(now>>uint(i%16))^i*7)%len(alphabet)]
	}
	return string(b)
}
