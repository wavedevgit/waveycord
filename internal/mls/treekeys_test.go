package mls

import (
	"bytes"
	"crypto/ecdh"
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

// TestNodeAddressedEnvelope proves a path secret can be addressed to a resolved
// internal node and opened only by a member that persisted that node's private key
// from an earlier epoch.
func TestNodeAddressedEnvelope(t *testing.T) {
	a := newMember(t, "na")
	b := newMember(t, "nb")
	c := newMember(t, "nc")
	d := newMember(t, "nd")
	e := newMember(t, "ne")
	roster := []Leaf{a.leaf(), b.leaf(), c.leaf()}
	signKeys := map[string]ed25519.PublicKey{
		a.id: a.signK, b.id: b.signK, c.id: c.signK, d.id: d.signK, e.id: e.signK,
	}

	// Epoch 1 is committed by c, so node 3 (covering c's leaf) is established and
	// published; c keeps the private keys on its path.
	commit1, epoch1, err := NewEpochCommit(1, roster, nil, c.id, c.signP, c.priv)
	if err != nil {
		t.Fatalf("epoch1: %v", err)
	}
	cRoot, cKeys, err := DerivePathRootWithKeys(commit1, c.id, c.priv, nil)
	if err != nil {
		t.Fatalf("c epoch1: %v", err)
	}
	if !bytes.Equal(cRoot, epoch1.Root) {
		t.Fatal("c epoch1 root mismatch")
	}
	if len(cKeys) < 3 {
		t.Fatalf("c persisted %d node keys, want leaf+ancestors", len(cKeys))
	}
	if _, ok := cKeys[3]; !ok {
		t.Fatal("c did not persist node 3")
	}

	// Epoch 2 adds d; a commits, and the sibling of node 2 is the present internal
	// node 3, so that path secret must be node-addressed.
	commit2, epoch2, err := NewEpochCommitWithTree(2, roster,
		[]Proposal{{Type: "add", UserID: d.id, Public: d.pub}}, a.id, a.signP, a.priv,
		epoch1.TreePublics)
	if err != nil {
		t.Fatalf("epoch2: %v", err)
	}
	if err := VerifyEpochCommit(commit2, roster, signKeys); err != nil {
		t.Fatalf("epoch2 verify: %v", err)
	}
	nodeAddressed := 0
	for _, r := range commit2.Path.Recipients {
		if r.TargetNode == 3 && r.UserID == "" {
			nodeAddressed++
		}
	}
	if nodeAddressed == 0 {
		t.Fatalf("no node-addressed envelope in %+v", commit2.Path.Recipients)
	}

	// Leaf-addressed members and the node-key holder all reach the same root.
	aRoot, _, err := DerivePathRootWithKeys(commit2, a.id, a.priv, nil)
	if err != nil {
		t.Fatalf("a epoch2: %v", err)
	}
	if !bytes.Equal(aRoot, epoch2.Root) {
		t.Fatal("a epoch2 root mismatch")
	}
	bRoot, _, err := DerivePathRootWithKeys(commit2, b.id, b.priv, nil)
	if err != nil {
		t.Fatalf("b epoch2: %v", err)
	}
	if !bytes.Equal(bRoot, epoch2.Root) {
		t.Fatal("b epoch2 root mismatch")
	}
	cRoot2, cKeys2, err := DerivePathRootWithKeys(commit2, c.id, c.priv, cKeys)
	if err != nil {
		t.Fatalf("c epoch2 via node key: %v", err)
	}
	if !bytes.Equal(cRoot2, epoch2.Root) {
		t.Fatal("c epoch2 root mismatch via node key")
	}
	if _, ok := cKeys2[1]; !ok {
		t.Fatal("c did not learn the new root secret")
	}

	// Without the persisted node key the envelope cannot be opened: a non-member
	// with a random key fails, and the newcomer has no node key from the Welcome.
	if _, _, err := DerivePathRootWithKeys(commit2, e.id, e.priv, nil); err == nil {
		t.Fatal("non-member opened the path")
	}
	wrongKeys := map[int][]byte{3: make([]byte, 32)}
	if _, _, err := DerivePathRootWithKeys(commit2, e.id, e.priv, wrongKeys); err == nil {
		t.Fatal("wrong node key opened the envelope")
	}
	if _, _, err := DerivePathRootWithKeys(commit2, d.id, d.priv, nil); err == nil {
		t.Fatal("newcomer without node keys opened a node-addressed envelope")
	}
}

// TestStableLeafSlotsAcrossRemoval proves removal blanks a slot instead of
// compacting: the tree size is unchanged, a retained member keeps its index and its
// persisted node key still opens a node-addressed envelope, while the removed member
// (under a different subtree) cannot derive the new root.
func TestStableLeafSlotsAcrossRemoval(t *testing.T) {
	a := newMember(t, "sa")
	b := newMember(t, "sb")
	c := newMember(t, "sc")
	roster := []Leaf{a.leaf(), b.leaf(), c.leaf()}
	signKeys := map[string]ed25519.PublicKey{a.id: a.signK, b.id: b.signK, c.id: c.signK}

	commit1, epoch1, err := NewEpochCommit(1, roster, nil, c.id, c.signP, c.priv)
	if err != nil {
		t.Fatalf("epoch1: %v", err)
	}
	if commit1.Tree == nil || commit1.Tree.Size != 4 {
		t.Fatalf("epoch1 tree: %+v", commit1.Tree)
	}
	_, bKeys, err := DerivePathRootWithKeys(commit1, b.id, b.priv, nil)
	if err != nil {
		t.Fatalf("b epoch1: %v", err)
	}
	cRoot, cKeys, err := DerivePathRootWithKeys(commit1, c.id, c.priv, nil)
	if err != nil || !bytes.Equal(cRoot, epoch1.Root) {
		t.Fatalf("c epoch1: root ok=%v err=%v", bytes.Equal(cRoot, epoch1.Root), err)
	}
	if _, ok := cKeys[3]; !ok {
		t.Fatal("c did not persist node 3")
	}

	// Remove b; the tree must stay size 4 with c still in slot 2.
	commit2, epoch2, err := NewEpochCommitWithState(2, roster,
		[]Proposal{{Type: "remove", UserID: b.id}}, a.id, a.signP, a.priv, epoch1.Tree)
	if err != nil {
		t.Fatalf("epoch2: %v", err)
	}
	if err := VerifyEpochCommit(commit2, roster, signKeys); err != nil {
		t.Fatalf("epoch2 verify: %v", err)
	}
	if commit2.Tree.Size != 4 {
		t.Fatalf("tree shrank on removal: size %d", commit2.Tree.Size)
	}
	if slot := commit2.Tree.SlotFor(c.id); slot != 2 {
		t.Fatalf("c slot = %d, want 2", slot)
	}
	if slot := commit2.Tree.SlotFor(b.id); slot != -1 {
		t.Fatalf("removed member still has a slot: %d", slot)
	}

	nodeAddressed := 0
	for _, r := range commit2.Path.Recipients {
		if r.TargetNode == 3 && r.UserID == "" {
			nodeAddressed++
		}
	}
	if nodeAddressed == 0 {
		t.Fatalf("no node-addressed envelope: %+v", commit2.Path.Recipients)
	}

	aRoot, _, err := DerivePathRootWithKeys(commit2, a.id, a.priv, nil)
	if err != nil || !bytes.Equal(aRoot, epoch2.Root) {
		t.Fatalf("a epoch2: %v", err)
	}
	cRoot2, cKeys2, err := DerivePathRootWithKeys(commit2, c.id, c.priv, cKeys)
	if err != nil || !bytes.Equal(cRoot2, epoch2.Root) {
		t.Fatalf("c epoch2 via persisted node key: %v", err)
	}
	if _, ok := cKeys2[1]; !ok {
		t.Fatal("c did not learn the new root key")
	}
	// b sat under node 2, which is not node-addressed, and has no leaf envelope.
	if _, _, err := DerivePathRootWithKeys(commit2, b.id, b.priv, bKeys); err == nil {
		t.Fatal("removed member derived the epoch-2 root")
	}
}

// TestRemovedMemberBranchExcluded proves a node that covered a removed member stops
// being addressable: no envelope targets its old key, a retained member under it gets
// a leaf envelope instead, and the removed member cannot derive the new root even
// with the node key it held before.
func TestRemovedMemberBranchExcluded(t *testing.T) {
	a := newMember(t, "xa")
	b := newMember(t, "xb")
	d := newMember(t, "xd")
	e := newMember(t, "xe")
	roster := []Leaf{a.leaf(), b.leaf(), d.leaf(), e.leaf()}
	signKeys := map[string]ed25519.PublicKey{a.id: a.signK, b.id: b.signK, d.id: d.signK, e.id: e.signK}

	// e commits epoch 1, so node 3 covers d (slot 2) and e (slot 3).
	commit1, epoch1, err := NewEpochCommit(1, roster, nil, e.id, e.signP, e.priv)
	if err != nil {
		t.Fatalf("epoch1: %v", err)
	}
	dKeys := map[int][]byte{}
	if _, dKeys, err = DerivePathRootWithKeys(commit1, d.id, d.priv, nil); err != nil {
		t.Fatalf("d epoch1: %v", err)
	}
	if _, ok := dKeys[3]; !ok {
		t.Fatal("d did not hold node 3")
	}

	// Remove d: node 3 still holds e but also covered the removed member, so its key
	// must not be addressed or reused.
	commit2, epoch2, err := NewEpochCommitWithState(2, roster,
		[]Proposal{{Type: "remove", UserID: d.id}}, a.id, a.signP, a.priv, epoch1.Tree)
	if err != nil {
		t.Fatalf("epoch2: %v", err)
	}
	if err := VerifyEpochCommit(commit2, roster, signKeys); err != nil {
		t.Fatalf("epoch2 verify: %v", err)
	}
	addressed := 0
	for _, r := range commit2.Path.Recipients {
		if r.TargetNode == 3 {
			addressed++
		}
	}
	if addressed == 0 {
		t.Fatalf("rekeyed branch was not node-addressed: %+v", commit2.Path.Recipients)
	}
	oldPub, hadOld := commit1.Tree.Publics[3]
	newPub, hasNew := commit2.Tree.Publics[3]
	if !hadOld || !hasNew {
		t.Fatalf("node 3 key missing (old=%v new=%v)", hadOld, hasNew)
	}
	if oldPub == newPub {
		t.Fatal("node 3 public key was not rotated")
	}
	if slot := commit2.Tree.SlotFor(d.id); slot != -1 {
		t.Fatalf("removed member retained slot %d", slot)
	}
	// The removed member cannot reach the new root even with its old node key.
	if _, _, err := DerivePathRootWithKeys(commit2, d.id, d.priv, dKeys); err == nil {
		t.Fatal("removed member derived the epoch-2 root with its stale node key")
	}
	// The retained member under the rekeyed node learns the fresh node secret and the
	// root, and persists the rotated node key for later commits.
	eRoot, eKeys, err := DerivePathRootWithKeys(commit2, e.id, e.priv, nil)
	if err != nil || !bytes.Equal(eRoot, epoch2.Root) {
		t.Fatalf("e epoch2: %v", err)
	}
	fresh, ok := eKeys[3]
	if !ok {
		t.Fatal("e did not persist the rekeyed node key")
	}
	if clone32(fresh) != newPub {
		// The node secret must match the public key that was published.
		priv, kerr := ecdh.X25519().NewPrivateKey(fresh)
		if kerr != nil || clone32(priv.PublicKey().Bytes()) != newPub {
			t.Fatal("e's node key does not match the published rekeyed public key")
		}
	}
	aRoot, _, err := DerivePathRootWithKeys(commit2, a.id, a.priv, nil)
	if err != nil || !bytes.Equal(aRoot, epoch2.Root) {
		t.Fatalf("a epoch2: %v", err)
	}
}
