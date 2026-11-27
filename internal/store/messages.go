package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/wavedevgit/waveycord/internal/id"
	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/search"
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
func (s *Store) hydrate(r rawMessage, depth int, viewer string) model.Message {
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
	if r.Type == 32 {
		if poll, err := s.PollFor(r.ID, viewer); err == nil {
			m.Poll = &poll
		}
	}
	if r.Ref.Valid && depth > 0 {
		if parent, err := scanRawMessage(s.DB.QueryRow(`SELECT `+messageColumns+` FROM messages WHERE id = ?`, r.Ref.String)); err == nil {
			pm := s.hydrate(parent, depth-1, viewer)
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
	WebhookID           string
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
	ts := now()
	bucket := ts / bucketMillisPerDay
	var guild any
	if p.GuildID != "" {
		guild = p.GuildID
	}
	var webhook any
	if p.WebhookID != "" {
		webhook = p.WebhookID
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return model.Message{}, mapErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO messages
		(id, channel_id, guild_id, author_id, content, timestamp, tts, mention_everyone,
		 mentions, mention_roles, pinned, type, flags, referenced_message_id, components,
		 attachments, embeds, sticker_items, nonce, bucket, webhook_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		mid, p.ChannelID, guild, p.AuthorID, p.Content, ts, p.TTS, mentionEveryone,
		jsonStrings(mentionIDs), jsonStrings(mentionRoles), p.Pinned, p.Type, p.Flags,
		p.ReferencedMessageID, string(comps), string(atts), string(embeds), "[]", p.Nonce, bucket, webhook); err != nil {
		return model.Message{}, mapErr(err)
	}
	if _, err := tx.Exec(`INSERT INTO message_index (id, channel_id, bucket, created_at)
		VALUES (?,?,?,?)`, mid, p.ChannelID, bucket, ts); err != nil {
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
	queued := s.Search != nil && s.DurableSearchQueue() && p.GuildID != ""
	if queued {
		if err := s.enqueueSearchTx(tx, SearchJob{Op: SearchOpIndex, MessageID: mid,
			GuildID: p.GuildID, ChannelID: p.ChannelID, AuthorID: p.AuthorID, Content: p.Content, CreatedAt: ts}); err != nil {
			return model.Message{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.Message{}, mapErr(err)
	}
	if !queued {
		s.indexMessage(mid, p.GuildID, p.ChannelID, p.AuthorID, p.Content, ts)
	}
	s.noteGuildWrite(p.GuildID)
	return s.Message(mid)
}

// indexMessage writes the search document if an index is configured. Indexing is
// best-effort relative to the write: the message row is authoritative.
func (s *Store) indexMessage(messageID, guildID, channelID, authorID, content string, createdAt int64) {
	if s.Search == nil || guildID == "" {
		return
	}
	_ = s.Search.Index(context.Background(), search.Doc{
		MessageID: messageID, GuildID: guildID, ChannelID: channelID,
		AuthorID: authorID, Content: content, CreatedAt: createdAt,
	})
}

// Message loads one message with reactions attached.
func (s *Store) Message(idv string) (model.Message, error) {
	return s.MessageFor(idv, "")
}

// MessageFor loads a message with poll results relative to one viewer.
func (s *Store) MessageFor(idv, viewer string) (model.Message, error) {
	var raw rawMessage
	var err error
	// The index makes an id-only read a single-partition lookup. Rows written
	// before the index existed fall back to the partitioned scan.
	var channelID string
	if idxErr := s.DB.QueryRow(`SELECT channel_id FROM message_index WHERE id = ?`, idv).Scan(&channelID); idxErr == nil {
		raw, err = scanRawMessage(s.read().QueryRow(
			`SELECT `+messageColumns+` FROM messages WHERE channel_id = ? AND id = ?`, channelID, idv))
		if errors.Is(err, sql.ErrNoRows) && s.ReadDB != nil {
			// Replica lag: the row exists on the primary.
			raw, err = scanRawMessage(s.DB.QueryRow(
				`SELECT `+messageColumns+` FROM messages WHERE channel_id = ? AND id = ?`, channelID, idv))
		}
	} else {
		raw, err = scanRawMessage(s.read().QueryRow(`SELECT `+messageColumns+` FROM messages WHERE id = ?`, idv))
	}
	if err != nil {
		return model.Message{}, mapErr(err)
	}
	m := s.hydrate(raw, 1, viewer)
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
	return s.MessagesPageFor(channelID, before, after, around, limit, "")
}

// MessagesPageFor is MessagesPage with poll results relative to one viewer.
func (s *Store) MessagesPageFor(channelID, before, after, around string, limit int, viewer string) ([]model.Message, error) {
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
			m := s.hydrate(raw, 1, viewer)
			m.Reactions, _ = s.MessageReactions(m.ID)
			out = append(out, m)
		}
		return out
	}
	base := `SELECT ` + messageColumns + ` FROM messages WHERE channel_id = ?`

	if around != "" {
		half := limit / 2
		lo, hi, haveBucket := bucketWindow(around)
		bucketClause := ""
		var bucketArgs []any
		if haveBucket {
			bucketClause = ` AND bucket BETWEEN ? AND ?`
			bucketArgs = []any{lo, hi}
		}
		newer, err := query(base+bucketClause+` AND id >= ? ORDER BY id ASC LIMIT ?`,
			append(append([]any{channelID}, bucketArgs...), around, half+1)...)
		if err != nil {
			return nil, err
		}
		older, err := query(base+bucketClause+` AND id < ? ORDER BY id DESC LIMIT ?`,
			append(append([]any{channelID}, bucketArgs...), around, half)...)
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
		lo, _, haveBucket := bucketWindow(after)
		q := base + ` AND id > ? ORDER BY id ASC LIMIT ?`
		args := []any{channelID, after, limit}
		if haveBucket {
			q = base + ` AND bucket >= ? AND id > ? ORDER BY id ASC LIMIT ?`
			args = []any{channelID, lo, after, limit}
		}
		raws, err := query(q, args...)
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
		_, hi, haveBucket := bucketWindow(before)
		if haveBucket {
			q += ` AND bucket <= ?`
			args = append(args, hi)
		}
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
	msg, err := s.updateMessage(idv, content, embeds, flags, components)
	if err != nil {
		return msg, err
	}
	if s.Search != nil && !s.DurableSearchQueue() {
		if raw, rerr := scanRawMessage(s.DB.QueryRow(`SELECT `+messageColumns+` FROM messages WHERE id = ?`, idv)); rerr == nil {
			guildID := ""
			if raw.GuildID.Valid {
				guildID = raw.GuildID.String
			}
			s.indexMessage(raw.ID, guildID, raw.ChannelID, raw.AuthorID, raw.Content, raw.Timestamp)
		}
	}
	return msg, nil
}

func (s *Store) updateMessage(idv string, content *string, embeds *[]model.Embed, flags *int, components *[]any) (model.Message, error) {
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
	tx, err := s.DB.Begin()
	if err != nil {
		return model.Message{}, mapErr(err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE messages SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return model.Message{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.Message{}, ErrNotFound
	}
	if s.Search != nil && s.DurableSearchQueue() {
		raw, err := scanRawMessage(tx.QueryRow(`SELECT `+messageColumns+` FROM messages WHERE id = ?`, idv))
		if err != nil {
			return model.Message{}, mapErr(err)
		}
		if raw.GuildID.Valid {
			if err := s.enqueueSearchTx(tx, SearchJob{Op: SearchOpIndex, MessageID: raw.ID,
				GuildID: raw.GuildID.String, ChannelID: raw.ChannelID, AuthorID: raw.AuthorID, Content: raw.Content, CreatedAt: raw.Timestamp}); err != nil {
				return model.Message{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return model.Message{}, mapErr(err)
	}
	return s.Message(idv)
}

// DeleteMessage removes one message and rewinds last_message_id if needed.
func (s *Store) DeleteMessage(idv string) (string, error) {
	var channelID string
	if err := s.DB.QueryRow(`SELECT channel_id FROM message_index WHERE id = ?`, idv).Scan(&channelID); err != nil {
		if err := s.DB.QueryRow(`SELECT channel_id FROM messages WHERE id = ?`, idv).Scan(&channelID); err != nil {
			return "", mapErr(err)
		}
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return "", mapErr(err)
	}
	defer tx.Rollback()
	// Reactions and pins reference the message by id, which is not unique across
	// partitions, so the cascade is explicit.
	for _, q := range []string{
		`DELETE FROM message_reactions WHERE channel_id = ? AND message_id = ?`,
		`DELETE FROM pins WHERE channel_id = ? AND message_id = ?`,
		`DELETE FROM polls WHERE message_id = ?`,
		`DELETE FROM message_index WHERE id = ?`,
		`DELETE FROM messages WHERE channel_id = ? AND id = ?`,
	} {
		var err error
		if q == `DELETE FROM message_index WHERE id = ?` || q == `DELETE FROM polls WHERE message_id = ?` {
			_, err = tx.Exec(q, idv)
		} else {
			_, err = tx.Exec(q, channelID, idv)
		}
		if err != nil {
			return "", mapErr(err)
		}
	}
	queued := s.Search != nil && s.DurableSearchQueue()
	if queued {
		if err := s.enqueueSearchTx(tx, SearchJob{Op: SearchOpDelete, MessageID: idv}); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", mapErr(err)
	}
	if s.Search != nil && !queued {
		_ = s.Search.Delete(context.Background(), idv)
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
	tx, err := s.DB.Begin()
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM messages WHERE channel_id = ? AND id IN (`+placeholders+`)`, args...)
	if err != nil {
		return mapErr(err)
	}
	var deletedIDs []string
	for rows.Next() {
		var messageID string
		if err := rows.Scan(&messageID); err != nil {
			rows.Close()
			return mapErr(err)
		}
		deletedIDs = append(deletedIDs, messageID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return mapErr(err)
	}
	for _, q := range []string{
		`DELETE FROM message_reactions WHERE channel_id = ? AND message_id IN (` + placeholders + `)`,
		`DELETE FROM pins WHERE channel_id = ? AND message_id IN (` + placeholders + `)`,
		`DELETE FROM message_index WHERE channel_id = ? AND id IN (` + placeholders + `)`,
		`DELETE FROM messages WHERE channel_id = ? AND id IN (` + placeholders + `)`,
	} {
		if _, err := tx.Exec(q, args...); err != nil {
			return mapErr(err)
		}
	}
	queued := s.Search != nil && s.DurableSearchQueue()
	if queued {
		for _, idv := range deletedIDs {
			if err := s.enqueueSearchTx(tx, SearchJob{Op: SearchOpDelete, MessageID: idv}); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return mapErr(err)
	}
	if s.Search != nil && !queued {
		for _, idv := range deletedIDs {
			_ = s.Search.Delete(context.Background(), idv)
		}
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

// bucketMillisPerDay is the message partition window. Kept in sync with
// db.DayBucket; a mismatch only costs pruning, not correctness.
const bucketMillisPerDay = 86400000

// bucketWindow returns the day-bucket bounds an id can fall inside. The window is
// widened by one day on each side so clock skew at a boundary never hides a row.
func bucketWindow(snowflake string) (int64, int64, bool) {
	ts, err := id.Timestamp(snowflake)
	if err != nil {
		return 0, 0, false
	}
	b := ts.UnixMilli() / bucketMillisPerDay
	return b - 1, b + 1, true
}
