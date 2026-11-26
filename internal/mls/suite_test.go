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
	outsider := SignProposalMessage(Proposal{Type: "remove", UserID: bob.id}, "stranger", strangerKey(t))
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

// TestProposalReferenceBinding proves each proposal carries its own reference, the
// commit's list binds those references exactly, and forged/duplicate/reordered
// references are rejected.
func TestProposalReferenceBinding(t *testing.T) {
	alice := newMember(t, "rba")
	bob := newMember(t, "rbb")
	charlie := newMember(t, "rbc")
	roster := []Leaf{alice.leaf(), bob.leaf(), charlie.leaf()}
	signKeys := map[string]ed25519.PublicKey{alice.id: alice.signK, bob.id: bob.signK, charlie.id: charlie.signK}

	p1 := Proposal{Type: "remove", UserID: charlie.id}
	p2 := Proposal{Type: "remove", UserID: bob.id}
	m1 := SignProposalMessage(p1, bob.id, bob.signP)
	m2 := SignProposalMessage(p2, alice.id, alice.signP)
	if m1.Ref == "" || m1.Ref == m2.Ref {
		t.Fatalf("proposal refs not unique: %q %q", m1.Ref, m2.Ref)
	}
	if m1.Ref != ProposalMessageRef(m1) {
		t.Fatal("message ref is not derivable")
	}
	commit, _, err := NewEpochCommitWithMessages(1, roster,
		[]Proposal{p1, p2}, []ProposalMessage{m1, m2}, alice.id, alice.signP, alice.priv, nil)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(commit.ProposalRefs) != 2 || commit.ProposalRefs[0] != m1.Ref || commit.ProposalRefs[1] != m2.Ref {
		t.Fatalf("commit refs = %v", commit.ProposalRefs)
	}
	if err := VerifyEpochCommit(commit, roster, signKeys); err != nil {
		t.Fatalf("valid refs rejected: %v", err)
	}

	// A forged message Ref is rejected.
	forged := commit
	forged.ProposalMessages = append([]ProposalMessage(nil), commit.ProposalMessages...)
	forged.ProposalMessages[0].Ref = "deadbeefdeadbeefdeadbeefdeadbeef"
	forged.ProposalRefs = []string{forged.ProposalMessages[0].Ref, commit.ProposalRefs[1]}
	if err := VerifyEpochCommit(forged, roster, signKeys); err == nil {
		t.Fatal("forged proposal ref accepted")
	}
	// Reordered refs no longer match the message order.
	reordered := commit
	reordered.ProposalRefs = []string{commit.ProposalRefs[1], commit.ProposalRefs[0]}
	if err := VerifyEpochCommit(reordered, roster, signKeys); err == nil {
		t.Fatal("reordered proposal refs accepted")
	}
	// Duplicate references are rejected.
	dupMsg := append([]ProposalMessage(nil), commit.ProposalMessages...)
	dupMsg[1] = dupMsg[0]
	dup := commit
	dup.ProposalMessages = dupMsg
	dup.Proposals = []Proposal{p1, p1}
	dup.ProposalRefs = []string{m1.Ref, m1.Ref}
	if err := VerifyEpochCommit(dup, roster, signKeys); err == nil {
		t.Fatal("duplicate proposal refs accepted")
	}
}

// TestExternalProposalReference proves a commit can reference a previously relayed
// proposal by ref, that the reference binds the exact signed message, and that a
// missing or mismatched referenced message fails verification.
func TestExternalProposalReference(t *testing.T) {
	alice := newMember(t, "epa")
	bob := newMember(t, "epb")
	charlie := newMember(t, "epc")
	roster := []Leaf{alice.leaf(), bob.leaf()}
	signKeys := map[string]ed25519.PublicKey{alice.id: alice.signK, bob.id: bob.signK, charlie.id: charlie.signK}

	add := addProposalFor(t, charlie)
	ext := SignProposalMessage(add, bob.id, bob.signP)
	commit, _, err := NewEpochCommitExternal(1, roster, []Proposal{add}, nil,
		[]ProposalMessage{ext}, SuiteX25519AES256GCM, alice.id, alice.signP, alice.priv, nil)
	if err != nil {
		t.Fatalf("external commit: %v", err)
	}
	if len(commit.ProposalMessages) != 0 || len(commit.ProposalRefs) != 1 || commit.ProposalRefs[0] != ext.Ref {
		t.Fatalf("external framing: messages=%d refs=%v", len(commit.ProposalMessages), commit.ProposalRefs)
	}
	external := map[string]ProposalMessage{ext.Ref: ext}
	if err := VerifyEpochCommitWithProposals(commit, roster, 0, external, signKeys); err != nil {
		t.Fatalf("external reference rejected: %v", err)
	}
	found := false
	for _, l := range commit.Roster {
		if l.UserID == charlie.id {
			found = true
		}
	}
	if !found {
		t.Fatal("referenced proposal was not applied to the roster")
	}
	// Without the referenced message the commit cannot be verified.
	if err := VerifyEpochCommit(commit, roster, signKeys); err == nil {
		t.Fatal("commit verified without the referenced proposal")
	}
	// A different message under the same ref is rejected.
	wrong := SignProposalMessage(Proposal{Type: "remove", UserID: bob.id}, bob.id, bob.signP)
	if err := VerifyEpochCommitWithProposals(commit, roster, 0,
		map[string]ProposalMessage{ext.Ref: wrong}, signKeys); err == nil {
		t.Fatal("mismatched external message accepted")
	}
	// A commit whose ref list does not match the applied proposal order fails.
	misordered, _, err := NewEpochCommitExternal(1, roster, []Proposal{add}, nil,
		[]ProposalMessage{ext}, SuiteX25519AES256GCM, alice.id, alice.signP, alice.priv, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	misordered.Proposals = []Proposal{{Type: "remove", UserID: bob.id}}
	misordered.Signature = ed25519.Sign(alice.signP, canonical(misordered.Commit))
	if err := VerifyEpochCommitWithProposals(misordered, roster, 0, external, signKeys); err == nil {
		t.Fatal("proposal body/ref mismatch accepted")
	}
}
