package store

import (
	"database/sql"
	"errors"
)

// BackupCode is one recovery code and whether it has been spent.
type BackupCode struct {
	Code string `json:"code"`
	Used bool   `json:"used"`
}

// SetMFASecret stores the TOTP secret and marks the account MFA-enabled.
func (s *Store) SetMFASecret(userID, secret string) error {
	res, err := s.DB.Exec(`UPDATE users SET mfa_secret = ?, mfa_enabled = 1 WHERE id = ?`, secret, userID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DisableMFA clears the secret, the flag and every backup code.
func (s *Store) DisableMFA(userID string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET mfa_secret = NULL, mfa_enabled = 0 WHERE id = ?`, userID); err != nil {
		return mapErr(err)
	}
	if _, err := tx.Exec(`DELETE FROM mfa_backup_codes WHERE user_id = ?`, userID); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit())
}

// MFASecret returns the stored TOTP secret and whether MFA is enabled.
func (s *Store) MFASecret(userID string) (string, bool, error) {
	var secret sql.NullString
	var enabled bool
	err := s.DB.QueryRow(`SELECT mfa_secret, mfa_enabled FROM users WHERE id = ?`, userID).Scan(&secret, &enabled)
	if err != nil {
		return "", false, mapErr(err)
	}
	return secret.String, enabled, nil
}

// ReplaceBackupCodes stores a fresh set, discarding any previous set.
func (s *Store) ReplaceBackupCodes(userID string, codes []string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM mfa_backup_codes WHERE user_id = ?`, userID); err != nil {
		return mapErr(err)
	}
	for _, code := range codes {
		if _, err := tx.Exec(`INSERT INTO mfa_backup_codes (user_id, code, used) VALUES (?,?,0)`, userID, code); err != nil {
			return mapErr(err)
		}
	}
	return mapErr(tx.Commit())
}

// BackupCodes lists the account's recovery codes.
func (s *Store) BackupCodes(userID string) ([]BackupCode, error) {
	rows, err := s.DB.Query(`SELECT code, used FROM mfa_backup_codes WHERE user_id = ? ORDER BY code ASC`, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []BackupCode{}
	for rows.Next() {
		var c BackupCode
		if err := rows.Scan(&c.Code, &c.Used); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, c)
	}
	return out, mapErr(rows.Err())
}

// SpendBackupCode marks a code used, atomically. It reports whether the code was
// valid and unused; a spent or unknown code returns false.
func (s *Store) SpendBackupCode(userID, code string) (bool, error) {
	res, err := s.DB.Exec(`UPDATE mfa_backup_codes SET used = 1 WHERE user_id = ? AND code = ? AND used = 0`, userID, code)
	if err != nil {
		return false, mapErr(err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// CreateMFATicket stores a short-lived login ticket for a user who must complete
// a second factor.
func (s *Store) CreateMFATicket(id, userID string, expires int64) error {
	_, err := s.DB.Exec(`INSERT INTO mfa_tickets (id, user_id, expires) VALUES (?,?,?)`, id, userID, expires)
	return mapErr(err)
}

// ValidateMFATicket returns the user for an unexpired ticket without consuming
// it, so a wrong second factor does not force a full login restart.
func (s *Store) ValidateMFATicket(id string) (string, error) {
	var userID string
	var expires int64
	err := s.DB.QueryRow(`SELECT user_id, expires FROM mfa_tickets WHERE id = ?`, id).Scan(&userID, &expires)
	if err != nil {
		return "", mapErr(err)
	}
	if now() > expires {
		return "", ErrMFATicketExpired
	}
	return userID, nil
}

// DeleteMFATicket consumes a ticket after a successful second factor.
func (s *Store) DeleteMFATicket(id string) error {
	_, err := s.DB.Exec(`DELETE FROM mfa_tickets WHERE id = ?`, id)
	return mapErr(err)
}

// ErrMFATicketExpired is returned for an expired login ticket.
var ErrMFATicketExpired = errors.New("store: mfa ticket expired")
