package mls

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/sha256"
	"testing"
)

// TestExpandWithLabelAndApplicationKeys pins the labeled KDF and the per-epoch
// application key schedule.
func TestExpandWithLabelAndApplicationKeys(t *testing.T) {
	secret := bytes.Repeat([]byte{0x42}, 32)

	if !bytes.Equal(ExpandWithLabel(secret, "path", nil, 32), ExpandWithLabel(secret, "path", nil, 32)) {
		t.Fatal("ExpandWithLabel is not deterministic")
	}
	if bytes.Equal(ExpandWithLabel(secret, "path", nil, 32), ExpandWithLabel(secret, "key", nil, 32)) {
		t.Fatal("labels collide")
	}
	if bytes.Equal(ExpandWithLabel(secret, "path", nil, 32), ExpandWithLabel(secret, "path", []byte("ctx"), 32)) {
		t.Fatal("context ignored")
	}

	k1 := DeriveApplicationKeys(secret, 1)
	k2 := DeriveApplicationKeys(secret, 2)
	if len(k1.Key) != 32 || len(k1.BaseNonce) != 12 || len(k1.Exporter) != 32 {
		t.Fatalf("sizes key=%d nonce=%d exporter=%d", len(k1.Key), len(k1.BaseNonce), len(k1.Exporter))
	}
	if bytes.Equal(k1.Key, k1.Exporter) || bytes.Equal(k1.Key, k1.BaseNonce) {
		t.Fatal("schedule outputs are not separated")
	}
	if bytes.Equal(k1.Key, k2.Key) || bytes.Equal(k1.BaseNonce, k2.BaseNonce) || bytes.Equal(k1.Exporter, k2.Exporter) {
		t.Fatal("epoch is not bound into the schedule")
	}
	if !bytes.Equal(k1.Key, DeriveApplicationKeys(secret, 1).Key) {
		t.Fatal("schedule is not deterministic")
	}

	// NonceFor is an XOR ratchet over the base nonce.
	if !bytes.Equal(NonceFor(k1.BaseNonce, 0), k1.BaseNonce) {
		t.Fatal("counter 0 changed the base nonce")
	}
	n1 := NonceFor(k1.BaseNonce, 1)
	n2 := NonceFor(k1.BaseNonce, 2)
	if bytes.Equal(n1, n2) || bytes.Equal(n1, k1.BaseNonce) {
		t.Fatal("nonce ratchet does not advance")
	}
	// XOR is reversible and keeps the length.
	if !bytes.Equal(NonceFor(n1, 1), k1.BaseNonce) {
		t.Fatal("nonce XOR is not reversible")
	}

	block, err := aes.NewCipher(k1.Key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	header := []byte("hdr")
	sealed := gcm.Seal(nil, n1, []byte("payload"), header)
	plain, err := gcm.Open(nil, n1, sealed, header)
	if err != nil || string(plain) != "payload" {
		t.Fatalf("open under scheduled key/nonce: %v", err)
	}
	if _, err := gcm.Open(nil, n2, sealed, header); err == nil {
		t.Fatal("wrong counter nonce opened the record")
	}
	other := DeriveApplicationKeys(secret, 2)
	otherBlock, _ := aes.NewCipher(other.Key)
	otherGCM, _ := cipher.NewGCM(otherBlock)
	if _, err := otherGCM.Open(nil, n1, sealed, header); err == nil {
		t.Fatal("wrong epoch key opened the record")
	}
}

// TestCiphersuiteNegotiation proves a second suite is selectable, that its key
// schedule uses the matching key length, and that unsupported suites are refused.
func TestCiphersuiteNegotiation(t *testing.T) {
	alice := newMember(t, "nsa")
	bob := newMember(t, "nsb")
	roster := []Leaf{alice.leaf(), bob.leaf()}
	signKeys := map[string]ed25519.PublicKey{alice.id: alice.signK, bob.id: bob.signK}

	commit, epoch, err := NewEpochCommitWithSuite(1, roster, nil, nil, SuiteX25519AES128GCM,
		alice.id, alice.signP, alice.priv, nil)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if commit.CipherSuite != SuiteX25519AES128GCM || !commit.CipherSuite.Supported() {
		t.Fatalf("suite = %#x", commit.CipherSuite)
	}
	if err := VerifyEpochCommit(commit, roster, signKeys); err != nil {
		t.Fatalf("AES-128 commit rejected: %v", err)
	}

	secret := EpochSecret(epoch.Root, roster)
	keys256 := DeriveApplicationKeysFor(secret, 1, SuiteX25519AES256GCM)
	keys128 := DeriveApplicationKeysFor(secret, 1, SuiteX25519AES128GCM)
	if len(keys128.Key) != 16 || len(keys128.BaseNonce) != 12 || len(keys128.Exporter) != 16 {
		t.Fatalf("AES-128 sizes key=%d nonce=%d exporter=%d", len(keys128.Key), len(keys128.BaseNonce), len(keys128.Exporter))
	}
	if len(keys256.Key) != 32 {
		t.Fatalf("AES-256 key size %d", len(keys256.Key))
	}
	if bytes.Equal(keys128.Key, keys256.Key[:16]) {
		t.Fatal("suite is not bound into the key schedule")
	}

	block, err := aes.NewCipher(keys128.Key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := NonceFor(keys128.BaseNonce, 3)
	sealed := gcm.Seal(nil, nonce, []byte("aes128"), []byte("hdr"))
	if plain, err := gcm.Open(nil, nonce, sealed, []byte("hdr")); err != nil || string(plain) != "aes128" {
		t.Fatalf("AES-128 round trip: %v", err)
	}

	if _, _, err := NewEpochCommitWithSuite(1, roster, nil, nil, Ciphersuite(0x0099),
		alice.id, alice.signP, alice.priv, nil); err == nil {
		t.Fatal("unsupported suite accepted at construction")
	}

	// A suite swapped after signing is rejected before signature checking.
	bad := commit
	bad.CipherSuite = Ciphersuite(0x0099)
	if err := VerifyEpochCommit(bad, roster, signKeys); err == nil {
		t.Fatal("unsupported negotiated suite accepted")
	}
}

// TestProposalSuiteNegotiation proves a ciphersuite changes only through a signed
// ciphersuite proposal and that silent changes are rejected against the previous
// epoch's suite.
func TestProposalSuiteNegotiation(t *testing.T) {
	a := newMember(t, "psa")
	b := newMember(t, "psb")
	roster := []Leaf{a.leaf(), b.leaf()}
	signKeys := map[string]ed25519.PublicKey{a.id: a.signK, b.id: b.signK}

	negotiated, epoch, err := NewEpochCommitNegotiating(2, roster,
		[]Proposal{{Type: "remove", UserID: b.id}}, SuiteX25519AES128GCM,
		a.id, a.signP, a.priv, nil)
	if err != nil {
		t.Fatalf("negotiating commit: %v", err)
	}
	if err := VerifyEpochCommitWithSuite(negotiated, roster, SuiteX25519AES256GCM, signKeys); err != nil {
		t.Fatalf("negotiated change rejected: %v", err)
	}
	sawSuite := false
	for _, p := range negotiated.Proposals {
		if p.Type == "ciphersuite" && p.Suite == SuiteX25519AES128GCM {
			sawSuite = true
		}
	}
	if !sawSuite {
		t.Fatal("ciphersuite proposal missing from the commit")
	}
	if negotiated.CipherSuite != SuiteX25519AES128GCM || len(epoch.Root) != 32 {
		t.Fatalf("commit suite %#x", negotiated.CipherSuite)
	}

	// The same commit verifies without a previous-suite constraint, but a silent
	// change (no proposal) is rejected against the previous suite.
	silent, _, err := NewEpochCommitWithSuite(2, roster, nil, nil, SuiteX25519AES128GCM,
		a.id, a.signP, a.priv, nil)
	if err != nil {
		t.Fatalf("silent commit: %v", err)
	}
	if err := VerifyEpochCommit(silent, roster, signKeys); err != nil {
		t.Fatalf("silent commit with no previous suite: %v", err)
	}
	if err := VerifyEpochCommitWithSuite(silent, roster, SuiteX25519AES256GCM, signKeys); err == nil {
		t.Fatal("silent mid-epoch suite change accepted")
	}
	// Staying on the previous suite without a proposal is allowed.
	same, _, err := NewEpochCommitWithSuite(2, roster, nil, nil, SuiteX25519AES256GCM,
		a.id, a.signP, a.priv, nil)
	if err != nil {
		t.Fatalf("same-suite commit: %v", err)
	}
	if err := VerifyEpochCommitWithSuite(same, roster, SuiteX25519AES256GCM, signKeys); err != nil {
		t.Fatalf("same-suite epoch rejected: %v", err)
	}

	// Two ciphersuite proposals are rejected.
	dup, _, err := NewEpochCommitWithSuite(2, roster, []Proposal{
		{Type: "ciphersuite", Suite: SuiteX25519AES128GCM},
		{Type: "ciphersuite", Suite: SuiteX25519AES128GCM},
	}, nil, SuiteX25519AES128GCM, a.id, a.signP, a.priv, nil)
	if err != nil {
		t.Fatalf("duplicate proposal build: %v", err)
	}
	if err := VerifyEpochCommit(dup, roster, signKeys); err == nil {
		t.Fatal("duplicate ciphersuite proposals accepted")
	}

	// A proposal naming a different suite than the commit is refused at build time.
	if _, _, err := NewEpochCommitWithSuite(2, roster, []Proposal{
		{Type: "ciphersuite", Suite: SuiteX25519AES128GCM},
	}, nil, SuiteX25519AES256GCM, a.id, a.signP, a.priv, nil); err == nil {
		t.Fatal("mismatched suite proposal accepted")
	}
	// An unsupported suite proposal is refused when applied.
	if _, _, err := NewEpochCommitNegotiating(2, roster, nil, Ciphersuite(0x0099),
		a.id, a.signP, a.priv, nil); err == nil {
		t.Fatal("unsupported negotiation accepted")
	}
}

// TestCommitContextChaining proves each commit commits to the previous tree hash and
// chains the confirmed transcript hash.
func TestCommitContextChaining(t *testing.T) {
	a := newMember(t, "cha")
	b := newMember(t, "chb")
	roster := []Leaf{a.leaf(), b.leaf()}
	base, _, err := NewEpochCommit(1, roster, nil, a.id, a.signP, a.priv)
	if err != nil {
		t.Fatalf("epoch1: %v", err)
	}
	if base.ConfirmedHash == ([32]byte{}) || base.TreeHash != ([32]byte{}) {
		t.Fatalf("epoch1 context: tree=%x confirmed=%x", base.TreeHash, base.ConfirmedHash)
	}
	commit2, _, err := NewEpochCommitWithState(2, roster, nil, a.id, a.signP, a.priv, base.Tree)
	if err != nil {
		t.Fatalf("epoch2: %v", err)
	}
	// Tree hash must cover the previous tree.
	tw := &tlsWriter{}
	if err := encodeTree(tw, base.Tree); err != nil {
		t.Fatal(err)
	}
	wantTree := sha256.Sum256(tw.buf)
	if commit2.TreeHash != wantTree {
		t.Fatalf("epoch2 tree hash mismatch")
	}
	if got := ChainedConfirmed(base.ConfirmedHash, commit2.TreeHash, 2); got != commit2.ConfirmedHash {
		t.Fatalf("confirmed hash not chained: %x vs %x", got, commit2.ConfirmedHash)
	}
	if ChainedConfirmed(base.ConfirmedHash, commit2.TreeHash, 3) == commit2.ConfirmedHash {
		t.Fatal("epoch is not bound into the chain")
	}
}
