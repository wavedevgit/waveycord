package api

import (
	"errors"
	"net/url"
	"sort"

	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

type createGuildRequest struct {
	Name                        string  `json:"name"`
	Icon                        *string `json:"icon"`
	VerificationLevel           *int    `json:"verification_level"`
	DefaultMessageNotifications *int    `json:"default_message_notifications"`
	ExplicitContentFilter       *int    `json:"explicit_content_filter"`
	AFKChannelID                *string `json:"afk_channel_id"`
	AFKTimeout                  *int    `json:"afk_timeout"`
	SystemChannelID             *string `json:"system_channel_id"`
}

func (s *Server) handleCreateGuild(c *Ctx) {
	var req createGuildRequest
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	name := sanitizeName(req.Name)
	if len([]rune(name)) < 2 || len([]rune(name)) > 100 {
		c.FailForm("name", "Must be between 2 and 100 characters")
		return
	}
	g, err := s.Store.CreateGuild(c.User.ID, store.CreateGuildParams{
		Name: name, Icon: req.Icon, Verification: req.VerificationLevel,
		Notifications: req.DefaultMessageNotifications, ContentFilter: req.ExplicitContentFilter,
		AFKChannelID: req.AFKChannelID, AFKTimeout: req.AFKTimeout,
		SystemChannelID: req.SystemChannelID,
	})
	if err != nil {
		fail(c, err)
		return
	}
	if s.Hub != nil {
		s.Hub.DispatchUser(c.User.ID, "GUILD_CREATE", g)
	}
	c.JSONStatus(201, g)
}

func (s *Server) handleGetGuild(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	withCounts := c.R.URL.Query().Get("with_counts") == "true"
	if withCounts {
		n, _ := s.Store.MemberCount(g.ID)
		g.ApproximateMemberCount = &n
		zero := 0
		g.ApproximatePresenceCount = &zero
	}
	g.Permissions = strPtr(perm.Format(mustPerms(s.guildPermissions(c.User.ID, g.ID))))
	c.JSON(g)
}

func mustPerms(v uint64, _ error) uint64 { return v }

type updateGuildRequest struct {
	Name                        *string `json:"name"`
	Icon                        *string `json:"icon"`
	Banner                      *string `json:"banner"`
	Splash                      *string `json:"splash"`
	Description                 *string `json:"description"`
	VerificationLevel           *int    `json:"verification_level"`
	DefaultMessageNotifications *int    `json:"default_message_notifications"`
	ExplicitContentFilter       *int    `json:"explicit_content_filter"`
	AFKChannelID                *string `json:"afk_channel_id"`
	AFKTimeout                  *int    `json:"afk_timeout"`
	SystemChannelID             *string `json:"system_channel_id"`
	RulesChannelID              *string `json:"rules_channel_id"`
	PreferredLocale             *string `json:"preferred_locale"`
	OwnerID                     *string `json:"owner_id"`
}

func (s *Server) handleUpdateGuild(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	var req updateGuildRequest
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.Name != nil {
		name := sanitizeName(*req.Name)
		if len([]rune(name)) < 2 || len([]rune(name)) > 100 {
			c.FailForm("name", "Must be between 2 and 100 characters")
			return
		}
		req.Name = &name
	}
	// Only the owner may transfer ownership.
	if req.OwnerID != nil && g.OwnerID != c.User.ID {
		fail(c, ErrMissingPerms)
		return
	}
	updated, err := s.Store.UpdateGuild(g.ID, store.UpdateGuildParams{
		Name: req.Name, Icon: req.Icon, Banner: req.Banner, Splash: req.Splash,
		Description: req.Description, Verification: req.VerificationLevel,
		Notifications: req.DefaultMessageNotifications, ContentFilter: req.ExplicitContentFilter,
		AFKChannelID: req.AFKChannelID, AFKTimeout: req.AFKTimeout,
		SystemChannelID: req.SystemChannelID, RulesChannelID: req.RulesChannelID,
		PreferredLocale: req.PreferredLocale, OwnerID: req.OwnerID,
	})
	if err != nil {
		fail(c, err)
		return
	}
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_UPDATE", updated)
	}
	c.JSON(updated)
}

func (s *Server) handleDeleteGuild(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	if g.OwnerID != c.User.ID {
		fail(c, ErrMissingPerms)
		return
	}
	members, _ := s.Store.Members(g.ID, 100, "")
	if err := s.Store.DeleteGuild(g.ID); err != nil {
		fail(c, err)
		return
	}
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_DELETE", map[string]any{"id": g.ID, "unavailable": false})
		for _, m := range members {
			if m.User != nil {
				s.Hub.DispatchUser(m.User.ID, "GUILD_DELETE", map[string]any{"id": g.ID, "unavailable": false})
			}
		}
	}
	c.NoContent()
}

func (s *Server) handleLeaveGuild(c *Ctx) {
	guildID := c.Param("guildID")
	if _, err := s.Store.Member(guildID, c.User.ID); err != nil {
		fail(c, ErrUnknownMember)
		return
	}
	if err := s.Store.RemoveMember(guildID, c.User.ID); err != nil {
		fail(c, err)
		return
	}
	if s.Hub != nil {
		s.Hub.DispatchUser(c.User.ID, "GUILD_DELETE", map[string]any{"id": guildID, "unavailable": false})
		s.Hub.DispatchGuild(guildID, "GUILD_MEMBER_REMOVE", map[string]any{"guild_id": guildID, "user": c.User})
	}
	c.NoContent()
}

func (s *Server) handleGuildPreview(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	members, _ := s.Store.MemberCount(g.ID)
	zero := 0
	c.JSON(map[string]any{
		"id":                         g.ID,
		"name":                       g.Name,
		"icon":                       g.Icon,
		"description":                g.Description,
		"splash":                     g.Splash,
		"discovery_splash":           g.DiscoverySplash,
		"emojis":                     g.Emojis,
		"features":                   g.Features,
		"approximate_member_count":   members,
		"approximate_presence_count": zero,
		"stickers":                   []any{},
	})
}

func (s *Server) handleGuildChannels(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	channels, err := s.Store.GuildChannels(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	// A member without VIEW_CHANNEL must not see the channel.
	out := make([]model.Channel, 0, len(channels))
	for _, ch := range channels {
		have, err := s.channelPermissions(c.User.ID, ch)
		if err != nil || !perm.Has(have, perm.ViewChannel) {
			continue
		}
		out = append(out, ch)
	}
	c.JSON(out)
}

type createChannelRequest struct {
	Name                 string            `json:"name"`
	Type                 *int              `json:"type"`
	Topic                *string           `json:"topic"`
	NSFW                 *bool             `json:"nsfw"`
	Bitrate              *int              `json:"bitrate"`
	UserLimit            *int              `json:"user_limit"`
	RateLimitPerUser     *int              `json:"rate_limit_per_user"`
	Position             *int              `json:"position"`
	ParentID             *string           `json:"parent_id"`
	PermissionOverwrites []model.Overwrite `json:"permission_overwrites"`
}

func (s *Server) handleCreateGuildChannel(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageChannels)
	if err != nil {
		fail(c, err)
		return
	}
	var req createChannelRequest
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	name := sanitizeName(req.Name)
	if name == "" || len([]rune(name)) > 100 {
		c.FailForm("name", "Must be between 1 and 100 characters")
		return
	}
	typ := store.ChannelText
	if req.Type != nil {
		typ = *req.Type
	}
	switch typ {
	case store.ChannelText, store.ChannelVoice, store.ChannelCategory, store.ChannelAnnouncement,
		store.ChannelStage, store.ChannelForum, store.ChannelMedia:
	default:
		c.FailForm("type", "Value must be one of {0, 2, 4, 5, 13, 15, 16}")
		return
	}
	if req.RateLimitPerUser != nil && (*req.RateLimitPerUser < 0 || *req.RateLimitPerUser > 21600) {
		c.FailForm("rate_limit_per_user", "Must be between 0 and 21600")
		return
	}
	ch, err := s.Store.CreateChannel(g.ID, store.CreateChannelParams{
		Type: typ, Name: &name, Topic: req.Topic, NSFW: req.NSFW, Bitrate: req.Bitrate,
		UserLimit: req.UserLimit, RateLimitPerUser: req.RateLimitPerUser,
		Position: req.Position, ParentID: req.ParentID,
		PermissionOverwrites: req.PermissionOverwrites,
	})
	if err != nil {
		fail(c, err)
		return
	}
	s.audit(c, g.ID, store.AuditChannelCreate, ch.ID, []store.AuditChange{{Key: "name", NewValue: ch.Name}})
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "CHANNEL_CREATE", ch)
	}
	c.JSONStatus(201, ch)
}

func (s *Server) handleModifyChannelPositions(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageChannels)
	if err != nil {
		fail(c, err)
		return
	}
	var req []struct {
		ID              string  `json:"id"`
		Position        *int    `json:"position"`
		ParentID        *string `json:"parent_id"`
		LockPermissions *bool   `json:"lock_permissions"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	for _, item := range req {
		ch, err := s.Store.Channel(item.ID)
		if err != nil || ch.GuildID == nil || *ch.GuildID != g.ID {
			fail(c, ErrUnknownChannel)
			return
		}
		if _, err := s.Store.UpdateChannel(ch.ID, store.UpdateChannelParams{
			Position: item.Position, ParentID: item.ParentID,
		}); err != nil {
			fail(c, err)
			return
		}
	}
	c.NoContent()
}

func (s *Server) handleActiveThreads(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	threads, err := s.Store.ActiveThreads(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	members := []model.ThreadMember{}
	for _, th := range threads {
		if tm, err := s.Store.ThreadMembers(th.ID); err == nil {
			members = append(members, tm...)
		}
	}
	c.JSON(map[string]any{"threads": threads, "members": members, "has_more": false})
}

func (s *Server) handleListMembers(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	limit := 1
	if v := c.R.URL.Query().Get("limit"); v != "" {
		limit = atoiSafe(v)
	}
	members, err := s.Store.Members(g.ID, limit, c.R.URL.Query().Get("after"))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(members)
}

func (s *Server) handleGetMember(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	m, err := s.Store.Member(g.ID, c.Param("userID"))
	if err != nil {
		fail(c, ErrUnknownMember)
		return
	}
	c.JSON(m)
}

type updateMemberRequest struct {
	Nick      *string   `json:"nick"`
	Roles     *[]string `json:"roles"`
	Mute      *bool     `json:"mute"`
	Deaf      *bool     `json:"deaf"`
	ChannelID *string   `json:"channel_id"`
}

func (s *Server) handleUpdateMember(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	target := c.Param("userID")
	var req updateMemberRequest
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	have, err := s.guildPermissions(c.User.ID, g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	if req.Nick != nil {
		if target != c.User.ID && !perm.Has(have, perm.ManageNicknames) {
			fail(c, ErrMissingPerms)
			return
		}
		if target == c.User.ID && !perm.Has(have, perm.ChangeNickname) && !perm.Has(have, perm.ManageNicknames) {
			fail(c, ErrMissingPerms)
			return
		}
		if len([]rune(*req.Nick)) > 32 {
			c.FailForm("nick", "Must be 32 characters or fewer")
			return
		}
	}
	if req.Roles != nil && !perm.Has(have, perm.ManageRoles) {
		fail(c, ErrMissingPerms)
		return
	}
	if (req.Mute != nil || req.Deaf != nil) && !perm.Has(have, perm.MuteMembers) && !perm.Has(have, perm.DeafenMembers) {
		fail(c, ErrMissingPerms)
		return
	}
	m, err := s.Store.UpdateMember(g.ID, target, store.UpdateMemberParams{
		Nick: req.Nick, Roles: req.Roles, Mute: req.Mute, Deaf: req.Deaf, ChannelID: req.ChannelID,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, ErrUnknownMember)
			return
		}
		fail(c, err)
		return
	}
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_MEMBER_UPDATE", m)
	}
	s.enforceProfileAutoMod(g.ID, target)
	c.JSON(m)
}

func (s *Server) handleKickMember(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.KickMembers)
	if err != nil {
		fail(c, err)
		return
	}
	target := c.Param("userID")
	if target == g.OwnerID {
		fail(c, NewError(403, 50013, "Missing Permissions"))
		return
	}
	if err := s.Store.RemoveMember(g.ID, target); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, ErrUnknownMember)
			return
		}
		fail(c, err)
		return
	}
	s.audit(c, g.ID, store.AuditMemberKick, target, nil)
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_MEMBER_REMOVE", map[string]any{"guild_id": g.ID, "user": model.User{ID: target}})
	}
	c.NoContent()
}

func (s *Server) handleAddMemberRole(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageRoles)
	if err != nil {
		fail(c, err)
		return
	}
	if _, err := s.Store.Role(g.ID, c.Param("roleID")); err != nil {
		fail(c, ErrUnknownRole)
		return
	}
	if err := s.Store.AddMemberRole(g.ID, c.Param("userID"), c.Param("roleID")); err != nil {
		fail(c, ErrUnknownMember)
		return
	}
	c.NoContent()
}

func (s *Server) handleRemoveMemberRole(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageRoles)
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.RemoveMemberRole(g.ID, c.Param("userID"), c.Param("roleID")); err != nil {
		fail(c, ErrUnknownMember)
		return
	}
	c.NoContent()
}

func (s *Server) handleListRoles(c *Ctx) {
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
	c.JSON(roles)
}

type roleRequest struct {
	Name        *string `json:"name"`
	Permissions *string `json:"permissions"`
	Color       *int    `json:"color"`
	Hoist       *bool   `json:"hoist"`
	Mentionable *bool   `json:"mentionable"`
	Position    *int    `json:"position"`
}

func (s *Server) handleCreateRole(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageRoles)
	if err != nil {
		fail(c, err)
		return
	}
	var req roleRequest
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	name := "new role"
	if req.Name != nil {
		name = sanitizeName(*req.Name)
	}
	p := store.CreateRoleParams{Name: name}
	if req.Permissions != nil {
		if _, err := perm.Parse(*req.Permissions); err != nil {
			c.FailForm("permissions", "Invalid bitfield")
			return
		}
		p.Permissions = *req.Permissions
	}
	if req.Color != nil {
		p.Color = *req.Color
	}
	if req.Hoist != nil {
		p.Hoist = *req.Hoist
	}
	if req.Mentionable != nil {
		p.Mentionable = *req.Mentionable
	}
	role, err := s.Store.CreateRole(g.ID, p)
	if err != nil {
		fail(c, err)
		return
	}
	s.audit(c, g.ID, store.AuditRoleCreate, role.ID, []store.AuditChange{{Key: "name", NewValue: role.Name}})
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_ROLE_CREATE", map[string]any{"guild_id": g.ID, "role": role})
	}
	c.JSONStatus(201, role)
}

func (s *Server) handleUpdateRole(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageRoles)
	if err != nil {
		fail(c, err)
		return
	}
	var req roleRequest
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.Permissions != nil {
		if _, err := perm.Parse(*req.Permissions); err != nil {
			c.FailForm("permissions", "Invalid bitfield")
			return
		}
	}
	if req.Name != nil {
		name := sanitizeName(*req.Name)
		req.Name = &name
	}
	role, err := s.Store.UpdateRole(g.ID, c.Param("roleID"), store.UpdateRoleParams{
		Name: req.Name, Permissions: req.Permissions, Color: req.Color, Hoist: req.Hoist,
		Mentionable: req.Mentionable, Position: req.Position,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, ErrUnknownRole)
			return
		}
		fail(c, err)
		return
	}
	s.audit(c, g.ID, store.AuditRoleUpdate, role.ID, nil)
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_ROLE_UPDATE", map[string]any{"guild_id": g.ID, "role": role})
	}
	c.JSON(role)
}

func (s *Server) handleDeleteRole(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageRoles)
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.DeleteRole(g.ID, c.Param("roleID")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, ErrUnknownRole)
			return
		}
		fail(c, err)
		return
	}
	s.audit(c, g.ID, store.AuditRoleDelete, c.Param("roleID"), nil)
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_ROLE_DELETE", map[string]any{"guild_id": g.ID, "role_id": c.Param("roleID")})
	}
	c.NoContent()
}

func (s *Server) handleListBans(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.BanMembers)
	if err != nil {
		fail(c, err)
		return
	}
	bans, err := s.Store.Bans(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(bans)
}

func (s *Server) handleGetBan(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.BanMembers)
	if err != nil {
		fail(c, err)
		return
	}
	bans, err := s.Store.Bans(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	for _, b := range bans {
		if b.User.ID == c.Param("userID") {
			c.JSON(b)
			return
		}
	}
	fail(c, ErrUnknownBan)
}

func (s *Server) handleCreateBan(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.BanMembers)
	if err != nil {
		fail(c, err)
		return
	}
	target := c.Param("userID")
	if _, err := s.Store.UserByID(target); err != nil {
		fail(c, ErrUnknownUser)
		return
	}
	if target == g.OwnerID {
		fail(c, NewError(403, 50013, "Missing Permissions"))
		return
	}
	reason := decodeAuditReason(c.R.Header.Get("X-Audit-Log-Reason"))
	if err := s.Store.BanUser(g.ID, target, reason); err != nil {
		fail(c, err)
		return
	}
	s.audit(c, g.ID, store.AuditMemberBanAdd, target, nil)
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_BAN_ADD", map[string]any{"guild_id": g.ID, "user": model.User{ID: target}})
	}
	c.NoContent()
}

func (s *Server) handleDeleteBan(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.BanMembers)
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.UnbanUser(g.ID, c.Param("userID")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, ErrUnknownBan)
			return
		}
		fail(c, err)
		return
	}
	s.audit(c, g.ID, store.AuditMemberBanRemove, c.Param("userID"), nil)
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_BAN_REMOVE", map[string]any{"guild_id": g.ID, "user": model.User{ID: c.Param("userID")}})
	}
	c.NoContent()
}

func (s *Server) handleGuildInvites(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	invites, err := s.Store.GuildInvites(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(invites)
}

func (s *Server) handleGuildEmojis(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	emojis, err := s.Store.Emojis(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(emojis)
}

func (s *Server) handleVanityURL(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"code": g.VanityURLCode, "uses": 0})
}

func (s *Server) handleVoiceRegions(c *Ctx) {
	c.JSON([]map[string]any{
		{"id": "us-east", "name": "US East", "optimal": false, "deprecated": false, "custom": false},
		{"id": "us-west", "name": "US West", "optimal": false, "deprecated": false, "custom": false},
		{"id": "eu-central", "name": "Central Europe", "optimal": false, "deprecated": false, "custom": false},
	})
}

func decodeAuditReason(raw string) string {
	if raw == "" {
		return ""
	}
	if v, err := url.PathUnescape(raw); err == nil {
		return v
	}
	return raw
}

func atoiSafe(v string) int {
	n := 0
	for _, r := range v {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

var _ = sort.Ints
