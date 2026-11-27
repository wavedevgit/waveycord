package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/wavedevgit/waveycord/internal/model"
)

// Upload is a staged attachment.
type Upload struct {
	ID          string
	ChannelID   string
	UserID      string
	Filename    string
	ContentType *string
	Size        int64
	Path        string
	Uploaded    bool
	CreatedAt   int64
}

// CreateUpload stages an attachment row.
func (s *Store) CreateUpload(channelID, userID, filename string, contentType *string, size int64) (Upload, error) {
	u := Upload{ID: s.NewID(), ChannelID: channelID, UserID: userID, Filename: filename,
		ContentType: contentType, Size: size, CreatedAt: now()}
	_, err := s.DB.Exec(`INSERT INTO uploads (id, channel_id, user_id, filename, content_type, size, created_at)
		VALUES (?,?,?,?,?,?,?)`, u.ID, u.ChannelID, u.UserID, u.Filename, u.ContentType, u.Size, u.CreatedAt)
	return u, mapErr(err)
}

// Upload looks a staged attachment up.
func (s *Store) Upload(idv string) (Upload, error) {
	var (
		u     Upload
		ctype sql.NullString
		path  sql.NullString
	)
	err := s.DB.QueryRow(`SELECT id, channel_id, user_id, filename, content_type, size, path, uploaded, created_at
		FROM uploads WHERE id = ?`, idv).
		Scan(&u.ID, &u.ChannelID, &u.UserID, &u.Filename, &ctype, &u.Size, &path, &u.Uploaded, &u.CreatedAt)
	u.ContentType = ns(ctype)
	u.Path = stringOr(path, "")
	return u, mapErr(err)
}

// SaveUpload writes the bytes to disk and marks the row complete.
func (s *Store) SaveUpload(idv, dir string, data []byte, contentType string) error {
	u, err := s.Upload(idv)
	if err != nil {
		return err
	}
	if u.Size > 0 && int64(len(data)) > u.Size {
		return ErrConflict
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	path := filepath.Join(dir, u.ID+"_"+sanitizeFilename(u.Filename))
	if err := os.WriteFile(path, data, 0o640); err != nil {
		return err
	}
	_, err = s.DB.Exec(`UPDATE uploads SET path = ?, uploaded = 1, size = ?,
		content_type = COALESCE(NULLIF(?, ''), content_type) WHERE id = ?`,
		path, len(data), contentType, idv)
	return mapErr(err)
}

// UploadPath returns the on-disk path of a completed upload, for the CDN route.
func (s *Store) UploadPath(idv string) (string, error) {
	var path sql.NullString
	err := s.DB.QueryRow(`SELECT path FROM uploads WHERE id = ? AND uploaded = 1`, idv).Scan(&path)
	if err != nil {
		return "", mapErr(err)
	}
	if !path.Valid || path.String == "" {
		return "", ErrNotFound
	}
	return path.String, nil
}

func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	return name
}

// --- webhooks ---

// Webhook is the webhook object.
type Webhook struct {
	ID            string      `json:"id"`
	Type          int         `json:"type"`
	GuildID       *string     `json:"guild_id"`
	ChannelID     *string     `json:"channel_id"`
	User          *model.User `json:"user,omitempty"`
	Name          *string     `json:"name"`
	Avatar        *string     `json:"avatar"`
	Token         string      `json:"token,omitempty"`
	ApplicationID *string     `json:"application_id"`
	SourceGuild   *any        `json:"source_guild,omitempty"`
	SourceChannel *any        `json:"source_channel,omitempty"`
	URL           *string     `json:"url,omitempty"`
}

const webhookColumns = `id, type, guild_id, channel_id, creator_id, name, avatar, token, application_id`

func (s *Store) scanWebhook(row rowScanner, includeToken bool) (Webhook, string, error) {
	var (
		w       Webhook
		guild   sql.NullString
		channel sql.NullString
		creator sql.NullString
		name    sql.NullString
		avatar  sql.NullString
		token   string
		app     sql.NullString
	)
	if err := row.Scan(&w.ID, &w.Type, &guild, &channel, &creator, &name, &avatar, &token, &app); err != nil {
		return w, "", err
	}
	w.GuildID = ns(guild)
	w.ChannelID = ns(channel)
	w.Name = ns(name)
	w.Avatar = ns(avatar)
	w.ApplicationID = ns(app)
	if includeToken {
		w.Token = token
	}
	return w, stringOr(creator, ""), nil
}

// withCreator attaches the creator's user object after the row cursor closes.
func (w *Webhook) withCreator(s *Store, creatorID string) {
	if creatorID == "" {
		return
	}
	if u, err := s.UserByID(creatorID); err == nil {
		w.User = &u
	}
}

// CreateWebhook inserts a webhook owned by a channel.
func (s *Store) CreateWebhook(guildID, channelID, creatorID, name string, avatar *string) (Webhook, error) {
	token, err := randomToken()
	if err != nil {
		return Webhook{}, err
	}
	idv := s.NewID()
	tx, err := s.DB.Begin()
	if err != nil {
		return Webhook{}, mapErr(err)
	}
	defer tx.Rollback()
	// A webhook posts as a bot account whose id is the webhook id, which is what
	// the message author object references.
	if _, err := tx.Exec(`INSERT INTO users (id, username, discriminator, password_hash, bot, system, verified, locale, created_at)
		VALUES (?,?, '0000', '', 1, 1, 1, 'en-US', ?)`, idv, name, now()); err != nil {
		return Webhook{}, mapErr(err)
	}
	if _, err := tx.Exec(`INSERT INTO webhooks
		(id, type, guild_id, channel_id, creator_id, name, avatar, token, created_at)
		VALUES (?,1,?,?,?,?,?,?,?)`,
		idv, nullIfEmpty(guildID), channelID, creatorID, name, avatar, token, now()); err != nil {
		return Webhook{}, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return Webhook{}, mapErr(err)
	}
	return s.Webhook(idv)
}

// Webhook loads one webhook by id.
func (s *Store) Webhook(idv string) (Webhook, error) {
	w, creator, err := s.scanWebhook(s.DB.QueryRow(`SELECT `+webhookColumns+` FROM webhooks WHERE id = ?`, idv), true)
	if err != nil {
		return w, mapErr(err)
	}
	w.withCreator(s, creator)
	return w, nil
}

// WebhookByToken loads a webhook for execution.
func (s *Store) WebhookByToken(idv, token string) (Webhook, error) {
	w, creator, err := s.scanWebhook(s.DB.QueryRow(`SELECT `+webhookColumns+` FROM webhooks WHERE id = ? AND token = ?`, idv, token), true)
	if err != nil {
		return w, mapErr(err)
	}
	w.withCreator(s, creator)
	return w, nil
}

// ChannelWebhooks lists webhooks on a channel.
func (s *Store) ChannelWebhooks(channelID string) ([]Webhook, error) {
	return s.webhooksWhere(`channel_id = ?`, channelID)
}

// GuildWebhooks lists webhooks in a guild.
func (s *Store) GuildWebhooks(guildID string) ([]Webhook, error) {
	return s.webhooksWhere(`guild_id = ?`, guildID)
}

func (s *Store) webhooksWhere(where string, arg string) ([]Webhook, error) {
	rows, err := s.DB.Query(`SELECT `+webhookColumns+` FROM webhooks WHERE `+where+` ORDER BY id ASC`, arg)
	if err != nil {
		return nil, mapErr(err)
	}
	type pending struct {
		w       Webhook
		creator string
	}
	var raw []pending
	for rows.Next() {
		w, creator, err := s.scanWebhook(rows, true)
		if err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		raw = append(raw, pending{w: w, creator: creator})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	out := make([]Webhook, 0, len(raw))
	for _, p := range raw {
		p.w.withCreator(s, p.creator)
		out = append(out, p.w)
	}
	return out, nil
}

// UpdateWebhook patches name/avatar/channel.
func (s *Store) UpdateWebhook(idv string, name *string, avatar *string, channelID *string) (Webhook, error) {
	set := []string{}
	args := []any{}
	if name != nil {
		set = append(set, "name = ?")
		args = append(args, *name)
	}
	if avatar != nil {
		set = append(set, "avatar = ?")
		args = append(args, nullIfEmpty(*avatar))
	}
	if channelID != nil {
		set = append(set, "channel_id = ?")
		args = append(args, *channelID)
	}
	if len(set) == 0 {
		return s.Webhook(idv)
	}
	args = append(args, idv)
	res, err := s.DB.Exec(`UPDATE webhooks SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return Webhook{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Webhook{}, ErrNotFound
	}
	return s.Webhook(idv)
}

// DeleteWebhook removes a webhook.
func (s *Store) DeleteWebhook(idv string) error {
	res, err := s.DB.Exec(`DELETE FROM webhooks WHERE id = ?`, idv)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- audit log ---

// AuditEntry is one audit log entry.
type AuditEntry struct {
	ID         string         `json:"id"`
	ActorID    *string        `json:"actor_id"`
	ActionType int            `json:"action_type"`
	TargetID   *string        `json:"target_id"`
	Changes    []AuditChange  `json:"changes"`
	Reason     *string        `json:"reason"`
	Options    map[string]any `json:"options,omitempty"`
	CreatedAt  string         `json:"-"`
}

// AuditChange is one changed key.
type AuditChange struct {
	Key      string `json:"key"`
	NewValue any    `json:"new_value,omitempty"`
	OldValue any    `json:"old_value,omitempty"`
}

// Audit action types from the audit log events table.
const (
	AuditChannelCreate          = 10
	AuditChannelUpdate          = 11
	AuditChannelDelete          = 12
	AuditChannelOverwriteCreate = 13
	AuditChannelOverwriteUpdate = 14
	AuditChannelOverwriteDelete = 15
	AuditMemberKick             = 20
	AuditMemberPrune            = 21
	AuditMemberBanAdd           = 22
	AuditMemberBanRemove        = 23
	AuditMemberUpdate           = 24
	AuditMemberRoleUpdate       = 25
	AuditMemberMove             = 26
	AuditMemberDisconnect       = 27
	AuditRoleCreate             = 30
	AuditRoleUpdate             = 31
	AuditRoleDelete             = 32
	AuditInviteCreate           = 40
	AuditInviteUpdate           = 41
	AuditInviteDelete           = 42
	AuditWebhookCreate          = 50
	AuditWebhookUpdate          = 51
	AuditWebhookDelete          = 52
	AuditEmojiCreate            = 60
	AuditSoundboardSoundCreate  = 130
	AuditSoundboardSoundUpdate  = 131
	AuditSoundboardSoundDelete  = 132
	AuditEmojiUpdate            = 61
	AuditEmojiDelete            = 62
	AuditMessageDelete          = 72
	AuditMessageBulkDelete      = 73
	AuditMessagePin             = 74
	AuditMessageUnpin           = 75
	AuditGuildUpdate            = 1
)

// WriteAudit appends an entry. Failures are returned so handlers can log them,
// but callers do not fail the primary mutation.
func (s *Store) WriteAudit(guildID, actorID string, action int, targetID string, changes []AuditChange, reason string) error {
	if guildID == "" {
		return nil
	}
	raw, _ := json.Marshal(changes)
	if changes == nil {
		raw = []byte("[]")
	}
	_, err := s.DB.Exec(`INSERT INTO audit_logs (id, guild_id, actor_id, action_type, target_id, changes, reason, created_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		s.NewID(), guildID, nullIfEmpty(actorID), action, nullIfEmpty(targetID), string(raw), nullIfEmpty(reason), now())
	return mapErr(err)
}

// AuditLogs lists entries newest first with the documented filters.
func (s *Store) AuditLogs(guildID, userID string, actionType int, limit int, before string) ([]AuditEntry, error) {
	limit = clampLimit(limit)
	q := `SELECT id, actor_id, action_type, target_id, changes, reason, created_at
		FROM audit_logs WHERE guild_id = ?`
	args := []any{guildID}
	if userID != "" {
		q += ` AND actor_id = ?`
		args = append(args, userID)
	}
	if actionType != 0 {
		q += ` AND action_type = ?`
		args = append(args, actionType)
	}
	if before != "" {
		q += ` AND id < ?`
		args = append(args, before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var (
			e       AuditEntry
			actor   sql.NullString
			target  sql.NullString
			changes string
			reason  sql.NullString
			created int64
		)
		if err := rows.Scan(&e.ID, &actor, &e.ActionType, &target, &changes, &reason, &created); err != nil {
			return nil, mapErr(err)
		}
		e.ActorID = ns(actor)
		e.TargetID = ns(target)
		e.Reason = ns(reason)
		e.CreatedAt = iso(created)
		e.Changes = []AuditChange{}
		_ = json.Unmarshal([]byte(changes), &e.Changes)
		out = append(out, e)
	}
	return out, mapErr(rows.Err())
}

// --- read states ---

// AckChannel records the last read message.
func (s *Store) AckChannel(userID, channelID, messageID string, mentionCount int) error {
	_, err := s.DB.Exec(`INSERT INTO read_states (user_id, channel_id, last_message_id, mention_count)
		VALUES (?,?,?,?)
		ON CONFLICT(user_id, channel_id) DO UPDATE SET last_message_id = excluded.last_message_id,
			mention_count = excluded.mention_count`,
		userID, channelID, nullIfEmpty(messageID), mentionCount)
	return mapErr(err)
}

// ReadState returns one read state.
func (s *Store) ReadState(userID, channelID string) (model.ReadState, error) {
	var (
		rs      model.ReadState
		last    sql.NullString
		lastPin sql.NullInt64
	)
	err := s.DB.QueryRow(`SELECT channel_id, last_message_id, mention_count, last_pin_timestamp
		FROM read_states WHERE user_id = ? AND channel_id = ?`, userID, channelID).
		Scan(&rs.ID, &last, &rs.MentionCount, &lastPin)
	if err != nil {
		return rs, mapErr(err)
	}
	rs.LastMessageID = ns(last)
	if lastPin.Valid {
		v := iso(lastPin.Int64)
		rs.LastPinTimestamp = &v
	}
	return rs, nil
}

func randomToken() (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, 68)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, len(buf))
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

// --- emoji CRUD ---

// CreateEmoji inserts a guild emoji. Image bytes are not persisted; clients see
// the documented object shape.
func (s *Store) CreateEmoji(guildID, creatorID, name string, animated bool) (model.Emoji, error) {
	idv := s.NewID()
	_, err := s.DB.Exec(`INSERT INTO emojis (id, guild_id, name, creator_id, roles, require_colons, managed, animated, available)
		VALUES (?,?,?,?,'[]',1,0,?,1)`, idv, guildID, name, creatorID, animated)
	if err != nil {
		return model.Emoji{}, mapErr(err)
	}
	return s.Emoji(guildID, idv)
}

// Emoji loads one emoji.
func (s *Store) Emoji(guildID, idv string) (model.Emoji, error) {
	var (
		e        model.Emoji
		id       string
		name     sql.NullString
		creator  sql.NullString
		roles    string
		required int
	)
	err := s.DB.QueryRow(`SELECT id, name, creator_id, roles, require_colons, managed, animated, available
		FROM emojis WHERE guild_id = ? AND id = ?`, guildID, idv).
		Scan(&id, &name, &creator, &roles, &required, &e.Managed, &e.Animated, &e.Available)
	if err != nil {
		return e, mapErr(err)
	}
	e.ID = &id
	e.Name = ns(name)
	e.RequireColons = required != 0
	e.Roles = parseJSONStrings(roles)
	return e, nil
}

// UpdateEmoji renames an emoji or replaces its role whitelist.
func (s *Store) UpdateEmoji(guildID, idv string, name *string, roles *[]string) (model.Emoji, error) {
	set := []string{}
	args := []any{}
	if name != nil {
		set = append(set, "name = ?")
		args = append(args, *name)
	}
	if roles != nil {
		set = append(set, "roles = ?")
		args = append(args, jsonStrings(*roles))
	}
	if len(set) == 0 {
		return s.Emoji(guildID, idv)
	}
	args = append(args, guildID, idv)
	res, err := s.DB.Exec(`UPDATE emojis SET `+strings.Join(set, ", ")+` WHERE guild_id = ? AND id = ?`, args...)
	if err != nil {
		return model.Emoji{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.Emoji{}, ErrNotFound
	}
	return s.Emoji(guildID, idv)
}

// DeleteEmoji removes an emoji.
func (s *Store) DeleteEmoji(guildID, idv string) error {
	res, err := s.DB.Exec(`DELETE FROM emojis WHERE guild_id = ? AND id = ?`, guildID, idv)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
