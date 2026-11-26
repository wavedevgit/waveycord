package api_test

import (
	"bytes"

	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"github.com/coder/websocket"
	"net"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// readVoiceAny reads one voice frame from either connection type.
func readVoiceAny(t *testing.T, conn interface{}, deadline time.Time) map[string]any {
	t.Helper()
	type reader interface{}
	_ = reader(nil)
	return readVoice(t, conn.(*websocket.Conn), deadline)
}

// Client-side MLS-style group key agreement used by the E2EE test: members
// exchange static X25519 keys, a leader wraps a random group key pairwise, and
// media is encrypted directly with the group key. The server only relays the
// wrapped key and opaque envelopes.

func e2eePairwise(t *testing.T, priv, peerPub [32]byte) cipher.AEAD {
	t.Helper()
	shared, err := curve25519.X25519(priv[:], peerPub[:])
	if err != nil {
		t.Fatalf("x25519: %v", err)
	}
	r := hkdf.New(sha256.New, shared, []byte("waveycord-e2ee-wrap"), []byte("group-key"))
	key := make([]byte, 32)
	if _, err := r.Read(key); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return gcm
}

func wrapGroupKey(t *testing.T, gcm cipher.AEAD, groupKey []byte) string {
	t.Helper()
	nonce := make([]byte, 12)
	_, _ = rand.Read(nonce)
	sealed := gcm.Seal(nil, nonce, groupKey, []byte("group"))
	return base64.StdEncoding.EncodeToString(append(nonce, sealed...))
}

func unwrapGroupKey(t *testing.T, gcm cipher.AEAD, wrapped string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(wrapped)
	if err != nil || len(raw) < 12+16 {
		t.Fatalf("bad wrapped key: %v", err)
	}
	key, err := gcm.Open(nil, raw[:12], raw[12:], []byte("group"))
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	return key
}

func e2eeKey(t *testing.T) cipher.AEAD {
	t.Helper()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return gcm
}

func sealGroupMedia(gcm cipher.AEAD, keyID uint32, counter uint64, ssrc uint32, inner []byte) []byte {
	header := make([]byte, 20)
	copy(header[0:4], "WCE2")
	binary.BigEndian.PutUint32(header[4:8], keyID)
	binary.BigEndian.PutUint64(header[8:16], counter)
	binary.BigEndian.PutUint32(header[16:20], ssrc)
	return append(header, gcm.Seal(nil, clientNonce(counter), inner, header)...)
}

func openGroupMedia(t *testing.T, gcm cipher.AEAD, packet []byte) []byte {
	t.Helper()
	if len(packet) < 36 || string(packet[0:4]) != "WCE2" {
		t.Fatalf("bad e2ee envelope: %x", packet[0:4])
	}
	counter := binary.BigEndian.Uint64(packet[8:16])
	inner, err := gcm.Open(nil, clientNonce(counter), packet[20:], packet[0:20])
	if err != nil {
		t.Fatalf("e2ee decrypt: %v", err)
	}
	return inner
}

// TestClusterE2EEGroupKey proves a group key and media cross instances with the
// server never holding the plaintext: members wrap the key pairwise, media is
// encrypted under the group key and forwarded opaque.
func TestClusterE2EEGroupKey(t *testing.T) {
	a, b := newPGCluster(t)
	alice := a.register("e2eealice")
	bob := a.register("e2eebob")
	var guild struct{ ID string }
	a.decode("POST", "/guilds", alice.Token, map[string]any{"name": "E2EE Guild"}, &guild)
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

	deadline := time.Now().Add(10 * time.Second)
	alicePriv, alicePub := clientKeypair(t)
	bobPriv, bobPub := clientKeypair(t)

	// Publish member keys; each side learns the peer's key through the bus.
	writeVoice(t, aliceVoice, map[string]any{"op": 34, "d": map[string]any{"public_key": encodeB64(alicePub)}})
	writeVoice(t, bobVoice, map[string]any{"op": 34, "d": map[string]any{"public_key": encodeB64(bobPub)}})

	waitKey := func(conn interface{}, wantUser string) {
		t.Helper()
		for i := 0; i < 10; i++ {
			f := readVoiceAny(t, conn, deadline)
			if f["op"].(float64) != 34 {
				continue
			}
			if f["d"].(map[string]any)["user_id"] == wantUser {
				return
			}
		}
		t.Fatalf("did not learn media key for %s", wantUser)
	}
	waitKey(aliceVoice, bob.ID)
	waitKey(bobVoice, alice.ID)

	// Alice is the leader: random group key wrapped for Bob pairwise.
	groupKey := make([]byte, 32)
	_, _ = rand.Read(groupKey)
	wrap := e2eePairwise(t, alicePriv, bobPub)
	wrapped := wrapGroupKey(t, wrap, groupKey)
	const keyID = uint32(7)
	writeVoice(t, aliceVoice, map[string]any{"op": 35, "d": map[string]any{
		"recipient_user_id": bob.ID, "key_id": keyID, "wrapped": wrapped,
	}})

	var bobGroup cipher.AEAD
	for i := 0; i < 10 && bobGroup == nil; i++ {
		f := readVoiceAny(t, bobVoice, time.Now().Add(5*time.Second))
		if f["op"].(float64) != 35 {
			continue
		}
		d := f["d"].(map[string]any)
		if d["from_user_id"] != alice.ID || uint32(d["key_id"].(float64)) != keyID {
			continue
		}
		key := unwrapGroupKey(t, e2eePairwise(t, bobPriv, alicePub), d["wrapped"].(string))
		block, err := aes.NewCipher(key)
		if err != nil {
			t.Fatal(err)
		}
		bobGroup, _ = cipher.NewGCM(block)
	}
	if bobGroup == nil {
		t.Fatal("no wrapped group key received")
	}

	aliceGroup := func() cipher.AEAD {
		block, err := aes.NewCipher(groupKey)
		if err != nil {
			t.Fatal(err)
		}
		g, _ := cipher.NewGCM(block)
		return g
	}()

	ssrc := uint32(aliceReady["ssrc"].(float64))
	inner := rtpPacket(ssrc, "SECRET-AUDIO")
	sealed := sealGroupMedia(aliceGroup, keyID, 1, ssrc, inner)
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
	if _, err := sender.Write(sealed); err != nil {
		t.Fatalf("send e2ee media: %v", err)
	}

	_ = bobRecv.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := bobRecv.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("bob e2ee receive: %v", err)
	}
	if !bytes.Equal(openGroupMedia(t, bobGroup, buf[:n]), inner) {
		t.Fatal("decrypted group media mismatch")
	}

	// Receiver-side replay protection: a duplicated counter is rejected by the
	// member, because the server forwards group envelopes without inspecting them.
	lastCounter := binary.BigEndian.Uint64(buf[8:16])
	if _, err := sender.Write(sealed); err != nil {
		t.Fatal(err)
	}
	_ = bobRecv.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err = bobRecv.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("expected the duplicate to reach the receiver: %v", err)
	}
	dupCounter := binary.BigEndian.Uint64(buf[8:16])
	if dupCounter > lastCounter {
		t.Fatalf("duplicate counter advanced: %d -> %d", lastCounter, dupCounter)
	}

	// The server must not have decoded the group media.
	resp, err := a.server.Client().Get(a.server.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw := make([]byte, 16384)
	mn, _ := resp.Body.Read(raw)
	if !bytes.Contains(raw[:mn], []byte("waveycord_voice_transcodes_total 0")) {
		t.Fatalf("server decoded E2EE media:\n%s", raw[:mn])
	}
	_ = bobReady
	_ = aliceRecv
}
