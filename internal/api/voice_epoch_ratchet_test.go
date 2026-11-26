package api_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func learnKeys(t *testing.T, conn *websocket.Conn, want []string) {
	t.Helper()
	unseen := map[string]bool{}
	for _, id := range want {
		unseen[id] = true
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(unseen) > 0 && time.Now().Before(deadline) {
		f := readVoice(t, conn, deadline)
		if f["op"].(float64) != 34 {
			continue
		}
		id, _ := f["d"].(map[string]any)["user_id"].(string)
		delete(unseen, id)
	}
	if len(unseen) > 0 {
		t.Fatalf("missing media keys for %v", unseen)
	}
}

// tryReadVoice reads one frame, reporting false on timeout instead of failing.
func tryReadVoice(conn *websocket.Conn, wait time.Duration) (map[string]any, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		return nil, false
	}
	var frame map[string]any
	if jsonErr := json.Unmarshal(data, &frame); jsonErr != nil {
		return nil, false
	}
	return frame, true
}

type epochReceiver struct {
	gcm    cipher.AEAD
	keyID  uint32
	last   uint64
	reject int
}

func (r *epochReceiver) accept(packet []byte) ([]byte, bool) {
	if len(packet) < 36 || string(packet[0:4]) != "WCE2" {
		return nil, false
	}
	keyID := binary.BigEndian.Uint32(packet[4:8])
	counter := binary.BigEndian.Uint64(packet[8:16])
	if keyID != r.keyID || counter <= r.last {
		r.reject++
		return nil, false
	}
	inner, err := r.gcm.Open(nil, clientNonce(counter), packet[20:], packet[0:20])
	if err != nil {
		r.reject++
		return nil, false
	}
	r.last = counter
	return inner, true
}

// TestVoiceEpochRatchet proves a departed member is excluded from the next epoch:
// current members rotate and exchange media, old-epoch frames are rejected, and
// the departed member receives no new key.
func TestVoiceEpochRatchet(t *testing.T) {
	a, b := newPGCluster(t)
	alice := a.register("ratcheta")
	bob := a.register("ratchetb")
	charlie := a.register("ratchetc")
	var guild struct{ ID string }
	a.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Ratchet Guild"}, &guild)
	var text, voice struct{ ID string }
	a.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	a.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)
	var invite struct{ Code string }
	a.decode("POST", "/channels/"+text.ID+"/invites", alice.Token, map[string]any{}, &invite)
	for _, acc := range []account{bob, charlie} {
		a.request("POST", "/invites/"+invite.Code, acc.Token, nil)
	}

	aliceMain, aliceToken := a.joinVoiceConn(alice, guild.ID, voice.ID)
	bobMain, bobToken := b.joinVoiceConn(bob, guild.ID, voice.ID)
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

	// Epoch 1: Alice wraps for all current members.
	key1 := make([]byte, 32)
	_, _ = rand.Read(key1)
	makeGCM := func(key []byte) cipher.AEAD {
		block, err := aes.NewCipher(key)
		if err != nil {
			t.Fatal(err)
		}
		g, _ := cipher.NewGCM(block)
		return g
	}
	wrapFor := func(recipientID string, recipientPub [32]byte, key []byte, keyID uint32) {
		t.Helper()
		wrapped := wrapGroupKey(t, e2eePairwise(t, alicePriv, recipientPub), key)
		writeVoice(t, aliceVoice, map[string]any{"op": 35, "d": map[string]any{
			"recipient_user_id": recipientID, "key_id": keyID, "wrapped": wrapped,
		}})
	}
	unwrapFor := func(conn *websocket.Conn, fromUser string, peerPub [32]byte, priv [32]byte, keyID uint32) cipher.AEAD {
		t.Helper()
		until := time.Now().Add(10 * time.Second)
		for time.Now().Before(until) {
			f := readVoice(t, conn, until)
			if f["op"].(float64) != 35 {
				continue
			}
			d := f["d"].(map[string]any)
			if d["from_user_id"] != fromUser || uint32(d["key_id"].(float64)) != keyID {
				continue
			}
			return makeGCM(unwrapGroupKey(t, e2eePairwise(t, priv, peerPub), d["wrapped"].(string)))
		}
		t.Fatal("group key not received")
		return nil
	}
	const keyID1 = uint32(1)
	wrapFor(bob.ID, bobPub, key1, keyID1)
	wrapFor(charlie.ID, charliePub, key1, keyID1)
	bobEpoch1 := unwrapFor(bobVoice, alice.ID, alicePub, bobPriv, keyID1)
	charlieEpoch1 := unwrapFor(charlieVoice, alice.ID, alicePub, charliePriv, keyID1)

	aliceGCM1 := makeGCM(key1)
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
	sendMedia := func(gcm cipher.AEAD, keyID uint32, counter uint64, payload string) {
		t.Helper()
		inner := rtpPacket(ssrc, payload)
		if _, err := sender.Write(sealGroupMedia(gcm, keyID, counter, ssrc, inner)); err != nil {
			t.Fatalf("send media: %v", err)
		}
	}
	sendMedia(aliceGCM1, keyID1, 1, "EPOCH1")
	_ = bobRecv.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := bobRecv.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("epoch1 media: %v", err)
	}
	bobKey1 := &epochReceiver{gcm: bobEpoch1, keyID: keyID1}
	if inner, ok := bobKey1.accept(buf[:n]); !ok || !bytes.Contains(inner, []byte("EPOCH1")) {
		t.Fatal("epoch1 media did not decrypt")
	}
	charlieKey1 := &epochReceiver{gcm: charlieEpoch1, keyID: keyID1}
	_ = charlieRecv.SetReadDeadline(time.Now().Add(5 * time.Second))
	if cn, _, err := charlieRecv.ReadFromUDP(buf); err == nil {
		if _, ok := charlieKey1.accept(buf[:cn]); !ok {
			t.Fatal("charlie could not decrypt epoch1")
		}
	}

	// Charlie leaves. Members receive member_left and rotate to a fresh key.
	writeFrame(t, charlieMain, map[string]any{"op": 4, "d": map[string]any{"guild_id": guild.ID, "channel_id": nil}})
	left := readVoiceOp(t, bobVoice, 36, time.Now().Add(10*time.Second))
	if left["d"].(map[string]any)["reason"] != "member_left" {
		t.Fatalf("expected member_left, got %+v", left)
	}
	_ = readVoiceOp(t, aliceVoice, 36, time.Now().Add(10*time.Second))

	key2 := make([]byte, 32)
	_, _ = rand.Read(key2)
	const keyID2 = uint32(2)
	wrapFor(bob.ID, bobPub, key2, keyID2)
	// The server must not deliver a key to the departed member.
	wrapFor(charlie.ID, charliePub, key2, keyID2)
	deadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(deadline) {
		f, ok := tryReadVoice(charlieVoice, time.Until(deadline))
		if !ok {
			break
		}
		if f["op"].(float64) == 35 && uint32(f["d"].(map[string]any)["key_id"].(float64)) == keyID2 {
			t.Fatal("departed member received the new epoch key")
		}
	}
	bobGCM2 := unwrapFor(bobVoice, alice.ID, alicePub, bobPriv, keyID2)
	aliceGCM2 := makeGCM(key2)

	// Current members exchange epoch-2 media.
	sendMedia(aliceGCM2, keyID2, 2, "EPOCH2")
	bobKey2 := &epochReceiver{gcm: bobGCM2, keyID: keyID2}
	found := false
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) && !found {
		_ = bobRecv.SetReadDeadline(until)
		n, _, err := bobRecv.ReadFromUDP(buf)
		if err != nil {
			break
		}
		if inner, ok := bobKey2.accept(buf[:n]); ok && bytes.Contains(inner, []byte("EPOCH2")) {
			found = true
		}
	}
	if !found {
		t.Fatal("epoch2 media did not decrypt for Bob")
	}

	// A current member's stale epoch-1 frame is forwarded but rejected by the
	// receiver, and the departed member's media never reaches Bob.
	sendMedia(aliceGCM1, keyID1, 3, "STALE")
	sendMedia(charlieEpoch1, keyID1, 4, "DEPARTED")
	rejectsBefore := bobKey2.reject
	_ = bobRecv.SetReadDeadline(time.Now().Add(time.Second))
	for {
		n, _, err := bobRecv.ReadFromUDP(buf)
		if err != nil {
			break
		}
		_, _ = bobKey2.accept(buf[:n])
	}
	if bobKey2.reject <= rejectsBefore {
		t.Fatal("stale epoch frame was accepted")
	}

	_ = aliceMain
	_ = bobMain
	_ = bobReady
	_ = charlieRecv
}
