package api_test

import (
	"strings"
	"testing"
)

// registerWebAuthnCredential runs the two-step registration for a test account.
func registerWebAuthnCredential(t *testing.T, h *harness, token string, auth *testAuthenticator, name string) string {
	t.Helper()
	var start struct {
		Ticket    string `json:"ticket"`
		Challenge string `json:"challenge"`
	}
	if status := h.decode("POST", "/users/@me/mfa/webauthn/credentials", token, map[string]any{}, &start); status != 200 {
		t.Fatalf("start registration %d", status)
	}
	var reg struct {
		ID string `json:"id"`
	}
	if status := h.decode("POST", "/users/@me/mfa/webauthn/credentials", token, map[string]any{
		"name": name, "ticket": start.Ticket, "credential": auth.registration(t, start.Challenge),
	}, &reg); status != 200 || reg.ID == "" {
		t.Fatalf("finish registration %d", status)
	}
	return reg.ID
}

func TestConditionalAndPasswordlessWebAuthn(t *testing.T) {
	h := newHarness(t)
	alice := h.register("condalice")
	bob := h.register("condbob")
	auth := newTestAuthenticator(t)
	credID := registerWebAuthnCredential(t, h, alice.Token, auth, "Phone key")

	// --- conditional (discoverable credential, unauthenticated) ---
	var start struct {
		Ticket    string `json:"ticket"`
		Challenge string `json:"challenge"`
		Timeout   int    `json:"timeout"`
	}
	if status := h.decode("POST", "/auth/conditional/start", "", map[string]any{}, &start); status != 200 ||
		start.Ticket == "" || start.Challenge == "" || start.Timeout <= 0 {
		t.Fatalf("conditional start %d %+v", status, start)
	}

	// Wrong challenge and wrong signature are rejected without consuming the ticket.
	if status, raw := h.request("POST", "/auth/conditional/finish", "", map[string]any{
		"ticket": start.Ticket, "credential": auth.assertion(t, "wrong-challenge", 1),
	}); status != 400 || !strings.Contains(string(raw), "Invalid authenticator response") {
		t.Fatalf("conditional wrong challenge %d %s", status, raw)
	}
	other := newTestAuthenticator(t)
	if status, raw := h.request("POST", "/auth/conditional/finish", "", map[string]any{
		"ticket": start.Ticket, "credential": other.assertion(t, start.Challenge, 1),
	}); status != 400 || !strings.Contains(string(raw), "Unknown credential") {
		t.Fatalf("conditional unknown credential %d %s", status, raw)
	}

	var completed struct{ Token string }
	if status := h.decode("POST", "/auth/conditional/finish", "", map[string]any{
		"ticket": start.Ticket, "credential": auth.assertion(t, start.Challenge, 2),
	}, &completed); status != 200 || completed.Token == "" {
		t.Fatalf("conditional finish %d", status)
	}
	var me struct {
		ID string `json:"id"`
	}
	h.decode("GET", "/users/@me", completed.Token, nil, &me)
	if me.ID != alice.ID {
		t.Fatalf("conditional session for %s, want %s", me.ID, alice.ID)
	}
	// Single-use ticket.
	if status, _ := h.request("POST", "/auth/conditional/finish", "", map[string]any{
		"ticket": start.Ticket, "credential": auth.assertion(t, start.Challenge, 3),
	}); status != 400 {
		t.Fatalf("conditional ticket reuse %d", status)
	}

	// --- passwordless (authenticated, own credential) ---
	if status, _ := h.request("POST", "/auth/passwordless/start", "", map[string]any{}); status != 401 {
		t.Fatalf("passwordless start unauthenticated %d", status)
	}
	if status, _ := h.request("POST", "/auth/passwordless/start", bob.Token, map[string]any{}); status != 403 {
		t.Fatalf("passwordless start without credentials %d", status)
	}
	var plStart struct {
		Ticket    string `json:"ticket"`
		Challenge string `json:"challenge"`
	}
	if status := h.decode("POST", "/auth/passwordless/start", alice.Token, map[string]any{}, &plStart); status != 200 ||
		plStart.Ticket == "" || plStart.Challenge == "" {
		t.Fatalf("passwordless start %d %+v", status, plStart)
	}
	// Bob cannot finish with Alice's credential.
	if status, raw := h.request("POST", "/auth/passwordless/finish", bob.Token, map[string]any{
		"ticket": plStart.Ticket, "credential": auth.assertion(t, plStart.Challenge, 4),
	}); status != 400 {
		t.Fatalf("passwordless cross-user %d %s", status, raw)
	}
	// A conditional ticket is not a passwordless ticket.
	if status, _ := h.request("POST", "/auth/passwordless/finish", alice.Token, map[string]any{
		"ticket": start.Ticket, "credential": auth.assertion(t, start.Challenge, 5),
	}); status != 400 {
		t.Fatalf("passwordless with wrong ticket kind %d", status)
	}

	var plDone struct{ Token string }
	if status := h.decode("POST", "/auth/passwordless/finish", alice.Token, map[string]any{
		"ticket": plStart.Ticket, "credential": auth.assertion(t, plStart.Challenge, 6),
	}, &plDone); status != 200 || plDone.Token == "" {
		t.Fatalf("passwordless finish %d", status)
	}
	h.decode("GET", "/users/@me", plDone.Token, nil, &me)
	if me.ID != alice.ID {
		t.Fatalf("passwordless session for %s", me.ID)
	}

	// Removing the credential invalidates discoverable login.
	if status, _ := h.request("DELETE", "/users/@me/mfa/webauthn/credentials/"+credID, alice.Token, nil); status != 204 {
		t.Fatalf("delete credential %d", status)
	}
	var third struct {
		Ticket    string `json:"ticket"`
		Challenge string `json:"challenge"`
	}
	h.decode("POST", "/auth/conditional/start", "", map[string]any{}, &third)
	status, raw := h.request("POST", "/auth/conditional/finish", "", map[string]any{
		"ticket": third.Ticket, "credential": auth.assertion(t, third.Challenge, 7),
	})
	if status != 400 || !strings.Contains(string(raw), "Unknown credential") {
		t.Fatalf("conditional after delete %d %s", status, raw)
	}
}
