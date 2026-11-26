package mls

import (
	"crypto/ed25519"
	"crypto/sha256"

	"fmt"
)

// RFC 9420 LeafNode and KeyPackage structures. A leaf carries an HPKE encryption key,
// a signature key, a basic credential (the member id) and a self-signature; a
// KeyPackage wraps a leaf with an init key and is signed by the leaf's signature key.

// Credential is a basic identity credential.
type Credential struct {
	Type     uint16 `json:"type"`
	Identity []byte `json:"identity"`
}

// LeafNode is the RFC-shaped tree leaf.
type LeafNode struct {
	EncryptionKey [32]byte          `json:"encryption_key"`
	SignatureKey  ed25519.PublicKey `json:"signature_key"`
	Credential    Credential        `json:"credential"`
	Source        uint8             `json:"leaf_node_source"`
	Extensions    []LeafExtension   `json:"extensions,omitempty"`
	Signature     []byte            `json:"signature"`
}

// LeafExtension is an opaque leaf extension.
type LeafExtension struct {
	Type uint16 `json:"type"`
	Data []byte `json:"data"`
}

// KeyPackage is the Add payload.
type KeyPackage struct {
	Version     uint16      `json:"version"`
	CipherSuite Ciphersuite `json:"cipher_suite"`
	Leaf        LeafNode    `json:"leaf_node"`
	InitKey     [32]byte    `json:"init_key"`
	Signature   []byte      `json:"signature"`
}

func credentialID(l LeafNode) string { return string(l.Credential.Identity) }

// leafCanonical is the signed leaf content.
func leafCanonical(l LeafNode) []byte {
	w := &tlsWriter{}
	_ = w.opaque16(l.EncryptionKey[:])
	_ = w.opaque16(l.SignatureKey)
	w.u16(l.Credential.Type)
	_ = w.opaque16(l.Credential.Identity)
	w.u8(l.Source)
	_ = w.vector16(len(l.Extensions), func(i int) error {
		w.u16(l.Extensions[i].Type)
		return w.opaque16(l.Extensions[i].Data)
	})
	return w.buf
}

// SignLeaf sets the leaf self-signature.
func (l *LeafNode) Sign(priv ed25519.PrivateKey) {
	l.Signature = ed25519.Sign(priv, leafCanonical(*l))
}

// VerifyLeaf checks the leaf self-signature against its signature key.
func VerifyLeaf(l LeafNode) error {
	if len(l.SignatureKey) != ed25519.PublicKeySize || len(l.Signature) == 0 {
		return ErrVerification
	}
	if !ed25519.Verify(l.SignatureKey, leafCanonical(l), l.Signature) {
		return ErrVerification
	}
	return nil
}

// keyPackageCanonical is the signed KeyPackage content (excluding its signature).
func keyPackageCanonical(k KeyPackage) []byte {
	w := &tlsWriter{}
	w.u16(k.Version)
	w.u16(uint16(k.CipherSuite))
	_ = w.opaque16(leafCanonical(k.Leaf))
	_ = w.opaque16(k.InitKey[:])
	return w.buf
}

// SignKeyPackage signs the package with the leaf signature key.
func (k *KeyPackage) Sign(priv ed25519.PrivateKey) {
	k.Signature = ed25519.Sign(priv, keyPackageCanonical(*k))
}

// VerifyKeyPackage checks the KeyPackage signature and the leaf self-signature.
func VerifyKeyPackage(k KeyPackage) error {
	if k.Version != 1 || !k.CipherSuite.Supported() {
		return ErrVerification
	}
	if len(k.Signature) == 0 || len(k.Leaf.SignatureKey) != ed25519.PublicKeySize {
		return ErrVerification
	}
	if !ed25519.Verify(k.Leaf.SignatureKey, keyPackageCanonical(k), k.Signature) {
		return ErrVerification
	}
	return VerifyLeaf(k.Leaf)
}

// EncodeKeyPackage serializes a KeyPackage with TLS framing.
func EncodeKeyPackage(k KeyPackage) ([]byte, error) {
	if !k.CipherSuite.Supported() {
		return nil, errTLS
	}
	w := &tlsWriter{}
	w.u16(k.Version)
	w.u16(uint16(k.CipherSuite))
	// Leaf node.
	_ = w.opaque16(k.Leaf.EncryptionKey[:])
	_ = w.opaque16(k.Leaf.SignatureKey)
	w.u16(k.Leaf.Credential.Type)
	_ = w.opaque16(k.Leaf.Credential.Identity)
	w.u8(k.Leaf.Source)
	if err := w.vector16(len(k.Leaf.Extensions), func(i int) error {
		w.u16(k.Leaf.Extensions[i].Type)
		return w.opaque16(k.Leaf.Extensions[i].Data)
	}); err != nil {
		return nil, err
	}
	_ = w.opaque16(k.Leaf.Signature)
	_ = w.opaque16(k.InitKey[:])
	_ = w.opaque16(k.Signature)
	return w.buf, nil
}

// DecodeKeyPackage parses a KeyPackage and verifies both signatures.
func DecodeKeyPackage(raw []byte) (KeyPackage, error) {
	var k KeyPackage
	r := &tlsReader{data: raw}
	version, err := r.u16()
	if err != nil {
		return k, err
	}
	suite, err := r.u16()
	if err != nil {
		return k, err
	}
	k.Version = version
	k.CipherSuite = Ciphersuite(suite)
	enc, err := r.opaque16()
	if err != nil {
		return k, err
	}
	if len(enc) != 32 {
		return k, errTLS
	}
	copy(k.Leaf.EncryptionKey[:], enc)
	sigKey, err := r.opaque16()
	if err != nil {
		return k, err
	}
	if len(sigKey) != ed25519.PublicKeySize {
		return k, errTLS
	}
	k.Leaf.SignatureKey = ed25519.PublicKey(append([]byte(nil), sigKey...))
	credType, err := r.u16()
	if err != nil {
		return k, err
	}
	identity, err := r.opaque16()
	if err != nil {
		return k, err
	}
	k.Leaf.Credential = Credential{Type: credType, Identity: identity}
	source, err := r.u8()
	if err != nil {
		return k, err
	}
	k.Leaf.Source = source
	extCount, err := r.vector16()
	if err != nil {
		return k, err
	}
	for i := 0; i < extCount; i++ {
		typ, err := r.u16()
		if err != nil {
			return k, err
		}
		data, err := r.opaque16()
		if err != nil {
			return k, err
		}
		k.Leaf.Extensions = append(k.Leaf.Extensions, LeafExtension{Type: typ, Data: data})
	}
	leafSig, err := r.opaque16()
	if err != nil {
		return k, err
	}
	k.Leaf.Signature = leafSig
	initKey, err := r.opaque16()
	if err != nil {
		return k, err
	}
	if len(initKey) != 32 {
		return k, errTLS
	}
	copy(k.InitKey[:], initKey)
	sig, err := r.opaque16()
	if err != nil {
		return k, err
	}
	k.Signature = sig
	if r.remaining() != 0 {
		return k, errTLS
	}
	if err := VerifyKeyPackage(k); err != nil {
		return k, err
	}
	return k, nil
}

// leafFromKeyPackage converts a verified KeyPackage into the internal leaf.
func leafFromKeyPackage(k KeyPackage) Leaf {
	return Leaf{
		UserID:       credentialID(k.Leaf),
		Public:       k.Leaf.EncryptionKey,
		SignatureKey: append(ed25519.PublicKey(nil), k.Leaf.SignatureKey...),
		KeyPackage:   append([]byte(nil), keyPackageCanonical(k)...),
	}
}

// LeafFromKeyPackage builds a tree leaf from a verified KeyPackage.
func LeafFromKeyPackage(k KeyPackage) (Leaf, error) {
	if err := VerifyKeyPackage(k); err != nil {
		return Leaf{}, err
	}
	return leafFromKeyPackage(k), nil
}

var _ = sha256.Sum256
var _ = fmt.Sprintf
