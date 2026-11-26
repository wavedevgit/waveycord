package mls

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

// TestKeyPackageLeaf proves an RFC-shaped KeyPackage carries a self-signed LeafNode,
// survives TLS framing, and drives Add proposal membership (identity and signature
// key come from the credential/leaf, not from the proposal).
func TestKeyPackageLeaf(t *testing.T) {
	_, _, _ = ed25519.GenerateKey(rand.Reader)
	signPub, signPriv, _ := ed25519.GenerateKey(rand.Reader)
	var encKey [32]byte
	if _, err := rand.Read(encKey[:]); err != nil {
		t.Fatal(err)
	}
	leaf := LeafNode{
		EncryptionKey: encKey,
		SignatureKey:  signPub,
		Credential:    Credential{Type: 1, Identity: []byte("newcomer")},
		Source:        1,
	}
	leaf.Sign(signPriv)
	if err := VerifyLeaf(leaf); err != nil {
		t.Fatalf("leaf verification: %v", err)
	}
	var initKey [32]byte
	if _, err := rand.Read(initKey[:]); err != nil {
		t.Fatal(err)
	}
	kp := KeyPackage{Version: 1, CipherSuite: SuiteX25519AES256GCM, Leaf: leaf, InitKey: initKey}
	kp.Sign(signPriv)
	raw, err := EncodeKeyPackage(kp)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	back, err := DecodeKeyPackage(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(back.Leaf.Credential.Identity) != "newcomer" || back.Leaf.SignatureKey == nil {
		t.Fatalf("decoded leaf: %+v", back.Leaf)
	}
	// A tampered leaf signature is rejected.
	bad := kp
	bad.Leaf.Signature = append([]byte(nil), leaf.Signature...)
	bad.Leaf.Signature[0] ^= 0xff
	if err := VerifyKeyPackage(bad); err == nil {
		t.Fatal("tampered leaf signature accepted")
	}

	// Add via KeyPackage: membership is taken from the credential, not the proposal.
	alice := newMember(t, "lna")
	roster := []Leaf{alice.leaf()}
	proposal := Proposal{Type: "add", KeyPackage: &back}
	next, err := ApplyProposals(roster, []Proposal{proposal})
	if err != nil {
		t.Fatalf("apply add: %v", err)
	}
	found := false
	for _, l := range next {
		if l.UserID == "newcomer" {
			found = true
			if l.Public != encKey || string(l.SignatureKey) != string(signPub) {
				t.Fatalf("leaf from key package: %+v", l)
			}
			if len(l.KeyPackage) == 0 {
				t.Fatal("leaf did not retain the key package binding")
			}
		}
	}
	if !found {
		t.Fatal("key package member missing from roster")
	}
	// A proposal whose declared user disagrees with the credential is refused.
	if _, err := ApplyProposals(roster, []Proposal{{
		Type: "add", UserID: "someone-else", KeyPackage: &back,
	}}); err == nil {
		t.Fatal("credential/user mismatch accepted")
	}
}
