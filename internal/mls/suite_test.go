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
	if len(commit.ProposalMessages) != 1 {
		t.Fatalf("proposal messages = %d, want 1", len(commit.ProposalMessages))
	}
	if commit.ProposalMessages[0].Proposer != alice.id {
		t.Fatalf("proposer = %q", commit.ProposalMessages[0].Proposer)
	}
	if err := VerifyProposalMessage(commit.ProposalMessages[0], signKeys); err != nil {
		t.Fatalf("wire message signature: %v", err)
	}
	want := ProposalRefsFor(commit.ProposalMessages)
	if len(commit.ProposalRefs) != len(want) || commit.ProposalRefs[0] != want[0] {
		t.Fatalf("proposal refs = %v, want %v", commit.ProposalRefs, want)
	}
	if ProposalMessageRef(commit.ProposalMessages[0]) != want[0] {
		t.Fatal("ProposalMessageRef is not deterministic")
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

	// The signed wire message is required, not just the body.
	noMessages := commit
	noMessages.ProposalMessages = nil
	if err := VerifyEpochCommit(noMessages, roster, signKeys); err == nil {
		t.Fatal("commit without proposal messages accepted")
	}

	// A body that disagrees with the signed message is rejected.
	swapped := commit
	swapped.Proposals = []Proposal{{Type: "remove", UserID: alice.id}}
	if err := VerifyEpochCommit(swapped, roster, signKeys); err == nil {
		t.Fatal("proposal body swapped past its message accepted")
	}

	// A tampered signature or proposer is rejected.
	badSig := commit
	badSig.ProposalMessages = append([]ProposalMessage(nil), commit.ProposalMessages...)
	badSig.ProposalMessages[0].Signature = []byte{1, 2, 3}
	if err := VerifyEpochCommit(badSig, roster, signKeys); err == nil {
		t.Fatal("tampered proposal signature accepted")
	}
	otherProposer := commit
	otherProposer.ProposalMessages = append([]ProposalMessage(nil), commit.ProposalMessages...)
	otherProposer.ProposalMessages[0].Proposer = bob.id
	if err := VerifyEpochCommit(otherProposer, roster, signKeys); err == nil {
		t.Fatal("foreign proposer accepted")
	}
}
