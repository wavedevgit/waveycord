//go:build opus

package api_test

import (
	"encoding/binary"
	"math"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hraban/opus"
)

// TestVoiceOpusTranscode proves real Opus frames are decoded and re-encoded on the
// media path: a client-encoded frame is delivered to a peer and decodes to PCM.
func TestVoiceOpusTranscode(t *testing.T) {
	h := newHarness(t)
	alice := h.register("opusowner")
	bob := h.register("opusmember")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Opus Guild"}, &guild)
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
	aliceRecv := udpListener(t)
	bobRecv := udpListener(t)
	selectProtocol(t, aliceVoice, aliceRecv)
	selectProtocol(t, bobVoice, bobRecv)

	// Encode a 20 ms 440 Hz tone at 48 kHz mono.
	pcm := make([]int16, 960)
	for i := range pcm {
		pcm[i] = int16(8000 * math.Sin(2*math.Pi*440*float64(i)/48000))
	}
	enc, err := opus.NewEncoder(48000, 1, opus.AppVoIP)
	if err != nil {
		t.Fatalf("encoder: %v", err)
	}
	payload := make([]byte, 4000)
	n, err := enc.Encode(pcm, payload)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	payload = payload[:n]

	ssrc := uint32(aliceReady["ssrc"].(float64))
	pkt := make([]byte, 12+len(payload))
	pkt[0] = 0x80
	pkt[1] = 0x78
	binary.BigEndian.PutUint16(pkt[2:4], 1)
	binary.BigEndian.PutUint32(pkt[4:8], 960)
	binary.BigEndian.PutUint32(pkt[8:12], ssrc)
	copy(pkt[12:], payload)

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
	if _, err := sender.Write(pkt); err != nil {
		t.Fatalf("send: %v", err)
	}

	_ = bobRecv.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1500)
	rn, _, err := bobRecv.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("bob receive: %v", err)
	}
	received := buf[:rn]
	if len(received) < 13 {
		t.Fatalf("short packet: %d", len(received))
	}
	dec, err := opus.NewDecoder(48000, 1)
	if err != nil {
		t.Fatalf("decoder: %v", err)
	}
	out := make([]int16, 5760)
	samples, err := dec.Decode(received[12:], out)
	if err != nil {
		t.Fatalf("peer decode: %v", err)
	}
	if samples < 480 || samples > 1920 {
		t.Fatalf("decoded samples = %d", samples)
	}
	var energy float64
	for _, s := range out[:samples] {
		energy += float64(s) * float64(s)
	}
	if rms := math.Sqrt(energy / float64(samples)); rms < 100 {
		t.Fatalf("decoded audio is silent (rms %.1f)", rms)
	}

	resp, err := h.server.Client().Get(h.server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw := make([]byte, 16384)
	mn, _ := resp.Body.Read(raw)
	if !strings.Contains(string(raw[:mn]), "waveycord_voice_transcodes_total 1") {
		t.Fatalf("transcode counter missing:\n%s", raw[:mn])
	}
	_ = bobReady
	_ = bobVoice
}
