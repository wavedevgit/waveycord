package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/perm"
)

const guildColumns = `id, name, icon, splash, discovery_splash, banner, owner_id, region,
	afk_channel_id, afk_timeout, widget_enabled, widget_channel_id, verification_level,
	default_message_notifications, explicit_content_filter, mfa_level, system_channel_id,
	system_channel_flags, rules_channel_id, features, preferred_locale, description,
	vanity_url_code, premium_tier, premium_subscription_count, nsfw_level`

func scanGuild(row rowScanner) (model.Guild, error) {
	var (
		g        model.Guild
		icon     sql.NullString
		splash   sql.NullString
		disc     sql.NullString
		banner   sql.NullString
		afk      sql.NullString
		widget   int
		widgetCh sql.NullString
		system   sql.NullString
		rules    sql.NullString
		features string
		desc     sql.NullString
		vanity   sql.NullString
	)
	err := row.Scan(&g.ID, &g.Name, &icon, &splash, &disc, &banner, &g.OwnerID, new(string),
		&afk, &g.AfkTimeout, &widget, &widgetCh, &g.VerificationLevel, &g.DefaultMessageNotifications,
		&g.ExplicitContentFilter, &g.MfaLevel, &system, &g.SystemChannelFlags, &rules,
		&features, &g.PreferredLocale, &desc, &vanity, &g.PremiumTier,
		&g.PremiumSubscriptionCount, &g.NSFWLevel)
	if err != nil {
		return g, err
	}
	g.Icon = ns(icon)
	g.Splash = ns(splash)
	g.DiscoverySplash = ns(disc)
	g.Banner = ns(banner)
	g.AfkChannelID = ns(afk)
	enabled := widget != 0
	g.WidgetEnabled = &enabled
	g.WidgetChannelID = ns(widgetCh)
	g.SystemChannelID = ns(system)
	g.RulesChannelID = ns(rules)
	g.Description = ns(desc)
	g.VanityURLCode = ns(vanity)
	g.Features = []string{}
	_ = json.Unmarshal([]byte(features), &g.Features)
	return g, nil
}

// CreateGuildParams holds the fields the documented create route accepts.
type CreateGuildParams struct {
	Name            string
	Description     *string
	Icon            *string
	Verification    *int
	Notifications   *int
	ContentFilter   *int
	AFKChannelID    *string
	AFKTimeout      *int
	SystemChannelID *string
}

// CreateGuild inserts a guild, its @everyone role and the owner membership.
func (s *Store) CreateGuild(ownerID string, p CreateGuildParams) (model.Guild, error) {
	gid := s.NewID()
	verification, notifications, filter, afkTimeout := 0, 0, 0, 300
	if p.Verification != nil {
		verification = *p.Verification
	}
	if p.Notifications != nil {
		notifications = *p.Notifications
	}
	if p.ContentFilter != nil {
		filter = *p.ContentFilter
	}
	if p.AFKTimeout != nil {
		afkTimeout = *p.AFKTimeout
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return model.Guild{}, mapErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO guilds
		(id, name, icon, description, owner_id, afk_channel_id, afk_timeout, verification_level,
		 default_message_notifications, explicit_content_filter, system_channel_id, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		gid, p.Name, p.Icon, p.Description, ownerID, p.AFKChannelID, afkTimeout, verification,
		notifications, filter, p.SystemChannelID, now()); err != nil {
		return model.Guild{}, mapErr(err)
	}
	if _, err := tx.Exec(`INSERT INTO roles
		(id, guild_id, name, color, hoist, position, permissions, managed, mentionable, flags)
		VALUES (?,?,?,0,0,0,?,0,0,0)`, gid, gid, "@everyone", perm.Format(defaultEveryonePermissions)); err != nil {
		return model.Guild{}, mapErr(err)
	}
	if _, err := tx.Exec(`INSERT INTO guild_members (guild_id, user_id, joined_at)
		VALUES (?,?,?)`, gid, ownerID, now()); err != nil {
		return model.Guild{}, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return model.Guild{}, mapErr(err)
	}
	return s.Guild(gid)
}

// defaultEveryonePermissions matches what a freshly created guild grants.
const defaultEveryonePermissions = perm.ViewChannel | perm.SendMessages | perm.SendMessagesInThreads |
	perm.CreatePublicThreads | perm.CreatePrivateThreads | perm.EmbedLinks | perm.AttachFiles |
	perm.ReadMessageHistory | perm.AddReactions | perm.UseExternalEmojis | perm.UseExternalStickers |
	perm.UseApplicationCommands | perm.UseEmbeddedActivities | perm.Connect | perm.Speak |
	perm.Stream | perm.UseVAD | perm.ChangeNickname | perm.UseExternalSounds | perm.UseSoundboard |
	perm.CreateInstantInvite | perm.SendVoiceMessages | perm.SendPolls | perm.UseExternalApps

// Guild returns a guild with its roles and emojis attached.
func (s *Store) Guild(idv string) (model.Guild, error) {
	g, err := scanGuild(s.DB.QueryRow(`SELECT `+guildColumns+` FROM guilds WHERE id = ?`, idv))
	if err != nil {
		return g, mapErr(err)
	}
	if g.Roles, err = s.Roles(idv); err != nil {
		return g, err
	}
	if g.Emojis, err = s.Emojis(idv); err != nil {
		return g, err
	}
	return g, nil
}

// GuildExists is a cheap membership-free existence check.
func (s *Store) GuildExists(idv string) (bool, error) {
	var one int
	err := s.DB.QueryRow(`SELECT 1 FROM guilds WHERE id = ?`, idv).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, mapErr(err)
}

// GuildsForUser lists guilds the account belongs to, newest first.
func (s *Store) GuildsForUser(userID string) ([]model.Guild, error) {
	rows, err := s.DB.Query(`SELECT `+guildColumns+` FROM guilds
		WHERE id IN (SELECT guild_id FROM guild_members WHERE user_id = ?)
		ORDER BY id DESC`, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []model.Guild
	for rows.Next() {
		g, err := scanGuild(rows)
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, g)
	}
	return out, mapErr(rows.Err())
}

// UpdateGuildParams holds nullable guild patches.
type UpdateGuildParams struct {
	Name            *string
	Icon            *string
	Banner          *string
	Splash          *string
	Description     *string
	Verification    *int
	Notifications   *int
	ContentFilter   *int
	AFKChannelID    *string
	AFKTimeout      *int
	SystemChannelID *string
	RulesChannelID  *string
	PreferredLocale *string
	OwnerID         *string
}

// UpdateGuild applies a patch and returns the guild.
func (s *Store) UpdateGuild(idv string, p UpdateGuildParams) (model.Guild, error) {
	set := []string{}
	args := []any{}
	add := func(col string, v any) { set = append(set, col+" = ?"); args = append(args, v) }
	if p.Name != nil {
		add("name", *p.Name)
	}
	if p.Icon != nil {
		add("icon", nullIfEmpty(*p.Icon))
	}
	if p.Banner != nil {
		add("banner", nullIfEmpty(*p.Banner))
	}
	if p.Splash != nil {
		add("splash", nullIfEmpty(*p.Splash))
	}
	if p.Description != nil {
		add("description", nullIfEmpty(*p.Description))
	}
	if p.Verification != nil {
		add("verification_level", *p.Verification)
	}
	if p.Notifications != nil {
		add("default_message_notifications", *p.Notifications)
	}
	if p.ContentFilter != nil {
		add("explicit_content_filter", *p.ContentFilter)
	}
	if p.AFKChannelID != nil {
		add("afk_channel_id", nullIfEmpty(*p.AFKChannelID))
	}
	if p.AFKTimeout != nil {
		add("afk_timeout", *p.AFKTimeout)
	}
	if p.SystemChannelID != nil {
		add("system_channel_id", nullIfEmpty(*p.SystemChannelID))
	}
	if p.RulesChannelID != nil {
		add("rules_channel_id", nullIfEmpty(*p.RulesChannelID))
	}
	if p.PreferredLocale != nil {
		add("preferred_locale", *p.PreferredLocale)
	}
	if p.OwnerID != nil {
		add("owner_id", *p.OwnerID)
	}
	if len(set) == 0 {
		return s.Guild(idv)
	}
	args = append(args, idv)
	if _, err := s.DB.Exec(`UPDATE guilds SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...); err != nil {
		return model.Guild{}, mapErr(err)
	}
	return s.Guild(idv)
}

// DeleteGuild removes the guild and cascades.
func (s *Store) DeleteGuild(idv string) error {
	res, err := s.DB.Exec(`DELETE FROM guilds WHERE id = ?`, idv)
	if err != nil {
		return mapErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- roles ---

const roleColumns = `id, guild_id, name, color, hoist, position, permissions, managed, mentionable, icon, unicode_emoji, flags`

func scanRole(row rowScanner) (model.Role, error) {
	var (
		r     model.Role
		icon  sql.NullString
		emoji sql.NullString
	)
	err := row.Scan(&r.ID, new(string), &r.Name, &r.Color, &r.Hoist, &r.Position,
		&r.Permissions, &r.Managed, &r.Mentionable, &icon, &emoji, &r.Flags)
	if err != nil {
		return r, err
	}
	r.Icon = ns(icon)
	r.UnicodeEmoji = ns(emoji)
	return r, nil
}

// Roles lists a guild's roles from highest to lowest.
func (s *Store) Roles(guildID string) ([]model.Role, error) {
	rows, err := s.DB.Query(`SELECT `+roleColumns+` FROM roles WHERE guild_id = ? ORDER BY position DESC, id ASC`, guildID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.Role{}
	for rows.Next() {
		r, err := scanRole(rows)
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, r)
	}
	return out, mapErr(rows.Err())
}

// Role returns one role.
func (s *Store) Role(guildID, roleID string) (model.Role, error) {
	r, err := scanRole(s.DB.QueryRow(`SELECT `+roleColumns+` FROM roles WHERE guild_id = ? AND id = ?`, guildID, roleID))
	return r, mapErr(err)
}

// CreateRoleParams holds role creation fields.
type CreateRoleParams struct {
	Name        string
	Permissions string
	Color       int
	Hoist       bool
	Mentionable bool
}

// CreateRole inserts a role above @everyone.
func (s *Store) CreateRole(guildID string, p CreateRoleParams) (model.Role, error) {
	var maxPos int
	_ = s.DB.QueryRow(`SELECT COALESCE(MAX(position),0) FROM roles WHERE guild_id = ?`, guildID).Scan(&maxPos)
	rid := s.NewID()
	_, err := s.DB.Exec(`INSERT INTO roles
		(id, guild_id, name, color, hoist, position, permissions, managed, mentionable, flags)
		VALUES (?,?,?,?,?,?,?,0,?,0)`,
		rid, guildID, p.Name, p.Color, p.Hoist, maxPos+1, p.Permissions, p.Mentionable)
	if err != nil {
		return model.Role{}, mapErr(err)
	}
	return s.Role(guildID, rid)
}

// UpdateRoleParams holds nullable role patches.
type UpdateRoleParams struct {
	Name        *string
	Permissions *string
	Color       *int
	Hoist       *bool
	Mentionable *bool
	Position    *int
}

// UpdateRole applies a patch.
func (s *Store) UpdateRole(guildID, roleID string, p UpdateRoleParams) (model.Role, error) {
	set := []string{}
	args := []any{}
	add := func(col string, v any) { set = append(set, col+" = ?"); args = append(args, v) }
	if p.Name != nil {
		add("name", *p.Name)
	}
	if p.Permissions != nil {
		add("permissions", *p.Permissions)
	}
	if p.Color != nil {
		add("color", *p.Color)
	}
	if p.Hoist != nil {
		add("hoist", *p.Hoist)
	}
	if p.Mentionable != nil {
		add("mentionable", *p.Mentionable)
	}
	if p.Position != nil {
		add("position", *p.Position)
	}
	if len(set) == 0 {
		return s.Role(guildID, roleID)
	}
	args = append(args, guildID, roleID)
	if _, err := s.DB.Exec(`UPDATE roles SET `+strings.Join(set, ", ")+` WHERE guild_id = ? AND id = ?`, args...); err != nil {
		return model.Role{}, mapErr(err)
	}
	return s.Role(guildID, roleID)
}

// DeleteRole removes a role and strips it from members.
func (s *Store) DeleteRole(guildID, roleID string) error {
	if roleID == guildID {
		return ErrConflict
	}
	res, err := s.DB.Exec(`DELETE FROM roles WHERE guild_id = ? AND id = ?`, guildID, roleID)
	if err != nil {
		return mapErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	rows, err := s.DB.Query(`SELECT user_id, role_ids FROM guild_members WHERE guild_id = ?`, guildID)
	if err != nil {
		return mapErr(err)
	}
	type upd struct{ user, roles string }
	var updates []upd
	for rows.Next() {
		var user, roles string
		if err := rows.Scan(&user, &roles); err != nil {
			rows.Close()
			return mapErr(err)
		}
		ids := parseJSONStrings(roles)
		kept := ids[:0]
		for _, v := range ids {
			if v != roleID {
				kept = append(kept, v)
			}
		}
		updates = append(updates, upd{user, jsonStrings(kept)})
	}
	rows.Close()
	for _, u := range updates {
		_, _ = s.DB.Exec(`UPDATE guild_members SET role_ids = ? WHERE guild_id = ? AND user_id = ?`, u.roles, guildID, u.user)
	}
	return nil
}

// --- members ---

// Member returns one member with the user attached.
func (s *Store) Member(guildID, userID string) (model.Member, error) {
	var (
		m       model.Member
		nick    sql.NullString
		avatar  sql.NullString
		banner  sql.NullString
		premium sql.NullInt64
		roles   string
		comms   sql.NullInt64
		joined  int64
	)
	err := s.DB.QueryRow(`SELECT nick, joined_at, premium_since, deaf, mute, pending,
		communication_disabled_until, avatar, banner, role_ids
		FROM guild_members WHERE guild_id = ? AND user_id = ?`, guildID, userID).
		Scan(&nick, &joined, &premium, &m.Deaf, &m.Mute, &m.Pending, &comms, &avatar, &banner, &roles)
	if err != nil {
		return m, mapErr(err)
	}
	u, err := s.UserByID(userID)
	if err != nil {
		return m, err
	}
	m.User = &u
	m.Nick = ns(nick)
	m.Avatar = ns(avatar)
	m.Banner = ns(banner)
	m.JoinedAt = iso(joined)
	m.PremiumSince = isoPtr(premium)
	m.CommunicationDisabledUntil = isoPtr(comms)
	m.Roles = parseJSONStrings(roles)
	return m, nil
}

// Members lists members ordered by join time.
func (s *Store) Members(guildID string, limit int, after string) ([]model.Member, error) {
	limit = clampLimit(limit)
	q := `SELECT user_id, nick, joined_at, premium_since, deaf, mute, pending,
		communication_disabled_until, avatar, banner, role_ids
		FROM guild_members WHERE guild_id = ?`
	args := []any{guildID}
	if after != "" {
		q += ` AND user_id > ?`
		args = append(args, after)
	}
	q += ` ORDER BY user_id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	type rawMember struct {
		userID  string
		nick    sql.NullString
		joined  int64
		premium sql.NullInt64
		deaf    bool
		mute    bool
		pending bool
		comms   sql.NullInt64
		avatar  sql.NullString
		banner  sql.NullString
		roles   string
	}
	var raw []rawMember
	for rows.Next() {
		var r rawMember
		if err := rows.Scan(&r.userID, &r.nick, &r.joined, &r.premium, &r.deaf, &r.mute,
			&r.pending, &r.comms, &r.avatar, &r.banner, &r.roles); err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		raw = append(raw, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	out := []model.Member{}
	for _, r := range raw {
		u, err := s.UserByID(r.userID)
		if err != nil {
			continue
		}
		m := model.Member{
			User: &u, Nick: ns(r.nick), Avatar: ns(r.avatar), Banner: ns(r.banner),
			JoinedAt: iso(r.joined), PremiumSince: isoPtr(r.premium),
			Deaf: r.deaf, Mute: r.mute, Pending: r.pending,
			CommunicationDisabledUntil: isoPtr(r.comms), Roles: parseJSONStrings(r.roles),
		}
		out = append(out, m)
	}
	return out, nil
}

// AddMember inserts a membership, ignoring a duplicate join.
func (s *Store) AddMember(guildID, userID string) (model.Member, error) {
	_, err := s.DB.Exec(`INSERT INTO guild_members (guild_id, user_id, joined_at)
		VALUES (?,?,?) ON CONFLICT(guild_id, user_id) DO NOTHING`, guildID, userID, now())
	if err != nil {
		return model.Member{}, mapErr(err)
	}
	return s.Member(guildID, userID)
}

// IsMember reports membership.
// GuildMemberIDs lists every member id of a guild, used for per-viewer fanout.
func (s *Store) GuildMemberIDs(guildID string) ([]string, error) {
	rows, err := s.DB.Query(`SELECT user_id FROM guild_members WHERE guild_id = ?`, guildID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, uid)
	}
	return out, mapErr(rows.Err())
}

func (s *Store) IsMember(guildID, userID string) (bool, error) {
	var one int
	err := s.DB.QueryRow(`SELECT 1 FROM guild_members WHERE guild_id = ? AND user_id = ?`, guildID, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, mapErr(err)
}

// UpdateMemberParams holds nullable member patches.
type UpdateMemberParams struct {
	Nick      *string
	Deaf      *bool
	Mute      *bool
	Roles     *[]string
	ChannelID *string
}

// UpdateMember applies a patch and returns the member.
func (s *Store) UpdateMember(guildID, userID string, p UpdateMemberParams) (model.Member, error) {
	set := []string{}
	args := []any{}
	if p.Nick != nil {
		set = append(set, "nick = ?")
		args = append(args, nullIfEmpty(*p.Nick))
	}
	if p.Deaf != nil {
		set = append(set, "deaf = ?")
		args = append(args, *p.Deaf)
	}
	if p.Mute != nil {
		set = append(set, "mute = ?")
		args = append(args, *p.Mute)
	}
	if p.Roles != nil {
		set = append(set, "role_ids = ?")
		args = append(args, jsonStrings(*p.Roles))
	}
	if len(set) == 0 {
		return s.Member(guildID, userID)
	}
	args = append(args, guildID, userID)
	res, err := s.DB.Exec(`UPDATE guild_members SET `+strings.Join(set, ", ")+` WHERE guild_id = ? AND user_id = ?`, args...)
	if err != nil {
		return model.Member{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.Member{}, ErrNotFound
	}
	return s.Member(guildID, userID)
}

// SetMemberTimeout applies a communication timeout in milliseconds.
func (s *Store) SetMemberTimeout(guildID, userID string, untilMs int64) error {
	res, err := s.DB.Exec(`UPDATE guild_members SET communication_disabled_until = ?
		WHERE guild_id = ? AND user_id = ?`, untilMs, guildID, userID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// EnsureSystemUser returns a system account for automated messages, creating it
// once per process lifetime.
func (s *Store) EnsureSystemUser() (model.User, error) {
	var id string
	err := s.DB.QueryRow(`SELECT id FROM users WHERE system = 1 AND username = 'AutoMod' LIMIT 1`).Scan(&id)
	if err == nil {
		return s.UserByID(id)
	}
	created, err := s.CreateUser(CreateUserParams{
		Username: "AutoMod", PasswordHash: "", System: true, Verified: true,
	})
	return created, err
}

// RemoveMember deletes a membership.
func (s *Store) RemoveMember(guildID, userID string) error {
	res, err := s.DB.Exec(`DELETE FROM guild_members WHERE guild_id = ? AND user_id = ?`, guildID, userID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddMemberRole adds one role id to a member.
func (s *Store) AddMemberRole(guildID, userID, roleID string) error {
	m, err := s.Member(guildID, userID)
	if err != nil {
		return err
	}
	for _, r := range m.Roles {
		if r == roleID {
			return nil
		}
	}
	m.Roles = append(m.Roles, roleID)
	_, err = s.UpdateMember(guildID, userID, UpdateMemberParams{Roles: &m.Roles})
	return err
}

// RemoveMemberRole removes one role id from a member.
func (s *Store) RemoveMemberRole(guildID, userID, roleID string) error {
	m, err := s.Member(guildID, userID)
	if err != nil {
		return err
	}
	kept := []string{}
	for _, r := range m.Roles {
		if r != roleID {
			kept = append(kept, r)
		}
	}
	_, err = s.UpdateMember(guildID, userID, UpdateMemberParams{Roles: &kept})
	return err
}

// BasePermissions returns the union of @everyone and the member's role bits.
func (s *Store) BasePermissions(guildID, userID string) (uint64, error) {
	roles, err := s.Roles(guildID)
	if err != nil {
		return 0, err
	}
	m, err := s.Member(guildID, userID)
	if err != nil {
		return 0, err
	}
	var out uint64
	for _, r := range roles {
		if r.ID == guildID {
			v, _ := perm.Parse(r.Permissions)
			out |= v
		}
	}
	for _, rid := range m.Roles {
		for _, r := range roles {
			if r.ID == rid {
				v, _ := perm.Parse(r.Permissions)
				out |= v
			}
		}
	}
	return out, nil
}

// --- bans ---

// Ban is a guild ban with the banned user.
type Ban struct {
	User   model.User `json:"user"`
	Reason *string    `json:"reason"`
}

// Bans lists guild bans.
func (s *Store) Bans(guildID string) ([]Ban, error) {
	rows, err := s.DB.Query(`SELECT user_id, reason FROM bans WHERE guild_id = ? ORDER BY user_id ASC`, guildID)
	if err != nil {
		return nil, mapErr(err)
	}
	type rawBan struct {
		uid    string
		reason sql.NullString
	}
	var raw []rawBan
	for rows.Next() {
		var r rawBan
		if err := rows.Scan(&r.uid, &r.reason); err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		raw = append(raw, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	out := []Ban{}
	for _, r := range raw {
		u, err := s.UserByID(r.uid)
		if err != nil {
			continue
		}
		out = append(out, Ban{User: u, Reason: ns(r.reason)})
	}
	return out, nil
}

// BanUser bans and removes the membership.
func (s *Store) BanUser(guildID, userID, reason string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO bans (guild_id, user_id, reason, banned_at)
		VALUES (?,?,?,?) ON CONFLICT(guild_id, user_id) DO UPDATE SET reason = excluded.reason`,
		guildID, userID, nullIfEmpty(reason), now()); err != nil {
		return mapErr(err)
	}
	if _, err := tx.Exec(`DELETE FROM guild_members WHERE guild_id = ? AND user_id = ?`, guildID, userID); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit())
}

// UnbanUser lifts a ban.
func (s *Store) UnbanUser(guildID, userID string) error {
	res, err := s.DB.Exec(`DELETE FROM bans WHERE guild_id = ? AND user_id = ?`, guildID, userID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
