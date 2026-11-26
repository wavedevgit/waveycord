package store

// MLSCredential is a member's published MLS key material. The directory persists it
// server-side so the gateway can resolve signing keys across instances and restarts
// instead of trusting whatever arrived on the wire last.
type MLSCredential struct {
	UserID     string
	MediaKey   string
	SigningKey string
}

// SetMLSCredential stores or replaces one member's credentials.
func (s *Store) SetMLSCredential(userID, mediaKey, signingKey string) error {
	if userID == "" {
		return ErrNotFound
	}
	_, err := s.DB.Exec(`INSERT INTO mls_credentials (user_id, media_key, signing_key, updated_at)
		VALUES (?,?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET media_key = excluded.media_key,
			signing_key = excluded.signing_key, updated_at = excluded.updated_at`,
		userID, mediaKey, signingKey, now())
	return mapErr(err)
}

// MLSCredential returns one member's credentials.
func (s *Store) MLSCredential(userID string) (MLSCredential, error) {
	c := MLSCredential{UserID: userID}
	err := s.DB.QueryRow(`SELECT media_key, signing_key FROM mls_credentials WHERE user_id = ?`, userID).
		Scan(&c.MediaKey, &c.SigningKey)
	return c, mapErr(err)
}

// MLSCredentials returns the whole directory as user id -> signing key.
func (s *Store) MLSCredentials() (map[string]string, error) {
	rows, err := s.DB.Query(`SELECT user_id, signing_key FROM mls_credentials`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var uid, key string
		if err := rows.Scan(&uid, &key); err != nil {
			return nil, mapErr(err)
		}
		out[uid] = key
	}
	return out, mapErr(rows.Err())
}

// ChannelVoiceUserIDs lists users currently in a voice channel, from the shared
// voice-state table rather than one instance's connections.
func (s *Store) ChannelVoiceUserIDs(channelID string) (map[string]bool, error) {
	rows, err := s.DB.Query(`SELECT user_id FROM voice_states WHERE channel_id = ?`, channelID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, mapErr(err)
		}
		out[uid] = true
	}
	return out, mapErr(rows.Err())
}
