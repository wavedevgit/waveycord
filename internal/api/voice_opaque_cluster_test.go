package api_test

import (
	"encoding/binary"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestClusterOpaqueGapShedding proves loss handling for server-opaque group media
// across two instances: frames are delivered in counter order, a hole is shed after
// the hold window (no concealment frame is possible), and an already emitted counter
// is not delivered twice.
func TestClusterOpaqueGapShedding(t *testing.T) {
	a, b := newPGCluster(t)
	alice := a.register("gapalice")
	bob := b.register("gapbob")
	var guild struct{ ID string }
	a.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Gap Guild"}, &guild)
	var text, voice struct{ ID string }
	a.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	a.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)
	var invite struct{ Code string }
	a.decode("POST", "/channels/"+text.ID+"/invites", alice.Token, map[string]any{}, &invite)
	b.request("POST", "/invites/"+invite.Code, bob.Token, nil)

	aliceToken := a.joinVoice(alice, guild.ID, voice.ID)
	bobToken := b.joinVoice(bob, guild.ID, voice.ID)
	aliceVoice, aliceReady := voiceEndpoint(t, a, aliceToken, alice.ID)
	bobVoice, _ := voiceEndpoint(t, b, bobToken, bob.ID)
	aliceRecv := udpListener(t)
	bobRecv := udpListener(t)
	selectProtocol(t, aliceVoice, aliceRecv)
	selectProtocol(t, bobVoice, bobRecv)

	ssrc := uint32(aliceReady["ssrc"].(float64))
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

	send := func(counter uint64) {
		t.Helper()
		header := make([]byte, 20)
		copy(header[0:4], "WCE2")
		binary.BigEndian.PutUint32(header[4:8], 1)
		binary.BigEndian.PutUint64(header[8:16], counter)
		binary.BigEndian.PutUint32(header[16:20], ssrc)
		body := make([]byte, 8)
		for i := range body {
			body[i] = byte(counter)
		}
		if _, err := sender.Write(append(header, body...)); err != nil {
			t.Fatalf("send %d: %v", counter, err)
		}
	}

	// 1,2,3 then a hole at 4..9 and a resumed stream at 10,11.
	for _, c := range []uint64{1, 2, 3} {
		send(c)
		time.Sleep(3 * time.Millisecond)
	}
	for _, c := range []uint64{10, 11} {
		send(c)
		time.Sleep(3 * time.Millisecond)
	}

	want := []uint64{1, 2, 3, 10, 11}
	gotCounters := make([]uint64, 0, len(want))
	for i := 0; i < len(want); i++ {
		_ = bobRecv.SetReadDeadline(time.Now().Add(10 * time.Second))
		buf := make([]byte, 1500)
		n, _, err := bobRecv.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("listener frame %d: %v (got %v)", i, err, gotCounters)
		}
		if n < 20 || string(buf[0:4]) != "WCE2" {
			t.Fatalf("frame %d is not an opaque envelope", i)
		}
		gotCounters = append(gotCounters, binary.BigEndian.Uint64(buf[8:16]))
	}
	for i := range want {
		if gotCounters[i] != want[i] {
			t.Fatalf("delivery order %v, want %v", gotCounters, want)
		}
	}

	// Nothing is fabricated for the missing counters, and no later frame leaks in.
	_ = bobRecv.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	extra := make([]byte, 1500)
	if n, _, err := bobRecv.ReadFromUDP(extra); err == nil {
		t.Fatalf("unexpected extra frame counter %d", binary.BigEndian.Uint64(extra[8:16]))
		_ = n
	}

	// A replayed counter is dropped at the receiving instance.
	send(11)
	_ = bobRecv.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, _, err := bobRecv.ReadFromUDP(extra); err == nil {
		t.Fatal("duplicate counter was delivered")
	}

	// The gap is visible in the receiver's metrics; no concealment frame exists.
	resp, err := b.server.Client().Get(b.server.URL + "/metrics")
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	defer resp.Body.Close()
	rawBody := make([]byte, 16384)
	rn, _ := resp.Body.Read(rawBody)
	metricsText := string(rawBody[:rn])
	if !strings.Contains(metricsText, "waveycord_voice_e2ee_gaps_total 1") &&
		!strings.Contains(metricsText, "waveycord_voice_e2ee_gaps_total 2") {
		t.Fatalf("gap counter missing:\n%s", metricsText)
	}
	if strings.Contains(metricsText, "waveycord_voice_mixes_total 1") {
		t.Fatalf("opaque frames must not be mixed:\n%s", metricsText)
	}
}
