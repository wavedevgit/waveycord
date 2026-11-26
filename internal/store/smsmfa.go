package store

import (
	"database/sql"
	"errors"
	"time"
)

// SMS second factor state and one-time codes. Codes are short-lived and stored
// verbatim so a test (or an operator inspecting a staging box) can read them; the
// delivered-with-SMS step is out of scope and recorded as a gap.

// SMSCodePurpose distinguishes the enrollment code from the login code.
const (
	SMSCodeEnable = "enable"
	SMSCodeLogin  = "login"
)

// SetSMSMFA records the verified phone and enables the factor.
func (s *Store) SetSMSMFA(userID, phone string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO sms_mfa (user_id, phone, enabled, created_at)
		VALUES (?,?,1,?)
		ON CONFLICT(user_id) DO UPDATE SET phone = excluded.phone, enabled = 1`,
		userID, phone, now()); err != nil {
		return mapErr(err)
	}
	if _, err := tx.Exec(`UPDATE users SET mfa_enabled = 1 WHERE id = ?`, userID); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit())
}

// SMSMFA returns the phone and whether the factor is enabled.
func (s *Store) SMSMFA(userID string) (string, bool, error) {
	var phone string
	var enabled bool
	err := s.DB.QueryRow(`SELECT phone, enabled FROM sms_mfa WHERE user_id = ?`, userID).Scan(&phone, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, mapErr(err)
	}
	return phone, enabled, nil
}

// HasSMS reports whether SMS is enabled for a user.
func (s *Store) HasSMS(userID string) bool {
	_, enabled, err := s.SMSMFA(userID)
	return err == nil && enabled
}

// DisableSMSMFA removes the factor and its codes.
func (s *Store) DisableSMSMFA(userID string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM sms_mfa WHERE user_id = ?`, userID); err != nil {
		return mapErr(err)
	}
	if _, err := tx.Exec(`DELETE FROM sms_mfa_codes WHERE user_id = ?`, userID); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit())
}

// SetSMSCode replaces the outstanding code for one purpose.
func (s *Store) SetSMSCode(userID, purpose, code string, ttl time.Duration) error {
	_, err := s.DB.Exec(`INSERT INTO sms_mfa_codes (user_id, purpose, code, expires)
		VALUES (?,?,?,?)
		ON CONFLICT(user_id, purpose) DO UPDATE SET code = excluded.code, expires = excluded.expires`,
		userID, purpose, code, time.Now().Add(ttl).UnixMilli())
	return mapErr(err)
}

// ConsumeSMSCode validates a code for one purpose and deletes it on success.
func (s *Store) ConsumeSMSCode(userID, purpose, code string) (bool, error) {
	var stored string
	var expires int64
	err := s.DB.QueryRow(`SELECT code, expires FROM sms_mfa_codes WHERE user_id = ? AND purpose = ?`,
		userID, purpose).Scan(&stored, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, mapErr(err)
	}
	if time.Now().UnixMilli() > expires {
		_, _ = s.DB.Exec(`DELETE FROM sms_mfa_codes WHERE user_id = ? AND purpose = ?`, userID, purpose)
		return false, nil
	}
	if stored != code {
		return false, nil
	}
	res, err := s.DB.Exec(`DELETE FROM sms_mfa_codes WHERE user_id = ? AND purpose = ? AND code = ?`,
		userID, purpose, code)
	if err != nil {
		return false, mapErr(err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
