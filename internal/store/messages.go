package store

import (
	"database/sql"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/wavedevgit/waveycord/internal/id"
	"github.com/wavedevgit/waveycord/internal/model"
)

const messageColumns = `id, channel_id, guild_id, author_id, content, timestamp, edited_timestamp,
	tts, mention_everyone, mentions, mention_roles, pinned, type, flags,
	referenced_message_id, webhook_id, application_id, components, attachments, embeds, sticker_items, nonce`

var (
	userMentionRe = regexp.MustCompile(`<@!?([0-9]{17,20})>`)
	roleMentionRe = regexp.MustCompile(`<@&([0-9]{17,20})>`)
)

// rawMessage is the row projection before users are resolved.
type rawMessage struct {
	ID           string
	ChannelID    string
	GuildID      sql.NullString
	AuthorID     string
	Content      string
	Timestamp    int64
	Edited       sql.NullInt64
	TTS          bool
	MentionAll   bool
	Mentions     []string
	MentionRoles []string
	Pinned       bool
	Type         int
	Flags        int
	Ref          sql.NullString
	Webhook      sql.NullString
	App          sql.NullString
	Components   string
	Attachments  string
	Embeds       string
	Stickers     string
	Nonce        sql.NullString
}

func scanRawMessage(row rowScanner) (rawMessage, error) {
	var r rawMessage
	var mentions, roles string
	err := row.Scan(&r.ID, &r.ChannelID, &r.GuildID, &r.AuthorID, &r.Content, &r.Timestamp,
		&r.Edited, &r.TTS, &r.MentionAll, &mentions, &roles, &r.Pinned, &r.Type, &r.Flags,
		&r.Ref, &r.Webhook, &r.App, &r.Components, &r.Attachments, &r.Embeds, &r.Stickers, &r.Nonce)
	if err != nil {
		return r, err
	}
	r.Mentions = parseJSONStrings(mentions)
	r.MentionRoles = parseJSONStrings(roles)
	return r, nil
}

// hydrate turns a row into the API object. depth bounds referenced-message
// expansion so a reply chain cannot recurse without limit.
func (s *Store) hydrate(r rawMessage, depth int) model.Message {
	m := model.Message{
		ID:              r.ID,
		ChannelID:       r.ChannelID,
		Content:         r.Content,
		Timestamp:       iso(r.Timestamp),
		EditedTimestamp: isoPtr(r.Edited),
		TTS:             r.TTS,
		MentionEveryone: r.MentionAll,
		MentionRoles:    r.MentionRoles,
		Pinned:          r.Pinned,
		Type:            r.Type,
		Flags:           r.Flags,
		Attachments:     []model.Attachment{},
		Embeds:          []model.Embed{},
		Components:      []any{},
		Mentions:        []model.User{},
		StickerItems:    []model.StickerItem{},
		Nonce:           ns(r.Nonce),
		WebhookID:       ns(r.Webhook),
		ApplicationID:   ns(r.App),
		GuildID:         ns(r.GuildID),
	}
	_ = json.Unmarshal([]byte(r.Attachments), &m.Attachments)
	_ = json.Unmarshal([]byte(r.Embeds), &m.Embeds)
	_ = json.Unmarshal([]byte(r.Components), &m.Components)
	_ = json.Unmarshal([]byte(r.Stickers), &m.StickerItems)
	if m.Attachments == nil {
		m.Attachments = []model.Attachment{}
	}
	if m.Embeds == nil {
		m.Embeds = []model.Embed{}
	}
	if m.Components == nil {
		m.Components = []any{}
	}
	if u, err := s.UserByID(r.AuthorID); err == nil {
		m.Author = u
	} else {
		m.Author = model.User{ID: r.AuthorID, Username: "deleted-user", Discriminator: "0"}
	}
	for _, uid := range r.Mentions {
		if u, err := s.UserByID(uid); err == nil {
			m.Mentions = append(m.Mentions, u)
		}
	}
	if r.GuildID.Valid {
		if member, err := s.Member(r.GuildID.String, r.AuthorID); err == nil {
			m.Member = &member
		}
	}
	if r.Ref.Valid && depth > 0 {
		if parent, err := scanRawMessage(s.DB.QueryRow(`SELECT `+messageColumns+` FROM messages WHERE id = ?`, r.Ref.String)); err == nil {
			pm := s.hydrate(parent, depth-1)
			pm.Reactions, _ = s.MessageReactions(pm.ID)
			m.ReferencedMessage = &pm
		}
		if m.ReferencedMessage == nil {
			m.ReferencedMessage = &model.Message{
				ID: r.Ref.String, ChannelID: r.ChannelID, Timestamp: m.Timestamp,
				Author:      model.User{ID: "0", Username: "Deleted User", Discriminator: "0"},
				Attachments: []model.Attachment{}, Embeds: []model.Embed{},
				Components: []any{}, Mentions: []model.User{},
			}
		}
	}
	return m
}

// CreateMessageParams is the documented message create payload after validation.
type CreateMessageParams struct {
	ChannelID           string
	GuildID             string
	AuthorID            string
	Content             string
	TTS                 bool
	Embeds              []model.Embed
	Attachments         []model.Attachment
	Components          []any
	Nonce               *string
	Type                int
	ReferencedMessageID *string
	Flags               int
	Pinned              bool
}

// CreateMessage persists a message and advances the channel's last_message_id.
func (s *Store) CreateMessage(p CreateMessageParams) (model.Message, error) {
	mentionEveryone := strings.Contains(p.Content, "@everyone") || strings.Contains(p.Content, "@here")
	mentionIDs := []string{}
	seen := map[string]bool{}
	for _, m := range userMentionRe.FindAllStringSubmatch(p.Content, -1) {
		if seen[m[1]] {
			continue
		}
		if _, err := s.UserByID(m[1]); err == nil {
			seen[m[1]] = true
			mentionIDs = append(mentionIDs, m[1])
		}
	}
	mentionRoles := []string{}
	for _, m := range roleMentionRe.FindAllStringSubmatch(p.Content, -1) {
		mentionRoles = append(mentionRoles, m[1])
	}
	if p.ReferencedMessageID != nil && !id.Valid(*p.ReferencedMessageID) {
		p.ReferencedMessageID = nil
	}
	atts, _ := json.Marshal(p.Attachments)
	embeds, _ := json.Marshal(p.Embeds)
	comps, _ := json.Marshal(p.Components)
	if p.Components == nil {
		comps = []byte("[]")
	}
	mid := s.NewID()
	var guild any
	if p.GuildID != "" {
		guild = p.GuildID
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return model.Message{}, mapErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO messages
		(id, channel_id, guild_id, author_id, content, timestamp, tts, mention_everyone,
		 mentions, mention_roles, pinned, type, flags, referenced_message_id, components,
		 attachments, embeds, sticker_items, nonce)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		mid, p.ChannelID, guild, p.AuthorID, p.Content, now(), p.TTS, mentionEveryone,
		jsonStrings(mentionIDs), jsonStrings(mentionRoles), p.Pinned, p.Type, p.Flags,
		p.ReferencedMessageID, string(comps), string(atts), string(embeds), "[]", p.Nonce); err != nil {
		return model.Message{}, mapErr(err)
	}
	if _, err := tx.Exec(`UPDATE channels SET last_message_id = ?, message_count = message_count + 1 WHERE id = ?`, mid, p.ChannelID); err != nil {
		return model.Message{}, mapErr(err)
	}
	if p.Pinned {
		if _, err := tx.Exec(`INSERT INTO pins (channel_id, message_id, pinned_at) VALUES (?,?,?)`, p.ChannelID, mid, now()); err != nil {
			return model.Message{}, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return model.Message{}, mapErr(err)
	}
	return s.Message(mid)
}

// Message loads one message with reactions attached.
func (s *Store) Message(idv string) (model.Message, error) {
	raw, err := scanRawMessage(s.read().QueryRow(`SELECT `+messageColumns+` FROM messages WHERE id = ?`, idv))
	if err != nil {
		return model.Message{}, mapErr(err)
	}
	m := s.hydrate(raw, 1)
	m.Reactions, _ = s.MessageReactions(m.ID)
	return m, nil
}

// MessageChannel returns the owning channel id, for authorization.
func (s *Store) MessageChannel(messageID string) (string, error) {
	var ch string
	err := s.DB.QueryRow(`SELECT channel_id FROM messages WHERE id = ?`, messageID).Scan(&ch)
	return ch, mapErr(err)
}

// MessagesPage implements before/after/around with newest-first output.
func (s *Store) MessagesPage(channelID, before, after, around string, limit int) ([]model.Message, error) {
	limit = clampLimit(limit)
	// Rows must be fully read and closed before hydration: hydration issues its
	// own queries, and the store runs on a single writer connection.
	query := func(q string, args ...any) ([]rawMessage, error) {
		rows, err := s.read().Query(q, args...)
		if err != nil {
			return nil, mapErr(err)
		}
		defer rows.Close()
		out := []rawMessage{}
		for rows.Next() {
			raw, err := scanRawMessage(rows)
			if err != nil {
				return nil, mapErr(err)
			}
			out = append(out, raw)
		}
		return out, mapErr(rows.Err())
	}
	hydrateAll := func(raws []rawMessage) []model.Message {
		out := make([]model.Message, 0, len(raws))
		for _, raw := range raws {
			m := s.hydrate(raw, 1)
			m.Reactions, _ = s.MessageReactions(m.ID)
			out = append(out, m)
		}
		return out
	}
	base := `SELECT ` + messageColumns + ` FROM messages WHERE channel_id = ?`

	if around != "" {
		half := limit / 2
		newer, err := query(base+` AND id >= ? ORDER BY id ASC LIMIT ?`, channelID, around, half+1)
		if err != nil {
			return nil, err
		}
		older, err := query(base+` AND id < ? ORDER BY id DESC LIMIT ?`, channelID, around, half)
		if err != nil {
			return nil, err
		}
		raws := append(newer, older...)
		sort.Slice(raws, func(i, j int) bool { return raws[i].ID > raws[j].ID })
		if len(raws) > limit {
			raws = raws[:limit]
		}
		return hydrateAll(raws), nil
	}

	if after != "" {
		raws, err := query(base+` AND id > ? ORDER BY id ASC LIMIT ?`, channelID, after, limit)
		if err != nil {
			return nil, err
		}
		for i, j := 0, len(raws)-1; i < j; i, j = i+1, j-1 {
			raws[i], raws[j] = raws[j], raws[i]
		}
		return hydrateAll(raws), nil
	}

	q := base
	args := []any{channelID}
	if before != "" {
		q += ` AND id < ?`
		args = append(args, before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	raws, err := query(q, args...)
	if err != nil {
		return nil, err
	}
	return hydrateAll(raws), nil
}

// UpdateMessage applies a content/embed patch.
func (s *Store) UpdateMessage(idv string, content *string, embeds *[]model.Embed, flags *int, components *[]any) (model.Message, error) {
	set := []string{"edited_timestamp = ?"}
	args := []any{now()}
	if content != nil {
		set = append(set, "content = ?")
		args = append(args, *content)
	}
	if embeds != nil {
		raw, _ := json.Marshal(*embeds)
		set = append(set, "embeds = ?")
		args = append(args, string(raw))
	}
	if flags != nil {
		set = append(set, "flags = ?")
		args = append(args, *flags)
	}
	if components != nil {
		raw, _ := json.Marshal(*components)
		set = append(set, "components = ?")
		args = append(args, string(raw))
	}
	args = append(args, idv)
	res, err := s.DB.Exec(`UPDATE messages SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return model.Message{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.Message{}, ErrNotFound
	}
	return s.Message(idv)
}

// DeleteMessage removes one message and rewinds last_message_id if needed.
func (s *Store) DeleteMessage(idv string) (string, error) {
	var channelID string
	if err := s.DB.QueryRow(`SELECT channel_id FROM messages WHERE id = ?`, idv).Scan(&channelID); err != nil {
		return "", mapErr(err)
	}
	if _, err := s.DB.Exec(`DELETE FROM messages WHERE id = ?`, idv); err != nil {
		return "", mapErr(err)
	}
	s.refreshLastMessage(channelID)
	return channelID, nil
}

// DeleteMessages bulk deletes and refreshes the channel pointer.
func (s *Store) DeleteMessages(channelID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, channelID)
	for _, v := range ids {
		args = append(args, v)
	}
	if _, err := s.DB.Exec(`DELETE FROM messages WHERE channel_id = ? AND id IN (`+placeholders+`)`, args...); err != nil {
		return mapErr(err)
	}
	s.refreshLastMessage(channelID)
	return nil
}

func (s *Store) refreshLastMessage(channelID string) {
	var last sql.NullString
	_ = s.DB.QueryRow(`SELECT id FROM messages WHERE channel_id = ? ORDER BY id DESC LIMIT 1`, channelID).Scan(&last)
	var v any
	if last.Valid {
		v = last.String
	}
	_, _ = s.DB.Exec(`UPDATE channels SET last_message_id = ? WHERE id = ?`, v, channelID)
}

// --- reactions ---

// ReactionKey normalises an emoji into the storage key.
func ReactionKey(emoji model.Emoji) string {
	if emoji.ID != nil && *emoji.ID != "" {
		return *emoji.ID
	}
	if emoji.Name != nil {
		return *emoji.Name
	}
	return ""
}

// AddReaction stores one reaction.
func (s *Store) AddReaction(messageID, channelID, userID string, emoji model.Emoji) error {
	name := ""
	if emoji.Name != nil {
		name = *emoji.Name
	}
	_, err := s.DB.Exec(`INSERT INTO message_reactions
		(message_id, channel_id, user_id, emoji_key, emoji_name, emoji_id, animated, created_at)
		VALUES (?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
		messageID, channelID, userID, ReactionKey(emoji), name, emoji.ID, emoji.Animated, now())
	return mapErr(err)
}

// RemoveReaction removes one user's reaction.
func (s *Store) RemoveReaction(messageID, userID, key string) error {
	res, err := s.DB.Exec(`DELETE FROM message_reactions WHERE message_id = ? AND user_id = ? AND emoji_key = ?`, messageID, userID, key)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RemoveEmojiReactions clears one emoji from a message.
func (s *Store) RemoveEmojiReactions(messageID, key string) error {
	_, err := s.DB.Exec(`DELETE FROM message_reactions WHERE message_id = ? AND emoji_key = ?`, messageID, key)
	return mapErr(err)
}

// RemoveAllReactions clears every reaction on a message.
func (s *Store) RemoveAllReactions(messageID string) error {
	_, err := s.DB.Exec(`DELETE FROM message_reactions WHERE message_id = ?`, messageID)
	return mapErr(err)
}

// MessageReactions aggregates reactions for a message.
func (s *Store) MessageReactions(messageID string) ([]model.Reaction, error) {
	rows, err := s.DB.Query(`SELECT emoji_key, emoji_name, emoji_id, animated, COUNT(*),
		MAX(CASE WHEN user_id = '' THEN 1 ELSE 0 END)
		FROM message_reactions WHERE message_id = ?
		GROUP BY emoji_key, emoji_name, emoji_id, animated ORDER BY MIN(created_at) ASC`, messageID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.Reaction{}
	for rows.Next() {
		var (
			key, name string
			emojiID   sql.NullString
			animated  int
			count     int
			me        int
		)
		if err := rows.Scan(&key, &name, &emojiID, &animated, &count, &me); err != nil {
			return nil, mapErr(err)
		}
		r := model.Reaction{Count: count}
		r.CountDetails.Normal = count
		r.BurstColors = []string{}
		e := model.Emoji{Animated: animated != 0}
		if emojiID.Valid {
			v := emojiID.String
			e.ID = &v
		}
		if name != "" {
			v := name
			e.Name = &v
		}
		r.Emoji = e
		out = append(out, r)
	}
	return out, mapErr(rows.Err())
}

// ReactionUsers lists the users who reacted with an emoji.
func (s *Store) ReactionUsers(messageID, key string, limit int) ([]model.User, error) {
	limit = clampLimit(limit)
	rows, err := s.DB.Query(`SELECT user_id FROM message_reactions
		WHERE message_id = ? AND emoji_key = ? ORDER BY created_at ASC LIMIT ?`, messageID, key, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	var ids []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		ids = append(ids, uid)
	}
	rows.Close()
	out := []model.User{}
	for _, uid := range ids {
		if u, err := s.UserByID(uid); err == nil {
			out = append(out, u)
		}
	}
	return out, nil
}

// GuildMessageCount is used by guild preview.
func (s *Store) GuildMessageCount(guildID string) (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE guild_id = ?`, guildID).Scan(&n)
	return n, mapErr(err)
}
