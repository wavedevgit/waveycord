package api_test

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/hkdf"
)

// Simplified TreeKEM-style epoch secret: every member contributes a per-epoch
// value derived from its own leaf key, contributions are wrapped pairwise, and the
// epoch media key is derived from the XOR of all contributions. No single member
// or the server can derive the epoch secret alone.

func mlsContribution(leafPriv [32]byte, epoch uint32) []byte {
	r := hkdf.New(sha256.New, leafPriv[:], []byte("waveycord-mls-epoch"),
		[]byte("epoch:"+strconv.FormatUint(uint64(epoch), 10)))
	out := make([]byte, 32)
	_, _ = r.Read(out)
	return out
}

func xorContribs(parts ...[]byte) []byte {
	out := make([]byte, 32)
	for _, p := range parts {
		for i := 0; i < 32 && i < len(p); i++ {
			out[i] ^= p[i]
		}
	}
	return out
}

func mlsMediaKey(epochSecret []byte) cipher.AEAD {
	r := hkdf.New(sha256.New, epochSecret, []byte("waveycord-mls-media"), []byte("rtp"))
	key := make([]byte, 32)
	_, _ = r.Read(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return g
}

// TestVoiceMLSEpochRatchet proves the epoch secret is jointly contributed, that
// current members derive it after a departure, and that the departed member cannot.
func TestVoiceMLSEpochRatchet(t *testing.T) {
	a, b := newPGCluster(t)
	alice := a.register("mlsa")
	bob := a.register("mlsb")
	charlie := a.register("mlsc")
	var guild struct{ ID string }
	a.decode("POST", "/guilds", alice.Token, map[string]any{"name": "MLS Guild"}, &guild)
	var text, voice struct{ ID string }
	a.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	a.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)
	var invite struct{ Code string }
	a.decode("POST", "/channels/"+text.ID+"/invites", alice.Token, map[string]any{}, &invite)
	for _, acc := range []account{bob, charlie} {
		a.request("POST", "/invites/"+invite.Code, acc.Token, nil)
	}

	_, aliceToken := a.joinVoiceConn(alice, guild.ID, voice.ID)
	_, bobToken := b.joinVoiceConn(bob, guild.ID, voice.ID)
	charlieMain, charlieToken := a.joinVoiceConn(charlie, guild.ID, voice.ID)
	aliceVoice, aliceReady := voiceEndpoint(t, a, aliceToken, alice.ID)
	bobVoice, bobReady := voiceEndpoint(t, b, bobToken, bob.ID)
	charlieVoice, _ := voiceEndpoint(t, a, charlieToken, charlie.ID)
	aliceRecv := udpListener(t)
	bobRecv := udpListener(t)
	charlieRecv := udpListener(t)
	selectProtocol(t, aliceVoice, aliceRecv)
	selectProtocol(t, bobVoice, bobRecv)
	selectProtocol(t, charlieVoice, charlieRecv)

	alicePriv, alicePub := clientKeypair(t)
	bobPriv, bobPub := clientKeypair(t)
	charliePriv, charliePub := clientKeypair(t)
	writeVoice(t, aliceVoice, map[string]any{"op": 34, "d": map[string]any{"public_key": encodeB64(alicePub)}})
	writeVoice(t, bobVoice, map[string]any{"op": 34, "d": map[string]any{"public_key": encodeB64(bobPub)}})
	writeVoice(t, charlieVoice, map[string]any{"op": 34, "d": map[string]any{"public_key": encodeB64(charliePub)}})
	learnKeys(t, aliceVoice, []string{bob.ID, charlie.ID})
	learnKeys(t, bobVoice, []string{alice.ID, charlie.ID})
	learnKeys(t, charlieVoice, []string{alice.ID, bob.ID})

	privs := map[string][32]byte{alice.ID: alicePriv, bob.ID: bobPriv, charlie.ID: charliePriv}
	pubs := map[string][32]byte{alice.ID: alicePub, bob.ID: bobPub, charlie.ID: charliePub}
	conns := map[string]*voiceTestConn{
		alice.ID:   {conn: aliceVoice, udp: aliceRecv},
		bob.ID:     {conn: bobVoice, udp: bobRecv},
		charlie.ID: {conn: charlieVoice, udp: charlieRecv},
	}
	_ = conns

	// Each member publishes its contribution for an epoch to every other member.
	sendContrib := func(from string, to []string, epoch uint32) {
		t.Helper()
		contrib := mlsContribution(privs[from], epoch)
		for _, peer := range to {
			wrapped := wrapGroupKey(t, e2eePairwise(t, privs[from], pubs[peer]), contrib)
			writeVoice(t, voiceConnOf(conns, from), map[string]any{"op": 35, "d": map[string]any{
				"recipient_user_id": peer, "key_id": epoch, "wrapped": wrapped, "kind": "contrib",
			}})
		}
	}
	// Collect peer contributions and fold them into the epoch secret.
	collectEpoch := func(self string, epoch uint32, peers []string) cipher.AEAD {
		t.Helper()
		secret := mlsContribution(privs[self], epoch)
		need := map[string]bool{}
		for _, p := range peers {
			need[p] = true
		}
		until := time.Now().Add(10 * time.Second)
		for len(need) > 0 && time.Now().Before(until) {
			f := readVoice(t, voiceConnOf(conns, self), until)
			if f["op"].(float64) != 35 {
				continue
			}
			d := f["d"].(map[string]any)
			if d["kind"] != "contrib" || uint32(d["key_id"].(float64)) != epoch {
				continue
			}
			from, _ := d["from_user_id"].(string)
			if !need[from] {
				continue
			}
			wrapped := d["wrapped"].(string)
			raw, err := base64.StdEncoding.DecodeString(wrapped)
			if err != nil || len(raw) < 12+16 {
				t.Fatalf("bad wrapped contribution")
			}
			plain, err := e2eePairwise(t, privs[self], pubs[from]).Open(nil, raw[:12], raw[12:], []byte("group"))
			if err != nil {
				t.Fatalf("unwrap contribution: %v", err)
			}
			secret = xorContribs(secret, plain)
			delete(need, from)
		}
		if len(need) > 0 {
			t.Fatalf("missing contributions from %v", need)
		}
		return mlsMediaKey(secret)
	}

	const epoch1 = uint32(1)
	sendContrib(alice.ID, []string{bob.ID, charlie.ID}, epoch1)
	sendContrib(bob.ID, []string{alice.ID, charlie.ID}, epoch1)
	sendContrib(charlie.ID, []string{alice.ID, bob.ID}, epoch1)
	aliceGCM1 := collectEpoch(alice.ID, epoch1, []string{bob.ID, charlie.ID})
	bobGCM1 := collectEpoch(bob.ID, epoch1, []string{alice.ID, charlie.ID})
	charlieGCM1 := collectEpoch(charlie.ID, epoch1, []string{alice.ID, bob.ID})

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
	inner := rtpPacket(ssrc, "MLS-EPOCH1")
	sealed1 := sealGroupMedia(aliceGCM1, epoch1, 1, ssrc, inner)
	if _, err := sender.Write(sealed1); err != nil {
		t.Fatal(err)
	}
	readDecrypt := func(udp *net.UDPConn, gcm cipher.AEAD, keyID uint32, want string) {
		t.Helper()
		_ = udp.SetReadDeadline(time.Now().Add(10 * time.Second))
		buf := make([]byte, 1500)
		n, _, err := udp.ReadFromUDP(buf)
		if err != nil {
			t.Fatalf("media receive: %v", err)
		}
		if binary.BigEndian.Uint32(buf[4:8]) != keyID {
			t.Fatalf("unexpected key id")
		}
		counter := binary.BigEndian.Uint64(buf[8:16])
		plain, err := gcm.Open(nil, clientNonce(counter), buf[20:n], buf[0:20])
		if err != nil {
			t.Fatalf("decrypt %s: %v", want, err)
		}
		if !bytes.Contains(plain, []byte(want)) {
			t.Fatalf("payload mismatch")
		}
	}
	readDecrypt(bobRecv, bobGCM1, epoch1, "MLS-EPOCH1")
	readDecrypt(charlieRecv, charlieGCM1, epoch1, "MLS-EPOCH1")

	// Charlie leaves; Alice and Bob rotate to epoch 2 without him.
	writeFrame(t, charlieMain, map[string]any{"op": 4, "d": map[string]any{
		"guild_id": guild.ID, "channel_id": nil,
	}})
	_ = readVoiceOp(t, bobVoice, 36, time.Now().Add(10*time.Second))

	const epoch2 = uint32(2)
	sendContrib(alice.ID, []string{bob.ID}, epoch2)
	sendContrib(bob.ID, []string{alice.ID}, epoch2)
	// A contribution wrapped for Charlie must not be delivered.
	charlieContrib2 := mlsContribution(charliePriv, epoch2)
	writeVoice(t, aliceVoice, map[string]any{"op": 35, "d": map[string]any{
		"recipient_user_id": charlie.ID, "key_id": epoch2, "kind": "contrib",
		"wrapped": wrapGroupKey(t, e2eePairwise(t, alicePriv, charliePub), charlieContrib2),
	}})
	aliceGCM2 := collectEpoch(alice.ID, epoch2, []string{bob.ID})
	bobGCM2 := collectEpoch(bob.ID, epoch2, []string{alice.ID})

	inner2 := rtpPacket(ssrc, "MLS-EPOCH2")
	sealed2 := sealGroupMedia(aliceGCM2, epoch2, 2, ssrc, inner2)
	if _, err := sender.Write(sealed2); err != nil {
		t.Fatal(err)
	}
	readDecrypt(bobRecv, bobGCM2, epoch2, "MLS-EPOCH2")

	// The departed member cannot derive the epoch-2 secret: he only holds his own
	// contribution, and the remaining contributions were never relayed to him.
	deadline := time.Now().Add(1200 * time.Millisecond)
	sawNewContrib := false
	for time.Now().Before(deadline) {
		f, ok := tryReadVoice(charlieVoice, time.Until(deadline))
		if !ok {
			break
		}
		if f["op"].(float64) == 35 && uint32(f["d"].(map[string]any)["key_id"].(float64)) == epoch2 {
			sawNewContrib = true
		}
	}
	if sawNewContrib {
		t.Fatal("departed member received epoch-2 contributions")
	}
	charlieCandidate := mlsMediaKey(xorContribs(charlieContrib2))
	counter := binary.BigEndian.Uint64(sealed2[8:16])
	if _, err := charlieCandidate.Open(nil, clientNonce(counter), sealed2[20:], sealed2[0:20]); err == nil {
		t.Fatal("departed member derived the epoch-2 secret")
	}
	_ = bobReady
}

// voiceTestConn pairs a control websocket with its media socket.
type voiceTestConn struct {
	conn *websocket.Conn
	udp  *net.UDPConn
}

func voiceConnOf(m map[string]*voiceTestConn, id string) *websocket.Conn {
	if c, ok := m[id]; ok {
		return c.conn
	}
	return nil
}
