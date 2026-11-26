package api_test

import (
	"encoding/json"
	"testing"
	"time"
)

// storedSMSCode reads the outstanding code directly: this server does not deliver
// SMS, so the row is the only place the value exists.
func storedSMSCode(t *testing.T, h *harness, userID, purpose string) string {
	t.Helper()
	var code string
	if err := h.store.DB.QueryRow(`SELECT code FROM sms_mfa_codes WHERE user_id = ? AND purpose = ?`,
		userID, purpose).Scan(&code); err != nil {
		t.Fatalf("read %s code: %v", purpose, err)
	}
	return code
}

func TestSMSMFAEnrollmentLoginAndDisable(t *testing.T) {
	h := newHarness(t)
	alice := h.register("smsalice")

	if status, _ := h.request("POST", "/users/@me/mfa/sms/send", alice.Token, map[string]any{
		"phone": "abc",
	}); status != 400 {
		t.Fatalf("bad phone status %d", status)
	}
	if status, _ := h.request("POST", "/users/@me/mfa/sms/send", alice.Token, map[string]any{
		"phone": "+15551234567",
	}); status != 204 {
		t.Fatalf("send status %d", status)
	}
	enableCode := storedSMSCode(t, h, alice.ID, "enable")
	if len(enableCode) != 6 {
		t.Fatalf("code %q", enableCode)
	}

	// Wrong code, then the right one; a code is single use.
	if status, _ := h.request("POST", "/users/@me/mfa/sms/enable", alice.Token, map[string]any{
		"phone": "+15551234567", "code": "000000",
	}); status != 400 {
		t.Fatalf("wrong code status %d", status)
	}
	var enabled struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if status := h.decode("POST", "/users/@me/mfa/sms/enable", alice.Token, map[string]any{
		"phone": "+15551234567", "code": enableCode,
	}, &enabled); status != 200 || len(enabled.RecoveryCodes) != 10 {
		t.Fatalf("enable status %d response %+v", status, enabled)
	}
	if status, _ := h.request("POST", "/users/@me/mfa/sms/enable", alice.Token, map[string]any{
		"phone": "+15551234567", "code": enableCode,
	}); status != 400 {
		t.Fatalf("code reuse status %d", status)
	}

	// Login gates on SMS and issues a login code for the ticket.
	login := func() map[string]any {
		var out map[string]any
		status, raw := h.request("POST", "/auth/login", "", map[string]any{
			"login": "smsalice@example.test", "password": testPassword,
		})
		if status != 200 {
			t.Fatalf("login %d %s", status, raw)
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := login()
	if first["mfa"] != true || first["sms"] != true || first["totp"] != false || first["webauthn"] != false {
		t.Fatalf("login flags %+v", first)
	}
	ticket, _ := first["ticket"].(string)
	if ticket == "" {
		t.Fatalf("no ticket: %+v", first)
	}
	loginCode := storedSMSCode(t, h, alice.ID, "login")

	if status, _ := h.request("POST", "/auth/mfa/sms", "", map[string]any{
		"ticket": ticket, "code": "000000",
	}); status != 400 {
		t.Fatalf("wrong login code status %d", status)
	}
	var completed struct{ Token string }
	if status := h.decode("POST", "/auth/mfa/sms", "", map[string]any{
		"ticket": ticket, "code": loginCode,
	}, &completed); status != 200 || completed.Token == "" {
		t.Fatalf("sms login status %d", status)
	}
	var me struct {
		ID string `json:"id"`
	}
	h.decode("GET", "/users/@me", completed.Token, nil, &me)
	if me.ID != alice.ID {
		t.Fatalf("session user %s", me.ID)
	}
	if status, _ := h.request("POST", "/auth/mfa/sms", "", map[string]any{
		"ticket": ticket, "code": loginCode,
	}); status != 400 {
		t.Fatalf("ticket reuse status %d", status)
	}

	// Resend issues a replacement code for the same ticket.
	second := login()
	resendTicket, _ := second["ticket"].(string)
	oldCode := storedSMSCode(t, h, alice.ID, "login")
	if status, _ := h.request("POST", "/auth/mfa/sms/send", "", map[string]any{
		"ticket": resendTicket,
	}); status != 204 {
		t.Fatalf("resend status %d", status)
	}
	newCode := storedSMSCode(t, h, alice.ID, "login")
	if newCode == oldCode {
		// A collision is possible but vanishingly unlikely; regenerate once.
		h.request("POST", "/auth/mfa/sms/send", "", map[string]any{"ticket": resendTicket})
		newCode = storedSMSCode(t, h, alice.ID, "login")
	}
	if status, _ := h.request("POST", "/auth/mfa/sms", "", map[string]any{
		"ticket": resendTicket, "code": oldCode,
	}); newCode != oldCode && status != 400 {
		t.Fatalf("superseded code accepted: %d", status)
	}
	h.request("POST", "/auth/mfa/sms/send", "", map[string]any{"ticket": resendTicket})
	finalCode := storedSMSCode(t, h, alice.ID, "login")
	if status := h.decode("POST", "/auth/mfa/sms", "", map[string]any{
		"ticket": resendTicket, "code": finalCode,
	}, &completed); status != 200 {
		t.Fatalf("resend login status %d", status)
	}

	// Disable requires the password and clears the factor.
	if status, _ := h.request("POST", "/users/@me/mfa/sms/disable", alice.Token, map[string]any{
		"password": "wrong password",
	}); status != 400 {
		t.Fatalf("disable bad password status %d", status)
	}
	var disabled struct {
		MFAEnabled bool `json:"mfa_enabled"`
	}
	if status := h.decode("POST", "/users/@me/mfa/sms/disable", alice.Token, map[string]any{
		"password": testPassword,
	}, &disabled); status != 200 || disabled.MFAEnabled {
		t.Fatalf("disable status %d response %+v", status, disabled)
	}
	if status, _ := h.request("POST", "/users/@me/mfa/sms/disable", alice.Token, map[string]any{
		"password": testPassword,
	}); status != 400 {
		t.Fatalf("disable twice status %d", status)
	}
	after := login()
	if after["token"] == nil || after["mfa"] != nil {
		t.Fatalf("login still gated: %+v", after)
	}

	// Expired enrollment codes are rejected.
	h.request("POST", "/users/@me/mfa/sms/send", alice.Token, map[string]any{"phone": "+15550000000"})
	expiredCode := storedSMSCode(t, h, alice.ID, "enable")
	if _, err := h.store.DB.Exec(`UPDATE sms_mfa_codes SET expires = ? WHERE user_id = ? AND purpose = 'enable'`,
		time.Now().Add(-time.Second).UnixMilli(), alice.ID); err != nil {
		t.Fatalf("force expiry: %v", err)
	}
	if status, _ := h.request("POST", "/users/@me/mfa/sms/enable", alice.Token, map[string]any{
		"phone": "+15550000000", "code": expiredCode,
	}); status != 400 {
		t.Fatalf("expired code status %d", status)
	}
}
