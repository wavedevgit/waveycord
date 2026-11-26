//go:build opus

package api_test

import (
	"encoding/binary"
	"math"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hraban/opus"
)

// TestClusterWideMixing proves PCM from senders on two instances is mixed once by
// the channel's owner instance and the mixed stream reaches a listener on the other
// instance.
func TestClusterWideMixing(t *testing.T) {
	prev, had := os.LookupEnv("WAVEYCORD_CLUSTER_MIX")
	os.Setenv("WAVEYCORD_CLUSTER_MIX", "1")
	defer func() {
		if had {
			os.Setenv("WAVEYCORD_CLUSTER_MIX", prev)
		} else {
			os.Unsetenv("WAVEYCORD_CLUSTER_MIX")
		}
	}()

	a, b := newPGCluster(t)
	alice := a.register("cmixa")
	bob := a.register("cmixb")
	charlie := a.register("cmixc")
	var guild struct{ ID string }
	a.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Cluster Mix"}, &guild)
	var text, voice struct{ ID string }
	a.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	a.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)
	var invite struct{ Code string }
	a.decode("POST", "/channels/"+text.ID+"/invites", alice.Token, map[string]any{}, &invite)
	for _, acc := range []account{bob, charlie} {
		a.request("POST", "/invites/"+invite.Code, acc.Token, nil)
	}

	aliceToken := a.joinVoice(alice, guild.ID, voice.ID)
	bobToken := b.joinVoice(bob, guild.ID, voice.ID)
	charlieToken := b.joinVoice(charlie, guild.ID, voice.ID)
	aliceVoice, aliceReady := voiceEndpoint(t, a, aliceToken, alice.ID)
	bobVoice, bobReady := voiceEndpoint(t, b, bobToken, bob.ID)
	charlieVoice, _ := voiceEndpoint(t, b, charlieToken, charlie.ID)
	aliceRecv := udpListener(t)
	bobRecv := udpListener(t)
	charlieRecv := udpListener(t)
	selectProtocol(t, aliceVoice, aliceRecv)
	selectProtocol(t, bobVoice, bobRecv)
	selectProtocol(t, charlieVoice, charlieRecv)

	sink := func(ready map[string]any) *net.UDPConn {
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
	aliceSink := sink(aliceReady)
	bobSink := sink(bobReady)

	enc20, _ := opus.NewEncoder(48000, 1, opus.AppVoIP)
	enc10, _ := opus.NewEncoder(48000, 1, opus.AppVoIP)
	aliceSSRC := uint32(aliceReady["ssrc"].(float64))
	bobSSRC := uint32(bobReady["ssrc"].(float64))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	send := func(socket *net.UDPConn, enc *opus.Encoder, ssrc uint32, samples int, freq float64, every time.Duration) {
		defer wg.Done()
		seq := uint16(1)
		for {
			select {
			case <-stop:
				return
			default:
			}
			pcm := make([]int16, samples)
			for i := range pcm {
				pcm[i] = int16(6000 * math.Sin(2*math.Pi*freq*float64(i)/48000))
			}
			buf := make([]byte, 4000)
			n, err := enc.Encode(pcm, buf)
			if err != nil {
				return
			}
			pkt := make([]byte, 12+n)
			pkt[0] = 0x80
			pkt[1] = 0x78
			binary.BigEndian.PutUint16(pkt[2:4], seq)
			binary.BigEndian.PutUint32(pkt[4:8], uint32(seq)*960)
			binary.BigEndian.PutUint32(pkt[8:12], ssrc)
			copy(pkt[12:], buf[:n])
			_, _ = socket.Write(pkt)
			seq++
			time.Sleep(every)
		}
	}
	go send(aliceSink, enc20, aliceSSRC, 960, 440, 20*time.Millisecond)
	go send(bobSink, enc10, bobSSRC, 480, 880, 10*time.Millisecond)
	defer func() { close(stop); wg.Wait() }()

	dec, err := opus.NewDecoder(48000, 1)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]int16, 5760)
	buf := make([]byte, 1500)
	deadline := time.Now().Add(15 * time.Second)
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
		if toneAmplitude(pcm[:samples], 440) > 250 && toneAmplitude(pcm[:samples], 880) > 250 {
			mixed = true
			t.Logf("cluster mixed frame: samples=%d", samples)
		}
	}
	if !mixed {
		t.Fatal("listener on the other instance never received a mixed frame")
	}
	_ = bobReady
}
