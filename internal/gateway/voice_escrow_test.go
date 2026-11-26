package gateway

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestEscrowWrapBinding(t *testing.T) {
	priv, pub, err := newEphemeralKey()
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{9}, 32)
	wrap, err := sealEscrow(pub, key, 7, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	got, err := unsealEscrow(priv, wrap, 7, "session-a")
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("roundtrip: %v", err)
	}
	for _, tc := range []struct {
		name    string
		packet  []byte
		id      uint32
		session string
	}{
		{"key-id", wrap, 8, "session-a"},
		{"session", wrap, 7, "session-b"},
		{"truncated", wrap[:len(wrap)-1], 7, "session-a"},
		{"extended", append(append([]byte{}, wrap...), 0), 7, "session-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := unsealEscrow(priv, tc.packet, tc.id, tc.session); err == nil {
				t.Fatal("accepted invalid wrap")
			}
		})
	}
	wrap[len(wrap)-1] ^= 1
	if _, err := unsealEscrow(priv, wrap, 7, "session-a"); err == nil {
		t.Fatal("accepted tampered wrap")
	}
	if _, err := sealEscrow(pub, key[:16], 7, "session-a"); err == nil {
		t.Fatal("accepted short key")
	}
}

func TestEscrowMediaAuthentication(t *testing.T) {
	gcm, err := newGCM(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	c := &voiceConn{ssrc: 42, escrow: gcm, escrowKeyID: 7}
	inner := make([]byte, 13)
	inner[0] = 0x80
	binary.BigEndian.PutUint32(inner[8:12], 42)
	frame := func(counter uint64, ssrc uint32, payload []byte) []byte {
		header := make([]byte, 20)
		copy(header, e2eeMagic)
		binary.BigEndian.PutUint32(header[4:], 7)
		binary.BigEndian.PutUint64(header[8:], counter)
		binary.BigEndian.PutUint32(header[16:], ssrc)
		return append(header, gcm.Seal(nil, escrowMediaNonce(ssrc, counter), payload, header)...)
	}
	valid := frame(1, 42, inner)
	if _, err := c.openEscrowMedia(valid); err != nil {
		t.Fatal(err)
	}
	if _, err := c.openEscrowMedia(valid); err != errReplay {
		t.Fatalf("replay: %v", err)
	}
	badInner := append([]byte{}, inner...)
	binary.BigEndian.PutUint32(badInner[8:], 43)
	for _, bad := range [][]byte{frame(2, 43, inner), frame(2, 42, badInner), frame(2, 42, inner[:8])} {
		if _, err := c.openEscrowMedia(bad); err == nil {
			t.Fatal("accepted wrong source or malformed RTP")
		}
	}
	tampered := frame(2, 42, inner)
	tampered[len(tampered)-1] ^= 1
	if _, err := c.openEscrowMedia(tampered); err == nil {
		t.Fatal("accepted invalid tag")
	}
	if c.escrowCounter != 1 {
		t.Fatal("invalid packet advanced replay state")
	}
	if _, err := c.openEscrowMedia(frame(2, 42, inner)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.openEscrowMedia(frame(4, 42, inner)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.openEscrowMedia(frame(3, 42, inner)); err != nil {
		t.Fatalf("reordered frame rejected: %v", err)
	}
	if _, err := c.openEscrowMedia(frame(3, 42, inner)); err != errReplay {
		t.Fatalf("reordered replay: %v", err)
	}
	if _, err := c.openEscrowMedia(frame(100, 42, inner)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.openEscrowMedia(frame(5, 42, inner)); err != errReplay {
		t.Fatalf("old-window replay: %v", err)
	}
}
