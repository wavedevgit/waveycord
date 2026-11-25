package api_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// voiceEndpoint performs the voice handshake and returns the connection plus the
// READY payload (ssrc, ip, port).
func voiceEndpoint(t *testing.T, h *harness, token, userID string) (*websocket.Conn, map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.server.URL, "http")+"/voice?token="+token, nil)
	if err != nil {
		t.Fatalf("dial voice: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	deadline := time.Now().Add(10 * time.Second)
	if f := readVoice(t, conn, deadline); f["op"].(float64) != 8 {
		t.Fatalf("expected voice HELLO: %+v", f)
	}
	writeVoice(t, conn, map[string]any{"op": 0, "d": map[string]any{
		"user_id": userID, "token": token,
	}})
	ready := readVoice(t, conn, deadline)
	if ready["op"].(float64) != 2 {
		t.Fatalf("expected voice READY: %+v", ready)
	}
	return conn, ready["d"].(map[string]any)
}

func udpListener(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("udp listen: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func selectProtocol(t *testing.T, conn *websocket.Conn, recv *net.UDPConn) {
	t.Helper()
	writeVoice(t, conn, map[string]any{"op": 1, "d": map[string]any{
		"protocol": "udp",
		"data": map[string]any{
			"address": "127.0.0.1",
			"port":    recv.LocalAddr().(*net.UDPAddr).Port,
			"mode":    "opus",
		},
	}})
	deadline := time.Now().Add(5 * time.Second)
	if f := readVoice(t, conn, deadline); f["op"].(float64) != 4 {
		t.Fatalf("expected SESSION_DESCRIPTION: %+v", f)
	}
}

func rtpPacket(ssrc uint32, payload string) []byte {
	pkt := make([]byte, 12+len(payload))
	pkt[0] = 0x80
	pkt[1] = 0x78
	binary.BigEndian.PutUint16(pkt[2:4], 7)
	binary.BigEndian.PutUint32(pkt[4:8], 99)
	binary.BigEndian.PutUint32(pkt[8:12], ssrc)
	copy(pkt[12:], payload)
	return pkt
}

// TestVoiceRTPFanout proves a packet sent by one voice connection is forwarded to
// another connection in the same channel, and not echoed to the sender.
func TestVoiceRTPFanout(t *testing.T) {
	h := newHarness(t)
	alice := h.register("sfuowner")
	bob := h.register("sfumember")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "SFU Guild"}, &guild)
	var text, voice struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)
	var invite struct{ Code string }
	h.decode("POST", "/channels/"+text.ID+"/invites", alice.Token, map[string]any{}, &invite)
	h.request("POST", "/invites/"+invite.Code, bob.Token, nil)

	aliceToken := h.joinVoice(alice, guild.ID, voice.ID)
	bobToken := h.joinVoice(bob, guild.ID, voice.ID)

	aliceVoice, aliceReady := voiceEndpoint(t, h, aliceToken, alice.ID)
	bobVoice, bobReady := voiceEndpoint(t, h, bobToken, bob.ID)
	aliceSSRC := uint32(aliceReady["ssrc"].(float64))
	bobSSRC := uint32(bobReady["ssrc"].(float64))

	aliceRecv := udpListener(t)
	bobRecv := udpListener(t)
	selectProtocol(t, aliceVoice, aliceRecv)
	selectProtocol(t, bobVoice, bobRecv)

	// Bob sends RTP to the voice server's UDP socket.
	serverAddr := net.JoinHostPort(bobReady["ip"].(string), strconv.Itoa(int(bobReady["port"].(float64))))
	dst, err := net.ResolveUDPAddr("udp", serverAddr)
	if err != nil {
		t.Fatal(err)
	}
	sender, err := net.DialUDP("udp", nil, dst)
	if err != nil {
		t.Fatalf("sender dial: %v", err)
	}
	defer sender.Close()
	packet := rtpPacket(bobSSRC, "AUDIO-BOB")
	if _, err := sender.Write(packet); err != nil {
		t.Fatalf("send rtp: %v", err)
	}

	// Alice receives the same payload.
	_ = aliceRecv.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := aliceRecv.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("alice receive: %v", err)
	}
	if !bytes.Equal(buf[:n], packet) {
		t.Fatalf("forwarded packet mismatch: %x vs %x", buf[:n], packet)
	}

	// The sender does not get its own packet back.
	_ = bobRecv.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, _, err := bobRecv.ReadFromUDP(buf); err == nil {
		t.Fatal("sender received its own RTP packet")
	}

	// Counters reflect the forward.
	resp, err := h.server.Client().Get(h.server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw := make([]byte, 16384)
	rn, _ := resp.Body.Read(raw)
	metricsText := string(raw[:rn])
	if !strings.Contains(metricsText, "waveycord_voice_rtp_forwarded_total 1") {
		t.Fatalf("forwarded counter missing:\n%s", metricsText)
	}
	_ = aliceSSRC
}

// TestClusterVoiceRTPFanout proves the same forward crosses instances via the
// bus: Alice sends to instance A, Bob receives from instance B.
func TestClusterVoiceRTPFanout(t *testing.T) {
	a, b := newPGCluster(t)
	alice := a.register("clustersfua")
	bob := a.register("clustersfub")
	var guild struct{ ID string }
	a.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Cluster SFU"}, &guild)
	var text, voice struct{ ID string }
	a.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	a.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)
	var invite struct{ Code string }
	a.decode("POST", "/channels/"+text.ID+"/invites", alice.Token, map[string]any{}, &invite)
	a.request("POST", "/invites/"+invite.Code, bob.Token, nil)

	aliceToken := a.joinVoice(alice, guild.ID, voice.ID)
	bobToken := b.joinVoice(bob, guild.ID, voice.ID)
	aliceVoice, aliceReady := voiceEndpoint(t, a, aliceToken, alice.ID)
	bobVoice, bobReady := voiceEndpoint(t, b, bobToken, bob.ID)
	aliceRecv := udpListener(t)
	bobRecv := udpListener(t)
	selectProtocol(t, aliceVoice, aliceRecv)
	selectProtocol(t, bobVoice, bobRecv)

	dst, err := net.ResolveUDPAddr("udp", net.JoinHostPort(aliceReady["ip"].(string),
		strconv.Itoa(int(aliceReady["port"].(float64)))))
	if err != nil {
		t.Fatal(err)
	}
	sender, err := net.DialUDP("udp", nil, dst)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	packet := rtpPacket(uint32(aliceReady["ssrc"].(float64)), "AUDIO-CROSS")
	if _, err := sender.Write(packet); err != nil {
		t.Fatal(err)
	}

	_ = bobRecv.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := bobRecv.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("bob receive cross-instance: %v", err)
	}
	if !bytes.Equal(buf[:n], packet) {
		t.Fatalf("cross-instance packet mismatch: %x vs %x", buf[:n], packet)
	}
	_ = bobReady
	_ = bobVoice
	_ = json.Marshal
}
