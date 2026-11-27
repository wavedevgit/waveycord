package api

import (
	"errors"
	"strings"
	"time"

	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

// handleSearchMembers returns guild members whose username, display name or
// nickname contains the query.
func (s *Server) handleSearchMembers(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	query := strings.ToLower(strings.TrimSpace(c.R.URL.Query().Get("query")))
	limit := queryInt(c, "limit", 1, 1000, 1)
	if query == "" {
		c.JSON([]model.Member{})
		return
	}
	members, err := s.Store.Members(g.ID, 1000, "0")
	if err != nil {
		fail(c, err)
		return
	}
	out := []model.Member{}
	for _, m := range members {
		if m.User == nil {
			continue
		}
		hay := strings.ToLower(m.User.Username)
		if m.User.GlobalName != nil {
			hay += " " + strings.ToLower(*m.User.GlobalName)
		}
		if m.Nick != nil {
			hay += " " + strings.ToLower(*m.Nick)
		}
		if strings.Contains(hay, query) {
			out = append(out, m)
			if len(out) >= limit {
				break
			}
		}
	}
	c.JSON(out)
}

// handleGuildMemberPermissions resolves one member's effective permissions in a
// guild channel named by the channel_id query parameter.
func (s *Server) handleGuildMemberPermissions(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageRoles)
	if err != nil {
		fail(c, err)
		return
	}
	channelID := c.R.URL.Query().Get("channel_id")
	ch, err := s.Store.Channel(channelID)
	if err != nil || ch.GuildID == nil || *ch.GuildID != g.ID {
		c.FailForm("channel_id", "Must be a channel in this guild")
		return
	}
	target := c.Param("userID")
	if _, err := s.Store.Member(g.ID, target); err != nil {
		fail(c, ErrUnknownMember)
		return
	}
	perms, err := s.channelPermissions(target, ch)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"permissions": perm.Format(perms)})
}

// handleGuildRolePermissions resolves a role's effective permissions in a guild
// channel named by the channel_id query parameter.
func (s *Server) handleGuildRolePermissions(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageRoles)
	if err != nil {
		fail(c, err)
		return
	}
	channelID := c.R.URL.Query().Get("channel_id")
	ch, err := s.Store.Channel(channelID)
	if err != nil || ch.GuildID == nil || *ch.GuildID != g.ID {
		c.FailForm("channel_id", "Must be a channel in this guild")
		return
	}
	role, err := s.Store.Role(g.ID, c.Param("roleID"))
	if err != nil {
		fail(c, ErrUnknownRole)
		return
	}
	base, _ := perm.Parse(role.Permissions)
	if base&perm.Administrator != 0 {
		c.JSON(map[string]any{"permissions": perm.Format(perm.AllPermissions)})
		return
	}
	resolved := perm.Resolve(base, g.ID, []string{role.ID}, "", roleOverwrites(s, ch.ID, g.ID, role.ID))
	c.JSON(map[string]any{"permissions": perm.Format(resolved)})
}

// handleMemberCounts reports how many members hold each role.
func (s *Server) handleMemberCounts(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	roles, err := s.Store.Roles(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	counts := map[string]int{}
	members, err := s.Store.Members(g.ID, 100000, "0")
	if err != nil {
		fail(c, err)
		return
	}
	for _, m := range members {
		counts[g.ID]++
		for _, r := range m.Roles {
			counts[r]++
		}
	}
	out := make([]map[string]any, 0, len(roles))
	for _, r := range roles {
		out = append(out, map[string]any{"id": r.ID, "count": counts[r.ID]})
	}
	c.JSON(out)
}

// handleGetPruneCount estimates how many members a prune would remove.
func (s *Server) handleGetPruneCount(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.KickMembers)
	if err != nil {
		fail(c, err)
		return
	}
	days := queryInt(c, "days", 1, 30, 7)
	count := 0
	cutoff := time.Now().AddDate(0, 0, -days).UnixMilli()
	members, err := s.Store.Members(g.ID, 100000, "0")
	if err != nil {
		fail(c, err)
		return
	}
	for _, m := range members {
		if m.User == nil || m.User.ID == g.OwnerID {
			continue
		}
		joined, err := time.Parse(time.RFC3339, m.JoinedAt)
		if err == nil && joined.UnixMilli() < cutoff {
			count++
		}
	}
	c.JSON(map[string]any{"prune_count": count})
}

// handleBeginPrune kicks inactive members and reports the count.
func (s *Server) handleBeginPrune(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.KickMembers)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		Days              int      `json:"days"`
		ComputePruneCount *bool    `json:"compute_prune_count"`
		IncludeRoles      []string `json:"include_roles"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.Days < 1 || req.Days > 30 {
		c.FailForm("days", "Must be between 1 and 30")
		return
	}
	for _, roleID := range req.IncludeRoles {
		if _, err := s.Store.Role(g.ID, roleID); err != nil {
			c.FailForm("include_roles", "Unknown role")
			return
		}
	}
	removed, err := s.Store.PruneMembers(g.ID, req.Days, req.IncludeRoles)
	if err != nil {
		fail(c, err)
		return
	}
	s.audit(c, g.ID, store.AuditMemberPrune, "", []store.AuditChange{
		{Key: "delete_member_days", NewValue: req.Days},
		{Key: "members_removed", NewValue: removed},
	})
	c.JSON(map[string]any{"pruned": removed})
}

// handleBulkBan bans up to 200 users in one request.
func (s *Server) handleBulkBan(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.BanMembers)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		UserIDs              []string `json:"user_ids"`
		DeleteMessageSeconds int      `json:"delete_message_seconds"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if len(req.UserIDs) == 0 || len(req.UserIDs) > 200 {
		c.FailForm("user_ids", "Must contain between 1 and 200 ids")
		return
	}
	if req.DeleteMessageSeconds < 0 || req.DeleteMessageSeconds > 604800 {
		c.FailForm("delete_message_seconds", "Must be between 0 and 604800")
		return
	}
	banned, failed := []string{}, []string{}
	for _, id := range req.UserIDs {
		if _, err := s.Store.UserByID(id); err != nil || id == g.OwnerID {
			failed = append(failed, id)
			continue
		}
		if err := s.Store.BanUser(g.ID, id, ""); err != nil {
			failed = append(failed, id)
			continue
		}
		banned = append(banned, id)
		s.audit(c, g.ID, store.AuditMemberBanAdd, id, nil)
		if s.Hub != nil {
			s.Hub.DispatchGuild(g.ID, "GUILD_BAN_ADD", map[string]any{"guild_id": g.ID, "user": model.User{ID: id}})
		}
	}
	c.JSON(map[string]any{"banned_users": banned, "failed_users": failed})
}

// handleIncidentActions sets the guild's invite/DM lockdown timestamps.
func (s *Server) handleIncidentActions(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		InvitesDisabledUntil *string `json:"invites_disabled_until"`
		DMSDisabledUntil     *string `json:"dms_disabled_until"`
		DMSDisabled          *bool   `json:"dms_disabled"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	parse := func(raw *string) (*int64, bool) {
		if raw == nil {
			return nil, true
		}
		if *raw == "" {
			zero := int64(0)
			return &zero, true
		}
		t, err := time.Parse(time.RFC3339, *raw)
		if err != nil {
			return nil, false
		}
		ms := t.UnixMilli()
		return &ms, true
	}
	invites, ok := parse(req.InvitesDisabledUntil)
	if !ok {
		c.FailForm("invites_disabled_until", "Must be an ISO8601 timestamp")
		return
	}
	dms, ok := parse(req.DMSDisabledUntil)
	if !ok {
		c.FailForm("dms_disabled_until", "Must be an ISO8601 timestamp")
		return
	}
	if err := s.Store.SetIncidentActions(g.ID, invites, dms, req.DMSDisabled); err != nil {
		fail(c, err)
		return
	}
	updated, err := s.Store.Guild(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_UPDATE", updated)
	}
	c.NoContent()
}

// handleRolePositions applies the bulk role position update.
func (s *Server) handleRolePositions(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageRoles)
	if err != nil {
		fail(c, err)
		return
	}
	var req []struct {
		ID       string `json:"id"`
		Position *int   `json:"position"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	positions := map[string]int{}
	for _, entry := range req {
		if entry.Position == nil {
			continue
		}
		if entry.ID == g.ID {
			c.FailForm("id", "The @everyone role cannot be moved")
			return
		}
		if _, err := s.Store.Role(g.ID, entry.ID); err != nil {
			c.FailForm("id", "Unknown role")
			return
		}
		positions[entry.ID] = *entry.Position
	}
	if len(positions) > 0 {
		if err := s.Store.SetRolePositions(g.ID, positions); err != nil {
			fail(c, err)
			return
		}
		s.audit(c, g.ID, store.AuditRoleUpdate, "", nil)
	}
	roles, err := s.Store.Roles(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(roles)
}

// handleModifyCurrentMember patches the caller's own membership.
func (s *Server) handleModifyCurrentMember(c *Ctx) {
	s.handleMemberSelfPatch(c, false)
}

// handleModifyCurrentNick patches only the caller's nickname.
func (s *Server) handleModifyCurrentNick(c *Ctx) {
	s.handleMemberSelfPatch(c, true)
}

func (s *Server) handleMemberSelfPatch(c *Ctx, nickOnly bool) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		Nick *string `json:"nick"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.Nick != nil && len([]rune(*req.Nick)) > 32 {
		c.FailForm("nick", "Must be 32 characters or fewer")
		return
	}
	if nickOnly && req.Nick == nil {
		c.FailForm("nick", "This field is required")
		return
	}
	member, err := s.Store.UpdateMember(g.ID, c.User.ID, store.UpdateMemberParams{Nick: req.Nick})
	if err != nil {
		fail(c, err)
		return
	}
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_MEMBER_UPDATE", member)
	}
	c.JSON(member)
}

// handleGetOwnVoiceState returns the caller's voice state in the guild.
func (s *Server) handleGetOwnVoiceState(c *Ctx) {
	s.voiceStateFor(c, c.User.ID)
}

// handleGetUserVoiceState returns another member's voice state.
func (s *Server) handleGetUserVoiceState(c *Ctx) {
	s.voiceStateFor(c, c.Param("userID"))
}

func (s *Server) voiceStateFor(c *Ctx, userID string) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	if _, err := s.Store.Member(g.ID, userID); err != nil {
		fail(c, ErrUnknownMember)
		return
	}
	vs, err := s.Store.VoiceState(g.ID, userID)
	if err != nil {
		fail(c, NewError(404, 10065, "Unknown Voice State"))
		return
	}
	c.JSON(vs)
}

// handlePatchOwnVoiceState moves the caller between channels.
func (s *Server) handlePatchOwnVoiceState(c *Ctx) {
	s.patchVoiceState(c, c.User.ID, false)
}

// handlePatchUserVoiceState moves another member (MOVE_MEMBERS).
func (s *Server) handlePatchUserVoiceState(c *Ctx) {
	s.patchVoiceState(c, c.Param("userID"), true)
}

func (s *Server) patchVoiceState(c *Ctx, userID string, other bool) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	if other {
		if _, err := s.requireGuild(c.User.ID, g.ID, perm.MoveMembers); err != nil {
			fail(c, err)
			return
		}
	}
	var req struct {
		ChannelID               *string `json:"channel_id"`
		Suppress                *bool   `json:"suppress"`
		RequestToSpeakTimestamp *string `json:"request_to_speak_timestamp"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	vs, err := s.Store.VoiceState(g.ID, userID)
	if err != nil {
		fail(c, NewError(404, 10065, "Unknown Voice State"))
		return
	}
	if req.ChannelID != nil {
		if *req.ChannelID == "" {
			vs.ChannelID = nil
		} else {
			ch, err := s.Store.Channel(*req.ChannelID)
			if err != nil || ch.GuildID == nil || *ch.GuildID != g.ID {
				c.FailForm("channel_id", "Must be a channel in this guild")
				return
			}
			vs.ChannelID = &ch.ID
		}
	}
	if req.Suppress != nil {
		vs.Suppress = *req.Suppress
	}
	if req.RequestToSpeakTimestamp != nil {
		if *req.RequestToSpeakTimestamp == "" {
			vs.RequestToSpeakTimestamp = nil
		} else {
			if _, err := time.Parse(time.RFC3339, *req.RequestToSpeakTimestamp); err != nil {
				c.FailForm("request_to_speak_timestamp", "Must be an ISO8601 timestamp")
				return
			}
			vs.RequestToSpeakTimestamp = req.RequestToSpeakTimestamp
		}
	}
	updated, err := s.Store.PutVoiceState(vs, vs.Token)
	if err != nil {
		fail(c, err)
		return
	}
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "VOICE_STATE_UPDATE", updated)
	}
	c.JSON(updated)
}

// handleDeleteRoleConnection clears the caller's role connection for an app.
func (s *Server) handleDeleteRoleConnection(c *Ctx) {
	if err := s.Store.DeleteRoleConnection(c.User.ID, c.Param("applicationID")); err != nil && !errors.Is(err, store.ErrNotFound) {
		fail(c, err)
		return
	}
	c.NoContent()
}

// handlePatchMyApplication applies a patch to the bot's own application.
// handleUpdateApplication resolves the literal "@me" itself.
func (s *Server) handlePatchMyApplication(c *Ctx) {
	c.Params["applicationID"] = "@me"
	s.handleUpdateApplication(c)
}

// handleGetStickerPack returns one sticker pack from the catalogue. The
// catalogue is empty in this server, so every pack id is unknown.
func (s *Server) handleGetStickerPack(c *Ctx) {
	fail(c, NewError(404, 10060, "Unknown Sticker Pack"))
}

// handleSendSoundboardSound plays a soundboard sound in a voice channel.
func (s *Server) handleSendSoundboardSound(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.UseSoundboard)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		SoundID       string  `json:"sound_id"`
		SourceGuildID *string `json:"source_guild_id"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.SoundID == "" {
		c.FailForm("sound_id", "This field is required")
		return
	}
	if ch.GuildID == nil {
		fail(c, ErrGuildOnly)
		return
	}
	if _, err := s.Store.VoiceState(*ch.GuildID, c.User.ID); err != nil {
		fail(c, NewError(400, 40032, "You must be in a voice channel to use soundboard sounds"))
		return
	}
	if s.Hub != nil {
		s.Hub.DispatchGuild(*ch.GuildID, "VOICE_CHANNEL_EFFECT_SEND", map[string]any{
			"channel_id": ch.ID, "user_id": c.User.ID, "sound_id": req.SoundID,
		})
	}
	c.NoContent()
}

// queryInt reads a bounded integer query parameter.
func queryInt(c *Ctx, name string, min, max, def int) int {
	raw := c.R.URL.Query().Get(name)
	if raw == "" {
		return def
	}
	v := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			return def
		}
		v = v*10 + int(r-'0')
		if v > max {
			return max
		}
	}
	if v < min {
		return min
	}
	return v
}
