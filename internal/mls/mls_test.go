package mls

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

type member struct {
	id    string
	priv  [32]byte
	pub   [32]byte
	signK ed25519.PublicKey
	signP ed25519.PrivateKey
}

func newMember(t *testing.T, id string) member {
	t.Helper()
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var m member
	m.id = id
	copy(m.priv[:], priv.Bytes())
	copy(m.pub[:], priv.PublicKey().Bytes())
	m.signK, m.signP, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (m member) leaf() Leaf { return Leaf{UserID: m.id, Public: m.pub} }

// TestEpochCommitProposalsAndRemoval proves a signed Add/Remove commit rekeys the
// epoch: current members derive the same root, a removed member has no envelope
// and cannot derive it, and tampered commits fail verification.
func TestEpochCommitProposalsAndRemoval(t *testing.T) {
	alice := newMember(t, "alice")
	bob := newMember(t, "bob")
	charlie := newMember(t, "charlie")
	dave := newMember(t, "dave")
	rosters := []Leaf{alice.leaf(), bob.leaf(), charlie.leaf()}
	signKeys := map[string]ed25519.PublicKey{
		alice.id: alice.signK, bob.id: bob.signK, charlie.id: charlie.signK, dave.id: dave.signK,
	}

	// Epoch 1: initial roster, no proposals.
	commit1, _, err := NewCommit(1, rosters, nil, alice.id, alice.signP)
	if err != nil {
		t.Fatalf("commit1: %v", err)
	}
	if err := Verify(commit1, rosters, signKeys); err != nil {
		t.Fatalf("verify1: %v", err)
	}
	roots := map[string][]byte{}
	for _, m := range []member{alice, bob, charlie} {
		root, err := DecryptRoot(commit1, m.id, m.priv)
		if err != nil {
			t.Fatalf("%s decrypt1: %v", m.id, err)
		}
		roots[m.id] = root
	}
	if !bytes.Equal(roots["alice"], roots["bob"]) || !bytes.Equal(roots["alice"], roots["charlie"]) {
		t.Fatal("epoch1 roots differ")
	}

	// Tree schedule: deterministic from the root, blank leaves marked.
	tree := DeriveTree(roots["alice"], rosters)
	if len(tree) != 8 { // 4 leaves -> size 4 -> 8 nodes
		t.Fatalf("tree nodes = %d", len(tree))
	}
	blankLeaves := 0
	for i := 4; i < 8; i++ {
		if tree[i].Blank {
			blankLeaves++
		}
	}
	if blankLeaves != 1 {
		t.Fatalf("blank leaves = %d, want 1", blankLeaves)
	}
	if !bytes.Equal(tree[1].Secret, DeriveTree(roots["bob"], rosters)[1].Secret) {
		t.Fatal("tree schedule is not deterministic")
	}
	secret1 := EpochSecret(roots["alice"], commit1.Roster)

	// Epoch 2: remove Charlie, add Dave. Charlie must get no envelope.
	proposals := []Proposal{
		{Type: "remove", UserID: charlie.id},
		addProposalFor(t, dave),
	}
	commit2, _, err := NewCommit(2, commit1.Roster, proposals, bob.id, bob.signP)
	if err != nil {
		t.Fatalf("commit2: %v", err)
	}
	if err := Verify(commit2, commit1.Roster, signKeys); err != nil {
		t.Fatalf("verify2: %v", err)
	}
	if _, ok := commit2.Envelopes[charlie.id]; ok {
		t.Fatal("removed member received an envelope")
	}
	var aliceRoot2, daveRoot2 []byte
	for _, m := range []member{alice, dave} {
		root, err := DecryptRoot(commit2, m.id, m.priv)
		if err != nil {
			t.Fatalf("%s decrypt2: %v", m.id, err)
		}
		if m.id == alice.id {
			aliceRoot2 = root
		} else {
			daveRoot2 = root
		}
	}
	if !bytes.Equal(aliceRoot2, daveRoot2) {
		t.Fatal("current members derived different roots")
	}
	if bytes.Equal(aliceRoot2, roots["charlie"]) {
		t.Fatal("epoch root did not rotate")
	}
	secret2 := EpochSecret(aliceRoot2, commit2.Roster)
	if bytes.Equal(secret1, secret2) {
		t.Fatal("epoch secret did not change")
	}
	if _, err := DecryptRoot(commit2, charlie.id, charlie.priv); err == nil {
		t.Fatal("removed member could decrypt the new epoch")
	}
	if _, err := DecryptRoot(commit2, dave.id, bob.priv); err == nil {
		t.Fatal("wrong leaf key decrypted an envelope")
	}

	// Tampered commit and forged proposal are rejected.
	tampered := commit2
	tampered.Signature = append([]byte(nil), commit2.Signature...)
	tampered.Signature[0] ^= 0xff
	if err := Verify(tampered, commit1.Roster, signKeys); err == nil {
		t.Fatal("tampered signature verified")
	}
	badProposal := commit2
	badProposal.Proposals = []Proposal{{Type: "remove", UserID: dave.id}}
	badProposal.Roster = append(append([]Leaf(nil), commit2.Roster...), charlie.leaf())
	if err := Verify(badProposal, commit1.Roster, signKeys); err == nil {
		t.Fatal("commit with inconsistent roster verified")
	}

	// Removing a non-member is rejected at commit time.
	if _, _, err := NewCommit(3, commit2.Roster, []Proposal{{Type: "remove", UserID: charlie.id}}, alice.id, alice.signP); err == nil {
		t.Fatal("removing a non-member succeeded")
	}
}

// addProposalFor builds a KeyPackage-backed Add proposal for a test member.
func addProposalFor(t *testing.T, m member) Proposal {
	t.Helper()
	kp, err := NewKeyPackageFor(m.id, m.pub, m.signP, SuiteX25519AES256GCM)
	if err != nil {
		t.Fatalf("key package for %s: %v", m.id, err)
	}
	return Proposal{Type: "add", KeyPackage: &kp}
}
