package mls

import (
	"bytes"
	"testing"
)

func sampleGroupInfo() GroupInfo {
	var pub [32]byte
	for i := range pub {
		pub[i] = byte(i + 1)
	}
	return GroupInfo{
		Epoch:  7,
		Roster: []Leaf{{UserID: "alice", Public: pub}, {UserID: "bob", Public: pub}},
		Tree: &TreeState{
			Size:      4,
			Users:     []string{"alice", "bob"},
			Positions: []int{0, 2},
			Publics:   map[int][32]byte{1: pub, 2: pub},
		},
		Root:     bytes.Repeat([]byte{0xAB}, 32),
		NodeKeys: map[int][]byte{2: bytes.Repeat([]byte{0xCD}, 32)},
	}
}

// TestGroupInfoTLSRoundTrip proves the encrypted Welcome payload uses TLS
// presentation-language encoding and decodes strictly.
func TestGroupInfoTLSRoundTrip(t *testing.T) {
	g := sampleGroupInfo()
	raw, err := EncodeGroupInfo(g)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if bytes.Contains(raw, []byte("alice")) == false {
		t.Fatal("user id should be an opaque field in the encoding")
	}
	if bytes.Contains(raw, []byte(`"epoch"`)) || raw[0] == '{' {
		t.Fatalf("payload is still JSON: %q", raw[:16])
	}
	// RFC-shaped prefix: version then cipher suite, network order.
	if got := uint16(raw[0])<<8 | uint16(raw[1]); got != 1 {
		t.Fatalf("version prefix = %d", got)
	}

	back, err := DecodeGroupInfo(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if back.Epoch != g.Epoch || len(back.Roster) != 2 || back.Roster[1].UserID != "bob" {
		t.Fatalf("decoded group info: %+v", back)
	}
	if !bytes.Equal(back.Root, g.Root) || !bytes.Equal(back.NodeKeys[2], g.NodeKeys[2]) {
		t.Fatal("root or node keys did not round trip")
	}
	if back.Tree == nil || back.Tree.Size != 4 || back.Tree.Positions[1] != 2 || back.Tree.Publics[2] != g.Tree.Publics[2] {
		t.Fatalf("tree did not round trip: %+v", back.Tree)
	}

	// Truncation and trailing bytes are rejected.
	if _, err := DecodeGroupInfo(raw[:len(raw)-1]); err == nil {
		t.Fatal("truncated payload accepted")
	}
	if _, err := DecodeGroupInfo(append(append([]byte(nil), raw...), 0)); err == nil {
		t.Fatal("trailing byte accepted")
	}
	// A tampered confirmation tag or tree hash is rejected.
	tamperedTag := append([]byte(nil), raw...)
	tamperedTag[len(tamperedTag)-36] ^= 0xff
	if _, err := DecodeGroupInfo(tamperedTag); err == nil {
		t.Fatal("tampered confirmation tag accepted")
	}
	movedHash := append([]byte(nil), raw...)
	// tree_hash sits after version(2)+suite(2)+group_id(2+0)+epoch(8)+len(2)
	movedHash[16] ^= 0xff
	if _, err := DecodeGroupInfo(movedHash); err == nil {
		t.Fatal("tampered tree hash accepted")
	}
	if back.Suite != SuiteX25519AES256GCM || back.Version != 1 || len(back.Tag) != 32 {
		t.Fatalf("group info metadata: v=%d suite=%#x tag=%d", back.Version, back.Suite, len(back.Tag))
	}

	// A wrong-sized root is rejected.
	bad := g
	bad.Root = []byte{1, 2, 3}
	rawBad, err := EncodeGroupInfo(bad)
	if err != nil {
		t.Fatalf("encode bad root: %v", err)
	}
	if _, err := DecodeGroupInfo(rawBad); err == nil {
		t.Fatal("short root accepted")
	}
}
