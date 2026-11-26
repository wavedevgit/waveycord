package store

import (
	"database/sql"

	"github.com/wavedevgit/waveycord/internal/model"
)

// Sticker is the sticker object for guild stickers.
type Sticker struct {
	ID          string      `json:"id"`
	PackID      *string     `json:"pack_id"`
	Name        string      `json:"name"`
	Description *string     `json:"description"`
	Tags        string      `json:"tags"`
	Type        int         `json:"type"`
	FormatType  int         `json:"format_type"`
	Available   bool        `json:"available"`
	GuildID     *string     `json:"guild_id,omitempty"`
	User        *model.User `json:"user,omitempty"`
	SortValue   *int        `json:"sort_value,omitempty"`
}

const stickerColumns = `id, guild_id, name, description, tags, format_type, available, user_id, sort_value`

func scanSticker(row rowScanner) (Sticker, string, error) {
	var (
		s     Sticker
		guild sql.NullString
		desc  sql.NullString
		user  sql.NullString
		sort  int
	)
	if err := row.Scan(&s.ID, &guild, &s.Name, &desc, &s.Tags, &s.FormatType, &s.Available, &user, &sort); err != nil {
		return s, "", err
	}
	s.GuildID = ns(guild)
	s.Description = ns(desc)
	s.Type = 2
	s.SortValue = &sort
	return s, stringOr(user, ""), nil
}

// helper indirection replaced below; kept simple via package function variable.
var s2User = func(id string) (model.User, error) { return model.User{}, ErrNotFound }

// SetUserLookup wires the sticker user projection to the store.
func (s *Store) initStickerLookup() {
	s2User = func(id string) (model.User, error) { return s.UserByID(id) }
}

// CreateSticker inserts a guild sticker.
func (s *Store) CreateSticker(guildID, userID, name string, description *string, tags string, formatType int) (Sticker, error) {
	s.initStickerLookup()
	id := s.NewID()
	var maxSort int
	_ = s.DB.QueryRow(`SELECT COALESCE(MAX(sort_value),0) FROM stickers WHERE guild_id = ?`, guildID).Scan(&maxSort)
	_, err := s.DB.Exec(`INSERT INTO stickers
		(id, guild_id, name, description, tags, format_type, available, user_id, sort_value, created_at)
		VALUES (?,?,?,?,?,?,1,?,?,?)`,
		id, guildID, name, description, tags, formatType, userID, maxSort+1, now())
	if err != nil {
		return Sticker{}, mapErr(err)
	}
	return s.Sticker(id)
}

// Sticker loads one sticker.
func (s *Store) Sticker(id string) (Sticker, error) {
	s.initStickerLookup()
	st, userID, err := scanSticker(s.DB.QueryRow(`SELECT `+stickerColumns+` FROM stickers WHERE id = ?`, id))
	if err != nil {
		return st, mapErr(err)
	}
	if userID != "" {
		if u, err := s2User(userID); err == nil {
			st.User = &u
		}
	}
	return st, nil
}

// GuildStickers lists a guild's stickers.
func (s *Store) GuildStickers(guildID string) ([]Sticker, error) {
	s.initStickerLookup()
	rows, err := s.DB.Query(`SELECT `+stickerColumns+` FROM stickers WHERE guild_id = ? ORDER BY sort_value ASC, id ASC`, guildID)
	if err != nil {
		return nil, mapErr(err)
	}
	type pending struct {
		st     Sticker
		userID string
	}
	var raw []pending
	for rows.Next() {
		st, userID, err := scanSticker(rows)
		if err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		raw = append(raw, pending{st: st, userID: userID})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	out := make([]Sticker, 0, len(raw))
	for _, p := range raw {
		st := p.st
		if p.userID != "" {
			if u, err := s2User(p.userID); err == nil {
				st.User = &u
			}
		}
		out = append(out, st)
	}
	return out, nil
}

// UpdateSticker patches name/description/tags.
func (s *Store) UpdateSticker(guildID, id string, name, description, tags *string) (Sticker, error) {
	set := []string{}
	args := []any{}
	if name != nil {
		set = append(set, "name = ?")
		args = append(args, *name)
	}
	if description != nil {
		set = append(set, "description = ?")
		args = append(args, *description)
	}
	if tags != nil {
		set = append(set, "tags = ?")
		args = append(args, *tags)
	}
	if len(set) == 0 {
		return s.Sticker(id)
	}
	args = append(args, guildID, id)
	if _, err := s.DB.Exec(`UPDATE stickers SET `+joinComma(set)+` WHERE guild_id = ? AND id = ?`, args...); err != nil {
		return Sticker{}, mapErr(err)
	}
	return s.Sticker(id)
}

// DeleteSticker removes a guild sticker.
func (s *Store) DeleteSticker(guildID, id string) error {
	res, err := s.DB.Exec(`DELETE FROM stickers WHERE guild_id = ? AND id = ?`, guildID, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetWidget writes widget settings.
func (s *Store) SetWidget(guildID string, enabled bool, channelID *string) error {
	res, err := s.DB.Exec(`UPDATE guilds SET widget_enabled = ?, widget_channel_id = ? WHERE id = ?`,
		enabled, channelID, guildID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Widget returns the widget settings.
func (s *Store) Widget(guildID string) (bool, *string, error) {
	var enabled bool
	var channel sql.NullString
	err := s.DB.QueryRow(`SELECT widget_enabled, widget_channel_id FROM guilds WHERE id = ?`, guildID).
		Scan(&enabled, &channel)
	return enabled, ns(channel), mapErr(err)
}
