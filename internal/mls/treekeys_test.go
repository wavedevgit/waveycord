package mls

import (
	"bytes"
	"crypto/ed25519"
	"testing"
)

// TestResolutionWithBlanks proves the resolution algorithm replaces blank nodes
// with their children's resolutions and treats padding subtrees as absent.
func TestResolutionWithBlanks(t *testing.T) {
	size := 8
	present := map[int]bool{8: true, 15: true} // leaf 0 and leaf 7 only
	cases := map[int][]int{
		1: {8, 15},
		2: {8},
		3: {15},
		4: {8},
		5: nil,
		6: nil,
		7: {15},
		8: {8},
		9: nil,
	}
	for node, want := range cases {
		got := Resolution(node, size, present)
		if len(got) != len(want) {
			t.Fatalf("Resolution(%d) = %v, want %v", node, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("Resolution(%d) = %v, want %v", node, got, want)
			}
		}
	}
	// A persisted internal node short-circuits to itself.
	present[2] = true
	if got := Resolution(2, size, present); len(got) != 1 || got[0] != 2 {
		t.Fatalf("present node resolution = %v, want [2]", got)
	}
	// A fully blank tree resolves to nothing.
	if got := Resolution(1, size, map[int]bool{}); len(got) != 0 {
		t.Fatalf("blank tree resolution = %v", got)
	}
}

// TestTreePublicsPersistAcrossEpochs proves node keys outside an update's path are
// carried forward, and that adding a member with the previous tree still recovers
// the root for existing members.
func TestTreePublicsPersistAcrossEpochs(t *testing.T) {
	alice := newMember(t, "alice")
	bob := newMember(t, "bob")
	charlie := newMember(t, "charlie")
	dave := newMember(t, "dave")
	roster := []Leaf{alice.leaf(), bob.leaf()}
	signKeys := map[string]ed25519.PublicKey{
		alice.id: alice.signK, bob.id: bob.signK, charlie.id: charlie.signK, dave.id: dave.signK,
	}

	commit1, epoch1, err := NewEpochCommit(1, roster, nil, alice.id, alice.signP, alice.priv)
	if err != nil {
		t.Fatalf("epoch1: %v", err)
	}
	if len(commit1.TreePublics) == 0 || len(epoch1.TreePublics) == 0 {
		t.Fatal("epoch1 published no tree keys")
	}
	if len(commit1.TreePublics) != len(commit1.Path.Publics) {
		t.Fatalf("tree publics %d, path publics %d", len(commit1.TreePublics), len(commit1.Path.Publics))
	}

	// Epoch 2 adds Charlie while carrying epoch 1's node keys forward.
	commit2, epoch2, err := NewEpochCommitWithTree(2, commit1.Roster,
		[]Proposal{{Type: "add", UserID: charlie.id, Public: charlie.pub}},
		bob.id, bob.signP, bob.priv, epoch1.TreePublics)
	if err != nil {
		t.Fatalf("epoch2: %v", err)
	}
	if err := VerifyEpochCommit(commit2, commit1.Roster, signKeys); err != nil {
		t.Fatalf("epoch2 verify: %v", err)
	}
	carried := 0
	for node := range epoch1.TreePublics {
		if _, ok := commit2.TreePublics[node]; ok {
			carried++
		}
	}
	if carried == 0 {
		t.Fatal("no previous tree keys were carried forward")
	}
	if len(commit2.TreePublics) < len(commit2.Path.Publics) {
		t.Fatal("path keys missing from the merged tree")
	}

	// Alice and the newcomer both reach the same root.
	aliceRoot, err := DerivePathRoot(commit2, alice.id, alice.priv)
	if err != nil {
		t.Fatalf("alice epoch2 root: %v", err)
	}
	if !bytes.Equal(aliceRoot, epoch2.Root) {
		t.Fatal("alice root mismatch with carried tree")
	}
	welcomeRoot, err := DecryptWelcome(commit2, charlie.id, charlie.priv)
	if err != nil {
		t.Fatalf("charlie welcome: %v", err)
	}
	if !bytes.Equal(welcomeRoot, epoch2.Root) {
		t.Fatal("welcome root mismatch")
	}

	// Removing Bob and adding Dave in epoch 3 still lets the retained members and
	// the newcomer agree, with the off-path keys kept across two updates.
	commit3, epoch3, err := NewEpochCommitWithTree(3, commit2.Roster,
		[]Proposal{{Type: "remove", UserID: bob.id}, {Type: "add", UserID: dave.id, Public: dave.pub}},
		alice.id, alice.signP, alice.priv, epoch2.TreePublics)
	if err != nil {
		t.Fatalf("epoch3: %v", err)
	}
	if err := VerifyEpochCommit(commit3, commit2.Roster, signKeys); err != nil {
		t.Fatalf("epoch3 verify: %v", err)
	}
	daveRoot, err := DecryptWelcome(commit3, dave.id, dave.priv)
	if err != nil {
		t.Fatalf("dave welcome: %v", err)
	}
	if !bytes.Equal(daveRoot, epoch3.Root) {
		t.Fatal("dave root mismatch")
	}
	charlieRoot, err := DerivePathRoot(commit3, charlie.id, charlie.priv)
	if err != nil {
		t.Fatalf("charlie epoch3 root: %v", err)
	}
	if !bytes.Equal(charlieRoot, epoch3.Root) {
		t.Fatal("charlie root mismatch")
	}
	if _, err := DerivePathRoot(commit3, bob.id, bob.priv); err == nil {
		t.Fatal("removed member derived the epoch-3 root")
	}
}
