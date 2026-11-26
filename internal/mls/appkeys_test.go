package mls

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
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
