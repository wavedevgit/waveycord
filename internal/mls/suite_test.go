package mls

import (
	"crypto/ed25519"
	"testing"
)

// TestCiphersuiteAndProposalRefs proves negotiation is explicit and proposal
// references are bound into the signed commit.
func TestCiphersuiteAndProposalRefs(t *testing.T) {
	alice := newMember(t, "alice")
	bob := newMember(t, "bob")
	roster := []Leaf{alice.leaf(), bob.leaf()}
	signKeys := map[string]ed25519.PublicKey{alice.id: alice.signK, bob.id: bob.signK}

	proposals := []Proposal{{Type: "remove", UserID: bob.id}}
	commit, _, err := NewEpochCommit(1, roster, proposals, alice.id, alice.signP, alice.priv)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if commit.CipherSuite != SuiteX25519AES256GCM || !commit.CipherSuite.Supported() {
		t.Fatalf("unexpected suite %#x", commit.CipherSuite)
	}
	want := ProposalRefs(proposals)
	if len(commit.ProposalRefs) != len(want) || commit.ProposalRefs[0] != want[0] {
		t.Fatalf("proposal refs = %v, want %v", commit.ProposalRefs, want)
	}
	if ProposalRef(proposals[0]) != want[0] {
		t.Fatal("ProposalRef is not deterministic")
	}
	if err := VerifyEpochCommit(commit, roster, signKeys); err != nil {
		t.Fatalf("valid commit: %v", err)
	}

	// Unsupported negotiated suite is rejected.
	badSuite := commit
	badSuite.CipherSuite = 0x0099
	if err := VerifyEpochCommit(badSuite, roster, signKeys); err == nil {
		t.Fatal("unsupported ciphersuite accepted")
	}

	// Tampered proposal reference list is rejected.
	badRefs := commit
	badRefs.ProposalRefs = []string{"deadbeef"}
	if err := VerifyEpochCommit(badRefs, roster, signKeys); err == nil {
		t.Fatal("tampered proposal refs accepted")
	}

	// Missing refs for a proposal are rejected.
	missing := commit
	missing.ProposalRefs = nil
	if err := VerifyEpochCommit(missing, roster, signKeys); err == nil {
		t.Fatal("missing proposal refs accepted")
	}
}
