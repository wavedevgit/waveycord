package gateway

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/curve25519"

	"github.com/wavedevgit/waveycord/internal/store"
	"github.com/wavedevgit/waveycord/internal/voicecodec"
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

	udp        *net.UDPConn
	ip         string
	port       int
	mu         sync.Mutex
	conns      map[*voiceConn]struct{}
	session    atomic.Int64
	rtp        atomic.Int64
	speak      atomic.Int64
	forwarded  atomic.Int64
	transcodes atomic.Int64
	publish    func(name string, data any)
	codecReady bool
	done       chan struct{}
	tick       *time.Ticker
	mixMu      sync.Mutex
	mixers     map[string]*channelMixer
	mixes      atomic.Int64
	instanceID string
	clusterMix bool
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
		port: conn.LocalAddr().(*net.UDPAddr).Port, conns: map[*voiceConn]struct{}{},
		codecReady: voicecodec.Available(), done: make(chan struct{})}
	if v.codecReady {
		logger.Info("voice opus codec enabled")
	}
	go v.readUDP()
	go v.jitterLoop()
	go v.mixLoop()
	return v, nil
}

// Port is the UDP discovery port advertised in READY.
func (v *VoiceServer) Port() int { return v.port }

// Stats exposes counters for metrics and tests.
func (v *VoiceServer) Stats() (sessions, rtpPackets, speaking int64) {
	return v.session.Load(), v.rtp.Load(), v.speak.Load()
}

// Close stops the UDP socket, the jitter loop and all clients.
func (v *VoiceServer) Close() {
	select {
	case <-v.done:
	default:
		close(v.done)
	}
	if v.udp != nil {
		_ = v.udp.Close()
	}
	v.mu.Lock()
	id := v.instanceID
	cluster := v.clusterMix
	for c := range v.conns {
		c.close(1000, "server shutdown")
	}
	v.mu.Unlock()
	if cluster && id != "" && v.store != nil {
		_ = v.store.UnregisterInstance(id)
	}
}

// DeliverRTP forwards a packet received from another instance to local voice
// connections in the channel.
func (v *VoiceServer) DeliverRTP(channelID string, packet []byte) {
	if len(packet) == 0 {
		return
	}
	v.mu.Lock()
	targets := make([]*voiceConn, 0, len(v.conns))
	for c := range v.conns {
		if c.channel() == channelID {
			targets = append(targets, c)
		}
	}
	v.mu.Unlock()
	for _, c := range targets {
		if addr := c.udp(); addr != nil {
			_, _ = v.udp.WriteToUDP(packet, addr)
			v.forwarded.Add(1)
		}
	}
}

// publishE2EEKey announces a member's static media key to the channel and other
// instances.
func (v *VoiceServer) publishE2EEKey(userID string, pub [32]byte, signingKey string) {
	channel := ""
	v.mu.Lock()
	var self *voiceConn
	targets := make([]*voiceConn, 0, len(v.conns))
	for c := range v.conns {
		if c.userID == userID {
			self = c
			channel = c.channel()
		}
	}
	if channel != "" {
		for c := range v.conns {
			if c.userID != userID && c.channel() == channel {
				targets = append(targets, c)
			}
		}
	}
	// Existing peers, so the publisher can wrap a group key immediately.
	var peers []*voiceConn
	if self != nil {
		for c := range v.conns {
			if c.userID != userID && c.channel() == channel && c.hasE2EE {
				peers = append(peers, c)
			}
		}
	}
	v.mu.Unlock()
	encoded := encodeKey(pub)
	for _, c := range targets {
		c.send(map[string]any{"op": 34, "d": map[string]any{
			"user_id": userID, "public_key": encoded, "signing_key": signingKey,
		}})
	}
	for _, peer := range peers {
		peer.mu.Lock()
		peerPub, peerID, peerSign := peer.e2eePub, peer.userID, peer.e2eeSign
		peer.mu.Unlock()
		self.send(map[string]any{"op": 34, "d": map[string]any{
			"user_id": peerID, "public_key": encodeKey(peerPub), "signing_key": peerSign,
		}})
	}
	if v.publish != nil {
		v.publish("VOICE_E2EE_PUBLISH", map[string]any{
			"channel_id": channel, "user_id": userID, "public_key": encoded,
			"signing_key": signingKey,
		})
	}
}

// DeliverE2EEKey publishes a member key announcement received from another
// instance to local peers.
func (v *VoiceServer) DeliverE2EEKey(channelID, userID, publicKey, signingKey string) {
	v.mu.Lock()
	targets := make([]*voiceConn, 0, len(v.conns))
	for c := range v.conns {
		if c.channel() == channelID && c.userID != userID {
			targets = append(targets, c)
		}
	}
	v.mu.Unlock()
	for _, c := range targets {
		c.send(map[string]any{"op": 34, "d": map[string]any{
			"user_id": userID, "public_key": publicKey, "signing_key": signingKey,
		}})
	}
}

// relayE2EEKey routes a wrapped group key to one member and other instances.
func (v *VoiceServer) relayE2EEKey(channelID, fromUserID, toUserID string, keyID uint32, wrapped, kind string) {
	v.mu.Lock()
	targets := make([]*voiceConn, 0, 1)
	for c := range v.conns {
		if c.channel() == channelID && c.userID == toUserID {
			targets = append(targets, c)
		}
	}
	v.mu.Unlock()
	for _, c := range targets {
		c.send(map[string]any{"op": 35, "d": map[string]any{
			"from_user_id": fromUserID, "key_id": keyID, "wrapped": wrapped, "kind": kind,
		}})
	}
	if v.publish != nil {
		v.publish("VOICE_E2EE_KEY", map[string]any{
			"channel_id": channelID, "from_user_id": fromUserID,
			"to_user_id": toUserID, "key_id": keyID, "wrapped": wrapped, "kind": kind,
		})
	}
}

// DeliverE2EEKeyRelay forwards a wrapped group key received from another instance.
func (v *VoiceServer) DeliverE2EEKeyRelay(channelID, fromUserID, toUserID string, keyID uint32, wrapped, kind string) {
	v.mu.Lock()
	targets := make([]*voiceConn, 0, 1)
	for c := range v.conns {
		if c.channel() == channelID && c.userID == toUserID {
			targets = append(targets, c)
		}
	}
	v.mu.Unlock()
	for _, c := range targets {
		c.send(map[string]any{"op": 35, "d": map[string]any{
			"from_user_id": fromUserID, "key_id": keyID, "wrapped": wrapped, "kind": kind,
		}})
	}
}

// MemberChanged signals a voice channel membership change so members rotate the
// group key (join: include the newcomer; leave: exclude the departed member). It
// also moves the member's live voice connection in or out of the channel, so key
// relays and media fanout stop for departed members.
func (v *VoiceServer) MemberChanged(channelID, userID, reason string) {
	if channelID == "" {
		return
	}
	v.mu.Lock()
	for c := range v.conns {
		c.mu.Lock()
		if c.userID == userID {
			if reason == "member_left" {
				c.channelID = ""
			} else {
				c.channelID = channelID
			}
		}
		c.mu.Unlock()
	}
	targets := make([]*voiceConn, 0, len(v.conns))
	for c := range v.conns {
		if c.channel() == channelID {
			targets = append(targets, c)
		}
	}
	v.mu.Unlock()
	epoch := time.Now().UnixMilli()
	members := v.roster(channelID)
	for _, c := range targets {
		c.send(map[string]any{"op": 36, "d": map[string]any{
			"channel_id": channelID, "epoch": epoch, "reason": reason, "members": members,
		}})
	}
	if v.publish != nil {
		v.publish("VOICE_E2EE_REKEY", map[string]any{
			"channel_id": channelID, "epoch": epoch, "reason": reason, "members": members,
		})
	}
}

// DeliverE2EERekey forwards a rekey signal received from another instance.
func (v *VoiceServer) DeliverE2EERekey(channelID string, epoch int64, reason string, members []string) {
	v.mu.Lock()
	targets := make([]*voiceConn, 0, len(v.conns))
	for c := range v.conns {
		if c.channel() == channelID {
			targets = append(targets, c)
		}
	}
	v.mu.Unlock()
	for _, c := range targets {
		c.send(map[string]any{"op": 36, "d": map[string]any{
			"channel_id": channelID, "epoch": epoch, "reason": reason, "members": members,
		}})
	}
}

// roster lists the guild voice members in a channel from the shared database.
func (v *VoiceServer) roster(channelID string) []string {
	if v.store == nil {
		return []string{}
	}
	states, err := v.store.VoiceStatesForChannel(channelID)
	if err != nil {
		return []string{}
	}
	ids := make([]string, 0, len(states))
	for _, st := range states {
		ids = append(ids, st.UserID)
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	return ids
}

// relayCommit broadcasts a signed MLS commit to channel peers and other instances.
func (v *VoiceServer) relayCommit(channelID, sender string, commit json.RawMessage) {
	v.mu.Lock()
	targets := make([]*voiceConn, 0, len(v.conns))
	for c := range v.conns {
		if c.channel() == channelID && c.userID != sender {
			targets = append(targets, c)
		}
	}
	v.mu.Unlock()
	for _, c := range targets {
		c.send(map[string]any{"op": 38, "d": map[string]any{
			"from_user_id": sender, "commit": commit,
		}})
	}
	if v.publish != nil {
		v.publish("VOICE_MLS_COMMIT", map[string]any{
			"channel_id": channelID, "from_user_id": sender, "commit": commit,
		})
	}
}

// DeliverMLSCommit forwards a commit received from another instance.
func (v *VoiceServer) DeliverMLSCommit(channelID, sender string, commit json.RawMessage) {
	v.mu.Lock()
	targets := make([]*voiceConn, 0, len(v.conns))
	for c := range v.conns {
		if c.channel() == channelID && c.userID != sender {
			targets = append(targets, c)
		}
	}
	v.mu.Unlock()
	for _, c := range targets {
		c.send(map[string]any{"op": 38, "d": map[string]any{
			"from_user_id": sender, "commit": commit,
		}})
	}
}

// SetPublisher enables cross-instance speaking events.
func (v *VoiceServer) SetPublisher(fn func(name string, data any)) {
	if v == nil {
		return
	}
	v.publish = fn
}

// DeliverSpeaking broadcasts a speaking event received from another instance to
// local voice connections in the channel.
func (v *VoiceServer) DeliverSpeaking(channelID string, speaking, delay int, ssrc uint32) {
	v.mu.Lock()
	targets := make([]*voiceConn, 0, len(v.conns))
	for c := range v.conns {
		if c.channel() == channelID {
			targets = append(targets, c)
		}
	}
	v.mu.Unlock()
	for _, c := range targets {
		c.send(map[string]any{"op": voiceOpSpeaking, "d": map[string]any{
			"speaking": speaking, "delay": delay, "ssrc": ssrc,
		}})
	}
}

// Forwarded counts RTP packets sent to other voice connections.
func (v *VoiceServer) Forwarded() int64 { return v.forwarded.Load() }

// Transcodes counts frames decoded/re-encoded by the Opus path.
func (v *VoiceServer) Transcodes() int64 { return v.transcodes.Load() }

// Mixes counts mixed frames emitted by the channel mixer.
func (v *VoiceServer) Mixes() int64 { return v.mixes.Load() }

// rtpPayloadOffset returns the RTP header length, or 0 when the packet is too
// short to carry a payload.
func rtpPayloadOffset(packet []byte) int {
	if len(packet) < 12 {
		return 0
	}
	offset := 12 + 4*int(packet[0]&0x0f)
	if packet[0]&0x10 != 0 {
		if len(packet) < offset+4 {
			return 0
		}
		extWords := int(binary.BigEndian.Uint16(packet[offset+2 : offset+4]))
		offset += 4 + 4*extWords
	}
	if offset >= len(packet) {
		return 0
	}
	return offset
}

// ensureCodec lazily creates a per-connection Opus codec so decode state is not
// shared between speakers.
func (c *voiceConn) ensureCodec() voicecodec.Codec {
	c.mu.Lock()
	if c.codec != nil || !c.server.codecReady {
		codec := c.codec
		c.mu.Unlock()
		return codec
	}
	c.mu.Unlock()
	codec, err := voicecodec.New()
	if err != nil {
		return nil
	}
	c.mu.Lock()
	if c.codec == nil {
		c.codec = codec
	} else {
		codec.Close()
		codec = c.codec
	}
	c.mu.Unlock()
	return codec
}

// transcode decodes an Opus payload and re-encodes it, keeping the RTP header.
func (c *voiceConn) transcode(packet []byte) []byte {
	codec := c.ensureCodec()
	if codec == nil {
		return packet
	}
	offset := rtpPayloadOffset(packet)
	if offset == 0 {
		return packet
	}
	pcm, err := codec.Decode(packet[offset:])
	if err != nil || len(pcm) == 0 {
		return packet
	}
	encoded, err := codec.Encode(pcm)
	if err != nil || len(encoded) == 0 {
		return packet
	}
	c.server.transcodes.Add(1)
	out := make([]byte, offset+len(encoded))
	copy(out, packet[:offset])
	copy(out[offset:], encoded)
	return out
}

// jitterState buffers frames per sender, emits them in sequence order and fills
// gaps with libopus concealment.
type jitterState struct {
	started    bool
	emitted    bool
	expected   uint16
	buf        map[uint16]jitterFrame
	gapSince   time.Time
	plcs       int
	lastHeader []byte
	lastTS     uint32
}

type jitterFrame struct {
	header  []byte
	payload []byte
	ts      uint32
}

const (
	jitterDepth = 2
	jitterDelay = 60 * time.Millisecond
	maxPLC      = 3
)

func (v *VoiceServer) jitterLoop() {
	ticker := time.NewTicker(10 * time.Millisecond)
	v.tick = ticker
	for {
		select {
		case <-v.done:
			return
		case <-ticker.C:
			v.mu.Lock()
			conns := make([]*voiceConn, 0, len(v.conns))
			for c := range v.conns {
				conns = append(conns, c)
			}
			v.mu.Unlock()
			for _, c := range conns {
				v.drainJitter(c)
			}
		}
	}
}

// ingestRTP buffers a plaintext RTP packet for ordered emission.
func (v *VoiceServer) ingestRTP(sender *voiceConn, packet []byte) {
	offset := rtpPayloadOffset(packet)
	if offset == 0 {
		return
	}
	seq := binary.BigEndian.Uint16(packet[2:4])
	ts := binary.BigEndian.Uint32(packet[4:8])
	ssrc := binary.BigEndian.Uint32(packet[8:12])
	header := append([]byte(nil), packet[:offset]...)
	payload := append([]byte(nil), packet[offset:]...)
	if v.codecReady && sender.channel() != "" {
		if codec := sender.ensureCodec(); codec != nil {
			if pcm, err := codec.Decode(payload); err == nil && len(pcm) > 0 {
				channel := sender.channel()
				v.mu.Lock()
				cluster := v.clusterMix
				v.mu.Unlock()
				if cluster && v.mixerOwner(channel) != v.instanceID {
					// Another instance mixes this channel: ship PCM and let the
					// owner's mixed output come back over the bus.
					v.publishPCM(channel, ssrc, pcm)
					return
				}
				if v.feedMixer(channel, ssrc, pcm) {
					return
				}
			}
		}
	}
	sender.mu.Lock()
	if sender.jitter == nil {
		sender.jitter = &jitterState{buf: map[uint16]jitterFrame{}}
	}
	j := sender.jitter
	if !j.started {
		j.started = true
		j.expected = seq
	}
	if seqLess(seq, j.expected) {
		sender.mu.Unlock()
		return
	}
	j.buf[seq] = jitterFrame{header: header, payload: payload, ts: ts}
	sender.mu.Unlock()
	v.drainJitter(sender)
}

func seqLess(a, b uint16) bool { return int16(a-b) < 0 }

// drainJitter emits contiguous frames, inserting PLC after the gap delay.
func (v *VoiceServer) drainJitter(sender *voiceConn) {
	sender.mu.Lock()
	j := sender.jitter
	if j == nil || !j.started {
		sender.mu.Unlock()
		return
	}
	for {
		if !j.emitted && len(j.buf) < jitterDepth {
			// Hold the first frame briefly so a reordered neighbour can arrive,
			// then start streaming instead of stalling a single-frame sender.
			if j.gapSince.IsZero() {
				j.gapSince = time.Now()
				sender.mu.Unlock()
				return
			}
			if time.Since(j.gapSince) < 20*time.Millisecond {
				sender.mu.Unlock()
				return
			}
		}
		if f, ok := j.buf[j.expected]; ok {
			delete(j.buf, j.expected)
			j.expected++
			j.plcs = 0
			j.gapSince = time.Time{}
			j.emitted = true
			j.lastHeader = f.header
			j.lastTS = f.ts
			sender.mu.Unlock()
			v.emitFrame(sender, f.header, f.payload)
			sender.mu.Lock()
			continue
		}
		if len(j.buf) == 0 {
			sender.mu.Unlock()
			return
		}
		if j.gapSince.IsZero() {
			j.gapSince = time.Now()
			sender.mu.Unlock()
			return
		}
		if time.Since(j.gapSince) < jitterDelay {
			sender.mu.Unlock()
			return
		}
		if j.plcs >= maxPLC {
			// Give up on the hole and jump to the next buffered sequence.
			var next uint16
			first := true
			for seq := range j.buf {
				if first || seqLess(seq, next) {
					next = seq
					first = false
				}
			}
			j.expected = next
			j.plcs = 0
			j.gapSince = time.Time{}
			continue
		}
		header := plcHeader(j.lastHeader, j.expected, j.lastTS+960*uint32(j.plcs+1))
		j.expected++
		j.plcs++
		j.gapSince = time.Now()
		sender.mu.Unlock()
		v.emitPLC(sender, header)
		sender.mu.Lock()
	}
}

func plcHeader(last []byte, seq uint16, ts uint32) []byte {
	header := make([]byte, 12)
	if len(last) >= 12 {
		copy(header, last[:12])
	} else {
		header[0] = 0x80
	}
	binary.BigEndian.PutUint16(header[2:4], seq)
	binary.BigEndian.PutUint32(header[4:8], ts)
	return header
}

func (v *VoiceServer) emitFrame(sender *voiceConn, header, payload []byte) {
	packet := append(append([]byte(nil), header...), payload...)
	packet = sender.transcode(packet)
	v.deliver(sender, packet)
	v.publishRTP(sender, packet)
}

func (v *VoiceServer) emitPLC(sender *voiceConn, header []byte) {
	codec := sender.ensureCodec()
	if codec == nil {
		return
	}
	pcm := make([]int16, voicecodec.FrameSize)
	n, err := codec.Conceal(pcm)
	if err != nil || n == 0 {
		return
	}
	encoded, err := codec.Encode(pcm[:n])
	if err != nil || len(encoded) == 0 {
		return
	}
	v.transcodes.Add(1)
	packet := append(append([]byte(nil), header...), encoded...)
	v.deliver(sender, packet)
	v.publishRTP(sender, packet)
}

// deliver sends a packet to the sender's channel peers.
func (v *VoiceServer) deliver(sender *voiceConn, packet []byte) {
	channel := sender.channel()
	if channel == "" {
		return
	}
	v.mu.Lock()
	targets := make([]*voiceConn, 0, len(v.conns))
	for c := range v.conns {
		if c != sender && c.channel() == channel {
			targets = append(targets, c)
		}
	}
	v.mu.Unlock()
	for _, c := range targets {
		if addr := c.udp(); addr != nil {
			_, _ = v.udp.WriteToUDP(packet, addr)
			v.forwarded.Add(1)
		}
	}
}

func (v *VoiceServer) findBySSRC(ssrc uint32) *voiceConn {
	v.mu.Lock()
	defer v.mu.Unlock()
	for c := range v.conns {
		c.mu.Lock()
		match := c.ready && c.ssrc == ssrc
		c.mu.Unlock()
		if match {
			return c
		}
	}
	return nil
}

// forwardEncrypted re-wraps decrypted media for every peer that has a key.
func (v *VoiceServer) forwardEncrypted(ssrc uint32, plaintext []byte) {
	sender := v.findBySSRC(ssrc)
	if sender == nil {
		return
	}
	plaintext = sender.transcode(plaintext)
	channel := sender.channel()
	v.mu.Lock()
	targets := make([]*voiceConn, 0, len(v.conns))
	for c := range v.conns {
		if c != sender && c.channel() == channel {
			targets = append(targets, c)
		}
	}
	v.mu.Unlock()
	for _, c := range targets {
		c.mu.Lock()
		gcm, keyID := c.gcm, c.keyID
		if gcm != nil {
			c.outCounter++
			counter := c.outCounter
			c.mu.Unlock()
			if addr := c.udp(); addr != nil {
				frame := encodeEnvelope(gcm, keyID, counter, ssrc, plaintext)
				_, _ = v.udp.WriteToUDP(frame, addr)
				v.forwarded.Add(1)
			}
			continue
		}
		c.mu.Unlock()
	}
}

// publishRTP sends an emitted packet to other instances.
func (v *VoiceServer) publishRTP(sender *voiceConn, packet []byte) {
	if v.publish == nil {
		return
	}
	v.publish("VOICE_RTP", map[string]any{
		"channel_id": sender.channel(),
		"packet":     base64.StdEncoding.EncodeToString(packet),
	})
}

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
		// End-to-end group envelope ("WCE2"): forwarded untouched, never decrypted.
		if n > 20 && string(buf[0:4]) == e2eeMagic {
			packet := append([]byte(nil), buf[:n]...)
			v.rtp.Add(1)
			ssrc := binary.BigEndian.Uint32(packet[16:20])
			if sender := v.findBySSRC(ssrc); sender != nil {
				v.deliver(sender, packet)
				if v.publish != nil {
					v.publish("VOICE_RTP_E2EE", map[string]any{
						"channel_id": sender.channel(),
						"packet":     base64.StdEncoding.EncodeToString(packet),
					})
				}
			}
			continue
		}
		// Encrypted media envelope ("WCDF").
		if n > 20 && string(buf[0:4]) == daveMagic {
			packet := append([]byte(nil), buf[:n]...)
			v.rtp.Add(1)
			ssrc := binary.BigEndian.Uint32(packet[16:20])
			if sender := v.findBySSRC(ssrc); sender != nil {
				sender.mu.Lock()
				gcm, keyID, last := sender.gcm, sender.keyID, sender.lastCounter
				sender.mu.Unlock()
				if gcm != nil {
					counter, _, plaintext, err := openEnvelope(gcm, keyID, last, packet)
					if err == nil {
						sender.mu.Lock()
						if counter > sender.lastCounter {
							sender.lastCounter = counter
						}
						sender.mu.Unlock()
						if plaintext != nil {
							v.forwardEncrypted(ssrc, plaintext)
						}
					}
				}
			}
			continue
		}
		// RTP v2 packet: count, then fan out to the same channel (SFU-style).
		if buf[0]>>6 == 2 && n >= 12 {
			v.rtp.Add(1)
			packet := append([]byte(nil), buf[:n]...)
			ssrc := binary.BigEndian.Uint32(packet[8:12])
			if sender := v.findBySSRC(ssrc); sender != nil {
				v.ingestRTP(sender, packet)
			}
			_ = from
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
	ws      *websocket.Conn
	udpAddr *net.UDPAddr
	server  *VoiceServer
	token   string

	mu          sync.Mutex
	writeMu     sync.Mutex
	userID      string
	guildID     string
	channelID   string
	ssrc        uint32
	ready       bool
	gcm         cipher.AEAD
	keyID       uint32
	lastCounter uint64
	outCounter  uint64
	peerPub     [32]byte
	codec       voicecodec.Codec
	jitter      *jitterState
	e2eePub     [32]byte
	e2eeSign    string
	hasE2EE     bool
	closed      bool
	done        chan struct{}
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
			token := identify.Token
			if token == "" {
				token = c.token // token from the connection query string
			}
			state, err := v.store.VoiceStateByToken(token)
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
			if sel.Data.Address != "" && sel.Data.Port > 0 {
				if addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(sel.Data.Address, strconv.Itoa(sel.Data.Port))); err == nil {
					c.mu.Lock()
					c.udpAddr = addr
					c.mu.Unlock()
				}
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
		case 30: // CLIENT_KEY_EXCHANGE
			var ex struct {
				PublicKey string `json:"public_key"`
			}
			if err := json.Unmarshal(frame.D, &ex); err != nil {
				c.close(voiceCloseUnknownOpcode, "Invalid key exchange")
				return
			}
			peer, err := decodeKey(ex.PublicKey)
			if err != nil {
				c.close(voiceCloseUnknownOpcode, "Invalid public key")
				return
			}
			priv, pub, err := newEphemeralKey()
			if err != nil {
				c.close(voiceCloseUnknownOpcode, "Key generation failed")
				return
			}
			shared, err := curve25519.X25519(priv[:], peer[:])
			if err != nil {
				c.close(voiceCloseUnknownOpcode, "Key agreement failed")
				return
			}
			key, err := deriveMediaKey(shared)
			if err != nil {
				c.close(voiceCloseUnknownOpcode, "Key derivation failed")
				return
			}
			gcm, err := newGCM(key)
			if err != nil {
				c.close(voiceCloseUnknownOpcode, "Cipher init failed")
				return
			}
			c.mu.Lock()
			keyID := randomUint32()
			if keyID == 0 {
				keyID = 1
			}
			c.gcm = gcm
			c.keyID = keyID
			c.lastCounter = 0
			c.peerPub = peer
			c.mu.Unlock()
			c.send(map[string]any{"op": 31, "d": map[string]any{
				"public_key": encodeKey(pub), "key_id": keyID, "rotation_seconds": 3600,
			}})
		case 33: // CLIENT_KEY_ROTATE
			c.mu.Lock()
			hasKey := c.gcm != nil
			c.mu.Unlock()
			if !hasKey {
				c.close(voiceCloseNotAuth, "Key exchange first")
				return
			}
			// Rotation re-runs the exchange; the client supplies a fresh key.
			var ex struct {
				PublicKey string `json:"public_key"`
			}
			_ = json.Unmarshal(frame.D, &ex)
			if ex.PublicKey != "" {
				// handled by the client sending op 30 again after rotation request
			}
			priv, pub, err := newEphemeralKey()
			if err != nil {
				c.close(voiceCloseUnknownOpcode, "Key generation failed")
				return
			}
			c.mu.Lock()
			peerPub := c.peerPub
			c.mu.Unlock()
			shared, err := curve25519.X25519(priv[:], peerPub[:])
			if err != nil {
				c.close(voiceCloseUnknownOpcode, "Key agreement failed")
				return
			}
			key, err := deriveMediaKey(shared)
			if err != nil {
				c.close(voiceCloseUnknownOpcode, "Key derivation failed")
				return
			}
			gcm, err := newGCM(key)
			if err != nil {
				c.close(voiceCloseUnknownOpcode, "Cipher init failed")
				return
			}
			c.mu.Lock()
			keyID := randomUint32()
			if keyID == 0 {
				keyID = 1
			}
			c.gcm = gcm
			c.keyID = keyID
			c.lastCounter = 0
			c.mu.Unlock()
			c.send(map[string]any{"op": 31, "d": map[string]any{
				"public_key": encodeKey(pub), "key_id": keyID, "rotation_seconds": 3600,
			}})
		case 34: // E2EE_PUBLISH: member static media key for pairwise wraps
			var ex struct {
				PublicKey  string `json:"public_key"`
				SigningKey string `json:"signing_key"`
			}
			if err := json.Unmarshal(frame.D, &ex); err != nil {
				c.close(voiceCloseUnknownOpcode, "Invalid e2ee key")
				return
			}
			pub, err := decodeKey(ex.PublicKey)
			if err != nil {
				c.close(voiceCloseUnknownOpcode, "Invalid e2ee key")
				return
			}
			c.mu.Lock()
			c.e2eePub = pub
			c.e2eeSign = ex.SigningKey
			c.hasE2EE = true
			userID := c.userID
			c.mu.Unlock()
			v.publishE2EEKey(userID, pub, ex.SigningKey)
		case 35: // E2EE_KEY: wrapped key or epoch contribution relayed between members
			var ex struct {
				RecipientUserID string `json:"recipient_user_id"`
				KeyID           uint32 `json:"key_id"`
				Wrapped         string `json:"wrapped"`
				Kind            string `json:"kind"`
			}
			if err := json.Unmarshal(frame.D, &ex); err != nil || ex.RecipientUserID == "" || ex.Wrapped == "" {
				c.close(voiceCloseUnknownOpcode, "Invalid e2ee key relay")
				return
			}
			c.mu.Lock()
			sender := c.userID
			channel := c.channelID
			c.mu.Unlock()
			if channel == "" {
				continue
			}
			v.relayE2EEKey(channel, sender, ex.RecipientUserID, ex.KeyID, ex.Wrapped, ex.Kind)
		case 38: // MLS_COMMIT: signed roster commit relayed to channel members
			var commit json.RawMessage
			if len(frame.D) == 0 || json.Unmarshal(frame.D, &commit) != nil {
				c.close(voiceCloseUnknownOpcode, "Invalid commit")
				return
			}
			c.mu.Lock()
			sender := c.userID
			channel := c.channelID
			c.mu.Unlock()
			if channel == "" {
				continue
			}
			v.relayCommit(channel, sender, commit)
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

func (c *voiceConn) udp() *net.UDPAddr {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.udpAddr
}

func (c *voiceConn) channel() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.channelID
}

// broadcastSpeaking delivers OP 5 to other voice connections in the same channel
// and publishes it for other instances.
func (c *voiceConn) broadcastSpeaking(speaking, delay int, ssrc uint32) {
	v := c.server
	channel := c.channel()
	if v.publish != nil && channel != "" {
		v.publish("VOICE_SPEAKING", map[string]any{
			"channel_id": channel, "speaking": speaking, "delay": delay, "ssrc": ssrc,
		})
	}
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
