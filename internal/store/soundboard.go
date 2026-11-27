package store

import (
	"database/sql"

	"github.com/wavedevgit/waveycord/internal/model"
)

// SoundboardSound is the documented soundboard sound object.
type SoundboardSound struct {
	Name      string      `json:"name"`
	SoundID   string      `json:"sound_id"`
	Volume    float64     `json:"volume"`
	EmojiID   *string     `json:"emoji_id"`
	EmojiName *string     `json:"emoji_name"`
	GuildID   *string     `json:"guild_id,omitempty"`
	Available bool        `json:"available"`
	User      *model.User `json:"user,omitempty"`
}

const soundboardColumns = `sound_id, guild_id, name, volume, emoji_id, emoji_name, available, creator_id`

func scanSoundboard(row rowScanner) (SoundboardSound, string, error) {
	var (
		s       SoundboardSound
		guild   string
		emoji   sql.NullString
		emojiN  sql.NullString
		avail   int
		creator sql.NullString
	)
	if err := row.Scan(&s.SoundID, &guild, &s.Name, &s.Volume, &emoji, &emojiN, &avail, &creator); err != nil {
		return s, "", mapErr(err)
	}
	s.GuildID = &guild
	s.EmojiID = ns(emoji)
	s.EmojiName = ns(emojiN)
	s.Available = avail != 0
	out := ""
	if creator.Valid {
		out = creator.String
	}
	return s, out, nil
}

// GuildSoundboardSounds lists a guild's soundboard sounds.
func (s *Store) GuildSoundboardSounds(guildID string) ([]SoundboardSound, error) {
	rows, err := s.DB.Query(`SELECT `+soundboardColumns+` FROM soundboard_sounds
		WHERE guild_id = ? ORDER BY sound_id`, guildID)
	if err != nil {
		return nil, mapErr(err)
	}
	type row struct {
		sound   SoundboardSound
		creator string
	}
	var found []row
	for rows.Next() {
		sd, creator, err := scanSoundboard(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		found = append(found, row{sound: sd, creator: creator})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, mapErr(err)
	}
	rows.Close()
	out := []SoundboardSound{}
	for _, r := range found {
		if r.creator != "" {
			if u, err := s.UserByID(r.creator); err == nil {
				r.sound.User = &u
			}
		}
		out = append(out, r.sound)
	}
	return out, nil
}

// GuildSoundboardSound loads one guild soundboard sound.
func (s *Store) GuildSoundboardSound(guildID, soundID string) (SoundboardSound, error) {
	row := s.DB.QueryRow(`SELECT `+soundboardColumns+` FROM soundboard_sounds
		WHERE guild_id = ? AND sound_id = ?`, guildID, soundID)
	sd, creator, err := scanSoundboard(row)
	if err != nil {
		return SoundboardSound{}, err
	}
	if creator != "" {
		if u, err := s.UserByID(creator); err == nil {
			sd.User = &u
		}
	}
	return sd, nil
}

// CreateSoundboardParams holds the documented create fields.
type CreateSoundboardParams struct {
	Name      string
	Volume    *float64
	EmojiID   *string
	EmojiName *string
}

// CreateGuildSoundboardSound inserts a guild soundboard sound.
func (s *Store) CreateGuildSoundboardSound(guildID, creatorID string, p CreateSoundboardParams) (SoundboardSound, error) {
	idv := s.NewID()
	volume := 1.0
	if p.Volume != nil {
		volume = *p.Volume
	}
	_, err := s.DB.Exec(`INSERT INTO soundboard_sounds
		(sound_id, guild_id, name, volume, emoji_id, emoji_name, available, creator_id, created_at)
		VALUES (?,?,?,?,?,?,1,?,?)`, idv, guildID, p.Name, volume, p.EmojiID, p.EmojiName, creatorID, now())
	if err != nil {
		return SoundboardSound{}, mapErr(err)
	}
	return s.GuildSoundboardSound(guildID, idv)
}

// UpdateSoundboardParams holds nullable modify fields.
type UpdateSoundboardParams struct {
	Name      *string
	Volume    *float64
	EmojiID   *string
	EmojiName *string
}

// UpdateGuildSoundboardSound applies a patch.
func (s *Store) UpdateGuildSoundboardSound(guildID, soundID string, p UpdateSoundboardParams) (SoundboardSound, error) {
	set := []string{}
	args := []any{}
	if p.Name != nil {
		set = append(set, "name = ?")
		args = append(args, *p.Name)
	}
	if p.Volume != nil {
		set = append(set, "volume = ?")
		args = append(args, *p.Volume)
	}
	if p.EmojiID != nil {
		set = append(set, "emoji_id = ?")
		args = append(args, *p.EmojiID)
	}
	if p.EmojiName != nil {
		set = append(set, "emoji_name = ?")
		args = append(args, *p.EmojiName)
	}
	if len(set) == 0 {
		return s.GuildSoundboardSound(guildID, soundID)
	}
	args = append(args, guildID, soundID)
	res, err := s.DB.Exec(`UPDATE soundboard_sounds SET `+joinComma(set)+` WHERE guild_id = ? AND sound_id = ?`, args...)
	if err != nil {
		return SoundboardSound{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return SoundboardSound{}, ErrNotFound
	}
	return s.GuildSoundboardSound(guildID, soundID)
}

// DeleteGuildSoundboardSound removes a guild soundboard sound.
func (s *Store) DeleteGuildSoundboardSound(guildID, soundID string) error {
	res, err := s.DB.Exec(`DELETE FROM soundboard_sounds WHERE guild_id = ? AND sound_id = ?`, guildID, soundID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
