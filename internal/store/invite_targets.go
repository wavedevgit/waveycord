package store

import "github.com/wavedevgit/waveycord/internal/sqlx"

// InviteTargetUsers lists the user ids allowed to accept an invite.
func (s *Store) InviteTargetUsers(code string) ([]string, error) {
	rows, err := s.DB.Query(`SELECT user_id FROM invite_target_users WHERE invite_code = ? ORDER BY user_id`, code)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var idv string
		if err := rows.Scan(&idv); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, idv)
	}
	return out, mapErr(rows.Err())
}

// ReplaceInviteTargetUsers swaps the whole target list in one transaction.
func (s *Store) ReplaceInviteTargetUsers(code string, userIDs []string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM invite_target_users WHERE invite_code = ?`, code); err != nil {
		return mapErr(err)
	}
	if err := insertTargetUsers(tx, code, userIDs); err != nil {
		return err
	}
	return mapErr(tx.Commit())
}

// AddInviteTargetUsers adds users to an invite's target list.
func (s *Store) AddInviteTargetUsers(code string, userIDs []string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	if err := insertTargetUsers(tx, code, userIDs); err != nil {
		return err
	}
	return mapErr(tx.Commit())
}

// RemoveInviteTargetUsers removes users from an invite's target list.
func (s *Store) RemoveInviteTargetUsers(code string, userIDs []string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	for _, userID := range userIDs {
		if _, err := tx.Exec(`DELETE FROM invite_target_users WHERE invite_code = ? AND user_id = ?`, code, userID); err != nil {
			return mapErr(err)
		}
	}
	return mapErr(tx.Commit())
}

func insertTargetUsers(tx *sqlx.Tx, code string, userIDs []string) error {
	for _, userID := range userIDs {
		if _, err := tx.Exec(`INSERT INTO invite_target_users (invite_code, user_id)
			VALUES (?,?) ON CONFLICT(invite_code, user_id) DO NOTHING`, code, userID); err != nil {
			return mapErr(err)
		}
	}
	return nil
}
