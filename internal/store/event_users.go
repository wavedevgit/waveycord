package store

import "github.com/wavedevgit/waveycord/internal/model"

type EventUser struct {
	EventID  string        `json:"guild_scheduled_event_id"`
	UserID   string        `json:"user_id"`
	Response int           `json:"response"`
	User     *model.User   `json:"user,omitempty"`
	Member   *model.Member `json:"member,omitempty"`
}

func (s *Store) SetEventInterest(eventID, userID string, interested bool) (bool, error) {
	query := `INSERT INTO scheduled_event_users (event_id, user_id) VALUES (?,?) ON CONFLICT DO NOTHING`
	if !interested {
		query = `DELETE FROM scheduled_event_users WHERE event_id = ? AND user_id = ?`
	}
	result, err := s.DB.Exec(query, eventID, userID)
	if err != nil {
		return false, mapErr(err)
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

func (s *Store) EventUserCount(eventID string) (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM scheduled_event_users WHERE event_id = ?`, eventID).Scan(&n)
	return n, mapErr(err)
}

func (s *Store) EventUsers(eventID, guildID, before, after string, limit int, withMember bool) ([]EventUser, error) {
	query := `SELECT user_id FROM scheduled_event_users WHERE event_id = ?`
	args := []any{eventID}
	if before != "" {
		query += ` AND user_id < ?`
		args = append(args, before)
	} else if after != "" {
		query += ` AND user_id > ?`
		args = append(args, after)
	}
	if limit < 1 || limit > 100 {
		limit = 100
	}
	query += ` ORDER BY user_id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	out := make([]EventUser, 0, len(ids))
	for _, id := range ids {
		user, err := s.UserByID(id)
		if err != nil {
			return nil, err
		}
		entry := EventUser{EventID: eventID, UserID: id, Response: 1, User: &user}
		if withMember {
			if member, err := s.Member(guildID, id); err == nil {
				entry.Member = &member
			}
		}
		out = append(out, entry)
	}
	return out, nil
}
