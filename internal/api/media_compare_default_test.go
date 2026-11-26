//go:build !opus

package api_test

import (
	"bytes"
	"testing"
)

// compareMedia requires byte-identical forwarding when no codec is compiled in.
func compareMedia(t *testing.T, want, got []byte) {
	t.Helper()
	if !bytes.Equal(want, got) {
		t.Fatalf("forwarded media mismatch: %x vs %x", want, got)
	}
}
