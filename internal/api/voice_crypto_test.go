package api_test

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"testing"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// Client-side implementation of the voice media crypto envelope, mirroring
// internal/gateway/voice_crypto.go.

func clientKeypair(t *testing.T) ([32]byte, [32]byte) {
	t.Helper()
	var priv, pub [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		t.Fatal(err)
	}
	curve25519.ScalarBaseMult(&pub, &priv)
	return priv, pub
}

func clientAEAD(t *testing.T, priv, serverPub [32]byte) cipher.AEAD {
	t.Helper()
	shared, err := curve25519.X25519(priv[:], serverPub[:])
	if err != nil {
		t.Fatalf("x25519: %v", err)
	}
	reader := hkdf.New(sha256.New, shared, []byte("waveycord-dave-v1"), []byte("rtp"))
	key := make([]byte, 32)
	if _, err := reader.Read(key); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return gcm
}

func clientNonce(counter uint64) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[4:], counter)
	return nonce
}

func clientSeal(gcm cipher.AEAD, keyID uint32, counter uint64, ssrc uint32, plaintext []byte) []byte {
	header := make([]byte, 20)
	copy(header[0:4], "WCDF")
	binary.BigEndian.PutUint32(header[4:8], keyID)
	binary.BigEndian.PutUint64(header[8:16], counter)
	binary.BigEndian.PutUint32(header[16:20], ssrc)
	return append(header, gcm.Seal(nil, clientNonce(counter), plaintext, header)...)
}

func clientOpen(t *testing.T, gcm cipher.AEAD, packet []byte) []byte {
	t.Helper()
	if len(packet) < 36 || string(packet[0:4]) != "WCDF" {
		t.Fatalf("bad envelope prefix: %x", packet[0:4])
	}
	counter := binary.BigEndian.Uint64(packet[8:16])
	plaintext, err := gcm.Open(nil, clientNonce(counter), packet[20:], packet[0:20])
	if err != nil {
		t.Fatalf("client decrypt: %v", err)
	}
	return plaintext
}

func parseServerKey(t *testing.T, frame map[string]any) (uint32, [32]byte) {
	t.Helper()
	if frame["op"].(float64) != 31 {
		t.Fatalf("expected key exchange reply: %+v", frame)
	}
	d := frame["d"].(map[string]any)
	keyID := uint32(d["key_id"].(float64))
	raw, err := base64.StdEncoding.DecodeString(d["public_key"].(string))
	if err != nil || len(raw) != 32 {
		t.Fatalf("bad server public key: %v", err)
	}
	var pub [32]byte
	copy(pub[:], raw)
	if keyID == 0 {
		t.Fatal("zero key id")
	}
	return keyID, pub
}
