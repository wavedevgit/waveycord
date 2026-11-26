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

// TestVoiceMLSCommitRejectsSilentSuiteChange proves the server refuses a commit that
// switches ciphersuite without a signed ciphersuite proposal, and accepts one that
// negotiates it properly.
func TestVoiceMLSCommitRejectsSilentSuiteChange(t *testing.T) {
	h := newHarness(t)
	alice := h.register("silentalice")
	bob := h.register("silentbob")
	var guild struct{ ID string }
	h.decode("POST", "/guilds", alice.Token, map[string]any{"name": "Silent Suite"}, &guild)
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
	first, _, err := mls.NewEpochCommit(1, leaves, nil, alice.ID, aSignPriv, aPriv)
	if err != nil {
		t.Fatalf("epoch1: %v", err)
	}
	sendMLSCommit(t, aliceVoice, first)
	readMLSCommit(t, bobVoice, time.Now().Add(20*time.Second))

	// A properly negotiated change is accepted.
	negotiated, _, err := mls.NewEpochCommitNegotiating(2, leaves, nil,
		mls.SuiteX25519AES128GCM, alice.ID, aSignPriv, aPriv, first.Tree)
	if err != nil {
		t.Fatalf("negotiated commit: %v", err)
	}
	sendMLSCommit(t, aliceVoice, negotiated)
	readMLSCommit(t, bobVoice, time.Now().Add(20*time.Second))

	// Switching again without a proposal must be refused.
	silent, _, err := mls.NewEpochCommitWithSuite(3, leaves, nil, nil,
		mls.SuiteX25519AES256GCM, alice.ID, aSignPriv, aPriv, negotiated.Tree)
	if err != nil {
		t.Fatalf("silent commit: %v", err)
	}
	sendMLSCommit(t, aliceVoice, silent)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, ok := tryReadVoice(bobVoice, time.Until(deadline))
		if !ok {
			break
		}
		if got["op"].(float64) == 38 {
			t.Fatalf("silent suite change was relayed: %+v", got)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		if _, _, err := aliceVoice.Read(ctx); err != nil {
			if ctx.Err() != nil {
				t.Fatal("sender stayed open after a silent suite change")
			}
			return
		}
	}
}
