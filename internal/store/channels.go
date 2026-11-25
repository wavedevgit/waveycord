package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/sqlx"
)

// Channel types from the channel object table.
const (
	ChannelText               = 0
	ChannelDM                 = 1
	ChannelVoice              = 2
	ChannelGroupDM            = 3
	ChannelCategory           = 4
	ChannelAnnouncement       = 5
	ChannelAnnouncementThread = 10
	ChannelPublicThread       = 11
	ChannelPrivateThread      = 12
	ChannelStage              = 13
	ChannelDirectory          = 14
	ChannelForum              = 15
	ChannelMedia              = 16
)

const channelColumns = `id, type, guild_id, position, name, topic, nsfw, last_message_id,
	bitrate, user_limit, rate_limit_per_user, parent_id, owner_id, last_pin_timestamp,
	rtc_region, video_quality_mode, message_count, member_count, thread_metadata, archived, icon,
	flags, available_tags, default_reaction_emoji, default_sort_order, default_forum_layout`

func scanChannel(row rowScanner) (model.Channel, error) {
	var (
		c                model.Channel
		guild            sql.NullString
		name             sql.NullString
		topic            sql.NullString
		lastMsg          sql.NullString
		bitrate          sql.NullInt64
		userLimit        sql.NullInt64
		parent           sql.NullString
		owner            sql.NullString
		lastPin          sql.NullInt64
		rtc              sql.NullString
		threadMeta       sql.NullString
		icon             sql.NullString
		tags             sql.NullString
		defReact         sql.NullString
		defSort          sql.NullString
		nsfw, vqm        int
		archived         int
		pos, rl          int
		mcount, memcount int
		defLayout        int
	)
	err := row.Scan(&c.ID, &c.Type, &guild, &pos, &name, &topic, &nsfw, &lastMsg,
		&bitrate, &userLimit, &rl, &parent, &owner, &lastPin, &rtc, &vqm, &mcount,
		&memcount, &threadMeta, &archived, &icon, &c.Flags, &tags, &defReact, &defSort, &defLayout)
	if err != nil {
		return c, err
	}
	c.GuildID = ns(guild)
	c.Position = &pos
	c.Name = ns(name)
	c.Topic = ns(topic)
	nsfwBool := nsfw != 0
	c.NSFW = &nsfwBool
	c.LastMessageID = ns(lastMsg)
	if bitrate.Valid {
		v := int(bitrate.Int64)
		c.Bitrate = &v
	}
	if userLimit.Valid {
		v := int(userLimit.Int64)
		c.UserLimit = &v
	}
	c.RateLimitPerUser = &rl
	c.ParentID = ns(parent)
	c.OwnerID = ns(owner)
	if lastPin.Valid {
		s := iso(lastPin.Int64)
		c.LastPinTimestamp = &s
	}
	c.RTCRegion = ns(rtc)
	c.VideoQualityMode = &vqm
	c.MessageCount = &mcount
	c.MemberCount = &memcount
	if threadMeta.Valid && threadMeta.String != "" {
		var tm model.ThreadMetadata
		if err := json.Unmarshal([]byte(threadMeta.String), &tm); err == nil {
			tm.Archived = archived != 0
			c.ThreadMetadata = &tm
		}
	}
	c.Icon = ns(icon)
	if tags.Valid && tags.String != "" {
		_ = json.Unmarshal([]byte(tags.String), &c.AvailableTags)
	}
	if defReact.Valid && defReact.String != "" {
		var dr model.DefaultReaction
		if err := json.Unmarshal([]byte(defReact.String), &dr); err == nil {
			c.DefaultReactionEmoji = &dr
		}
	}
	if defSort.Valid && defSort.String != "" {
		var v int
		if err := json.Unmarshal([]byte(defSort.String), &v); err == nil {
			c.DefaultSortOrder = &v
		}
	}
	c.DefaultForumLayout = &defLayout
	return c, nil
}

// CreateChannelParams covers the guild channel, DM and thread creation routes.
type CreateChannelParams struct {
	Type                 int
	Name                 *string
	Topic                *string
	NSFW                 *bool
	Bitrate              *int
	UserLimit            *int
	RateLimitPerUser     *int
	Position             *int
	ParentID             *string
	PermissionOverwrites []model.Overwrite
	Recipients           []string
	OwnerID              *string
	// thread fields
	AutoArchiveDuration *int
	MessageID           *string
	Invitable           *bool
}

// CreateChannel inserts a channel. Guild channels get their position appended.
func (s *Store) CreateChannel(guildID string, p CreateChannelParams) (model.Channel, error) {
	idv := s.NewID()
	position := 0
	if p.Position != nil {
		position = *p.Position
	} else if p.ParentID != nil {
		var maxPos int
		_ = s.DB.QueryRow(`SELECT COALESCE(MAX(position),0) FROM channels WHERE guild_id = ? AND parent_id IS ?`, guildID, nullIfEmpty(*p.ParentID)).Scan(&maxPos)
		position = maxPos + 1
	} else if guildID != "" {
		var maxPos int
		_ = s.DB.QueryRow(`SELECT COALESCE(MAX(position),0) FROM channels WHERE guild_id = ?`, guildID).Scan(&maxPos)
		position = maxPos + 1
	}
	rl := 0
	if p.RateLimitPerUser != nil {
		rl = *p.RateLimitPerUser
	}
	nsfw := false
	if p.NSFW != nil {
		nsfw = *p.NSFW
	}
	var threadMeta any
	if p.Type == ChannelPublicThread || p.Type == ChannelPrivateThread || p.Type == ChannelAnnouncementThread {
		dur := 1440
		if p.AutoArchiveDuration != nil {
			dur = *p.AutoArchiveDuration
		}
		invitable := true
		if p.Invitable != nil {
			invitable = *p.Invitable
		}
		raw, _ := json.Marshal(model.ThreadMetadata{
			AutoArchiveDuration: dur,
			ArchiveTimestamp:    iso(now()),
			Invitable:           &invitable,
		})
		threadMeta = string(raw)
	}
	var guild any
	if guildID != "" {
		guild = guildID
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return model.Channel{}, mapErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO channels
		(id, type, guild_id, position, name, topic, nsfw, bitrate, user_limit,
		 rate_limit_per_user, parent_id, owner_id, thread_metadata, archived, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		idv, p.Type, guild, position, p.Name, p.Topic, nsfw, p.Bitrate, p.UserLimit,
		rl, p.ParentID, p.OwnerID, threadMeta, false, now()); err != nil {
		return model.Channel{}, mapErr(err)
	}
	for _, ow := range p.PermissionOverwrites {
		if err := upsertOverwrite(tx, idv, ow); err != nil {
			return model.Channel{}, err
		}
	}
	for _, uid := range p.Recipients {
		if _, err := tx.Exec(`INSERT INTO channel_recipients (channel_id, user_id) VALUES (?,?)`, idv, uid); err != nil {
			return model.Channel{}, mapErr(err)
		}
	}
	if p.OwnerID != nil && (p.Type == ChannelGroupDM || p.Type == ChannelDM) {
		if _, err := tx.Exec(`INSERT INTO channel_recipients (channel_id, user_id) VALUES (?,?) ON CONFLICT DO NOTHING`, idv, *p.OwnerID); err != nil {
			return model.Channel{}, mapErr(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return model.Channel{}, mapErr(err)
	}
	return s.Channel(idv)
}

// Channel loads a channel with overwrites and recipients.
func (s *Store) Channel(idv string) (model.Channel, error) {
	c, err := scanChannel(s.DB.QueryRow(`SELECT `+channelColumns+` FROM channels WHERE id = ?`, idv))
	if err != nil {
		return c, mapErr(err)
	}
	if c.PermissionOverwrites, err = s.Overwrites(idv); err != nil {
		return c, err
	}
	if c.Type == ChannelDM || c.Type == ChannelGroupDM {
		if c.Recipients, err = s.Recipients(idv); err != nil {
			return c, err
		}
	}
	return c, nil
}

// ChannelType returns only the numeric type, for permission checks.
func (s *Store) ChannelType(idv string) (int, error) {
	var t int
	err := s.DB.QueryRow(`SELECT type FROM channels WHERE id = ?`, idv).Scan(&t)
	return t, mapErr(err)
}

// GuildChannels lists a guild's channels ordered by position.
func (s *Store) GuildChannels(guildID string) ([]model.Channel, error) {
	rows, err := s.DB.Query(`SELECT `+channelColumns+` FROM channels WHERE guild_id = ? ORDER BY position ASC, id ASC`, guildID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	for i := range out {
		if out[i].PermissionOverwrites, err = s.Overwrites(out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UpdateChannelParams holds nullable channel patches.
type UpdateChannelParams struct {
	Name                       *string
	Topic                      *string
	NSFW                       *bool
	Bitrate                    *int
	UserLimit                  *int
	RateLimitPerUser           *int
	Position                   *int
	ParentID                   *string
	RTCRegion                  *string
	VideoQualityMode           *int
	Archived                   *bool
	AutoArchiveDuration        *int
	Locked                     *bool
	Invitable                  *bool
	AppliedTags                *[]string
	Flags                      *int
	DefaultAutoArchiveDuration *int
	Icon                       *string
}

// UpdateChannel applies a patch.
func (s *Store) UpdateChannel(idv string, p UpdateChannelParams) (model.Channel, error) {
	set := []string{}
	args := []any{}
	add := func(col string, v any) { set = append(set, col+" = ?"); args = append(args, v) }
	if p.Name != nil {
		add("name", *p.Name)
	}
	if p.Topic != nil {
		add("topic", *p.Topic)
	}
	if p.NSFW != nil {
		add("nsfw", *p.NSFW)
	}
	if p.Bitrate != nil {
		add("bitrate", *p.Bitrate)
	}
	if p.UserLimit != nil {
		add("user_limit", *p.UserLimit)
	}
	if p.RateLimitPerUser != nil {
		add("rate_limit_per_user", *p.RateLimitPerUser)
	}
	if p.Position != nil {
		add("position", *p.Position)
	}
	if p.ParentID != nil {
		add("parent_id", nullIfEmpty(*p.ParentID))
	}
	if p.RTCRegion != nil {
		add("rtc_region", nullIfEmpty(*p.RTCRegion))
	}
	if p.VideoQualityMode != nil {
		add("video_quality_mode", *p.VideoQualityMode)
	}
	if p.Locked != nil {
		add("available_tags", nil) // no-op guard; locked lives in thread_metadata
		set = set[:len(set)-1]
		args = args[:len(args)-1]
	}
	if p.Icon != nil {
		add("icon", nullIfEmpty(*p.Icon))
	}
	if p.Flags != nil {
		add("flags", *p.Flags)
	}
	if len(set) == 0 && p.Archived == nil && p.AutoArchiveDuration == nil && p.Locked == nil && p.Invitable == nil {
		return s.Channel(idv)
	}
	if len(set) > 0 {
		args = append(args, idv)
		if _, err := s.DB.Exec(`UPDATE channels SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...); err != nil {
			return model.Channel{}, mapErr(err)
		}
	}
	if p.Archived != nil || p.AutoArchiveDuration != nil || p.Locked != nil || p.Invitable != nil {
		c, err := s.Channel(idv)
		if err != nil {
			return c, err
		}
		tm := model.ThreadMetadata{ArchiveTimestamp: iso(now())}
		if c.ThreadMetadata != nil {
			tm = *c.ThreadMetadata
		}
		tm.ArchiveTimestamp = iso(now())
		if p.Archived != nil {
			tm.Archived = *p.Archived
		}
		if p.AutoArchiveDuration != nil {
			tm.AutoArchiveDuration = *p.AutoArchiveDuration
		}
		if p.Locked != nil {
			tm.Locked = *p.Locked
		}
		if p.Invitable != nil {
			tm.Invitable = p.Invitable
		}
		raw, _ := json.Marshal(tm)
		if _, err := s.DB.Exec(`UPDATE channels SET thread_metadata = ?, archived = ? WHERE id = ?`, string(raw), tm.Archived, idv); err != nil {
			return c, mapErr(err)
		}
	}
	return s.Channel(idv)
}

// DeleteChannel removes a channel.
func (s *Store) DeleteChannel(idv string) error {
	res, err := s.DB.Exec(`DELETE FROM channels WHERE id = ?`, idv)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Overwrites lists the overwrites on a channel.
func (s *Store) Overwrites(channelID string) ([]model.Overwrite, error) {
	rows, err := s.DB.Query(`SELECT target_id, type, allow, deny FROM permission_overwrites WHERE channel_id = ? ORDER BY target_id`, channelID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.Overwrite{}
	for rows.Next() {
		var ow model.Overwrite
		if err := rows.Scan(&ow.ID, &ow.Type, &ow.Allow, &ow.Deny); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, ow)
	}
	return out, mapErr(rows.Err())
}

func upsertOverwrite(tx *sqlx.Tx, channelID string, ow model.Overwrite) error {
	_, err := tx.Exec(`INSERT INTO permission_overwrites (channel_id, target_id, type, allow, deny)
		VALUES (?,?,?,?,?)
		ON CONFLICT(channel_id, target_id) DO UPDATE SET type = excluded.type,
			allow = excluded.allow, deny = excluded.deny`,
		channelID, ow.ID, ow.Type, ow.Allow, ow.Deny)
	return mapErr(err)
}

// PutOverwrite writes one overwrite.
func (s *Store) PutOverwrite(channelID string, ow model.Overwrite) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	if err := upsertOverwrite(tx, channelID, ow); err != nil {
		return err
	}
	return mapErr(tx.Commit())
}

// DeleteOverwrite removes one overwrite.
func (s *Store) DeleteOverwrite(channelID, targetID string) error {
	res, err := s.DB.Exec(`DELETE FROM permission_overwrites WHERE channel_id = ? AND target_id = ?`, channelID, targetID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- direct messages ---

// Recipients lists the users in a DM.
func (s *Store) Recipients(channelID string) ([]*model.User, error) {
	rows, err := s.DB.Query(`SELECT user_id FROM channel_recipients WHERE channel_id = ? ORDER BY user_id`, channelID)
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
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	out := []*model.User{}
	for _, uid := range ids {
		u, err := s.UserByID(uid)
		if err != nil {
			continue
		}
		out = append(out, &u)
	}
	return out, nil
}

// DMChannels lists the DM and group DM channels a user is in.
func (s *Store) DMChannels(userID string) ([]model.Channel, error) {
	rows, err := s.DB.Query(`SELECT `+channelColumns+` FROM channels
		WHERE type IN (?,?) AND id IN (SELECT channel_id FROM channel_recipients WHERE user_id = ?)
		ORDER BY COALESCE(last_message_id, id) DESC`, ChannelDM, ChannelGroupDM, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var c model.Channel
		c, err := scanChannel(rows)
		if err != nil {
			return nil, mapErr(err)
		}
		ids = append(ids, c.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	out := make([]model.Channel, 0, len(ids))
	for _, idv := range ids {
		c, err := s.Channel(idv)
		if err != nil {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// FindDM returns an existing 1:1 channel between two users.
func (s *Store) FindDM(a, b string) (model.Channel, error) {
	var idv string
	err := s.DB.QueryRow(`SELECT c.id FROM channels c
		JOIN channel_recipients r1 ON r1.channel_id = c.id AND r1.user_id = ?
		JOIN channel_recipients r2 ON r2.channel_id = c.id AND r2.user_id = ?
		WHERE c.type = ?
		AND (SELECT COUNT(*) FROM channel_recipients r3 WHERE r3.channel_id = c.id) = 2
		LIMIT 1`, a, b, ChannelDM).Scan(&idv)
	if err != nil {
		return model.Channel{}, mapErr(err)
	}
	return s.Channel(idv)
}

// OpenDM returns the existing DM with a user or creates one.
func (s *Store) OpenDM(selfID, otherID string) (model.Channel, error) {
	if c, err := s.FindDM(selfID, otherID); err == nil {
		return c, nil
	} else if !errors.Is(err, ErrNotFound) {
		return model.Channel{}, err
	}
	return s.CreateChannel("", CreateChannelParams{Type: ChannelDM, Recipients: []string{selfID, otherID}})
}

// AddRecipient adds a user to a group DM.
func (s *Store) AddRecipient(channelID, userID string) error {
	_, err := s.DB.Exec(`INSERT INTO channel_recipients (channel_id, user_id) VALUES (?,?) ON CONFLICT DO NOTHING`, channelID, userID)
	return mapErr(err)
}

// RemoveRecipient removes a user from a group DM.
func (s *Store) RemoveRecipient(channelID, userID string) error {
	_, err := s.DB.Exec(`DELETE FROM channel_recipients WHERE channel_id = ? AND user_id = ?`, channelID, userID)
	return mapErr(err)
}

// --- threads ---

// ThreadMembers lists the members of a thread.
func (s *Store) ThreadMembers(channelID string) ([]model.ThreadMember, error) {
	rows, err := s.DB.Query(`SELECT user_id, join_timestamp, flags FROM thread_members WHERE channel_id = ? ORDER BY join_timestamp ASC`, channelID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.ThreadMember{}
	for rows.Next() {
		var tm model.ThreadMember
		var uid string
		var joined int64
		if err := rows.Scan(&uid, &joined, &tm.Flags); err != nil {
			return nil, mapErr(err)
		}
		tm.UserID = &uid
		tm.ID = &uid
		tm.JoinTimestamp = iso(joined)
		out = append(out, tm)
	}
	return out, mapErr(rows.Err())
}

// JoinThread records membership and bumps member_count.
func (s *Store) JoinThread(channelID, userID string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO thread_members (channel_id, user_id, join_timestamp)
		VALUES (?,?,?) ON CONFLICT DO NOTHING`, channelID, userID, now())
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if _, err := tx.Exec(`UPDATE channels SET member_count = member_count + 1 WHERE id = ?`, channelID); err != nil {
			return mapErr(err)
		}
	}
	return mapErr(tx.Commit())
}

// LeaveThread removes membership and decrements member_count.
func (s *Store) LeaveThread(channelID, userID string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM thread_members WHERE channel_id = ? AND user_id = ?`, channelID, userID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		if _, err := tx.Exec(`UPDATE channels SET member_count = CASE WHEN member_count > 0 THEN member_count - 1 ELSE 0 END WHERE id = ?`, channelID); err != nil {
			return mapErr(err)
		}
	}
	return mapErr(tx.Commit())
}

// ActiveThreads lists non-archived threads in a guild.
func (s *Store) ActiveThreads(guildID string) ([]model.Channel, error) {
	rows, err := s.DB.Query(`SELECT `+channelColumns+` FROM channels
		WHERE guild_id = ? AND type IN (?,?,?) AND archived = 0
		ORDER BY id DESC`, guildID, ChannelAnnouncementThread, ChannelPublicThread, ChannelPrivateThread)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, c)
	}
	return out, mapErr(rows.Err())
}

// ArchivedThreads lists archived threads of one kind for a channel.
func (s *Store) ArchivedThreads(parentID string, private bool) ([]model.Channel, error) {
	kind := ChannelPublicThread
	if private {
		kind = ChannelPrivateThread
	}
	rows, err := s.DB.Query(`SELECT `+channelColumns+` FROM channels
		WHERE parent_id = ? AND type = ? AND archived = 1
		ORDER BY id DESC`, parentID, kind)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, c)
	}
	return out, mapErr(rows.Err())
}

// --- pins ---

// PinnedMessages lists pinned messages newest first.
func (s *Store) PinnedMessages(channelID string) ([]model.Message, error) {
	rows, err := s.DB.Query(`SELECT message_id FROM pins WHERE channel_id = ? ORDER BY pinned_at DESC`, channelID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var idv string
		if err := rows.Scan(&idv); err != nil {
			return nil, mapErr(err)
		}
		ids = append(ids, idv)
	}
	rows.Close()
	out := []model.Message{}
	for _, idv := range ids {
		m, err := s.Message(idv)
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// Pin marks a message pinned.
func (s *Store) Pin(channelID, messageID string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO pins (channel_id, message_id, pinned_at)
		VALUES (?,?,?) ON CONFLICT DO NOTHING`, channelID, messageID, now()); err != nil {
		return mapErr(err)
	}
	if _, err := tx.Exec(`UPDATE messages SET pinned = 1 WHERE id = ?`, messageID); err != nil {
		return mapErr(err)
	}
	if _, err := tx.Exec(`UPDATE channels SET last_pin_timestamp = ? WHERE id = ?`, now(), channelID); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit())
}

// Unpin clears the pinned flag.
func (s *Store) Unpin(channelID, messageID string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM pins WHERE channel_id = ? AND message_id = ?`, channelID, messageID); err != nil {
		return mapErr(err)
	}
	if _, err := tx.Exec(`UPDATE messages SET pinned = 0 WHERE id = ?`, messageID); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit())
}

// Emojis lists a guild's emojis.
func (s *Store) Emojis(guildID string) ([]model.Emoji, error) {
	rows, err := s.DB.Query(`SELECT id, name, creator_id, roles, require_colons, managed, animated, available
		FROM emojis WHERE guild_id = ? ORDER BY id`, guildID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.Emoji{}
	for rows.Next() {
		var (
			e        model.Emoji
			idv      string
			name     sql.NullString
			creator  sql.NullString
			roles    string
			required int
		)
		if err := rows.Scan(&idv, &name, &creator, &roles, &required, &e.Managed, &e.Animated, &e.Available); err != nil {
			return nil, mapErr(err)
		}
		e.ID = &idv
		e.Name = ns(name)
		e.RequireColons = required != 0
		e.Roles = parseJSONStrings(roles)
		out = append(out, e)
	}
	return out, mapErr(rows.Err())
}
