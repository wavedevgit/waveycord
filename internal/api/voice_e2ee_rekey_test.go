package api_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// joinVoiceConn keeps the main gateway connection so a test can leave the channel
// later, and returns the voice token plus the connection.
func (h *harness) joinVoiceConn(acc account, guildID, channelID string) (*websocket.Conn, string) {
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
		token := f["d"].(map[string]any)["token"].(string)
		return conn, token
	}
	h.t.Fatal("no VOICE_SERVER_UPDATE")
	return nil, ""
}

func readVoiceOp(t *testing.T, conn *websocket.Conn, op int, deadline time.Time) map[string]any {
	t.Helper()
	for {
		f := readVoice(t, conn, deadline)
		if int(f["op"].(float64)) == op {
			return f
		}
		if time.Now().After(deadline) {
			t.Fatalf("voice op %d not received", op)
		}
	}
}

// TestClusterVoiceEpochRekey proves membership changes signal a new epoch to
// members on both instances, and that members can rotate the group key and keep
// exchanging media.
func TestClusterVoiceEpochRekey(t *testing.T) {
	a, b := newPGCluster(t)
	alice := a.register("epochalice")
	bob := a.register("epochbob")
	charlie := a.register("epochcharlie")
	var guild struct{ ID string }
	a.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Epoch Guild"}, &guild)
	var text, voice struct{ ID string }
	a.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	a.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)
	var invite struct{ Code string }
	a.decode("POST", "/channels/"+text.ID+"/invites", alice.Token, map[string]any{}, &invite)
	a.request("POST", "/invites/"+invite.Code, bob.Token, nil)
	a.request("POST", "/invites/"+invite.Code, charlie.Token, nil)

	aliceMain, aliceToken := a.joinVoiceConn(alice, guild.ID, voice.ID)
	bobMain, bobToken := b.joinVoiceConn(bob, guild.ID, voice.ID)
	aliceVoice, aliceReady := voiceEndpoint(t, a, aliceToken, alice.ID)
	bobVoice, bobReady := voiceEndpoint(t, b, bobToken, bob.ID)
	aliceRecv := udpListener(t)
	bobRecv := udpListener(t)
	selectProtocol(t, aliceVoice, aliceRecv)
	selectProtocol(t, bobVoice, bobRecv)

	alicePriv, alicePub := clientKeypair(t)
	bobPriv, bobPub := clientKeypair(t)
	deadline := time.Now().Add(10 * time.Second)
	writeVoice(t, aliceVoice, map[string]any{"op": 34, "d": map[string]any{"public_key": encodeB64(alicePub)}})
	writeVoice(t, bobVoice, map[string]any{"op": 34, "d": map[string]any{"public_key": encodeB64(bobPub)}})
	for aliceVoice != nil { // wait until each has the other's key
		f := readVoice(t, aliceVoice, deadline)
		if f["op"].(float64) == 34 && f["d"].(map[string]any)["user_id"] == bob.ID {
			break
		}
	}
	for {
		f := readVoice(t, bobVoice, deadline)
		if f["op"].(float64) == 34 && f["d"].(map[string]any)["user_id"] == alice.ID {
			break
		}
	}

	// Charlie joins: both members on both instances receive the rekey signal.
	charlieMain, charlieToken := a.joinVoiceConn(charlie, guild.ID, voice.ID)
	aliceRekey := readVoiceOp(t, aliceVoice, 36, time.Now().Add(10*time.Second))
	if aliceRekey["d"].(map[string]any)["reason"] != "member_joined" {
		t.Fatalf("alice rekey payload: %+v", aliceRekey)
	}
	bobRekey := readVoiceOp(t, bobVoice, 36, time.Now().Add(10*time.Second))
	if bobRekey["d"].(map[string]any)["reason"] != "member_joined" {
		t.Fatalf("bob rekey payload: %+v", bobRekey)
	}
	if bobRekey["d"].(map[string]any)["epoch"].(float64) <= 0 {
		t.Fatalf("bad epoch: %+v", bobRekey)
	}

	// Charlie joins voice, publishes a key; Alice learns it (local) and Bob does
	// too (bus).
	charliePriv, charliePub := clientKeypair(t)
	_ = charliePriv
	charlieVoice, charlieReady := voiceEndpoint(t, a, charlieToken, charlie.ID)
	charlieRecv := udpListener(t)
	selectProtocol(t, charlieVoice, charlieRecv)
	writeVoice(t, charlieVoice, map[string]any{"op": 34, "d": map[string]any{"public_key": encodeB64(charliePub)}})
	for {
		f := readVoice(t, aliceVoice, deadline)
		if f["op"].(float64) != 34 {
			continue
		}
		if f["d"].(map[string]any)["user_id"] == charlie.ID {
			break
		}
	}

	// Rotate: Alice wraps a fresh group key for Bob and Charlie.
	groupKey := make([]byte, 32)
	_, _ = rand.Read(groupKey)
	const keyID = uint32(11)
	wrapBob := wrapGroupKey(t, e2eePairwise(t, alicePriv, bobPub), groupKey)
	wrapCharlie := wrapGroupKey(t, e2eePairwise(t, alicePriv, charliePub), groupKey)
	writeVoice(t, aliceVoice, map[string]any{"op": 35, "d": map[string]any{
		"recipient_user_id": bob.ID, "key_id": keyID, "wrapped": wrapBob,
	}})
	writeVoice(t, aliceVoice, map[string]any{"op": 35, "d": map[string]any{
		"recipient_user_id": charlie.ID, "key_id": keyID, "wrapped": wrapCharlie,
	}})

	// Bob unwraps through the bus.
	var bobGroup cipher.AEAD
	for bobGroup == nil {
		f := readVoice(t, bobVoice, time.Now().Add(10*time.Second))
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
	var charlieGroup cipher.AEAD
	for charlieGroup == nil {
		f := readVoice(t, charlieVoice, time.Now().Add(10*time.Second))
		if f["op"].(float64) != 35 {
			continue
		}
		d := f["d"].(map[string]any)
		key := unwrapGroupKey(t, e2eePairwise(t, charliePriv, alicePub), d["wrapped"].(string))
		block, err := aes.NewCipher(key)
		if err != nil {
			t.Fatal(err)
		}
		charlieGroup, _ = cipher.NewGCM(block)
	}

	// Media under the new epoch key reaches Bob cross-instance.
	aliceGroup := func() cipher.AEAD {
		block, _ := aes.NewCipher(groupKey)
		g, _ := cipher.NewGCM(block)
		return g
	}()
	ssrc := uint32(aliceReady["ssrc"].(float64))
	inner := rtpPacket(ssrc, "EPOCH2-AUDIO")
	sealed := sealGroupMedia(aliceGroup, keyID, 9, ssrc, inner)
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
		t.Fatal(err)
	}
	_ = bobRecv.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := bobRecv.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("bob epoch media: %v", err)
	}
	if !bytes.Equal(openGroupMedia(t, bobGroup, buf[:n]), inner) {
		t.Fatal("epoch media did not decrypt for Bob")
	}

	// Charlie leaves: remaining members get member_left.
	writeFrame(t, charlieMain, map[string]any{"op": 4, "d": map[string]any{
		"guild_id": guild.ID, "channel_id": nil,
	}})
	left := readVoiceOp(t, bobVoice, 36, time.Now().Add(10*time.Second))
	if left["d"].(map[string]any)["reason"] != "member_left" {
		t.Fatalf("bob leave rekey payload: %+v", left)
	}
	_ = readVoiceOp(t, aliceVoice, 36, time.Now().Add(10*time.Second))

	_ = bobMain
	_ = aliceMain
	_ = bobReady
	_ = charlieReady
	_ = charlieGroup
	_ = binary.BigEndian
}
