package api

import (
	"encoding/base64"
)

// Discoverable-credential (conditional UI) and passwordless sign-in. Both reuse
// the WebAuthn assertion verifier; the difference is where the challenge lives and
// whether the account is known before the assertion.

// handleConditionalStart issues a challenge that is not tied to an account, so the
// browser can offer any discoverable credential.
func (s *Server) handleConditionalStart(c *Ctx) {
	ticket, challenge, err := newChallenge()
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.CreateLoginChallenge(ticket, challenge, "conditional", nowMillisFn()+webauthnChallengeTTL); err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{
		"ticket": ticket, "challenge": challenge,
		"timeout": webauthnChallengeTTL,
	})
}

// handleConditionalFinish resolves the credential in the assertion to its owner,
// verifies the signature and returns a session token.
func (s *Server) handleConditionalFinish(c *Ctx) {
	var req struct {
		Ticket     string                 `json:"ticket"`
		Credential webauthnCredentialJSON `json:"credential"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	challenge, kind, err := s.Store.LoginChallenge(req.Ticket)
	if err != nil || kind != "conditional" {
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
	if err != nil {
		c.FailForm("credential", "Unknown credential")
		return
	}
	count, err := verifyAssertion(req.Credential, challenge, s.cfg, cred.PublicKey)
	if err != nil {
		c.FailForm("credential", "Invalid authenticator response")
		return
	}
	_ = s.Store.DeleteLoginChallenge(req.Ticket)
	s.Store.TouchWebAuthnCredential(cred.ID, count)
	token, err := NewToken()
	if err != nil {
		fail(c, err)
		return
	}
	if _, err := s.Store.CreateSession(cred.UserID, TokenHash(token), c.R.UserAgent()); err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"token": token})
}

// handlePasswordlessStart issues a challenge for the signed-in account. The caller
// proves possession of one of their own registered credentials to mint a fresh
// session, without presenting the password again.
func (s *Server) handlePasswordlessStart(c *Ctx) {
	if !s.Store.HasWebAuthn(c.User.ID) {
		fail(c, ErrMissingPerms)
		return
	}
	ticket, challenge, err := newChallenge()
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.CreateWebAuthnChallenge(ticket, c.User.ID, challenge, "passwordless",
		nowMillisFn()+webauthnChallengeTTL); err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{
		"ticket": ticket, "challenge": challenge,
		"timeout": webauthnChallengeTTL,
	})
}

// handlePasswordlessFinish verifies the assertion against one of the caller's own
// credentials and returns a fresh session token.
func (s *Server) handlePasswordlessFinish(c *Ctx) {
	var req struct {
		Ticket     string                 `json:"ticket"`
		Credential webauthnCredentialJSON `json:"credential"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	userID, challenge, kind, err := s.Store.WebAuthnChallenge(req.Ticket)
	if err != nil || kind != "passwordless" || userID != c.User.ID {
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
