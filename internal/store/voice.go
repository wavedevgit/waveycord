package store

import (
	"database/sql"

	"github.com/wavedevgit/waveycord/internal/model"
)

// VoiceState is the persisted signaling state for one user in one guild.
type VoiceState struct {
	GuildID                 string        `json:"guild_id"`
	ChannelID               *string       `json:"channel_id"`
	UserID                  string        `json:"user_id"`
	Member                  *model.Member `json:"member,omitempty"`
	SessionID               string        `json:"session_id"`
	Deaf                    bool          `json:"deaf"`
	Mute                    bool          `json:"mute"`
	SelfDeaf                bool          `json:"self_deaf"`
	SelfMute                bool          `json:"self_mute"`
	SelfStream              bool          `json:"self_stream"`
	SelfVideo               bool          `json:"self_video"`
	Suppress                bool          `json:"suppress"`
	RequestToSpeakTimestamp *string       `json:"request_to_speak_timestamp"`
	Status                  string        `json:"-"`
	Token                   string        `json:"-"`
}

const voiceColumns = `guild_id, channel_id, user_id, session_id, deaf, mute, self_deaf,
	self_mute, self_stream, self_video, suppress, request_to_speak_timestamp, status, voice_token`

func scanVoiceState(row rowScanner) (VoiceState, error) {
	var (
		v         VoiceState
		channel   string
		requested sql.NullString
	)
	err := row.Scan(&v.GuildID, &channel, &v.UserID, &v.SessionID, &v.Deaf, &v.Mute,
		&v.SelfDeaf, &v.SelfMute, &v.SelfStream, &v.SelfVideo, &v.Suppress, &requested,
		&v.Status, &v.Token)
	v.ChannelID = &channel
	v.RequestToSpeakTimestamp = ns(requested)
	return v, err
}

// VoiceState returns the user's state in a guild.
func (s *Store) VoiceState(guildID, userID string) (VoiceState, error) {
	v, err := scanVoiceState(s.DB.QueryRow(`SELECT `+voiceColumns+` FROM voice_states
		WHERE guild_id = ? AND user_id = ?`, guildID, userID))
	if err != nil {
		return v, mapErr(err)
	}
	if member, err := s.Member(guildID, userID); err == nil {
		v.Member = &member
	}
	return v, nil
}

// VoiceStatesForGuild lists every connected voice state.
func (s *Store) VoiceStatesForGuild(guildID string) ([]VoiceState, error) {
	rows, err := s.DB.Query(`SELECT `+voiceColumns+` FROM voice_states
		WHERE guild_id = ? ORDER BY user_id ASC`, guildID)
	if err != nil {
		return nil, mapErr(err)
	}
	var raw []VoiceState
	for rows.Next() {
		v, err := scanVoiceState(rows)
		if err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		raw = append(raw, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	for i := range raw {
		if member, err := s.Member(guildID, raw[i].UserID); err == nil {
			raw[i].Member = &member
		}
	}
	return raw, nil
}

// VoiceStatesForChannel lists the states in one channel.
func (s *Store) VoiceStatesForChannel(channelID string) ([]VoiceState, error) {
	rows, err := s.DB.Query(`SELECT `+voiceColumns+` FROM voice_states
		WHERE channel_id = ? ORDER BY user_id ASC`, channelID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []VoiceState{}
	for rows.Next() {
		v, err := scanVoiceState(rows)
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, v)
	}
	return out, mapErr(rows.Err())
}

// PutVoiceState upserts a voice state, preserving any existing status text.
func (s *Store) PutVoiceState(v VoiceState, token string) (VoiceState, error) {
	_, err := s.DB.Exec(`INSERT INTO voice_states
		(guild_id, channel_id, user_id, session_id, deaf, mute, self_deaf, self_mute,
		 self_stream, self_video, suppress, request_to_speak_timestamp, voice_token, status, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,'',?)
		ON CONFLICT(guild_id, user_id) DO UPDATE SET
			channel_id = excluded.channel_id,
			session_id = excluded.session_id,
			deaf = excluded.deaf, mute = excluded.mute,
			self_deaf = excluded.self_deaf, self_mute = excluded.self_mute,
			self_stream = excluded.self_stream, self_video = excluded.self_video,
			suppress = excluded.suppress,
			request_to_speak_timestamp = excluded.request_to_speak_timestamp,
			voice_token = excluded.voice_token,
			updated_at = excluded.updated_at`,
		v.GuildID, v.ChannelID, v.UserID, v.SessionID, v.Deaf, v.Mute, v.SelfDeaf, v.SelfMute,
		v.SelfStream, v.SelfVideo, v.Suppress, v.RequestToSpeakTimestamp, token, now())
	if err != nil {
		return VoiceState{}, mapErr(err)
	}
	return s.VoiceState(v.GuildID, v.UserID)
}

// DeleteVoiceState removes a state and reports the previous channel.
func (s *Store) DeleteVoiceState(guildID, userID string) (*string, error) {
	var channel string
	err := s.DB.QueryRow(`SELECT channel_id FROM voice_states WHERE guild_id = ? AND user_id = ?`,
		guildID, userID).Scan(&channel)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, mapErr(err)
	}
	if _, err := s.DB.Exec(`DELETE FROM voice_states WHERE guild_id = ? AND user_id = ?`, guildID, userID); err != nil {
		return nil, mapErr(err)
	}
	return &channel, nil
}

// SetVoiceStatus stores the custom voice-channel status for a user.
func (s *Store) SetVoiceStatus(channelID, userID, status string) error {
	res, err := s.DB.Exec(`UPDATE voice_states SET status = ?, updated_at = ?
		WHERE channel_id = ? AND user_id = ?`, status, now(), channelID, userID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// VoiceStatus returns the user's voice status in a channel.
func (s *Store) VoiceStatus(channelID, userID string) (string, error) {
	var status string
	err := s.DB.QueryRow(`SELECT status FROM voice_states WHERE channel_id = ? AND user_id = ?`,
		channelID, userID).Scan(&status)
	return status, mapErr(err)
}

// RemoveGuildVoiceStates clears states for a guild (used when a channel is deleted
// or a user leaves).
func (s *Store) RemoveGuildVoiceStates(guildID string) error {
	_, err := s.DB.Exec(`DELETE FROM voice_states WHERE guild_id = ?`, guildID)
	return mapErr(err)
}

// RemoveChannelVoiceStates clears states pointing at a deleted channel.
func (s *Store) RemoveChannelVoiceStates(channelID string) error {
	_, err := s.DB.Exec(`DELETE FROM voice_states WHERE channel_id = ?`, channelID)
	return mapErr(err)
}

// VoiceStateByToken resolves the state minted by OP 4, used by the voice gateway
// during IDENTIFY.
func (s *Store) VoiceStateByToken(token string) (VoiceState, error) {
	if token == "" {
		return VoiceState{}, ErrNotFound
	}
	v, err := scanVoiceState(s.DB.QueryRow(`SELECT `+voiceColumns+` FROM voice_states WHERE voice_token = ?`, token))
	return v, mapErr(err)
}
