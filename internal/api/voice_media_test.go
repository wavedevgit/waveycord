package api_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func (h *harness) joinVoice(acc account, guildID, channelID string) string {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.server.URL, "http")+"/gateway", nil)
	if err != nil {
		h.t.Fatalf("dial gateway: %v", err)
	}
	h.t.Cleanup(func() { conn.CloseNow() })
	deadline := time.Now().Add(10 * time.Second)
	readFrame(h.t, conn, deadline)
	writeFrame(h.t, conn, map[string]any{"op": 2, "d": map[string]any{"token": acc.Token, "intents": 0}})
	if f := readFrame(h.t, conn, deadline); f["t"] != "READY" {
		h.t.Fatalf("expected READY: %+v", f)
	}
	writeFrame(h.t, conn, map[string]any{"op": 14, "d": map[string]any{"guild_id": guildID}})
	time.Sleep(30 * time.Millisecond)
	writeFrame(h.t, conn, map[string]any{"op": 4, "d": map[string]any{
		"guild_id": guildID, "channel_id": channelID,
	}})
	for i := 0; i < 10; i++ {
		f := readFrame(h.t, conn, deadline)
		if f["t"] != "VOICE_SERVER_UPDATE" {
			continue
		}
		d := f["d"].(map[string]any)
		token, _ := d["token"].(string)
		if token == "" {
			h.t.Fatalf("empty voice token: %+v", d)
		}
		return token
	}
	h.t.Fatal("no VOICE_SERVER_UPDATE")
	return ""
}

func readVoice(t *testing.T, conn *websocket.Conn, deadline time.Time) map[string]any {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("voice read: %v", err)
	}
	var frame map[string]any
	if err := json.Unmarshal(data, &frame); err != nil {
		t.Fatalf("voice decode %q: %v", data, err)
	}
	return frame
}

func writeVoice(t *testing.T, conn *websocket.Conn, frame map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(frame)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatalf("voice write: %v", err)
	}
}

func TestVoiceMediaBoundary(t *testing.T) {
	h := newHarness(t)
	alice := h.register("mediaowner")
	bob := h.register("mediamember")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Media Guild"}, &guild)
	var text, voice struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)
	var invite struct{ Code string }
	h.decode("POST", "/channels/"+text.ID+"/invites", alice.Token, map[string]any{}, &invite)
	h.request("POST", "/invites/"+invite.Code, bob.Token, nil)

	bobToken := h.joinVoice(bob, guild.ID, voice.ID)
	aliceToken := h.joinVoice(alice, guild.ID, voice.ID)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	voiceURL := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/voice?token=" + bobToken
	bobConn, _, err := websocket.Dial(ctx, voiceURL, nil)
	if err != nil {
		t.Fatalf("dial voice: %v", err)
	}
	defer bobConn.CloseNow()
	deadline := time.Now().Add(10 * time.Second)
	if hello := readVoice(t, bobConn, deadline); hello["op"].(float64) != 8 {
		t.Fatalf("expected voice HELLO: %+v", hello)
	}
	writeVoice(t, bobConn, map[string]any{"op": 0, "d": map[string]any{
		"server_id": guild.ID, "user_id": bob.ID, "session_id": "sess", "token": bobToken,
	}})
	ready := readVoiceOp(t, bobConn, 2, deadline)
	d := ready["d"].(map[string]any)
	ssrc := uint32(d["ssrc"].(float64))
	udpIP, _ := d["ip"].(string)
	udpPort := int(d["port"].(float64))
	modes := d["modes"].([]any)
	if ssrc == 0 || udpIP != "127.0.0.1" || udpPort == 0 || len(modes) != 1 || modes[0] != "opus" {
		t.Fatalf("unexpected voice READY %+v", d)
	}

	// UDP discovery: 74-byte request, 66-byte response echoing the ssrc.
	udpAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(udpIP, strconv.Itoa(udpPort)))
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		t.Fatalf("udp dial: %v", err)
	}
	defer udp.Close()
	discover := make([]byte, 74)
	binary.BigEndian.PutUint16(discover[0:2], 1)
	binary.BigEndian.PutUint16(discover[2:4], 70)
	binary.BigEndian.PutUint32(discover[4:8], ssrc)
	if _, err := udp.Write(discover); err != nil {
		t.Fatalf("udp write: %v", err)
	}
	_ = udp.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp := make([]byte, 74)
	n, err := udp.Read(resp)
	if err != nil {
		t.Fatalf("udp discovery read: %v", err)
	}
	if n != 74 || binary.BigEndian.Uint32(resp[4:8]) != ssrc {
		t.Fatalf("bad discovery response n=%d ssrc=%d", n, binary.BigEndian.Uint32(resp[4:8]))
	}
	if ip := strings.TrimRight(string(resp[8:72]), "\x00"); ip != udpIP {
		t.Fatalf("discovery ip = %q", ip)
	}
	if port := binary.BigEndian.Uint16(resp[72:74]); int(port) != udpPort {
		t.Fatalf("discovery port = %d want %d", port, udpPort)
	}

	// SELECT_PROTOCOL completes the handshake with an opus session description.
	writeVoice(t, bobConn, map[string]any{"op": 1, "d": map[string]any{
		"protocol": "udp",
		"data":     map[string]any{"address": udpIP, "port": udpPort, "mode": "opus"},
	}})
	session := readVoiceOp(t, bobConn, 4, deadline)
	sd := session["d"].(map[string]any)
	key := sd["secret_key"].([]any)
	if sd["mode"] != "opus" || len(key) != 32 {
		t.Fatalf("unexpected session description %+v", sd)
	}

	// A second user's voice connection receives the speaking event.
	aliceConn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.server.URL, "http")+"/voice?token="+aliceToken, nil)
	if err != nil {
		t.Fatalf("dial alice voice: %v", err)
	}
	defer aliceConn.CloseNow()
	readVoice(t, aliceConn, deadline)
	writeVoice(t, aliceConn, map[string]any{"op": 0, "d": map[string]any{
		"server_id": guild.ID, "user_id": alice.ID, "session_id": "sess2", "token": aliceToken,
	}})
	readVoiceOp(t, aliceConn, 2, deadline)
	writeVoice(t, bobConn, map[string]any{"op": 5, "d": map[string]any{"speaking": 1, "delay": 0, "ssrc": ssrc}})
	sp := readVoice(t, aliceConn, deadline)
	if sp["op"].(float64) != 5 {
		t.Fatalf("expected SPEAKING: %+v", sp)
	}
	sdSp := sp["d"].(map[string]any)
	if int(sdSp["speaking"].(float64)) != 1 || uint32(sdSp["ssrc"].(float64)) != ssrc {
		t.Fatalf("unexpected speaking payload %+v", sdSp)
	}

	// Heartbeat is acknowledged.
	writeVoice(t, bobConn, map[string]any{"op": 3, "d": map[string]any{"t": 1}})
	for i := 0; i < 5; i++ {
		f := readVoice(t, bobConn, deadline)
		if f["op"].(float64) == 6 {
			break
		}
		if i == 4 {
			t.Fatal("no heartbeat ack")
		}
	}

	// RTP packets reach the UDP socket and are counted in metrics.
	rtp := make([]byte, 12+4)
	rtp[0] = 0x80
	rtp[1] = 0x78
	binary.BigEndian.PutUint16(rtp[2:4], 1)
	binary.BigEndian.PutUint32(rtp[4:8], ssrc)
	if _, err := udp.Write(rtp); err != nil {
		t.Fatalf("rtp write: %v", err)
	}
	deadlineMetrics := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadlineMetrics) {
		resp, err := h.server.Client().Get(h.server.URL + "/metrics")
		if err != nil {
			t.Fatalf("metrics: %v", err)
		}
		buf := make([]byte, 8192)
		n, _ := resp.Body.Read(buf)
		resp.Body.Close()
		text := string(buf[:n])
		if strings.Contains(text, "waveycord_voice_rtp_packets_total 1") ||
			strings.Contains(text, "waveycord_voice_rtp_packets_total 2") {
			if !strings.Contains(text, "waveycord_voice_speaking_total 1") {
				t.Fatalf("speaking counter missing:\n%s", text)
			}
			fmt.Println("voice media boundary verified")
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("RTP packet was not counted")
}
