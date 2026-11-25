package gateway

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/wavedevgit/waveycord/internal/store"
)

// Voice gateway opcodes from the voice websocket protocol.
const (
	voiceOpIdentify       = 0
	voiceOpSelectProtocol = 1
	voiceOpReady          = 2
	voiceOpHeartbeat      = 3
	voiceOpSessionDesc    = 4
	voiceOpSpeaking       = 5
	voiceOpHeartbeatAck   = 6
	voiceOpHello          = 8
)

const (
	voiceCloseUnknownOpcode = 4001
	voiceCloseNotAuth       = 4003
	voiceHeartbeatInterval  = 41250
)

// VoiceServer implements the voice gateway signaling boundary and the UDP
// discovery/RTP stub. It does not decode Opus or route media; it validates the
// token minted by OP 4, completes the documented handshake, answers UDP
// discovery, counts inbound RTP and broadcasts speaking events to the same
// channel.
type VoiceServer struct {
	store *store.Store
	log   *slog.Logger

	udp     *net.UDPConn
	ip      string
	port    int
	mu      sync.Mutex
	conns   map[*voiceConn]struct{}
	session atomic.Int64
	rtp     atomic.Int64
	speak   atomic.Int64
}

// VoiceOptions tunes the advertised address.
type VoiceOptions struct {
	IP  string
	Log *slog.Logger
}

// NewVoiceServer binds the UDP socket used for discovery and RTP reception.
func NewVoiceServer(st *store.Store, opts VoiceOptions) (*VoiceServer, error) {
	ip := opts.IP
	if ip == "" {
		ip = "127.0.0.1"
	}
	logger := opts.Log
	if logger == nil {
		logger = slog.Default()
	}
	addr := &net.UDPAddr{IP: net.ParseIP(ip), Port: 0}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("voice: bind udp: %w", err)
	}
	v := &VoiceServer{store: st, log: logger, udp: conn, ip: ip,
		port: conn.LocalAddr().(*net.UDPAddr).Port, conns: map[*voiceConn]struct{}{}}
	go v.readUDP()
	return v, nil
}

// Port is the UDP discovery port advertised in READY.
func (v *VoiceServer) Port() int { return v.port }

// Stats exposes counters for metrics and tests.
func (v *VoiceServer) Stats() (sessions, rtpPackets, speaking int64) {
	return v.session.Load(), v.rtp.Load(), v.speak.Load()
}

// Close stops the UDP socket and disconnects clients.
func (v *VoiceServer) Close() {
	if v.udp != nil {
		_ = v.udp.Close()
	}
	v.mu.Lock()
	for c := range v.conns {
		c.close(1000, "server shutdown")
	}
	v.mu.Unlock()
}

// readUDP answers discovery packets and counts RTP.
func (v *VoiceServer) readUDP() {
	buf := make([]byte, 1500)
	for {
		n, from, err := v.udp.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		// Discovery packet: 74 bytes, first uint16 type = 1.
		// IP discovery: type(2) length(2) ssrc(4) address(64) port(2) = 74 bytes.
		if n >= 74 && binary.BigEndian.Uint16(buf[:2]) == 1 {
			ssrc := binary.BigEndian.Uint32(buf[4:8])
			resp := make([]byte, 74)
			binary.BigEndian.PutUint16(resp[0:2], 2)
			binary.BigEndian.PutUint16(resp[2:4], 70)
			binary.BigEndian.PutUint32(resp[4:8], ssrc)
			copy(resp[8:72], v.ip)
			binary.BigEndian.PutUint16(resp[72:74], uint16(v.port))
			_, _ = v.udp.WriteToUDP(resp, from)
			continue
		}
		// RTP v2 packet.
		if buf[0]>>6 == 2 {
			v.rtp.Add(1)
		}
	}
}

// Handler upgrades /voice connections.
func (v *VoiceServer) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
			OriginPatterns:     []string{"*"},
		})
		if err != nil {
			return
		}
		v.serve(r.Context(), ws, token)
	})
}

type voiceConn struct {
	ws     *websocket.Conn
	server *VoiceServer
	token  string

	mu        sync.Mutex
	writeMu   sync.Mutex
	userID    string
	guildID   string
	channelID string
	ssrc      uint32
	ready     bool
	closed    bool
	done      chan struct{}
}

func (v *VoiceServer) serve(ctx context.Context, ws *websocket.Conn, token string) {
	c := &voiceConn{ws: ws, server: v, token: token, done: make(chan struct{})}
	v.mu.Lock()
	v.conns[c] = struct{}{}
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		delete(v.conns, c)
		v.mu.Unlock()
		v.session.Add(-1)
		close(c.done)
		_ = ws.CloseNow()
	}()

	c.send(map[string]any{"op": voiceOpHello, "d": map[string]any{
		"heartbeat_interval": voiceHeartbeatInterval,
	}})

	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var frame struct {
			Op int             `json:"op"`
			D  json.RawMessage `json:"d"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			c.close(voiceCloseUnknownOpcode, "Invalid payload")
			return
		}
		switch frame.Op {
		case voiceOpIdentify:
			var identify struct {
				ServerID  string `json:"server_id"`
				UserID    string `json:"user_id"`
				SessionID string `json:"session_id"`
				Token     string `json:"token"`
			}
			if err := json.Unmarshal(frame.D, &identify); err != nil {
				c.close(voiceCloseUnknownOpcode, "Invalid identify")
				return
			}
			state, err := v.store.VoiceStateByToken(identify.Token)
			if err != nil || (identify.UserID != "" && identify.UserID != state.UserID) {
				c.close(voiceCloseNotAuth, "Invalid voice token")
				return
			}
			c.mu.Lock()
			c.userID = state.UserID
			c.guildID = state.GuildID
			c.channelID = ""
			if state.ChannelID != nil {
				c.channelID = *state.ChannelID
			}
			c.ssrc = randomUint32()
			c.ready = true
			c.mu.Unlock()
			v.session.Add(1)
			c.send(map[string]any{"op": voiceOpReady, "d": map[string]any{
				"ssrc":               c.ssrc,
				"ip":                 v.ip,
				"port":               v.port,
				"modes":              []string{"opus"},
				"heartbeat_interval": voiceHeartbeatInterval,
				"experiments":        []string{},
			}})
		case voiceOpSelectProtocol:
			var sel struct {
				Protocol string `json:"protocol"`
				Data     struct {
					Address string `json:"address"`
					Port    int    `json:"port"`
					Mode    string `json:"mode"`
				} `json:"data"`
			}
			_ = json.Unmarshal(frame.D, &sel)
			if !c.isReady() {
				c.close(voiceCloseNotAuth, "Identify first")
				return
			}
			key := make([]byte, 32)
			_, _ = rand.Read(key)
			// Discord encodes secret_key as an array of byte values, not base64.
			keyInts := make([]int, len(key))
			for i, b := range key {
				keyInts[i] = int(b)
			}
			c.send(map[string]any{"op": voiceOpSessionDesc, "d": map[string]any{
				"mode":                  "opus",
				"secret_key":            keyInts,
				"dave_protocol_version": 0,
			}})
		case voiceOpHeartbeat:
			c.send(map[string]any{"op": voiceOpHeartbeatAck, "d": map[string]any{"t": time.Now().UnixMilli()}})
		case voiceOpSpeaking:
			var sp struct {
				Speaking int    `json:"speaking"`
				Delay    int    `json:"delay"`
				SSRC     uint32 `json:"ssrc"`
			}
			_ = json.Unmarshal(frame.D, &sp)
			if !c.isReady() {
				continue
			}
			v.speak.Add(1)
			c.broadcastSpeaking(sp.Speaking, sp.Delay, sp.SSRC)
		default:
			c.close(voiceCloseUnknownOpcode, "Unknown opcode")
			return
		}
	}
}

func (c *voiceConn) isReady() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ready
}

func (c *voiceConn) channel() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.channelID
}

// broadcastSpeaking delivers OP 5 to other voice connections in the same channel.
func (c *voiceConn) broadcastSpeaking(speaking, delay int, ssrc uint32) {
	v := c.server
	channel := c.channel()
	v.mu.Lock()
	targets := make([]*voiceConn, 0, len(v.conns))
	for other := range v.conns {
		if other != c && other.channel() == channel && channel != "" {
			targets = append(targets, other)
		}
	}
	v.mu.Unlock()
	for _, other := range targets {
		other.send(map[string]any{"op": voiceOpSpeaking, "d": map[string]any{
			"speaking": speaking, "delay": delay, "ssrc": ssrc,
		}})
	}
}

func (c *voiceConn) send(v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c.writeMu.Lock()
	_ = c.ws.Write(ctx, websocket.MessageText, raw)
	c.writeMu.Unlock()
}

func (c *voiceConn) close(code int, reason string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	c.writeMu.Lock()
	_ = c.ws.Close(websocket.StatusCode(code), reason)
	c.writeMu.Unlock()
}

func randomUint32() uint32 {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return binary.BigEndian.Uint32(buf)
}
