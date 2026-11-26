package api

import (
	"strings"

	"github.com/wavedevgit/waveycord/internal/store"
)

// mfaTicketTTL bounds a pending login ticket.
const mfaTicketTTL = 5 * 60 * 1000

// handleEnableTOTP enrolls a TOTP secret after proving the password and one valid
// code, then returns freshly generated recovery codes and a session token.
func (s *Server) handleEnableTOTP(c *Ctx) {
	var req struct {
		Secret   string `json:"secret"`
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	hash, err := s.Store.PasswordHash(c.User.ID)
	if err != nil || !VerifyPassword(req.Password, hash) {
		c.FailForm("password", "Password does not match")
		return
	}
	secret := normalizeTOTPSecret(req.Secret)
	if secret == "" {
		c.FailForm("secret", "This field is required")
		return
	}
	if _, err := b32NoPad.DecodeString(secret); err != nil {
		c.FailForm("secret", "Secret is not valid base32")
		return
	}
	if !verifyTOTP(secret, req.Code) {
		c.FailForm("code", "Invalid two factor auth code")
		return
	}
	if err := s.Store.SetMFASecret(c.User.ID, secret); err != nil {
		fail(c, err)
		return
	}
	codes, err := newBackupCodes(10)
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.ReplaceBackupCodes(c.User.ID, codes); err != nil {
		fail(c, err)
		return
	}
	token, err := NewToken()
	if err != nil {
		fail(c, err)
		return
	}
	if _, err := s.Store.CreateSession(c.User.ID, TokenHash(token), c.R.UserAgent()); err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"token": token, "recovery_codes": codes})
}

// handleDisableTOTP removes the second factor. A current TOTP code or an unused
// backup code is accepted.
func (s *Server) handleDisableTOTP(c *Ctx) {
	var req struct {
		Code string `json:"code"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	secret, enabled, err := s.Store.MFASecret(c.User.ID)
	if err != nil {
		fail(c, err)
		return
	}
	if !enabled {
		fail(c, NewError(400, 50035, "Two factor is not enabled"))
		return
	}
	code := strings.TrimSpace(req.Code)
	if !verifyTOTP(secret, code) {
		ok, err := s.Store.SpendBackupCode(c.User.ID, code)
		if err != nil {
			fail(c, err)
			return
		}
		if !ok {
			c.FailForm("code", "Invalid two factor auth code")
			return
		}
	}
	if err := s.Store.DisableMFA(c.User.ID); err != nil {
		fail(c, err)
		return
	}
	user, err := s.Store.UserByID(c.User.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(user)
}

// handleGetBackupCodes returns the stored recovery codes after a password check.
func (s *Server) handleGetBackupCodes(c *Ctx) {
	var req struct {
		Password string `json:"password"`
	}
	_ = c.Bind(&req)
	hash, err := s.Store.PasswordHash(c.User.ID)
	if err != nil || !VerifyPassword(req.Password, hash) {
		c.FailForm("password", "Password does not match")
		return
	}
	codes, err := s.Store.BackupCodes(c.User.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"recovery_codes": codes})
}

// handleGenerateBackupCodes replaces the recovery codes.
func (s *Server) handleGenerateBackupCodes(c *Ctx) {
	var req struct {
		Password string `json:"password"`
	}
	_ = c.Bind(&req)
	hash, err := s.Store.PasswordHash(c.User.ID)
	if err != nil || !VerifyPassword(req.Password, hash) {
		c.FailForm("password", "Password does not match")
		return
	}
	codes, err := newBackupCodes(10)
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.ReplaceBackupCodes(c.User.ID, codes); err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"recovery_codes": codes})
}

// handleTOTPLogin completes a login ticket with a TOTP or backup code.
func (s *Server) handleTOTPLogin(c *Ctx) {
	var req struct {
		Code     string `json:"code"`
		Ticket   string `json:"ticket"`
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.Ticket == "" {
		c.FailForm("ticket", "This field is required")
		return
	}
	userID, err := s.Store.ValidateMFATicket(req.Ticket)
	switch err {
	case nil:
	case store.ErrMFATicketExpired:
		c.JSONStatus(400, map[string]any{"code": 60008, "message": "Your login session has expired"})
		return
	default:
		c.JSONStatus(400, map[string]any{"code": 60008, "message": "Invalid ticket"})
		return
	}
	secret, enabled, err := s.Store.MFASecret(userID)
	if err != nil || !enabled {
		c.JSONStatus(400, map[string]any{"code": 60008, "message": "Invalid ticket"})
		return
	}
	code := strings.TrimSpace(req.Code)
	ok := verifyTOTP(secret, code)
	if !ok {
		spent, err := s.Store.SpendBackupCode(userID, code)
		if err != nil {
			fail(c, err)
			return
		}
		ok = spent
	}
	if !ok {
		c.FailForm("code", "Invalid two factor auth code")
		return
	}
	_ = s.Store.DeleteMFATicket(req.Ticket)
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
