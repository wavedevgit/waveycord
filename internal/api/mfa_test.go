package api_test

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

const testPassword = "correct horse battery"

// testTOTP derives a code independently of the server implementation.
func testTOTP(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	norm := ""
	for _, r := range secret {
		if r == ' ' || r == '=' {
			continue
		}
		norm += string(r)
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(norm)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	return fmt.Sprintf("%06d", v%1000000)
}

func testSecret(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
}

func TestTOTPEnrollmentLoginAndBackupCodes(t *testing.T) {
	h := newHarness(t)
	alice := h.register("mfaaalice")
	secret := testSecret(t)

	// Enrollment requires the password and a valid code.
	if status, _ := h.request("POST", "/users/@me/mfa/totp/enable", alice.Token, map[string]any{
		"secret": secret, "code": testTOTP(t, secret, time.Now()), "password": "wrong password",
	}); status != 400 {
		t.Fatalf("enable with bad password status %d", status)
	}
	if status, _ := h.request("POST", "/users/@me/mfa/totp/enable", alice.Token, map[string]any{
		"secret": secret, "code": "000000", "password": testPassword,
	}); status != 400 {
		t.Fatalf("enable with bad code status %d", status)
	}
	if status, _ := h.request("POST", "/users/@me/mfa/totp/enable", alice.Token, map[string]any{
		"secret": "not base32!", "code": "123456", "password": testPassword,
	}); status != 400 {
		t.Fatalf("enable with bad secret status %d", status)
	}

	var enabled struct {
		Token         string   `json:"token"`
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if status := h.decode("POST", "/users/@me/mfa/totp/enable", alice.Token, map[string]any{
		"secret": secret, "code": testTOTP(t, secret, time.Now()), "password": testPassword,
	}, &enabled); status != 200 {
		t.Fatalf("enable status %d", status)
	}
	if enabled.Token == "" || len(enabled.RecoveryCodes) != 10 {
		t.Fatalf("enable response: %+v", enabled)
	}

	var me struct {
		MFAEnabled bool `json:"mfa_enabled"`
	}
	h.decode("GET", "/users/@me", alice.Token, nil, &me)
	if !me.MFAEnabled {
		t.Fatal("mfa_enabled not set on user")
	}

	// Password login now yields a ticket instead of a token.
	login := func() map[string]any {
		var out map[string]any
		status, raw := h.request("POST", "/auth/login", "", map[string]any{
			"login": "mfaaalice@example.test", "password": testPassword,
		})
		if status != 200 {
			t.Fatalf("login status %d body %s", status, raw)
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("login decode: %v", err)
		}
		return out
	}
	first := login()
	if first["token"] != nil || first["mfa"] != true || first["totp"] != true {
		t.Fatalf("login did not gate on MFA: %+v", first)
	}
	ticket, _ := first["ticket"].(string)
	if ticket == "" {
		t.Fatalf("no ticket: %+v", first)
	}

	// Wrong code fails without consuming the ticket.
	if status, _ := h.request("POST", "/auth/mfa/totp", "", map[string]any{
		"ticket": ticket, "code": "000000",
	}); status != 400 {
		t.Fatalf("bad totp status %d", status)
	}
	var completed struct{ Token string }
	if status := h.decode("POST", "/auth/mfa/totp", "", map[string]any{
		"ticket": ticket, "code": testTOTP(t, secret, time.Now()),
	}, &completed); status != 200 || completed.Token == "" {
		t.Fatalf("totp login status %d", status)
	}
	h.decode("GET", "/users/@me", completed.Token, nil, &me)
	if !me.MFAEnabled {
		t.Fatal("session from totp login is not usable")
	}
	// The ticket is single-use.
	if status, _ := h.request("POST", "/auth/mfa/totp", "", map[string]any{
		"ticket": ticket, "code": testTOTP(t, secret, time.Now()),
	}); status != 400 {
		t.Fatalf("reused ticket status %d", status)
	}

	// A backup code completes a login exactly once.
	second := login()
	backup, _ := second["ticket"].(string)
	if status := h.decode("POST", "/auth/mfa/totp", "", map[string]any{
		"ticket": backup, "code": enabled.RecoveryCodes[0],
	}, &completed); status != 200 || completed.Token == "" {
		t.Fatalf("backup code login status %d", status)
	}
	third := login()
	reuseTicket, _ := third["ticket"].(string)
	if status, _ := h.request("POST", "/auth/mfa/totp", "", map[string]any{
		"ticket": reuseTicket, "code": enabled.RecoveryCodes[0],
	}); status != 400 {
		t.Fatalf("reused backup code status %d", status)
	}

	// Listing codes shows usage; wrong password is rejected.
	if status, _ := h.request("GET", "/users/@me/mfa/codes", alice.Token, map[string]any{"password": "nope"}); status != 400 {
		t.Fatalf("codes bad password status %d", status)
	}
	var listed struct {
		RecoveryCodes []struct {
			Code string `json:"code"`
			Used bool   `json:"used"`
		} `json:"recovery_codes"`
	}
	if status := h.decode("GET", "/users/@me/mfa/codes", alice.Token, map[string]any{"password": testPassword}, &listed); status != 200 {
		t.Fatalf("list codes status %d", status)
	}
	if len(listed.RecoveryCodes) != 10 {
		t.Fatalf("listed %d codes", len(listed.RecoveryCodes))
	}
	used := 0
	for _, c := range listed.RecoveryCodes {
		if c.Used {
			used++
		}
	}
	if used != 1 {
		t.Fatalf("used codes = %d, want 1", used)
	}

	// Regenerating invalidates the old set.
	var regen struct {
		RecoveryCodes []string `json:"recovery_codes"`
	}
	if status := h.decode("POST", "/users/@me/mfa/codes", alice.Token, map[string]any{"password": testPassword}, &regen); status != 200 ||
		len(regen.RecoveryCodes) != 10 {
		t.Fatalf("regenerate status %d", status)
	}
	if regen.RecoveryCodes[0] == enabled.RecoveryCodes[1] {
		t.Fatal("regenerated codes reuse the old set")
	}
	fourth := login()
	oldTicket, _ := fourth["ticket"].(string)
	if status, _ := h.request("POST", "/auth/mfa/totp", "", map[string]any{
		"ticket": oldTicket, "code": enabled.RecoveryCodes[1],
	}); status != 400 {
		t.Fatalf("old code still valid after regenerate: %d", status)
	}

	// Disable requires a valid factor, then login stops gating.
	if status, _ := h.request("POST", "/users/@me/mfa/totp/disable", alice.Token, map[string]any{"code": "000000"}); status != 400 {
		t.Fatalf("disable bad code status %d", status)
	}
	var disabled struct {
		MFAEnabled bool `json:"mfa_enabled"`
	}
	if status := h.decode("POST", "/users/@me/mfa/totp/disable", alice.Token, map[string]any{
		"code": testTOTP(t, secret, time.Now()),
	}, &disabled); status != 200 || disabled.MFAEnabled {
		t.Fatalf("disable status %d response %+v", status, disabled)
	}
	after := login()
	if after["token"] == nil || after["mfa"] != nil {
		t.Fatalf("login still gated after disable: %+v", after)
	}

	// A backup code also disables the factor.
	secret2 := testSecret(t)
	h.decode("POST", "/users/@me/mfa/totp/enable", alice.Token, map[string]any{
		"secret": secret2, "code": testTOTP(t, secret2, time.Now()), "password": testPassword,
	}, &enabled)
	if status := h.decode("POST", "/users/@me/mfa/totp/disable", alice.Token, map[string]any{
		"code": enabled.RecoveryCodes[0],
	}, &disabled); status != 200 || disabled.MFAEnabled {
		t.Fatalf("disable via backup code status %d", status)
	}
	if status, _ := h.request("POST", "/users/@me/mfa/totp/disable", alice.Token, map[string]any{"code": "000000"}); status != 400 {
		t.Fatalf("disable when not enabled status %d", status)
	}
}
