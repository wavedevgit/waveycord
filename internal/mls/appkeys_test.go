package mls

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
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
