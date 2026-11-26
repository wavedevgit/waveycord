package mls

import (
	"bytes"
	"crypto/ed25519"
	"testing"
)

// TestPathUpdateAndWelcome proves the path-chain epoch schedule: existing members
// recover the root from their divergence envelope, a removed member has no path
// envelope, and an added member joins from the Welcome bound to the commit hash.
func TestPathUpdateAndWelcome(t *testing.T) {
	alice := newMember(t, "alice")
	bob := newMember(t, "bob")
	charlie := newMember(t, "charlie")
	dave := newMember(t, "dave")
	roster := []Leaf{alice.leaf(), bob.leaf(), charlie.leaf()}
	signKeys := map[string]ed25519.PublicKey{
		alice.id: alice.signK, bob.id: bob.signK, charlie.id: charlie.signK, dave.id: dave.signK,
	}

	// Epoch 1: path update over the initial roster.
	commit1, epoch1, err := NewEpochCommit(1, roster, nil, alice.id, alice.signP, alice.priv)
	if err != nil {
		t.Fatalf("epoch1: %v", err)
	}
	if err := VerifyEpochCommit(commit1, roster, signKeys); err != nil {
		t.Fatalf("verify1: %v", err)
	}
	aliceRoot1, err := DerivePathRoot(commit1, alice.id, alice.priv)
	if err != nil {
		t.Fatalf("alice path1: %v", err)
	}
	if !bytes.Equal(aliceRoot1, epoch1.Root) {
		t.Fatal("committer root mismatch")
	}
	for _, m := range []member{bob, charlie} {
		root, err := DerivePathRoot(commit1, m.id, m.priv)
		if err != nil {
			t.Fatalf("%s path1: %v", m.id, err)
		}
		if !bytes.Equal(root, epoch1.Root) {
			t.Fatalf("%s derived a different root", m.id)
		}
	}

	// Epoch 2: remove Charlie; he must have no envelope and cannot derive.
	commit2, epoch2, err := NewEpochCommit(2, roster,
		[]Proposal{{Type: "remove", UserID: charlie.id}}, bob.id, bob.signP, bob.priv)
	if err != nil {
		t.Fatalf("epoch2: %v", err)
	}
	if err := VerifyEpochCommit(commit2, roster, signKeys); err != nil {
		t.Fatalf("verify2: %v", err)
	}
	for _, m := range []member{alice, bob} {
		root, err := DerivePathRoot(commit2, m.id, m.priv)
		if err != nil {
			t.Fatalf("%s path2: %v", m.id, err)
		}
		if !bytes.Equal(root, epoch2.Root) {
			t.Fatalf("%s epoch2 root mismatch", m.id)
		}
	}
	if _, err := DerivePathRoot(commit2, charlie.id, charlie.priv); err == nil {
		t.Fatal("removed member derived the epoch-2 root")
	}
	if bytes.Equal(epoch1.Root, epoch2.Root) {
		t.Fatal("epoch root did not rotate")
	}

	// Epoch 3: add Dave; the Welcome lets him join and binds the commit hash.
	commit3, epoch3, err := NewEpochCommit(3, commit2.Roster,
		[]Proposal{{Type: "add", UserID: dave.id, Public: dave.pub}}, alice.id, alice.signP, alice.priv)
	if err != nil {
		t.Fatalf("epoch3: %v", err)
	}
	if err := VerifyEpochCommit(commit3, commit2.Roster, signKeys); err != nil {
		t.Fatalf("verify3: %v", err)
	}
	welcomeRoot, err := DecryptWelcome(commit3, dave.id, dave.priv)
	if err != nil {
		t.Fatalf("dave welcome: %v", err)
	}
	if !bytes.Equal(welcomeRoot, epoch3.Root) {
		t.Fatal("welcome root mismatch")
	}
	bobRoot3, err := DerivePathRoot(commit3, bob.id, bob.priv)
	if err != nil {
		t.Fatalf("bob path3: %v", err)
	}
	if !bytes.Equal(bobRoot3, epoch3.Root) {
		t.Fatal("existing member root mismatch on add")
	}
	if _, ok := commit3.Welcome[charlie.id]; ok {
		t.Fatal("removed member received a Welcome")
	}

	// Tampered Welcome hash is rejected.
	tampered := commit3
	tampered.Welcome = map[string]Welcome{dave.id: commit3.Welcome[dave.id]}
	w := tampered.Welcome[dave.id]
	w.CommitHash = "deadbeef"
	tampered.Welcome[dave.id] = w
	if _, err := DecryptWelcome(tampered, dave.id, dave.priv); err == nil {
		t.Fatal("tampered welcome hash accepted")
	}
}
