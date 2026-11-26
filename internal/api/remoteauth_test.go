package api_test

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"
)

func testFingerprint(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw)
}

func TestRemoteAuthHandoff(t *testing.T) {
	h := newHarness(t)
	alice := h.register("remotealice")
	bob := h.register("remotebob")
	fp := testFingerprint(t)

	// Start validates the fingerprint shape.
	if status, _ := h.request("POST", "/users/@me/remote-auth", alice.Token, map[string]any{
		"fingerprint": "not-hex",
	}); status != 400 {
		t.Fatalf("bad fingerprint status %d", status)
	}
	if status, _ := h.request("POST", "/users/@me/remote-auth", "", map[string]any{
		"fingerprint": fp,
	}); status != 401 {
		t.Fatalf("unauthenticated start status %d", status)
	}

	var start struct {
		Ticket    string `json:"ticket"`
		ExpiresAt string `json:"expires_at"`
	}
	if status := h.decode("POST", "/users/@me/remote-auth", alice.Token, map[string]any{
		"fingerprint": fp,
	}, &start); status != 200 || start.Ticket == "" {
		t.Fatalf("start %d %+v", status, start)
	}
	if _, err := time.Parse(time.RFC3339, start.ExpiresAt); err != nil {
		t.Fatalf("expires_at %q: %v", start.ExpiresAt, err)
	}

	// Redemption before approval fails and does not consume the session.
	if status, _ := h.request("POST", "/users/@me/remote-auth/login", "", map[string]any{
		"ticket": start.Ticket, "fingerprint": fp,
	}); status != 400 {
		t.Fatalf("login before approval status %d", status)
	}
	// Another account cannot approve or cancel Alice's handshake.
	if status, _ := h.request("POST", "/users/@me/remote-auth/finish", bob.Token, map[string]any{
		"ticket": start.Ticket,
	}); status != 404 {
		t.Fatalf("foreign finish status %d", status)
	}
	if status, _ := h.request("POST", "/users/@me/remote-auth/cancel", bob.Token, map[string]any{
		"ticket": start.Ticket,
	}); status != 404 {
		t.Fatalf("foreign cancel status %d", status)
	}

	if status, _ := h.request("POST", "/users/@me/remote-auth/finish", alice.Token, map[string]any{
		"ticket": start.Ticket,
	}); status != 204 {
		t.Fatalf("finish status %d", status)
	}
	// Approval is one-way while pending.
	if status, _ := h.request("POST", "/users/@me/remote-auth/finish", alice.Token, map[string]any{
		"ticket": start.Ticket,
	}); status != 404 {
		t.Fatalf("double finish status %d", status)
	}

	// A mismatched fingerprint cannot redeem.
	if status, _ := h.request("POST", "/users/@me/remote-auth/login", "", map[string]any{
		"ticket": start.Ticket, "fingerprint": testFingerprint(t),
	}); status != 404 {
		t.Fatalf("wrong fingerprint redeem status %d", status)
	}

	var redeemed struct{ Token string }
	if status := h.decode("POST", "/users/@me/remote-auth/login", "", map[string]any{
		"ticket": start.Ticket, "fingerprint": fp,
	}, &redeemed); status != 200 || redeemed.Token == "" {
		t.Fatalf("redeem %d", status)
	}
	var me struct {
		ID string `json:"id"`
	}
	h.decode("GET", "/users/@me", redeemed.Token, nil, &me)
	if me.ID != alice.ID {
		t.Fatalf("redeemed session for %s, want %s", me.ID, alice.ID)
	}
	// The ticket is single use.
	if status, _ := h.request("POST", "/users/@me/remote-auth/login", "", map[string]any{
		"ticket": start.Ticket, "fingerprint": fp,
	}); status != 404 {
		t.Fatalf("reused ticket status %d", status)
	}

	// Cancel prevents approval and redemption.
	var cancelled struct{ Ticket string }
	h.decode("POST", "/users/@me/remote-auth", alice.Token, map[string]any{"fingerprint": fp}, &cancelled)
	if status, _ := h.request("POST", "/users/@me/remote-auth/cancel", alice.Token, map[string]any{
		"ticket": cancelled.Ticket,
	}); status != 204 {
		t.Fatalf("cancel status %d", status)
	}
	if status, _ := h.request("POST", "/users/@me/remote-auth/finish", alice.Token, map[string]any{
		"ticket": cancelled.Ticket,
	}); status != 404 {
		t.Fatalf("finish after cancel status %d", status)
	}
	if status, _ := h.request("POST", "/users/@me/remote-auth/login", "", map[string]any{
		"ticket": cancelled.Ticket, "fingerprint": fp,
	}); status != 400 {
		t.Fatalf("login after cancel status %d", status)
	}

	// Expired handshakes cannot be approved or redeemed.
	var expiring struct{ Ticket string }
	h.decode("POST", "/users/@me/remote-auth", alice.Token, map[string]any{"fingerprint": fp}, &expiring)
	if _, err := h.store.DB.Exec(`UPDATE remote_auth_sessions SET expires = ? WHERE ticket = ?`,
		time.Now().Add(-time.Second).UnixMilli(), expiring.Ticket); err != nil {
		t.Fatalf("force expiry: %v", err)
	}
	if status, raw := h.request("POST", "/users/@me/remote-auth/finish", alice.Token, map[string]any{
		"ticket": expiring.Ticket,
	}); status != 400 {
		t.Fatalf("finish expired status %d %s", status, raw)
	}
	if status, _ := h.request("POST", "/users/@me/remote-auth/login", "", map[string]any{
		"ticket": expiring.Ticket, "fingerprint": fp,
	}); status != 400 {
		t.Fatalf("login expired status %d", status)
	}

	// Unknown tickets are 404 everywhere except redemption, which is 404 too.
	if status, _ := h.request("POST", "/users/@me/remote-auth/finish", alice.Token, map[string]any{
		"ticket": "nope",
	}); status != 404 {
		t.Fatalf("unknown finish status %d", status)
	}
}
