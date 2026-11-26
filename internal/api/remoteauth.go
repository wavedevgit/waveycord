package api

import (
	"encoding/hex"
	"strings"

	"github.com/wavedevgit/waveycord/internal/store"
)

// Remote auth (QR) handoff. The signed-in client registers the fingerprint shown
// by the QR device, approves the handshake, and the QR device redeems the ticket
// once for a fresh session token. The transport is HTTP only: there is no QR
// websocket gateway in this server, and the redeemed token is not encrypted to
// the device's public key (the fingerprint is not a key). Both are recorded as
// gaps in docs/COMPATIBILITY.md.

// remoteAuthTTL bounds a handshake.
const remoteAuthTTL = 2 * 60 * 1000

// validFingerprint accepts a 32-byte hex fingerprint, the shape the client shows.
func validFingerprint(fp string) bool {
	if len(fp) != 64 {
		return false
	}
	_, err := hex.DecodeString(fp)
	return err == nil
}

// handleRemoteAuthStart registers a handshake for the signed-in caller.
func (s *Server) handleRemoteAuthStart(c *Ctx) {
	var req struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	fp := strings.ToLower(strings.TrimSpace(req.Fingerprint))
	if !validFingerprint(fp) {
		c.FailForm("fingerprint", "Fingerprint must be 32 hex-encoded bytes")
		return
	}
	ticket, _, err := newChallenge()
	if err != nil {
		fail(c, err)
		return
	}
	expires := nowMillisFn() + remoteAuthTTL
	if err := s.Store.CreateRemoteAuthSession(ticket, c.User.ID, fp, expires); err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"ticket": ticket, "expires_at": isoTime(expires)})
}

// handleRemoteAuthFinish approves a pending handshake.
func (s *Server) handleRemoteAuthFinish(c *Ctx) {
	var req struct {
		Ticket string `json:"ticket"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	err := s.Store.ApproveRemoteAuth(req.Ticket, c.User.ID)
	switch err {
	case nil:
		c.JSONStatus(204, nil)
	case store.ErrNotFound:
		fail(c, ErrUnknownRemoteAuth)
	case store.ErrRemoteAuthExpired:
		fail(c, ErrRemoteAuthExpired)
	default:
		fail(c, ErrUnknownRemoteAuth)
	}
}

// handleRemoteAuthCancel cancels a handshake.
func (s *Server) handleRemoteAuthCancel(c *Ctx) {
	var req struct {
		Ticket string `json:"ticket"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	err := s.Store.CancelRemoteAuth(req.Ticket, c.User.ID)
	switch err {
	case nil:
		c.JSONStatus(204, nil)
	case store.ErrNotFound:
		fail(c, ErrUnknownRemoteAuth)
	case store.ErrRemoteAuthExpired:
		fail(c, ErrRemoteAuthExpired)
	default:
		fail(c, ErrUnknownRemoteAuth)
	}
}

// handleRemoteAuthLogin redeems an approved handshake once. It is unauthenticated:
// possession of the ticket plus the matching fingerprint is the proof.
func (s *Server) handleRemoteAuthLogin(c *Ctx) {
	var req struct {
		Ticket      string `json:"ticket"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	fp := strings.ToLower(strings.TrimSpace(req.Fingerprint))
	if !validFingerprint(fp) {
		c.FailForm("fingerprint", "Fingerprint must be 32 hex-encoded bytes")
		return
	}
	userID, err := s.Store.RedeemRemoteAuth(req.Ticket, fp)
	if err != nil {
		switch err {
		case store.ErrRemoteAuthExpired:
			fail(c, ErrRemoteAuthExpired)
		case store.ErrRemoteAuthState:
			fail(c, ErrRemoteAuthNotApproved)
		default:
			fail(c, ErrUnknownRemoteAuth)
		}
		return
	}
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
