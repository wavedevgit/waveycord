package mls

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

func base64Decode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
func base64Encode(b []byte) string          { return base64.StdEncoding.EncodeToString(b) }

// TestMLSMessageRoundTrip proves the op 38 commit survives TLS framing with its path
// and Welcome and still verifies afterwards.
func TestMLSMessageRoundTrip(t *testing.T) {
	a := newMember(t, "mma")
	b := newMember(t, "mmb")
	c := newMember(t, "mmc")
	roster := []Leaf{a.leaf(), b.leaf()}
	signKeys := map[string]ed25519.PublicKey{a.id: a.signK, b.id: b.signK, c.id: c.signK}

	base, _, err := NewEpochCommit(1, roster, nil, a.id, a.signP, a.priv)
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	commit, epoch, err := NewEpochCommitWithState(2, roster,
		[]Proposal{{Type: "add", UserID: c.id, Public: c.pub}}, a.id, a.signP, a.priv, base.Tree)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	encoded, err := MarshalMLSMessage(commit)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(encoded, "epoch") {
		t.Fatal("message still carries JSON")
	}
	back, err := UnmarshalMLSMessage(encoded)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := VerifyEpochCommit(back, roster, signKeys); err != nil {
		t.Fatalf("framed commit rejected: %v", err)
	}
	if back.Path == nil || len(back.Path.Recipients) == 0 || back.Tree == nil || back.Welcome[c.id].CommitHash == "" {
		t.Fatal("path, tree or welcome lost in framing")
	}
	root, keys, err := DecryptWelcomeTree(back, c.id, c.priv)
	if err != nil || string(root) != string(epoch.Root) || len(keys) == 0 {
		t.Fatalf("welcome after framing: %v", err)
	}

	// Version/format changes and truncation are rejected.
	raw, err := base64Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func([]byte) []byte
	}{
		{"version", func(b []byte) []byte { b[1] ^= 0xff; return b }},
		{"format", func(b []byte) []byte { b[3] ^= 0xff; return b }},
		{"truncated", func(b []byte) []byte { return b[:len(b)-1] }},
	} {
		bad := tc.edit(append([]byte(nil), raw...))
		if _, err := UnmarshalMLSMessage(base64Encode(bad)); err == nil {
			t.Fatalf("%s accepted", tc.name)
		}
	}
}
