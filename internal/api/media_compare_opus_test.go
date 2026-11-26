//go:build opus

package api_test

import (
	"bytes"
	"testing"
)

// compareMedia accepts a transcoded payload: the RTP header must survive and the
// Opus payload is re-encoded, so only the header and presence are asserted.
func compareMedia(t *testing.T, want, got []byte) {
	t.Helper()
	if len(got) < 13 {
		t.Fatalf("forwarded packet too short: %d", len(got))
	}
	if !bytes.Equal(want[:12], got[:12]) {
		t.Fatalf("RTP header changed: %x vs %x", want[:12], got[:12])
	}
}
