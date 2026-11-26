package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/store"
)

// argon2id parameters. Kept in the encoded hash so verification survives a
// parameter change.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

// HashPassword returns an encoded argon2id hash.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword compares a plaintext password with an encoded hash.
func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var memory uint32
	var iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NewToken returns a 40-byte opaque bearer token.
func NewToken() (string, error) {
	buf := make([]byte, 40)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// sessionID returns a stable public id for a session row.
func sessionID(token string) string {
	sum := TokenHash(token)
	return hex.EncodeToString([]byte(sum))[:32]
}

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func validUsername(name string) bool {
	n := len([]rune(name))
	if n < 2 || n > 32 {
		return false
	}
	for _, bad := range []string{"@", "#", ":", "```", "discord"} {
		if strings.Contains(strings.ToLower(name), bad) {
			return false
		}
	}
	switch strings.ToLower(name) {
	case "everyone", "here":
		return false
	}
	return strings.TrimSpace(name) == name
}

type registerRequest struct {
	Username   string  `json:"username"`
	Password   string  `json:"password"`
	Email      *string `json:"email"`
	Phone      *string `json:"phone"`
	GlobalName *string `json:"global_name"`
	Invite     *string `json:"invite"`
	Consent    *bool   `json:"consent"`
}

func (s *Server) handleRegister(c *Ctx) {
	var req registerRequest
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if !validUsername(req.Username) {
		c.FailForm("username", "Must be between 2 and 32 characters and contain no forbidden substrings")
		return
	}
	if len(req.Password) < 8 || len(req.Password) > 72 {
		c.FailForm("password", "Must be between 8 and 72 characters")
		return
	}
	if req.Email == nil && req.Phone == nil {
		c.FailForm("email", "Either email or phone is required")
		return
	}
	if req.Email != nil && !emailRe.MatchString(*req.Email) {
		c.FailForm("email", "Not a well formed email address")
		return
	}
	if req.GlobalName != nil && len([]rune(*req.GlobalName)) > 32 {
		c.FailForm("global_name", "Must be 32 characters or fewer")
		return
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		fail(c, err)
		return
	}
	user, err := s.Store.CreateUser(store.CreateUserParams{
		Username:     req.Username,
		GlobalName:   req.GlobalName,
		Email:        req.Email,
		Phone:        req.Phone,
		PasswordHash: hash,
		Verified:     true,
	})
	if err != nil {
		if err == store.ErrConflict {
			c.FailForm("email", "Email is already registered")
			return
		}
		fail(c, err)
		return
	}
	// An invite on registration joins the account to the guild.
	if req.Invite != nil {
		if inv, err := s.Store.Invite(*req.Invite); err == nil && inv.Guild != nil {
			_, _ = s.Store.AddMember(inv.Guild.ID, user.ID)
			s.enforceProfileAutoMod(inv.Guild.ID, user.ID)
		}
	}
	token, err := NewToken()
	if err != nil {
		fail(c, err)
		return
	}
	if _, err := s.Store.CreateSession(user.ID, TokenHash(token), c.R.UserAgent()); err != nil {
		fail(c, err)
		return
	}
	c.JSONStatus(201, map[string]any{"token": token})
}

type loginRequest struct {
	Login    string `json:"login"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(c *Ctx) {
	var req loginRequest
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	login := req.Login
	if login == "" {
		login = req.Email
	}
	if login == "" || req.Password == "" {
		fail(c, NewError(400, 50035, "Invalid Form Body"))
		return
	}
	user, err := s.Store.UserByLogin(login)
	if err != nil {
		c.JSONStatus(400, map[string]any{"code": 50035, "message": "Invalid Form Body"})
		return
	}
	hash, err := s.Store.PasswordHash(user.ID)
	if err != nil || !VerifyPassword(req.Password, hash) {
		c.JSONStatus(400, map[string]any{"code": 50035, "message": "Invalid Form Body"})
		return
	}
	// With a second factor the password only yields a ticket; a session token is
	// issued after TOTP or a WebAuthn assertion.
	secret, enabled, _ := s.Store.MFASecret(user.ID)
	// mfa_enabled also covers WebAuthn-only and SMS-only accounts, so the TOTP flag
	// is only true when a secret actually exists.
	hasTOTP := enabled && secret != ""
	hasWebAuthn := s.Store.HasWebAuthn(user.ID)
	hasSMS := s.Store.HasSMS(user.ID)
	if hasTOTP || hasWebAuthn || hasSMS {
		ticket, err := NewToken()
		if err != nil {
			fail(c, err)
			return
		}
		if err := s.Store.CreateMFATicket(ticket, user.ID, nowMillisFn()+mfaTicketTTL); err != nil {
			fail(c, err)
			return
		}
		// When SMS is the only factor there is nothing else to present, so the
		// login code is issued up front; clients can resend it.
		if hasSMS && !hasTOTP && !hasWebAuthn {
			if _, err := s.sendSMSCode(user.ID, store.SMSCodeLogin); err != nil {
				fail(c, err)
				return
			}
		}
		c.JSON(map[string]any{
			"mfa": true, "totp": hasTOTP,
			"webauthn": hasWebAuthn, "sms": hasSMS, "ticket": ticket,
		})
		return
	}
	token, err := NewToken()
	if err != nil {
		fail(c, err)
		return
	}
	if _, err := s.Store.CreateSession(user.ID, TokenHash(token), c.R.UserAgent()); err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"token": token})
}

func (s *Server) handleLogout(c *Ctx) {
	if err := s.Store.DeleteSession(c.User.ID, c.SessionID); err != nil && err != store.ErrNotFound {
		fail(c, err)
		return
	}
	c.NoContent()
}

type sessionView struct {
	ID         string `json:"id"`
	ClientInfo string `json:"client_info"`
	CreatedAt  string `json:"created_at"`
}

func (s *Server) handleListSessions(c *Ctx) {
	sessions, err := s.Store.SessionsForUser(c.User.ID)
	if err != nil {
		fail(c, err)
		return
	}
	out := make([]sessionView, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, sessionView{ID: sess.ID, ClientInfo: sess.ClientInfo, CreatedAt: isoTime(sess.CreatedAt)})
	}
	c.JSON(out)
}

func (s *Server) handleLogoutSessions(c *Ctx) {
	var req struct {
		SessionID *string `json:"session_id"`
	}
	_ = c.Bind(&req)
	sessions, err := s.Store.SessionsForUser(c.User.ID)
	if err != nil {
		fail(c, err)
		return
	}
	for _, sess := range sessions {
		if req.SessionID == nil || sess.ID == *req.SessionID {
			_ = s.Store.DeleteSession(c.User.ID, sess.ID)
		}
	}
	c.NoContent()
}

// selfView is the private user projection.
func (s *Server) selfView(u model.User) model.User {
	v := u
	// The authenticated caller sees their own MFA state; public user views clear it.
	v.Email = u.Email
	v.Verified = true
	return v
}
