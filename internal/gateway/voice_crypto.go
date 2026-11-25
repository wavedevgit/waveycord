package gateway

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// Media crypto boundary. The voice gateway performs an X25519 exchange per
// connection, derives an AES-256-GCM key with HKDF, and wraps media frames in a
// replay-protected envelope. Frames are decrypted by the server and re-wrapped for
// each recipient, so this is a client<->server AEAD boundary, not MLS-based
// end-to-end encryption: the server sees plaintext. It is documented as such.
//
// Envelope: "WCDF" | key_id u32 | counter u64 | ssrc u32 | ciphertext+tag
// AAD covers the whole header, so key id, counter and ssrc are authenticated.

const daveMagic = "WCDF"

var errReplay = errors.New("voice: replayed counter")
var errKeyMismatch = errors.New("voice: key id mismatch")
var errTampered = errors.New("voice: frame authentication failed")

func newEphemeralKey() (priv [32]byte, pub [32]byte, err error) {
	if _, err := rand.Read(priv[:]); err != nil {
		return priv, pub, err
	}
	curve25519.ScalarBaseMult(&pub, &priv)
	return priv, pub, nil
}

func deriveMediaKey(shared []byte) ([]byte, error) {
	reader := hkdf.New(sha256.New, shared, []byte("waveycord-dave-v1"), []byte("rtp"))
	key := make([]byte, 32)
	if _, err := reader.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func mediaNonce(counter uint64) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[4:], counter)
	return nonce
}

func encodeEnvelope(gcm cipher.AEAD, keyID uint32, counter uint64, ssrc uint32, plaintext []byte) []byte {
	header := make([]byte, 20)
	copy(header[0:4], daveMagic)
	binary.BigEndian.PutUint32(header[4:8], keyID)
	binary.BigEndian.PutUint64(header[8:16], counter)
	binary.BigEndian.PutUint32(header[16:20], ssrc)
	sealed := gcm.Seal(nil, mediaNonce(counter), plaintext, header)
	return append(header, sealed...)
}

// openEnvelope validates the header, counter and AEAD tag.
func openEnvelope(gcm cipher.AEAD, keyID uint32, lastCounter uint64, packet []byte) (uint64, uint32, []byte, error) {
	if len(packet) < 20+16 {
		return 0, 0, nil, errTampered
	}
	if string(packet[0:4]) != daveMagic {
		return 0, 0, nil, errTampered
	}
	id := binary.BigEndian.Uint32(packet[4:8])
	if id != keyID {
		return 0, 0, nil, errKeyMismatch
	}
	counter := binary.BigEndian.Uint64(packet[8:16])
	if counter <= lastCounter {
		return 0, 0, nil, errReplay
	}
	ssrc := binary.BigEndian.Uint32(packet[16:20])
	plaintext, err := gcm.Open(nil, mediaNonce(counter), packet[20:], packet[0:20])
	if err != nil {
		return 0, 0, nil, errTampered
	}
	return counter, ssrc, plaintext, nil
}

func encodeKey(pub [32]byte) string { return base64.StdEncoding.EncodeToString(pub[:]) }

func decodeKey(raw string) ([32]byte, error) {
	var out [32]byte
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(b) != 32 {
		return out, errTampered
	}
	copy(out[:], b)
	return out, nil
}
