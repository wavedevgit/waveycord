package api_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
)

// --- minimal CBOR encoder for the WebAuthn test authenticator ---

func cborHead(major byte, n uint64) []byte {
	switch {
	case n < 24:
		return []byte{major<<5 | byte(n)}
	case n <= 0xff:
		return []byte{major<<5 | 24, byte(n)}
	case n <= 0xffff:
		return []byte{major<<5 | 25, byte(n >> 8), byte(n)}
	default:
		out := []byte{major<<5 | 27, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(out[1:], uint32(n))
		return out
	}
}

func cborBytes(b []byte) []byte {
	return append(cborHead(2, uint64(len(b))), b...)
}

func cborText(s string) []byte {
	return append(cborHead(3, uint64(len(s))), []byte(s)...)
}

func cborUint(n uint64) []byte { return cborHead(0, n) }
func cborNeg(n uint64) []byte  { return cborHead(1, n) }

func cborMap(pairs [][2][]byte) []byte {
	out := cborHead(5, uint64(len(pairs)))
	for _, p := range pairs {
		out = append(out, p[0]...)
		out = append(out, p[1]...)
	}
	return out
}

// coseKeyES256 encodes an ES256 COSE key.
func coseKeyES256(pub *ecdsa.PublicKey) []byte {
	x := pub.X.FillBytes(make([]byte, 32))
	y := pub.Y.FillBytes(make([]byte, 32))
	return cborMap([][2][]byte{
		{cborUint(1), cborUint(2)}, // kty: EC2
		{cborUint(3), cborNeg(6)},  // alg: -7 (ES256)
		{cborNeg(0), cborUint(1)},  // crv: P-256
		{cborNeg(1), cborBytes(x)}, // x
		{cborNeg(2), cborBytes(y)}, // y
	})
}

// testAuthenticator is a software security key for the ES256 algorithm.
type testAuthenticator struct {
	key    *ecdsa.PrivateKey
	credID []byte
	rpID   string
	origin string
}

func newTestAuthenticator(t *testing.T) *testAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credID := make([]byte, 32)
	if _, err := rand.Read(credID); err != nil {
		t.Fatal(err)
	}
	return &testAuthenticator{key: key, credID: credID, rpID: "localhost", origin: "http://localhost"}
}

func (a *testAuthenticator) clientData(t *testing.T, typ, challenge string) ([]byte, string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type": typ, "challenge": challenge, "origin": a.origin, "crossOrigin": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw, base64.RawURLEncoding.EncodeToString(raw)
}

func (a *testAuthenticator) authData(counter uint32, attested bool) []byte {
	rp := sha256.Sum256([]byte(a.rpID))
	flags := byte(0x01) // user present
	out := append([]byte(nil), rp[:]...)
	if attested {
		flags |= 0x40 // attested credential data included
	}
	out = append(out, flags)
	var c [4]byte
	binary.BigEndian.PutUint32(c[:], counter)
	out = append(out, c[:]...)
	if attested {
		out = append(out, make([]byte, 16)...) // AAGUID
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(a.credID)))
		out = append(out, l[:]...)
		out = append(out, a.credID...)
		out = append(out, coseKeyES256(&a.key.PublicKey)...)
	}
	return out
}

// registration builds the credential JSON for a create ceremony.
func (a *testAuthenticator) registration(t *testing.T, challenge string) map[string]any {
	t.Helper()
	_, cd := a.clientData(t, "webauthn.create", challenge)
	att := cborMap([][2][]byte{
		{cborText("fmt"), cborText("none")},
		{cborText("attStmt"), cborMap(nil)},
		{cborText("authData"), cborBytes(a.authData(0, true))},
	})
	return map[string]any{
		"id":    base64.RawURLEncoding.EncodeToString(a.credID),
		"rawId": base64.RawURLEncoding.EncodeToString(a.credID),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    cd,
			"attestationObject": base64.RawURLEncoding.EncodeToString(att),
		},
	}
}

// assertion builds the credential JSON for a get ceremony.
func (a *testAuthenticator) assertion(t *testing.T, challenge string, counter uint32) map[string]any {
	t.Helper()
	cdRaw, cd := a.clientData(t, "webauthn.get", challenge)
	authData := a.authData(counter, false)
	cdSum := sha256.Sum256(cdRaw)
	signed := append(append([]byte(nil), authData...), cdSum[:]...)
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, signed)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"id":    base64.RawURLEncoding.EncodeToString(a.credID),
		"rawId": base64.RawURLEncoding.EncodeToString(a.credID),
		"type":  "public-key",
		"response": map[string]any{
			"clientDataJSON":    cd,
			"authenticatorData": base64.RawURLEncoding.EncodeToString(authData),
			"signature":         base64.RawURLEncoding.EncodeToString(sig),
			"userHandle":        "",
		},
	}
}

func TestWebAuthnRegistrationAndLogin(t *testing.T) {
	h := newHarness(t)
	alice := h.register("webauthnalice")
	auth := newTestAuthenticator(t)

	var list []map[string]any
	if status := h.decode("GET", "/users/@me/mfa/webauthn/credentials", alice.Token, nil, &list); status != 200 || len(list) != 0 {
		t.Fatalf("initial list %d %+v", status, list)
	}

	var start struct {
		Ticket    string `json:"ticket"`
		Challenge string `json:"challenge"`
	}
	if status := h.decode("POST", "/users/@me/mfa/webauthn/credentials", alice.Token, map[string]any{}, &start); status != 200 ||
		start.Ticket == "" || start.Challenge == "" {
		t.Fatalf("start registration %d %+v", status, start)
	}
	if status, _ := h.request("POST", "/users/@me/mfa/webauthn/credentials", alice.Token, map[string]any{}); status != 200 {
		t.Fatalf("second start %d", status)
	}

	// A challenge mismatch is rejected and does not consume the ticket.
	if status, raw := h.request("POST", "/users/@me/mfa/webauthn/credentials", alice.Token, map[string]any{
		"name": "Backup key", "ticket": start.Ticket, "credential": auth.registration(t, "wrong-challenge"),
	}); status != 400 || !strings.Contains(string(raw), "Invalid authenticator response") {
		t.Fatalf("challenge mismatch status %d body %s", status, raw)
	}
	// A malformed attestation is rejected too.
	if status, _ := h.request("POST", "/users/@me/mfa/webauthn/credentials", alice.Token, map[string]any{
		"name": "Backup key", "ticket": start.Ticket,
		"credential": map[string]any{"id": "AA", "type": "public-key", "response": map[string]any{"clientDataJSON": "AA", "attestationObject": "AA"}},
	}); status != 400 {
		t.Fatalf("malformed attestation status %d", status)
	}
	if status, _ := h.request("POST", "/users/@me/mfa/webauthn/credentials", alice.Token, map[string]any{
		"name": "", "ticket": start.Ticket, "credential": auth.registration(t, start.Challenge),
	}); status != 400 {
		t.Fatalf("blank name status %d", status)
	}

	var registered struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		BackupCodes []string `json:"backup_codes"`
	}
	if status := h.decode("POST", "/users/@me/mfa/webauthn/credentials", alice.Token, map[string]any{
		"name": "YubiKey", "ticket": start.Ticket, "credential": auth.registration(t, start.Challenge),
	}, &registered); status != 200 {
		t.Fatalf("finish registration status %d", status)
	}
	if registered.ID == "" || registered.Name != "YubiKey" || len(registered.BackupCodes) != 10 {
		t.Fatalf("registration response %+v", registered)
	}

	if status := h.decode("GET", "/users/@me/mfa/webauthn/credentials", alice.Token, nil, &list); status != 200 ||
		len(list) != 1 || list[0]["name"] != "YubiKey" {
		t.Fatalf("list after register %d %+v", status, list)
	}

	// Rename.
	var renamed struct {
		Name string `json:"name"`
	}
	if status := h.decode("PATCH", "/users/@me/mfa/webauthn/credentials/"+registered.ID, alice.Token,
		map[string]any{"name": "Desk key"}, &renamed); status != 200 || renamed.Name != "Desk key" {
		t.Fatalf("rename %d %+v", status, renamed)
	}
	if status, _ := h.request("PATCH", "/users/@me/mfa/webauthn/credentials/nope", alice.Token,
		map[string]any{"name": "x"}); status != 404 {
		t.Fatalf("rename unknown status %d", status)
	}

	// Login is now gated on WebAuthn, not TOTP.
	var login map[string]any
	status, raw := h.request("POST", "/auth/login", "", map[string]any{
		"login": "webauthnalice@example.test", "password": testPassword,
	})
	if status != 200 {
		t.Fatalf("login %d %s", status, raw)
	}
	if err := json.Unmarshal(raw, &login); err != nil {
		t.Fatal(err)
	}
	if login["mfa"] != true || login["webauthn"] != true || login["totp"] != false {
		t.Fatalf("login factor flags %+v", login)
	}
	mfaTicket, _ := login["ticket"].(string)

	if status, _ := h.request("POST", "/auth/mfa/totp", "", map[string]any{
		"ticket": mfaTicket, "code": "123456",
	}); status != 400 {
		t.Fatalf("totp accepted for webauthn-only account: %d", status)
	}

	var challenge struct {
		Ticket    string `json:"ticket"`
		Challenge string `json:"challenge"`
	}
	if status := h.decode("POST", "/auth/mfa/webauthn/start", "", map[string]any{"ticket": mfaTicket}, &challenge); status != 200 {
		t.Fatalf("webauthn start %d", status)
	}

	// Wrong signature and wrong credential are rejected.
	bad := auth.assertion(t, challenge.Challenge, 1)
	bad["response"].(map[string]any)["signature"] = base64.RawURLEncoding.EncodeToString([]byte{1, 2, 3})
	if status, raw := h.request("POST", "/auth/mfa/webauthn", "", map[string]any{
		"ticket": challenge.Ticket, "credential": bad,
	}); status != 400 || !strings.Contains(string(raw), "Invalid authenticator response") {
		t.Fatalf("bad signature status %d body %s", status, raw)
	}
	other := newTestAuthenticator(t)
	if status, raw := h.request("POST", "/auth/mfa/webauthn", "", map[string]any{
		"ticket": challenge.Ticket, "credential": other.assertion(t, challenge.Challenge, 1),
	}); status != 400 || !strings.Contains(string(raw), "Unknown credential") {
		t.Fatalf("unknown credential status %d body %s", status, raw)
	}

	var completed struct{ Token string }
	if status := h.decode("POST", "/auth/mfa/webauthn", "", map[string]any{
		"ticket": challenge.Ticket, "credential": auth.assertion(t, challenge.Challenge, 2),
	}, &completed); status != 200 || completed.Token == "" {
		t.Fatalf("webauthn login %d", status)
	}
	var me struct {
		ID string `json:"id"`
	}
	h.decode("GET", "/users/@me", completed.Token, nil, &me)
	if me.ID != alice.ID {
		t.Fatalf("session user %s != %s", me.ID, alice.ID)
	}
	// The challenge is single-use.
	if status, _ := h.request("POST", "/auth/mfa/webauthn", "", map[string]any{
		"ticket": challenge.Ticket, "credential": auth.assertion(t, challenge.Challenge, 3),
	}); status != 400 {
		t.Fatalf("reused challenge status %d", status)
	}

	// Deleting the last credential drops MFA, so login returns a token directly.
	if status, _ := h.request("DELETE", "/users/@me/mfa/webauthn/credentials/"+registered.ID, alice.Token, nil); status != 204 {
		t.Fatalf("delete status %d", status)
	}
	status, raw = h.request("POST", "/auth/login", "", map[string]any{
		"login": "webauthnalice@example.test", "password": testPassword,
	})
	if status != 200 {
		t.Fatalf("login after delete %d", status)
	}
	login = nil
	if err := json.Unmarshal(raw, &login); err != nil {
		t.Fatal(err)
	}
	if login["token"] == nil || login["mfa"] != nil {
		t.Fatalf("login still gated: %+v", login)
	}
}
