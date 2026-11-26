package api_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/wavedevgit/waveycord/internal/mls"
)

// TestVoiceMLSCommitServerValidation proves the server validates op 38 commits
// before relaying them: a correctly signed commit reaches the peer, while a tampered
// signature closes the sender and is never relayed.
func TestVoiceMLSCommitServerValidation(t *testing.T) {
	h := newHarness(t)
	alice := h.register("valalice")
	bob := h.register("valbob")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Validate MLS"}, &guild)
	var text, voice struct{ ID string }
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "text"}, &text)
	h.decode("POST", "/guilds/"+guild.ID+"/channels", alice.Token, map[string]any{"name": "voice", "type": 2}, &voice)
	var invite struct{ Code string }
	h.decode("POST", "/channels/"+text.ID+"/invites", alice.Token, map[string]any{}, &invite)
	h.request("POST", "/invites/"+invite.Code, bob.Token, nil)

	aliceToken := h.joinVoice(alice, guild.ID, voice.ID)
	bobToken := h.joinVoice(bob, guild.ID, voice.ID)
	aliceVoice, _ := voiceEndpoint(t, h, aliceToken, alice.ID)
	bobVoice, _ := voiceEndpoint(t, h, bobToken, bob.ID)

	aPriv, aPub := clientKeypair(t)
	_, bPub := clientKeypair(t)
	aSignPub, aSignPriv, _ := ed25519.GenerateKey(rand.Reader)
	bSignPub, _, _ := ed25519.GenerateKey(rand.Reader)

	publish := func(conn *websocket.Conn, pub [32]byte, sign ed25519.PublicKey) {
		writeVoice(t, conn, map[string]any{"op": 34, "d": map[string]any{
			"public_key": encodeB64(pub), "signing_key": hex.EncodeToString(sign),
		}})
	}
	publish(aliceVoice, aPub, aSignPub)
	publish(bobVoice, bPub, bSignPub)
	// Let the server record both signing keys.
	time.Sleep(100 * time.Millisecond)

	leaves := []mls.Leaf{
		{UserID: alice.ID, Public: aPub},
		{UserID: bob.ID, Public: bPub},
	}
	commit, _, err := mls.NewEpochCommit(1, leaves, nil, alice.ID, aSignPriv, aPriv)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	writeVoice(t, aliceVoice, map[string]any{"op": 38, "d": commit})
	f := readVoiceOp(t, bobVoice, 38, time.Now().Add(20*time.Second))
	if f["op"].(float64) != 38 {
		t.Fatalf("valid commit not relayed: %+v", f)
	}

	// A tampered signature must be rejected and the sender disconnected without a
	// second relay.
	forged := commit
	forged.Signature = append([]byte(nil), commit.Signature...)
	forged.Signature[0] ^= 0xff
	forged.Epoch = 2
	if _, err := json.Marshal(forged); err != nil {
		t.Fatal(err)
	}
	writeVoice(t, aliceVoice, map[string]any{"op": 38, "d": forged})

	// The peer must not receive the forged commit, and Alice's connection closes.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, ok := tryReadVoice(bobVoice, time.Until(deadline))
		if !ok {
			break
		}
		if got["op"].(float64) == 38 {
			t.Fatalf("forged commit was relayed: %+v", got)
		}
	}
	// The sender's connection is closed by the server.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		if _, _, err := aliceVoice.Read(ctx); err != nil {
			if ctx.Err() != nil {
				t.Fatal("sender connection stayed open after an invalid commit")
			}
			break
		}
	}
}
