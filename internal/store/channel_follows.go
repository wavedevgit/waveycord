package store

// FollowedChannel is the documented followed channel object.
type FollowedChannel struct {
	ChannelID string `json:"channel_id"`
	WebhookID string `json:"webhook_id"`
}

// CreateChannelFollow records that a target channel follows an announcement
// channel through a webhook.
func (s *Store) CreateChannelFollow(sourceChannelID, targetChannelID, webhookID string) (FollowedChannel, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return FollowedChannel{}, mapErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO channel_follows (source_channel_id, target_channel_id, webhook_id)
		VALUES (?,?,?)
		ON CONFLICT(source_channel_id, target_channel_id)
		DO UPDATE SET webhook_id = excluded.webhook_id`, sourceChannelID, targetChannelID, webhookID); err != nil {
		return FollowedChannel{}, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return FollowedChannel{}, mapErr(err)
	}
	return FollowedChannel{ChannelID: sourceChannelID, WebhookID: webhookID}, nil
}

// ChannelFollow returns the target channel following a source channel, if any.
func (s *Store) ChannelFollow(sourceChannelID string) (FollowedChannel, error) {
	var fc FollowedChannel
	err := s.DB.QueryRow(`SELECT source_channel_id, webhook_id FROM channel_follows
		WHERE source_channel_id = ?`, sourceChannelID).Scan(&fc.ChannelID, &fc.WebhookID)
	return fc, mapErr(err)
}
