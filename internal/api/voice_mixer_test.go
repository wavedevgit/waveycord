//go:build opus

package api_test

import (
	"encoding/binary"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hraban/opus"
)

func tonePCM(samples int, freq float64, amp float64) []int16 {
	out := make([]int16, samples)
	for i := range out {
		out[i] = int16(amp * math.Sin(2*math.Pi*freq*float64(i)/48000))
	}
	return out
}

func toneAmplitude(samples []int16, freq float64) float64 {
	var re, im float64
	for i, s := range samples {
		ang := 2 * math.Pi * freq * float64(i) / 48000
		re += float64(s) * math.Cos(ang)
		im += float64(s) * math.Sin(ang)
	}
	return math.Hypot(re, im) / float64(len(samples))
}

func rtpWith(enc *opus.Encoder, seq uint16, ssrc uint32, pcm []int16) []byte {
	buf := make([]byte, 4000)
	n, err := enc.Encode(pcm, buf)
	if err != nil {
		panic(err)
	}
	pkt := make([]byte, 12+n)
	pkt[0] = 0x80
	pkt[1] = 0x78
	binary.BigEndian.PutUint16(pkt[2:4], seq)
	binary.BigEndian.PutUint32(pkt[4:8], uint32(seq)*960)
	binary.BigEndian.PutUint32(pkt[8:12], ssrc)
	copy(pkt[12:], buf[:n])
	return pkt
}

// TestVoiceCrossFrameSizeMixing proves the SFU mixes two senders that use
// different Opus frame durations into one 20 ms stream containing both tones.
func TestVoiceCrossFrameSizeMixing(t *testing.T) {
	h := newHarness(t)
	alice := h.register("mixalice")
	bob := h.register("mixbob")
	charlie := h.register("mixcharlie")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Mix Guild"}, &guild)
	var text, voice struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)
	var invite struct{ Code string }
	h.decode("POST", "/channels/"+text.ID+"/invites", alice.Token, map[string]any{}, &invite)
	h.request("POST", "/invites/"+invite.Code, bob.Token, nil)
	h.request("POST", "/invites/"+invite.Code, charlie.Token, nil)

	aliceToken := h.joinVoice(alice, guild.ID, voice.ID)
	bobToken := h.joinVoice(bob, guild.ID, voice.ID)
	charlieToken := h.joinVoice(charlie, guild.ID, voice.ID)
	aliceVoice, aliceReady := voiceEndpoint(t, h, aliceToken, alice.ID)
	bobVoice, bobReady := voiceEndpoint(t, h, bobToken, bob.ID)
	charlieVoice, _ := voiceEndpoint(t, h, charlieToken, charlie.ID)
	aliceRecv := udpListener(t)
	bobRecv := udpListener(t)
	charlieRecv := udpListener(t)
	selectProtocol(t, aliceVoice, aliceRecv)
	selectProtocol(t, bobVoice, bobRecv)
	selectProtocol(t, charlieVoice, charlieRecv)

	dialSink := func(ready map[string]any) *net.UDPConn {
		addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(ready["ip"].(string),
			strconv.Itoa(int(ready["port"].(float64)))))
		if err != nil {
			t.Fatal(err)
		}
		conn, err := net.DialUDP("udp", nil, addr)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	aliceSink := dialSink(aliceReady)
	bobSink := dialSink(bobReady)

	enc20, _ := opus.NewEncoder(48000, 1, opus.AppVoIP)
	enc10, _ := opus.NewEncoder(48000, 1, opus.AppVoIP)
	aliceSSRC := uint32(aliceReady["ssrc"].(float64))
	bobSSRC := uint32(bobReady["ssrc"].(float64))

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		seq := uint16(1)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = aliceSink.Write(rtpWith(enc20, seq, aliceSSRC, tonePCM(960, 440, 6000)))
			seq++
			time.Sleep(20 * time.Millisecond)
		}
	}()
	go func() {
		defer wg.Done()
		seq := uint16(1)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = bobSink.Write(rtpWith(enc10, seq, bobSSRC, tonePCM(480, 880, 6000)))
			seq++
			time.Sleep(10 * time.Millisecond)
		}
	}()
	defer func() {
		close(stop)
		wg.Wait()
	}()

	dec, err := opus.NewDecoder(48000, 1)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]int16, 5760)
	buf := make([]byte, 1500)
	deadline := time.Now().Add(10 * time.Second)
	mixed := false
	for time.Now().Before(deadline) && !mixed {
		_ = charlieRecv.SetReadDeadline(deadline)
		n, _, err := charlieRecv.ReadFromUDP(buf)
		if err != nil {
			break
		}
		samples, err := dec.Decode(buf[12:n], pcm)
		if err != nil || samples < 480 {
			continue
		}
		a440 := toneAmplitude(pcm[:samples], 440)
		a880 := toneAmplitude(pcm[:samples], 880)
		if a440 > 300 && a880 > 300 {
			mixed = true
			t.Logf("mixed frame: samples=%d amp440=%.0f amp880=%.0f", samples, a440, a880)
		}
	}
	if !mixed {
		t.Fatal("no frame contained both tones; mixing failed")
	}

	resp, err := h.server.Client().Get(h.server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw := make([]byte, 16384)
	mn, _ := resp.Body.Read(raw)
	if !strings.Contains(string(raw[:mn]), "waveycord_voice_mixes_total") {
		t.Fatal("mix counter missing")
	}
	_ = bobReady
	_ = charlieVoice
}
