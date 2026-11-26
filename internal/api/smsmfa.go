package api

import (
	"crypto/rand"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/wavedevgit/waveycord/internal/store"
)

// SMS second factor. Enrollment proves control of a phone with a short-lived code;
// login issues a fresh code and redeems it with the login ticket. No SMS is sent:
// the code is stored and surfaced only through server logs in a real deployment, so
// the delivery step is recorded as a gap.

const smsCodeTTL = 5 * time.Minute

var phonePattern = regexp.MustCompile(`^\+?[0-9]{6,20}$`)

func newSMSCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return pad6(n.Int64()), nil
}

func pad6(n int64) string {
	out := make([]byte, 6)
	for i := 5; i >= 0; i-- {
		out[i] = byte('0' + n%10)
		n /= 10
	}
	return string(out)
}

// sendSMSCode issues and stores a code for one purpose.
func (s *Server) sendSMSCode(userID, purpose string) (string, error) {
	code, err := newSMSCode()
	if err != nil {
		return "", err
	}
	if err := s.Store.SetSMSCode(userID, purpose, code, smsCodeTTL); err != nil {
		return "", err
	}
	return code, nil
}

// handleSendSMSMFA issues an enrollment code for a phone number.
func (s *Server) handleSendSMSMFA(c *Ctx) {
	var req struct {
		Phone string `json:"phone"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	phone := strings.TrimSpace(req.Phone)
	if !phonePattern.MatchString(phone) {
		c.FailForm("phone", "Phone must be 6-20 digits, optional leading +")
		return
	}
	if _, err := s.sendSMSCode(c.User.ID, store.SMSCodeEnable); err != nil {
		fail(c, err)
		return
	}
	c.JSONStatus(204, nil)
}

// handleEnableSMSMFA verifies the enrollment code and turns the factor on.
func (s *Server) handleEnableSMSMFA(c *Ctx) {
	var req struct {
		Phone string `json:"phone"`
		Code  string `json:"code"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	phone := strings.TrimSpace(req.Phone)
	if !phonePattern.MatchString(phone) {
		c.FailForm("phone", "Phone must be 6-20 digits, optional leading +")
		return
	}
	ok, err := s.Store.ConsumeSMSCode(c.User.ID, store.SMSCodeEnable, strings.TrimSpace(req.Code))
	if err != nil {
		fail(c, err)
		return
	}
	if !ok {
		c.FailForm("code", "Invalid SMS code")
		return
	}
	if err := s.Store.SetSMSMFA(c.User.ID, phone); err != nil {
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
	c.JSON(map[string]any{"recovery_codes": codes})
}

// handleDisableSMSMFA removes the factor after a password check.
func (s *Server) handleDisableSMSMFA(c *Ctx) {
	var req struct {
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
	if !s.Store.HasSMS(c.User.ID) {
		fail(c, ErrSMSNotEnabled)
		return
	}
	if err := s.Store.DisableSMSMFA(c.User.ID); err != nil {
		fail(c, err)
		return
	}
	s.refreshMFAFlag(c.User.ID)
	user, err := s.Store.UserByID(c.User.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(user)
}

// refreshMFAFlag clears mfa_enabled when no factor remains.
func (s *Server) refreshMFAFlag(userID string) {
	secret, enabled, err := s.Store.MFASecret(userID)
	if err != nil || !enabled {
		return
	}
	if secret != "" || s.Store.HasWebAuthn(userID) || s.Store.HasSMS(userID) {
		return
	}
	_ = s.Store.DisableMFA(userID)
}

// handleSMSLoginSend reissues the login code for an existing login ticket.
func (s *Server) handleSMSLoginSend(c *Ctx) {
	var req struct {
		Ticket string `json:"ticket"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	userID, err := s.Store.ValidateMFATicket(req.Ticket)
	if err != nil {
		fail(c, ErrRemoteAuthExpired)
		return
	}
	if !s.Store.HasSMS(userID) {
		fail(c, ErrSMSNotEnabled)
		return
	}
	if _, err := s.sendSMSCode(userID, store.SMSCodeLogin); err != nil {
		fail(c, err)
		return
	}
	c.JSONStatus(204, nil)
}

// handleSMSLogin completes an MFA login with an SMS code.
func (s *Server) handleSMSLogin(c *Ctx) {
	var req struct {
		Ticket string `json:"ticket"`
		Code   string `json:"code"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	userID, err := s.Store.ValidateMFATicket(req.Ticket)
	if err != nil {
		fail(c, ErrRemoteAuthExpired)
		return
	}
	ok, err := s.Store.ConsumeSMSCode(userID, store.SMSCodeLogin, strings.TrimSpace(req.Code))
	if err != nil {
		fail(c, err)
		return
	}
	if !ok {
		c.FailForm("code", "Invalid SMS code")
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
