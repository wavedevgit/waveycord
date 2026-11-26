package store_test

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"testing"

	"github.com/wavedevgit/waveycord/internal/db"
	"github.com/wavedevgit/waveycord/internal/mls"
	"github.com/wavedevgit/waveycord/internal/store"
)

// TestMLSProposalSurvivesReopen proves a relayed proposal is persisted, reloads after
// the store is reopened, and still satisfies a commit that references it by ref.
func TestMLSProposalSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proposals.db")
	handle, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(handle)
	aPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	aSignPub, aSignPriv, _ := ed25519.GenerateKey(rand.Reader)
	bSignPub, bSignPriv, _ := ed25519.GenerateKey(rand.Reader)
	var aPub, bPub, cPub [32]byte
	copy(aPub[:], aPriv.PublicKey().Bytes())
	copy(bPub[:], bPriv.PublicKey().Bytes())
	if _, err := rand.Read(cPub[:]); err != nil {
		t.Fatal(err)
	}
	roster := []mls.Leaf{{UserID: "a", Public: aPub}, {UserID: "b", Public: bPub}}
	proposal := mls.Proposal{Type: "add", UserID: "c", Public: cPub}
	msg := mls.SignProposalMessage(proposal, "b", bSignPriv)
	if err := st.SaveMLSProposal("chan-1", msg); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Also verify idempotence: saving again must not fail or duplicate.
	if err := st.SaveMLSProposal("chan-1", msg); err != nil {
		t.Fatalf("save twice: %v", err)
	}
	handle.Close()

	// Reopen the same database.
	handle2, err := db.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer handle2.Close()
	st2 := store.New(handle2)
	loaded, err := st2.MLSProposalsForChannel("chan-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	restored, ok := loaded[msg.Ref]
	if !ok {
		t.Fatalf("proposal %s missing after reopen (have %d)", msg.Ref, len(loaded))
	}
	keys := map[string]ed25519.PublicKey{"a": aSignPub, "b": bSignPub}
	if err := mls.VerifyProposalMessage(restored, keys); err != nil {
		t.Fatalf("restored signature: %v", err)
	}
	other, err := st2.MLSProposalsForChannel("other-channel")
	if err != nil || len(other) != 0 {
		t.Fatalf("channel scoping: %d err=%v", len(other), err)
	}

	// A commit referencing the reloaded proposal still verifies.
	var aLeafPriv [32]byte
	copy(aLeafPriv[:], aPriv.Bytes())
	commit, _, err := mls.NewEpochCommitExternal(1, roster, []mls.Proposal{proposal}, nil,
		[]mls.ProposalMessage{restored}, mls.SuiteX25519AES256GCM, "a", aSignPriv, aLeafPriv, nil)
	if err != nil {
		t.Fatalf("external commit: %v", err)
	}
	if err := mls.VerifyEpochCommitWithProposals(commit, roster, 0, loaded, keys); err != nil {
		t.Fatalf("commit with reloaded proposal: %v", err)
	}
}
