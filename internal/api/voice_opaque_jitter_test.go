package api_test

import (
	"context"
	"encoding/binary"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// wce2Frame builds a server-opaque group media envelope with the given counter.
func wce2Frame(counter uint64, ssrc uint32, filler byte) []byte {
	header := make([]byte, 20)
	copy(header[0:4], "WCE2")
	binary.BigEndian.PutUint32(header[4:8], 1)
	binary.BigEndian.PutUint64(header[8:16], counter)
	binary.BigEndian.PutUint32(header[16:20], ssrc)
	body := make([]byte, 16)
	for i := range body {
		body[i] = filler
	}
	return append(header, body...)
}

// TestVoiceOpaqueJitter proves group-E2EE frames (which the server cannot
// decrypt) are still reordered per sender and that a lost counter is released
// after the hold window rather than stalling or being fabricated.
func TestVoiceOpaqueJitter(t *testing.T) {
	h := newHarness(t)
	alice := h.register("opaquealice")
	bob := h.register("opaquebob")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Opaque Guild"}, &guild)
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
	base := "ws" + strings.TrimPrefix(h.server.URL, "http") + "/voice?token="

	bobConn, _, err := websocket.Dial(ctx, base+bobToken, nil)
	if err != nil {
		t.Fatalf("dial bob voice: %v", err)
	}
	defer bobConn.CloseNow()
	deadline := time.Now().Add(20 * time.Second)
	readVoiceOp(t, bobConn, 8, deadline)
	writeVoice(t, bobConn, map[string]any{"op": 0, "d": map[string]any{
		"server_id": guild.ID, "user_id": bob.ID, "session_id": "sessb", "token": bobToken,
	}})
	bobReady := readVoiceOp(t, bobConn, 2, deadline)
	bobD := bobReady["d"].(map[string]any)
	bobSSRC := uint32(bobD["ssrc"].(float64))
	udpIP := bobD["ip"].(string)
	udpPort := int(bobD["port"].(float64))

	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listener: %v", err)
	}
	defer listener.Close()
	writeVoice(t, bobConn, map[string]any{"op": 1, "d": map[string]any{
		"protocol": "udp",
		"data":     map[string]any{"address": udpIP, "port": listener.LocalAddr().(*net.UDPAddr).Port, "mode": "opus"},
	}})
	readVoiceOp(t, bobConn, 4, deadline)

	aliceConn, _, err := websocket.Dial(ctx, base+aliceToken, nil)
	if err != nil {
		t.Fatalf("dial alice voice: %v", err)
	}
	defer aliceConn.CloseNow()
	readVoiceOp(t, aliceConn, 8, deadline)
	writeVoice(t, aliceConn, map[string]any{"op": 0, "d": map[string]any{
		"server_id": guild.ID, "user_id": alice.ID, "session_id": "sessa", "token": aliceToken,
	}})
	aliceReady := readVoiceOp(t, aliceConn, 2, deadline)
	aliceD := aliceReady["d"].(map[string]any)
	aliceSSRC := uint32(aliceD["ssrc"].(float64))
	if aliceSSRC == bobSSRC || aliceSSRC == 0 {
		t.Fatalf("bad ssrcs alice=%d bob=%d", aliceSSRC, bobSSRC)
	}

	udpAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(udpIP, strconv.Itoa(udpPort)))
	if err != nil {
		t.Fatal(err)
	}
	sender, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		t.Fatalf("sender dial: %v", err)
	}
	defer sender.Close()

	recv := func() []byte {
		_ = listener.SetReadDeadline(time.Now().Add(6 * time.Second))
		buf := make([]byte, 1500)
		n, _, err := listener.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("listener read: %v", err)
		}
		return append([]byte(nil), buf[:n]...)
	}

	// Arrival order 2,1,3 must leave as 1,2,3.
	for _, c := range []uint64{2, 1, 3} {
		if _, err := sender.Write(wce2Frame(c, aliceSSRC, byte(c))); err != nil {
			t.Fatalf("send %d: %v", c, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	for want := uint64(1); want <= 3; want++ {
		pkt := recv()
		if string(pkt[0:4]) != "WCE2" {
			t.Fatalf("frame %d magic %q", want, pkt[0:4])
		}
		got := binary.BigEndian.Uint64(pkt[8:16])
		if got != want {
			t.Fatalf("emitted counter %d, want %d", got, want)
		}
		if pkt[20] != byte(want) {
			t.Fatalf("counter %d payload %d", want, pkt[20])
		}
		if binary.BigEndian.Uint32(pkt[16:20]) != aliceSSRC {
			t.Fatalf("counter %d wrong ssrc", want)
		}
	}

	// Counter 4 is dropped: 5 and 6 must arrive after the gap window, in order,
	// with no fabricated frame for 4.
	for _, c := range []uint64{5, 6} {
		if _, err := sender.Write(wce2Frame(c, aliceSSRC, byte(c))); err != nil {
			t.Fatalf("send gap %d: %v", c, err)
		}
	}
	for _, want := range []uint64{5, 6} {
		pkt := recv()
		got := binary.BigEndian.Uint64(pkt[8:16])
		if got != want {
			t.Fatalf("gap emitted counter %d, want %d", got, want)
		}
		if pkt[20] != byte(want) {
			t.Fatalf("gap counter %d payload corrupted", want)
		}
	}

	// A late duplicate of an already emitted counter is dropped.
	if _, err := sender.Write(wce2Frame(2, aliceSSRC, 0xEE)); err != nil {
		t.Fatal(err)
	}
	_ = listener.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	buf := make([]byte, 1500)
	if n, _, err := listener.ReadFromUDP(buf); err == nil {
		t.Fatalf("unexpected datagram after duplicate: %d bytes", n)
	}
}
