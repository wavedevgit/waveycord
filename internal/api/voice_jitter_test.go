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

func opusFrame(t *testing.T, enc *opus.Encoder, seq uint16, ts uint32, ssrc uint32) []byte {
	t.Helper()
	pcm := make([]int16, 960)
	for i := range pcm {
		pcm[i] = int16(6000 * math.Sin(2*math.Pi*440*float64(i)/48000))
	}
	payload := make([]byte, 4000)
	n, err := enc.Encode(pcm, payload)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	pkt := make([]byte, 12+n)
	pkt[0] = 0x80
	pkt[1] = 0x78
	binary.BigEndian.PutUint16(pkt[2:4], seq)
	binary.BigEndian.PutUint32(pkt[4:8], ts)
	binary.BigEndian.PutUint32(pkt[8:12], ssrc)
	copy(pkt[12:], payload[:n])
	return pkt
}

func readRTP(t *testing.T, conn *net.UDPConn, deadline time.Time) []byte {
	t.Helper()
	_ = conn.SetReadDeadline(deadline)
	buf := make([]byte, 1500)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("rtp read: %v", err)
	}
	return buf[:n]
}

// TestVoiceJitterBufferAndPLC proves the Opus path reorders frames and conceals a
// dropped one while the peer still decodes contiguous PCM.
func TestVoiceJitterBufferAndPLC(t *testing.T) {
	h := newHarness(t)
	alice := h.register("jittera")
	bob := h.register("jitterb")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Jitter Guild"}, &guild)
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
	enc, err := opus.NewEncoder(48000, 1, opus.AppVoIP)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := opus.NewDecoder(48000, 1)
	if err != nil {
		t.Fatal(err)
	}

	send := func(seq uint16, gap time.Duration) {
		if _, err := sender.Write(opusFrame(t, enc, seq, uint32(seq)*960, ssrc)); err != nil {
			t.Fatalf("send seq %d: %v", seq, err)
		}
		time.Sleep(gap)
	}

	// Reordered: 1, 3, then 2 within the jitter delay.
	send(1, 2*time.Millisecond)
	send(3, 10*time.Millisecond)
	send(2, 0)

	wantSeqs := []uint16{1, 2, 3}
	for i, want := range wantSeqs {
		pkt := readRTP(t, bobRecv, time.Now().Add(5*time.Second))
		if got := binary.BigEndian.Uint16(pkt[2:4]); got != want {
			t.Fatalf("frame %d seq = %d, want %d", i, got, want)
		}
		pcm := make([]int16, 5760)
		if _, err := dec.Decode(pkt[12:], pcm); err != nil {
			t.Fatalf("decode seq %d: %v", want, err)
		}
	}

	// Dropped frame: 4, 5, 7 -> PLC fills 6 before 7 is emitted.
	send(4, 2*time.Millisecond)
	send(5, 2*time.Millisecond)
	send(7, 0)
	wantSeqs = []uint16{4, 5, 6, 7}
	for i, want := range wantSeqs {
		pkt := readRTP(t, bobRecv, time.Now().Add(5*time.Second))
		if got := binary.BigEndian.Uint16(pkt[2:4]); got != want {
			t.Fatalf("post-drop frame %d seq = %d, want %d", i, got, want)
		}
		pcm := make([]int16, 5760)
		n, err := dec.Decode(pkt[12:], pcm)
		if err != nil || n == 0 {
			t.Fatalf("decode seq %d: n=%d err=%v", want, n, err)
		}
	}

	resp, err := h.server.Client().Get(h.server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw := make([]byte, 16384)
	mn, _ := resp.Body.Read(raw)
	if !strings.Contains(string(raw[:mn]), "waveycord_voice_transcodes_total") {
		t.Fatal("transcode counter missing")
	}
	_ = bobReady
	_ = bobVoice
}
