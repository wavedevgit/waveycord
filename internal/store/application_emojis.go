package store

import (
	"database/sql"

	"github.com/wavedevgit/waveycord/internal/model"
)

// applicationEmojiColumns is the shared projection for application-owned
// emojis. Application emojis live in the same table as guild emojis and are
// distinguished by a non-null application_id.
const applicationEmojiColumns = `id, name, creator_id, roles, require_colons, managed, animated, available`

// CreateApplicationEmoji inserts an emoji owned by an application.
func (s *Store) CreateApplicationEmoji(appID, creatorID, name string, animated bool) (model.Emoji, error) {
	idv := s.NewID()
	_, err := s.DB.Exec(`INSERT INTO emojis
		(id, guild_id, application_id, name, creator_id, roles, require_colons, managed, animated, available)
		VALUES (?,NULL,?,?,?,'[]',1,0,?,1)`, idv, appID, name, creatorID, animated)
	if err != nil {
		return model.Emoji{}, mapErr(err)
	}
	return s.ApplicationEmoji(appID, idv)
}

// ApplicationEmojis lists an application's emojis.
func (s *Store) ApplicationEmojis(appID string) ([]model.Emoji, error) {
	rows, err := s.DB.Query(`SELECT `+applicationEmojiColumns+` FROM emojis
		WHERE application_id = ? ORDER BY id`, appID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	type owned struct {
		emoji   model.Emoji
		creator string
	}
	var found []owned
	for rows.Next() {
		e, creator, err := scanApplicationEmoji(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, owned{emoji: e, creator: creator})
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	// Close the cursor before loading users: SQLite serialises statements on
	// one connection, so a nested query inside the row loop would block.
	rows.Close()
	out := []model.Emoji{}
	for _, f := range found {
		if f.creator != "" {
			if u, err := s.UserByID(f.creator); err == nil {
				f.emoji.User = &u
			}
		}
		out = append(out, f.emoji)
	}
	return out, nil
}

// ApplicationEmoji loads one application-owned emoji.
func (s *Store) ApplicationEmoji(appID, idv string) (model.Emoji, error) {
	row := s.DB.QueryRow(`SELECT `+applicationEmojiColumns+` FROM emojis
		WHERE application_id = ? AND id = ?`, appID, idv)
	e, creator, err := scanApplicationEmoji(row)
	if err != nil {
		return model.Emoji{}, err
	}
	if creator != "" {
		if u, err := s.UserByID(creator); err == nil {
			e.User = &u
		}
	}
	return e, nil
}

// UpdateApplicationEmoji renames an application emoji.
func (s *Store) UpdateApplicationEmoji(appID, idv string, name *string) (model.Emoji, error) {
	if name != nil {
		res, err := s.DB.Exec(`UPDATE emojis SET name = ? WHERE application_id = ? AND id = ?`, *name, appID, idv)
		if err != nil {
			return model.Emoji{}, mapErr(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return model.Emoji{}, ErrNotFound
		}
	}
	return s.ApplicationEmoji(appID, idv)
}

// DeleteApplicationEmoji removes an application-owned emoji.
func (s *Store) DeleteApplicationEmoji(appID, idv string) error {
	res, err := s.DB.Exec(`DELETE FROM emojis WHERE application_id = ? AND id = ?`, appID, idv)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanApplicationEmoji(row rowScanner) (model.Emoji, string, error) {
	var (
		e        model.Emoji
		idv      string
		name     sql.NullString
		creator  sql.NullString
		roles    string
		required int
	)
	if err := row.Scan(&idv, &name, &creator, &roles, &required, &e.Managed, &e.Animated, &e.Available); err != nil {
		return e, "", mapErr(err)
	}
	e.ID = &idv
	e.Name = ns(name)
	e.RequireColons = required != 0
	e.Roles = parseJSONStrings(roles)
	out := ""
	if creator.Valid {
		out = creator.String
	}
	return e, out, nil
}
