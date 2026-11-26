package store

import (
	"database/sql"
	"errors"
	"time"
)

// WebAuthnCredential is a registered security key.
type WebAuthnCredential struct {
	ID           string
	UserID       string
	CredentialID string
	PublicKey    []byte
	SignCount    uint32
	Name         string
	CreatedAt    int64
	LastUsed     *int64
}

// ErrWebAuthnChallenge is returned for an unknown or expired challenge ticket.
var ErrWebAuthnChallenge = errors.New("store: webauthn challenge invalid")

// AddWebAuthnCredential stores a verified credential.
func (s *Store) AddWebAuthnCredential(c WebAuthnCredential) error {
	_, err := s.DB.Exec(`INSERT INTO webauthn_credentials
		(id, user_id, credential_id, public_key, sign_count, name, created_at)
		VALUES (?,?,?,?,?,?,?)`,
		c.ID, c.UserID, c.CredentialID, c.PublicKey, c.SignCount, c.Name, c.CreatedAt)
	return mapErr(err)
}

// ListWebAuthnCredentials returns a user's credentials, oldest first.
func (s *Store) ListWebAuthnCredentials(userID string) ([]WebAuthnCredential, error) {
	rows, err := s.DB.Query(`SELECT id, user_id, credential_id, public_key, sign_count, name, created_at, last_used
		FROM webauthn_credentials WHERE user_id = ? ORDER BY created_at ASC`, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []WebAuthnCredential{}
	for rows.Next() {
		var c WebAuthnCredential
		var last sql.NullInt64
		if err := rows.Scan(&c.ID, &c.UserID, &c.CredentialID, &c.PublicKey, &c.SignCount,
			&c.Name, &c.CreatedAt, &last); err != nil {
			return nil, mapErr(err)
		}
		if last.Valid {
			v := last.Int64
			c.LastUsed = &v
		}
		out = append(out, c)
	}
	return out, mapErr(rows.Err())
}

// WebAuthnCredentialByCredentialID resolves an assertion's raw credential id.
func (s *Store) WebAuthnCredentialByCredentialID(credID string) (WebAuthnCredential, error) {
	var c WebAuthnCredential
	var last sql.NullInt64
	err := s.DB.QueryRow(`SELECT id, user_id, credential_id, public_key, sign_count, name, created_at, last_used
		FROM webauthn_credentials WHERE credential_id = ?`, credID).
		Scan(&c.ID, &c.UserID, &c.CredentialID, &c.PublicKey, &c.SignCount, &c.Name, &c.CreatedAt, &last)
	if err != nil {
		return c, mapErr(err)
	}
	if last.Valid {
		v := last.Int64
		c.LastUsed = &v
	}
	return c, nil
}

// HasWebAuthn reports whether a user has any registered credential.
func (s *Store) HasWebAuthn(userID string) bool {
	var one int
	return s.DB.QueryRow(`SELECT 1 FROM webauthn_credentials WHERE user_id = ? LIMIT 1`, userID).Scan(&one) == nil
}

// RenameWebAuthnCredential updates the display name.
func (s *Store) RenameWebAuthnCredential(userID, id, name string) (WebAuthnCredential, error) {
	res, err := s.DB.Exec(`UPDATE webauthn_credentials SET name = ? WHERE id = ? AND user_id = ?`, name, id, userID)
	if err != nil {
		return WebAuthnCredential{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return WebAuthnCredential{}, ErrNotFound
	}
	return s.WebAuthnCredentialByID(userID, id)
}

// WebAuthnCredentialByID loads one credential scoped to its owner.
func (s *Store) WebAuthnCredentialByID(userID, id string) (WebAuthnCredential, error) {
	var c WebAuthnCredential
	var last sql.NullInt64
	err := s.DB.QueryRow(`SELECT id, user_id, credential_id, public_key, sign_count, name, created_at, last_used
		FROM webauthn_credentials WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&c.ID, &c.UserID, &c.CredentialID, &c.PublicKey, &c.SignCount, &c.Name, &c.CreatedAt, &last)
	if err != nil {
		return c, mapErr(err)
	}
	if last.Valid {
		v := last.Int64
		c.LastUsed = &v
	}
	return c, nil
}

// DeleteWebAuthnCredential removes one credential.
func (s *Store) DeleteWebAuthnCredential(userID, id string) error {
	res, err := s.DB.Exec(`DELETE FROM webauthn_credentials WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchWebAuthnCredential records a successful assertion.
func (s *Store) TouchWebAuthnCredential(id string, signCount uint32) {
	_, _ = s.DB.Exec(`UPDATE webauthn_credentials SET sign_count = ?, last_used = ? WHERE id = ?`,
		signCount, time.Now().UnixMilli(), id)
}

// CreateWebAuthnChallenge stores a challenge ticket for one flow.
func (s *Store) CreateWebAuthnChallenge(ticket, userID, challenge, kind string, expires int64) error {
	_, err := s.DB.Exec(`INSERT INTO webauthn_challenges (ticket, user_id, challenge, kind, expires)
		VALUES (?,?,?,?,?)`, ticket, userID, challenge, kind, expires)
	return mapErr(err)
}

// WebAuthnChallenge resolves an unexpired challenge ticket.
func (s *Store) WebAuthnChallenge(ticket string) (userID, challenge, kind string, err error) {
	var expires int64
	err = s.DB.QueryRow(`SELECT user_id, challenge, kind, expires FROM webauthn_challenges WHERE ticket = ?`, ticket).
		Scan(&userID, &challenge, &kind, &expires)
	if err != nil {
		return "", "", "", mapErr(err)
	}
	if now() > expires {
		return "", "", "", ErrWebAuthnChallenge
	}
	return userID, challenge, kind, nil
}

// DeleteWebAuthnChallenge consumes a challenge ticket.
func (s *Store) DeleteWebAuthnChallenge(ticket string) error {
	_, err := s.DB.Exec(`DELETE FROM webauthn_challenges WHERE ticket = ?`, ticket)
	return mapErr(err)
}

// CreateLoginChallenge stores a challenge that is not bound to a user, used by the
// discoverable-credential (conditional) flow where the account is unknown until an
// assertion identifies a credential.
func (s *Store) CreateLoginChallenge(ticket, challenge, kind string, expires int64) error {
	_, err := s.DB.Exec(`INSERT INTO webauthn_login_challenges (ticket, challenge, kind, expires)
		VALUES (?,?,?,?)`, ticket, challenge, kind, expires)
	return mapErr(err)
}

// LoginChallenge resolves an unexpired user-less challenge.
func (s *Store) LoginChallenge(ticket string) (challenge, kind string, err error) {
	var expires int64
	err = s.DB.QueryRow(`SELECT challenge, kind, expires FROM webauthn_login_challenges WHERE ticket = ?`, ticket).
		Scan(&challenge, &kind, &expires)
	if err != nil {
		return "", "", mapErr(err)
	}
	if now() > expires {
		return "", "", ErrWebAuthnChallenge
	}
	return challenge, kind, nil
}

// DeleteLoginChallenge consumes a user-less challenge.
func (s *Store) DeleteLoginChallenge(ticket string) error {
	_, err := s.DB.Exec(`DELETE FROM webauthn_login_challenges WHERE ticket = ?`, ticket)
	return mapErr(err)
}
