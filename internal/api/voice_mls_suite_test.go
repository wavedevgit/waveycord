package api_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/wavedevgit/waveycord/internal/mls"
)

// TestVoiceMLSCommitRejectsUnsupportedSuite proves the server refuses a commit that
// negotiates a ciphersuite it does not implement, closing the sender.
func TestVoiceMLSCommitRejectsUnsupportedSuite(t *testing.T) {
	h := newHarness(t)
	alice := h.register("suitealice")
	bob := h.register("suitebob")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Suite MLS"}, &guild)
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
	writeVoice(t, aliceVoice, map[string]any{"op": 34, "d": map[string]any{
		"public_key": encodeB64(aPub), "signing_key": hex.EncodeToString(aSignPub),
	}})
	writeVoice(t, bobVoice, map[string]any{"op": 34, "d": map[string]any{
		"public_key": encodeB64(bPub), "signing_key": hex.EncodeToString(bSignPub),
	}})
	time.Sleep(100 * time.Millisecond)

	leaves := []mls.Leaf{{UserID: alice.ID, Public: aPub}, {UserID: bob.ID, Public: bPub}}
	commit, _, err := mls.NewEpochCommit(1, leaves, nil, alice.ID, aSignPriv, aPriv)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	// Rewrite the suite on the wire; the server must refuse it before relaying.
	raw, err := json.Marshal(commit)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	wire["cipher_suite"] = 0x0099
	writeVoice(t, aliceVoice, map[string]any{"op": 38, "d": wire})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, ok := tryReadVoice(bobVoice, time.Until(deadline))
		if !ok {
			break
		}
		if got["op"].(float64) == 38 {
			t.Fatalf("unsupported-suite commit was relayed: %+v", got)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		if _, _, err := aliceVoice.Read(ctx); err != nil {
			if ctx.Err() != nil {
				t.Fatal("sender stayed open after negotiating an unsupported suite")
			}
			return
		}
	}
}
