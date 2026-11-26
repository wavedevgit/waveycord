package store

import "errors"

// RemoteAuthSession is one QR sign-in handshake. The scanning (already signed in)
// client creates it with the fingerprint shown by the QR device, approves it, and
// the QR device redeems the ticket once for a session token.
type RemoteAuthSession struct {
	Ticket      string
	UserID      string
	Fingerprint string
	Status      string // pending | approved | cancelled
	CreatedAt   int64
	Expires     int64
}

// Remote auth session states.
const (
	RemoteAuthPending   = "pending"
	RemoteAuthApproved  = "approved"
	RemoteAuthCancelled = "cancelled"
)

var (
	// ErrRemoteAuthState is returned when a transition is not allowed.
	ErrRemoteAuthState = errors.New("store: remote auth in invalid state")
	// ErrRemoteAuthExpired is returned once the handshake window has passed.
	ErrRemoteAuthExpired = errors.New("store: remote auth expired")
)

// CreateRemoteAuthSession stores a pending handshake.
func (s *Store) CreateRemoteAuthSession(ticket, userID, fingerprint string, expires int64) error {
	_, err := s.DB.Exec(`INSERT INTO remote_auth_sessions
		(ticket, user_id, fingerprint, status, created_at, expires)
		VALUES (?,?,?,?,?,?)`, ticket, userID, fingerprint, RemoteAuthPending, now(), expires)
	return mapErr(err)
}

// RemoteAuthSessionByTicket loads a handshake and enforces expiry on read.
func (s *Store) RemoteAuthSessionByTicket(ticket string) (RemoteAuthSession, error) {
	var r RemoteAuthSession
	err := s.DB.QueryRow(`SELECT ticket, user_id, fingerprint, status, created_at, expires
		FROM remote_auth_sessions WHERE ticket = ?`, ticket).
		Scan(&r.Ticket, &r.UserID, &r.Fingerprint, &r.Status, &r.CreatedAt, &r.Expires)
	if err != nil {
		return r, mapErr(err)
	}
	if now() > r.Expires {
		return r, ErrRemoteAuthExpired
	}
	return r, nil
}

// ApproveRemoteAuth moves a pending handshake to approved.
func (s *Store) ApproveRemoteAuth(ticket, userID string) error {
	sess, err := s.RemoteAuthSessionByTicket(ticket)
	if err != nil {
		return err
	}
	if sess.UserID != userID {
		return ErrNotFound
	}
	if sess.Status != RemoteAuthPending {
		return ErrRemoteAuthState
	}
	res, err := s.DB.Exec(`UPDATE remote_auth_sessions SET status = ?
		WHERE ticket = ? AND status = ?`, RemoteAuthApproved, ticket, RemoteAuthPending)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrRemoteAuthState
	}
	return nil
}

// CancelRemoteAuth cancels a handshake. Cancelling an approved or cancelled
// handshake is a state error, not a silent success.
func (s *Store) CancelRemoteAuth(ticket, userID string) error {
	sess, err := s.RemoteAuthSessionByTicket(ticket)
	if err != nil {
		return err
	}
	if sess.UserID != userID {
		return ErrNotFound
	}
	if sess.Status != RemoteAuthPending && sess.Status != RemoteAuthApproved {
		return ErrRemoteAuthState
	}
	_, err = s.DB.Exec(`UPDATE remote_auth_sessions SET status = ? WHERE ticket = ?`, RemoteAuthCancelled, ticket)
	return mapErr(err)
}

// RedeemRemoteAuth atomically consumes an approved handshake and returns its user
// id. The fingerprint must match the one supplied at creation.
func (s *Store) RedeemRemoteAuth(ticket, fingerprint string) (string, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return "", mapErr(err)
	}
	defer tx.Rollback()
	var (
		userID   string
		storedFP string
		status   string
		expires  int64
	)
	err = tx.QueryRow(`SELECT user_id, fingerprint, status, expires FROM remote_auth_sessions WHERE ticket = ?`, ticket).
		Scan(&userID, &storedFP, &status, &expires)
	if err != nil {
		return "", mapErr(err)
	}
	if now() > expires {
		return "", ErrRemoteAuthExpired
	}
	if status != RemoteAuthApproved {
		return "", ErrRemoteAuthState
	}
	if storedFP != fingerprint {
		return "", ErrNotFound
	}
	res, err := tx.Exec(`DELETE FROM remote_auth_sessions WHERE ticket = ? AND status = ?`, ticket, RemoteAuthApproved)
	if err != nil {
		return "", mapErr(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", ErrRemoteAuthState
	}
	if err := tx.Commit(); err != nil {
		return "", mapErr(err)
	}
	return userID, nil
}

// ExpireRemoteAuthSessions deletes stale handshakes and reports how many were
// removed, so the table cannot grow without bound.
func (s *Store) ExpireRemoteAuthSessions(nowMs int64) (int64, error) {
	res, err := s.DB.Exec(`DELETE FROM remote_auth_sessions WHERE expires < ?`, nowMs)
	if err != nil {
		return 0, mapErr(err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
