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
	// A proposer claim without the matching key fails signature verification.
	otherProposer := commit
	otherProposer.ProposalMessages = append([]ProposalMessage(nil), commit.ProposalMessages...)
	otherProposer.ProposalMessages[0].Proposer = bob.id
	if err := VerifyEpochCommit(otherProposer, roster, signKeys); err == nil {
		t.Fatal("proposer claim without the matching key accepted")
	}
	// A proposer outside the previous roster is rejected even with a valid signature.
	outsider := ProposalMessage{
		Proposal:  Proposal{Type: "remove", UserID: bob.id},
		Proposer:  "stranger",
		Signature: ed25519.Sign(strangerKey(t), proposalMessageCanonical(ProposalMessage{Proposal: Proposal{Type: "remove", UserID: bob.id}, Proposer: "stranger"})),
	}
	badOutsider := Commit{Epoch: 1, CipherSuite: SuiteX25519AES256GCM, Roster: commit.Roster,
		Proposals:        []Proposal{{Type: "remove", UserID: bob.id}},
		ProposalMessages: []ProposalMessage{outsider},
		ProposalRefs:     []string{ProposalMessageRef(outsider)},
		Envelopes:        commit.Envelopes, Committer: alice.id}
	badOutsider.Signature = ed25519.Sign(alice.signP, canonical(badOutsider))
	if err := Verify(badOutsider, roster, signKeys); err == nil {
		t.Fatal("proposer outside the roster accepted")
	}
}

func strangerKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

// TestIndependentProposalAuthors proves a member other than the committer can author
// a proposal: the commit is accepted when the message carries that member's
// signature, and rejected when the signature does not match the claimed proposer.
func TestIndependentProposalAuthors(t *testing.T) {
	alice := newMember(t, "ia")
	bob := newMember(t, "ib")
	roster := []Leaf{alice.leaf(), bob.leaf()}
	signKeys := map[string]ed25519.PublicKey{alice.id: alice.signK, bob.id: bob.signK}

	proposal := Proposal{Type: "remove", UserID: bob.id}
	bobMsg := SignProposalMessage(proposal, bob.id, bob.signP)
	commit, _, err := NewEpochCommitWithMessages(1, roster, []Proposal{proposal},
		[]ProposalMessage{bobMsg}, alice.id, alice.signP, alice.priv, nil)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if commit.ProposalMessages[0].Proposer != bob.id {
		t.Fatalf("proposer = %q, want bob", commit.ProposalMessages[0].Proposer)
	}
	if err := VerifyEpochCommit(commit, roster, signKeys); err != nil {
		t.Fatalf("member-authored proposal rejected: %v", err)
	}

	// The committer signing on bob's behalf must be rejected.
	aliceMsg := SignProposalMessage(proposal, bob.id, alice.signP)
	bad, _, err := NewEpochCommitWithMessages(1, roster, []Proposal{proposal},
		[]ProposalMessage{aliceMsg}, alice.id, alice.signP, alice.priv, nil)
	if err != nil {
		t.Fatalf("bad commit build: %v", err)
	}
	if err := VerifyEpochCommit(bad, roster, signKeys); err == nil {
		t.Fatal("forged proposer signature accepted")
	}

	// A proposal authored by a member that is not in the previous roster is rejected
	// even if the commit drops that member.
	outsider := newMember(t, "ic")
	signKeys[outsider.id] = outsider.signK
	outMsg := SignProposalMessage(proposal, outsider.id, outsider.signP)
	bad2, _, err := NewEpochCommitWithMessages(1, roster, []Proposal{proposal},
		[]ProposalMessage{outMsg}, alice.id, alice.signP, alice.priv, nil)
	if err != nil {
		t.Fatalf("outsider commit build: %v", err)
	}
	if err := VerifyEpochCommit(bad2, roster, signKeys); err == nil {
		t.Fatal("proposal from outside the roster accepted")
	}
}
