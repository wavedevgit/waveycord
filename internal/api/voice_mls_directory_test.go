package api_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/wavedevgit/waveycord/internal/mls"
)

// TestVoiceMLSCommitRejectsUnknownCredential proves the first commit is validated
// against the credential directory and the shared voice-state table instead of
// trusting the roster it declares.
func TestVoiceMLSCommitRejectsUnknownCredential(t *testing.T) {
	h := newHarness(t)
	alice := h.register("credalice")
	bob := h.register("credbob")
	stranger := h.register("credstranger")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Cred MLS"}, &guild)
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

	// A roster that names an account with no credential and no voice presence must
	// be refused outright.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	var strangerPub [32]byte
	copy(strangerPub[:], raw)
	leaves := []mls.Leaf{
		{UserID: alice.ID, Public: aPub},
		{UserID: bob.ID, Public: bPub},
		{UserID: stranger.ID, Public: strangerPub},
	}
	commit, _, err := mls.NewEpochCommit(1, leaves, nil, alice.ID, aSignPriv, aPriv)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	sendMLSCommit(t, aliceVoice, commit)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, ok := tryReadVoice(bobVoice, time.Until(deadline))
		if !ok {
			break
		}
		if got["op"].(float64) == 38 {
			t.Fatalf("commit with an unregistered member was relayed: %+v", got)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		if _, _, err := aliceVoice.Read(ctx); err != nil {
			if ctx.Err() != nil {
				t.Fatal("sender stayed open after an unregistered roster")
			}
			return
		}
	}
}
