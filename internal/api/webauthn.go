package api

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/wavedevgit/waveycord/internal/store"
)

// WebAuthn (ES256) registration and assertion verification. Only the "none" and
// "packed" formats are accepted, and the attestation statement itself is not
// cryptographically verified: the authenticator data and the credential public
// key are, which is what the assertion path depends on.

var (
	errWebAuthn = errors.New("webauthn: verification failed")
)

// webauthnCredentialJSON is the browser's PublicKeyCredential JSON form.
type webauthnCredentialJSON struct {
	ID       string `json:"id"`
	RawID    string `json:"rawId"`
	Type     string `json:"type"`
	Response struct {
		ClientDataJSON    string `json:"clientDataJSON"`
		AttestationObject string `json:"attestationObject"`
		AuthenticatorData string `json:"authenticatorData"`
		Signature         string `json:"signature"`
		UserHandle        string `json:"userHandle"`
	} `json:"response"`
}

// clientData is the subset of the collected client data that is checked.
type clientData struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Origin    string `json:"origin"`
}

// b64WebAuthn accepts both standard and URL-safe base64, padded or not.
func b64WebAuthn(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errWebAuthn
	}
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding,
		base64.RawStdEncoding, base64.StdEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errWebAuthn
}

func decodeClientData(raw string) (clientData, error) {
	var cd clientData
	b, err := b64WebAuthn(raw)
	if err != nil {
		return cd, err
	}
	if err := json.Unmarshal(b, &cd); err != nil {
		return cd, errWebAuthn
	}
	return cd, nil
}

// checkClientData verifies the ceremony type, challenge and origin.
func checkClientData(raw, wantType, wantChallenge string, cfg Config) error {
	cd, err := decodeClientData(raw)
	if err != nil {
		return err
	}
	if cd.Type != wantType || cd.Challenge != wantChallenge {
		return errWebAuthn
	}
	if cfg.WebAuthnOrigin != "" && cd.Origin != cfg.WebAuthnOrigin {
		return errWebAuthn
	}
	return nil
}

// parseAuthData splits authenticator data and, for registration, extracts the
// attested credential id and COSE public key.
func parseAuthData(authData []byte, rpID string) (flags byte, count uint32, credID []byte, cose []byte, err error) {
	if len(authData) < 37 {
		return 0, 0, nil, nil, errWebAuthn
	}
	sum := sha256.Sum256([]byte(rpID))
	if !bytes.Equal(authData[:32], sum[:]) {
		return 0, 0, nil, nil, errWebAuthn
	}
	flags = authData[32]
	count = binary.BigEndian.Uint32(authData[33:37])
	off := 37
	if flags&0x40 != 0 {
		if len(authData) < off+18 {
			return 0, 0, nil, nil, errWebAuthn
		}
		off += 16 // AAGUID
		n := int(binary.BigEndian.Uint16(authData[off : off+2]))
		off += 2
		if n == 0 || len(authData) < off+n {
			return 0, 0, nil, nil, errWebAuthn
		}
		credID = append([]byte(nil), authData[off:off+n]...)
		off += n
		cose = append([]byte(nil), authData[off:]...)
	}
	return flags, count, credID, cose, nil
}

// coseToECDSAP256 decodes an ES256 COSE key (kty 2, alg -7, crv 1).
func coseToECDSAP256(raw []byte) (*ecdsa.PublicKey, error) {
	// COSE map keys are integers, which the text-key decoder rejects.
	keys := map[int64]any{}
	if err := cborIntMap(raw, keys); err != nil {
		return nil, err
	}
	if kty, _ := keys[1].(uint64); kty != 2 {
		return nil, errWebAuthn
	}
	if alg, _ := keys[3].(int64); alg != -7 {
		return nil, errWebAuthn
	}
	if crv, _ := keys[-1].(uint64); crv != 1 {
		return nil, errWebAuthn
	}
	x, _ := keys[-2].([]byte)
	y, _ := keys[-3].([]byte)
	if len(x) != 32 || len(y) != 32 {
		return nil, errWebAuthn
	}
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
	if !pub.Curve.IsOnCurve(pub.X, pub.Y) {
		return nil, errWebAuthn
	}
	return pub, nil
}

// cborIntMap decodes a COSE key map with integer keys.
func cborIntMap(raw []byte, out map[int64]any) error {
	d := &cborDecoder{data: raw}
	head, err := d.read(1)
	if err != nil {
		return err
	}
	if head[0]>>5 != 5 {
		return errWebAuthn
	}
	n, err := d.readArg(head[0] & 0x1f)
	if err != nil {
		return err
	}
	for i := uint64(0); i < n; i++ {
		k, err := d.value()
		if err != nil {
			return err
		}
		v, err := d.value()
		if err != nil {
			return err
		}
		var ki int64
		switch kv := k.(type) {
		case uint64:
			ki = int64(kv)
		case int64:
			ki = kv
		default:
			return errWebAuthn
		}
		out[ki] = v
	}
	return nil
}

// verifyAttestation checks the registration ceremony and returns the credential
// id, public key and signature counter.
func verifyAttestation(c webauthnCredentialJSON, challenge string, cfg Config) ([]byte, *ecdsa.PublicKey, uint32, error) {
	if c.Type != "public-key" {
		return nil, nil, 0, errWebAuthn
	}
	if err := checkClientData(c.Response.ClientDataJSON, "webauthn.create", challenge, cfg); err != nil {
		return nil, nil, 0, err
	}
	rawAtt, err := b64WebAuthn(c.Response.AttestationObject)
	if err != nil {
		return nil, nil, 0, err
	}
	v, err := cborDecodeWithIntKeys(rawAtt)
	if err != nil {
		return nil, nil, 0, err
	}
	authData, _ := v["authData"].([]byte)
	if len(authData) == 0 {
		return nil, nil, 0, errWebAuthn
	}
	flags, count, credID, cose, err := parseAuthData(authData, cfg.WebAuthnRPID)
	if err != nil {
		return nil, nil, 0, err
	}
	if flags&0x40 == 0 || len(credID) == 0 || len(cose) == 0 {
		return nil, nil, 0, errWebAuthn
	}
	pub, err := coseToECDSAP256(cose)
	if err != nil {
		return nil, nil, 0, err
	}
	// The client-reported id must match the attested credential id.
	rawID, err := b64WebAuthn(c.RawID)
	if err != nil {
		rawID, err = b64WebAuthn(c.ID)
		if err != nil {
			return nil, nil, 0, err
		}
	}
	if !bytes.Equal(rawID, credID) {
		return nil, nil, 0, errWebAuthn
	}
	return credID, pub, count, nil
}

// cborDecodeWithIntKeys is a permissive decoder for the attestation object: the
// top-level map uses text keys, so it delegates to cborDecode.
func cborDecodeWithIntKeys(raw []byte) (map[string]any, error) {
	v, err := cborDecode(raw)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errWebAuthn
	}
	return m, nil
}

// verifyAssertion checks a login ceremony.
func verifyAssertion(c webauthnCredentialJSON, challenge string, cfg Config, stored []byte) (uint32, error) {
	if c.Type != "public-key" {
		return 0, errWebAuthn
	}
	if err := checkClientData(c.Response.ClientDataJSON, "webauthn.get", challenge, cfg); err != nil {
		return 0, err
	}
	authData, err := b64WebAuthn(c.Response.AuthenticatorData)
	if err != nil {
		return 0, err
	}
	flags, count, _, _, err := parseAuthData(authData, cfg.WebAuthnRPID)
	if err != nil {
		return 0, err
	}
	if flags&0x01 == 0 { // user presence
		return 0, errWebAuthn
	}
	cdRaw, err := b64WebAuthn(c.Response.ClientDataJSON)
	if err != nil {
		return 0, err
	}
	sig, err := b64WebAuthn(c.Response.Signature)
	if err != nil {
		return 0, err
	}
	cdSum := sha256.Sum256(cdRaw)
	signed := append(append([]byte(nil), authData...), cdSum[:]...)
	x, y := elliptic.Unmarshal(elliptic.P256(), stored)
	if x == nil {
		return 0, errWebAuthn
	}
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}
	if !ecdsa.VerifyASN1(pub, signed, sig) {
		return 0, errWebAuthn
	}
	return count, nil
}

// newChallenge returns a fresh base64url challenge and ticket.
func newChallenge() (ticket, challenge string, err error) {
	tb := make([]byte, 32)
	if _, err := rand.Read(tb); err != nil {
		return "", "", err
	}
	cb := make([]byte, 32)
	if _, err := rand.Read(cb); err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(tb), base64.RawURLEncoding.EncodeToString(cb), nil
}

// webauthnChallengeTTL bounds a registration or login ceremony.
const webauthnChallengeTTL = 5 * 60 * 1000

// credentialView is the documented credential object.
type credentialView struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	CreatedAt string  `json:"created_at"`
	LastUsed  *string `json:"last_used"`
}

func credentialToView(c store.WebAuthnCredential) credentialView {
	v := credentialView{ID: c.ID, Name: c.Name, CreatedAt: isoTime(c.CreatedAt)}
	if c.LastUsed != nil {
		s := isoTime(*c.LastUsed)
		v.LastUsed = &s
	}
	return v
}

// handleWebAuthnCredentialPost dispatches the two documented uses of the same
// route: an empty body starts registration, a credential body finishes it. The
// body is read once because binding consumes it.
func (s *Server) handleWebAuthnCredentialPost(c *Ctx) {
	body, _ := io.ReadAll(http.MaxBytesReader(c.W, c.R.Body, c.Srv.cfg.MaxBodyBytes))
	var req struct {
		Name       string                 `json:"name"`
		Ticket     string                 `json:"ticket"`
		Credential webauthnCredentialJSON `json:"credential"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			fail(c, ErrInvalidForm)
			return
		}
	}
	if req.Ticket == "" && req.Credential.ID == "" {
		s.handleStartWebAuthnRegistration(c)
		return
	}
	s.finishWebAuthnRegistration(c, req)
}

// startWebAuthnRegistration creates a challenge bound to the caller.
func (s *Server) handleStartWebAuthnRegistration(c *Ctx) {
	ticket, challenge, err := newChallenge()
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.CreateWebAuthnChallenge(ticket, c.User.ID, challenge, "register",
		nowMillisFn()+webauthnChallengeTTL); err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"ticket": ticket, "challenge": challenge})
}

// finishWebAuthnRegistration verifies and stores a new credential. The caller
// has already parsed the body so the dispatcher can distinguish the two uses.
func (s *Server) finishWebAuthnRegistration(c *Ctx, req struct {
	Name       string                 `json:"name"`
	Ticket     string                 `json:"ticket"`
	Credential webauthnCredentialJSON `json:"credential"`
}) {
	userID, challenge, kind, err := s.Store.WebAuthnChallenge(req.Ticket)
	if err != nil || kind != "register" || userID != c.User.ID {
		c.FailForm("ticket", "Invalid or expired ticket")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 32 {
		c.FailForm("name", "Name must be 1-32 characters")
		return
	}
	credID, pub, count, err := verifyAttestation(req.Credential, challenge, s.cfg)
	if err != nil {
		c.FailForm("credential", "Invalid authenticator response")
		return
	}
	cred := store.WebAuthnCredential{
		ID: s.Store.NewID(), UserID: c.User.ID,
		CredentialID: base64.RawURLEncoding.EncodeToString(credID),
		PublicKey:    elliptic.Marshal(elliptic.P256(), pub.X, pub.Y),
		SignCount:    count, Name: name, CreatedAt: nowMillisFn(),
	}
	if err := s.Store.AddWebAuthnCredential(cred); err != nil {
		fail(c, err)
		return
	}
	_ = s.Store.DeleteWebAuthnChallenge(req.Ticket)
	_ = s.Store.SetMFASecret(c.User.ID, "")
	codes, err := newBackupCodes(10)
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.ReplaceBackupCodes(c.User.ID, codes); err != nil {
		fail(c, err)
		return
	}
	view := credentialView{ID: cred.ID, Name: cred.Name, CreatedAt: isoTime(cred.CreatedAt)}
	c.JSON(map[string]any{
		"id": view.ID, "name": view.Name, "created_at": view.CreatedAt,
		"last_used": nil, "backup_codes": codes,
	})
}

// handleListWebAuthnCredentials returns the caller's credentials.
func (s *Server) handleListWebAuthnCredentials(c *Ctx) {
	rows, err := s.Store.ListWebAuthnCredentials(c.User.ID)
	if err != nil {
		fail(c, err)
		return
	}
	out := make([]credentialView, 0, len(rows))
	for _, r := range rows {
		out = append(out, credentialToView(r))
	}
	c.JSON(out)
}

// handleRenameWebAuthnCredential renames one credential.
func (s *Server) handleRenameWebAuthnCredential(c *Ctx) {
	var req struct {
		Name string `json:"name"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 32 {
		c.FailForm("name", "Name must be 1-32 characters")
		return
	}
	cred, err := s.Store.RenameWebAuthnCredential(c.User.ID, c.Param("credentialID"), name)
	if err != nil {
		fail(c, ErrUnknownCredential)
		return
	}
	c.JSON(credentialToView(cred))
}

// handleDeleteWebAuthnCredential removes one credential.
func (s *Server) handleDeleteWebAuthnCredential(c *Ctx) {
	if err := s.Store.DeleteWebAuthnCredential(c.User.ID, c.Param("credentialID")); err != nil {
		fail(c, ErrUnknownCredential)
		return
	}
	// Removing the last factor leaves the account without MFA.
	if !s.Store.HasWebAuthn(c.User.ID) {
		if secret, enabled, err := s.Store.MFASecret(c.User.ID); err == nil && enabled && secret == "" {
			_ = s.Store.DisableMFA(c.User.ID)
		}
	}
	c.JSONStatus(204, nil)
}

// handleStartWebAuthnLogin challenges an unauthenticated login ticket.
func (s *Server) handleStartWebAuthnLogin(c *Ctx) {
	var req struct {
		Ticket string `json:"ticket"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	userID, err := s.Store.ValidateMFATicket(req.Ticket)
	if err != nil {
		c.JSONStatus(400, map[string]any{"code": 60008, "message": "Invalid ticket"})
		return
	}
	ticket, challenge, err := newChallenge()
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.CreateWebAuthnChallenge(ticket, userID, challenge, "login",
		nowMillisFn()+webauthnChallengeTTL); err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"ticket": ticket, "challenge": challenge})
}

// handleWebAuthnLogin completes an MFA login with an assertion.
func (s *Server) handleWebAuthnLogin(c *Ctx) {
	var req struct {
		Ticket     string                 `json:"ticket"`
		Credential webauthnCredentialJSON `json:"credential"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	userID, challenge, kind, err := s.Store.WebAuthnChallenge(req.Ticket)
	if err != nil || kind != "login" {
		c.FailForm("ticket", "Invalid or expired ticket")
		return
	}
	rawID, err := b64WebAuthn(req.Credential.RawID)
	if err != nil {
		rawID, err = b64WebAuthn(req.Credential.ID)
		if err != nil {
			c.FailForm("credential", "Invalid credential id")
			return
		}
	}
	cred, err := s.Store.WebAuthnCredentialByCredentialID(base64.RawURLEncoding.EncodeToString(rawID))
	if err != nil || cred.UserID != userID {
		c.FailForm("credential", "Unknown credential")
		return
	}
	count, err := verifyAssertion(req.Credential, challenge, s.cfg, cred.PublicKey)
	if err != nil {
		c.FailForm("credential", "Invalid authenticator response")
		return
	}
	_ = s.Store.DeleteWebAuthnChallenge(req.Ticket)
	s.Store.TouchWebAuthnCredential(cred.ID, count)

	token, err := NewToken()
	if err != nil {
		fail(c, err)
		return
	}
	if _, err := s.Store.CreateSession(userID, TokenHash(token), c.R.UserAgent()); err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"token": token})
}

// timeNow is a seam for tests that need deterministic timestamps.
var timeNow = time.Now
