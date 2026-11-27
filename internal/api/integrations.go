package api

import (
	"strings"

	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

// integrationTypes maps the documented string type to the stored id.
var integrationTypes = map[string]int{
	"twitch":  store.IntegrationTwitch,
	"youtube": store.IntegrationYouTube,
}

func (s *Server) handleListIntegrations(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	list, err := s.Store.Integrations(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	q := c.R.URL.Query()
	if q.Get("has_commands") == "true" {
		withCommands, err := s.Store.ApplicationsWithCommands(g.ID)
		if err != nil {
			fail(c, err)
			return
		}
		filtered := list[:0]
		for _, in := range list {
			if in.TypeID == store.IntegrationDiscord && withCommands[in.ID] {
				filtered = append(filtered, in)
			}
		}
		list = filtered
	}
	if q.Get("include_role_connections_metadata") == "true" {
		for i := range list {
			if list[i].TypeID != store.IntegrationDiscord {
				continue
			}
			app, err := s.Store.Application(list[i].ID)
			if err != nil {
				continue
			}
			if list[i].Application == nil {
				list[i].Application = map[string]any{"id": app.ID, "name": app.Name}
			}
			list[i].Application["role_connections_metadata"] = app.RoleConnectionMetadata
		}
	}
	c.JSON(list)
}

func (s *Server) handleCreateIntegration(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		Type        string  `json:"type"`
		ID          string  `json:"id"`
		AccountName *string `json:"account_name"`
		ChannelID   *string `json:"channel_id"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	typ, ok := integrationTypes[strings.ToLower(req.Type)]
	if !ok {
		c.FailForm("type", "Only twitch and youtube integrations can be enabled")
		return
	}
	accountID := strings.TrimSpace(req.ID)
	if accountID == "" {
		c.FailForm("id", "This field is required")
		return
	}
	accountName := accountID
	if req.AccountName != nil && *req.AccountName != "" {
		accountName = sanitizeName(*req.AccountName)
	}
	if req.ChannelID != nil {
		ch, err := s.requireChannel(c.User.ID, *req.ChannelID, perm.ManageChannels)
		if err != nil {
			fail(c, err)
			return
		}
		if ch.GuildID == nil || *ch.GuildID != g.ID {
			c.FailForm("channel_id", "Must be a channel in this guild")
			return
		}
		if _, err := s.Store.CreateChannelIntegration(g.ID, ch.ID, typ, accountID, accountName, c.User.ID); err != nil {
			fail(c, err)
			return
		}
	} else if _, err := s.Store.CreateIntegration(g.ID, typ, accountID, accountName, c.User.ID); err != nil {
		fail(c, err)
		return
	}
	s.audit(c, g.ID, 80, accountID, nil)
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_INTEGRATIONS_UPDATE", map[string]any{"guild_id": g.ID})
	}
	c.NoContent()
}

func (s *Server) handleGetIntegration(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	in, err := s.Store.Integration(g.ID, c.Param("integrationID"))
	if err != nil {
		fail(c, NewError(404, 10000, "Unknown Integration"))
		return
	}
	c.JSON(in)
}

func (s *Server) handleUpdateIntegration(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	in, err := s.Store.Integration(g.ID, c.Param("integrationID"))
	if err != nil {
		fail(c, NewError(404, 10000, "Unknown Integration"))
		return
	}
	var req struct {
		EnableEmoticons   *bool `json:"enable_emoticons"`
		ExpireBehavior    *int  `json:"expire_behavior"`
		ExpireGracePeriod *int  `json:"expire_grace_period"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.ExpireBehavior != nil && (*req.ExpireBehavior < 0 || *req.ExpireBehavior > 1) {
		c.FailForm("expire_behavior", "Must be 0 or 1")
		return
	}
	if req.ExpireGracePeriod != nil && !validGracePeriod(*req.ExpireGracePeriod) {
		c.FailForm("expire_grace_period", "Must be one of 1, 3, 7, 14 or 30")
		return
	}
	if in.TypeID == store.IntegrationDiscord && (req.EnableEmoticons != nil || req.ExpireBehavior != nil || req.ExpireGracePeriod != nil) {
		c.FailForm("expire_behavior", "Only Twitch and YouTube integrations have subscriber settings")
		return
	}
	if _, err := s.Store.UpdateIntegration(g.ID, in.ID, req.EnableEmoticons, req.ExpireBehavior, req.ExpireGracePeriod); err != nil {
		fail(c, NewError(404, 10000, "Unknown Integration"))
		return
	}
	s.audit(c, g.ID, 81, in.ID, nil)
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "INTEGRATION_UPDATE", map[string]any{
			"guild_id": g.ID, "integration": map[string]any{"id": in.ID},
		})
	}
	c.NoContent()
}

func validGracePeriod(v int) bool {
	switch v {
	case 1, 3, 7, 14, 30:
		return true
	}
	return false
}

func (s *Server) handleSyncIntegration(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	in, err := s.Store.Integration(g.ID, c.Param("integrationID"))
	if err != nil {
		fail(c, NewError(404, 10000, "Unknown Integration"))
		return
	}
	if in.TypeID == store.IntegrationDiscord {
		c.FailForm("integration_id", "Only Twitch and YouTube integrations can be synced")
		return
	}
	synced, err := s.Store.SyncIntegration(g.ID, in.ID)
	if err != nil {
		fail(c, NewError(404, 10000, "Unknown Integration"))
		return
	}
	s.audit(c, g.ID, 81, in.ID, nil)
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "INTEGRATION_UPDATE", map[string]any{
			"guild_id": g.ID, "integration": map[string]any{"id": synced.ID},
		})
	}
	c.NoContent()
}

func (s *Server) handleDeleteIntegration(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	id := c.Param("integrationID")
	botID, err := s.Store.DeleteIntegration(g.ID, id)
	if err != nil {
		fail(c, NewError(404, 10000, "Unknown Integration"))
		return
	}
	if botID != "" {
		_ = s.Store.RemoveMember(g.ID, botID)
		if s.Hub != nil {
			s.Hub.DispatchGuild(g.ID, "GUILD_MEMBER_REMOVE", map[string]any{"guild_id": g.ID, "user": map[string]any{"id": botID}})
		}
	}
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_INTEGRATIONS_UPDATE", map[string]any{"guild_id": g.ID})
		s.Hub.DispatchGuild(g.ID, "INTEGRATION_DELETE", map[string]any{
			"guild_id": g.ID, "id": id, "application_id": botID,
		})
	}
	c.NoContent()
}

// handleChannelIntegrations lists integrations attached to a private channel.
func (s *Server) handleChannelIntegrations(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ViewChannel)
	if err != nil {
		fail(c, err)
		return
	}
	list, err := s.Store.ChannelIntegrations(ch.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(list)
}

func (s *Server) handleDeleteChannelIntegration(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ViewChannel)
	if err != nil {
		fail(c, err)
		return
	}
	if ch.GuildID != nil {
		if _, err := s.requireGuild(c.User.ID, *ch.GuildID, perm.ManageChannels); err != nil {
			fail(c, err)
			return
		}
	}
	if err := s.Store.DeleteChannelIntegration(ch.ID, c.Param("integrationID")); err != nil {
		fail(c, NewError(404, 10000, "Unknown Integration"))
		return
	}
	if s.Hub != nil && ch.GuildID != nil {
		s.Hub.DispatchGuild(*ch.GuildID, "INTEGRATION_DELETE", map[string]any{
			"guild_id": *ch.GuildID, "id": c.Param("integrationID"), "channel_id": ch.ID,
		})
	}
	c.NoContent()
}

// handleMigrateCommandScope marks Discord integrations in the guild as having
// the applications.commands scope and reports their ids.
func (s *Server) handleMigrateCommandScope(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	withCommands, err := s.Store.ApplicationsWithCommands(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	list, err := s.Store.Integrations(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	ids := []string{}
	for _, in := range list {
		if in.TypeID != store.IntegrationDiscord || !withCommands[in.ID] {
			continue
		}
		ids = append(ids, in.ID)
	}
	if s.Hub != nil && len(ids) > 0 {
		s.Hub.DispatchGuild(g.ID, "GUILD_INTEGRATIONS_UPDATE", map[string]any{"guild_id": g.ID})
		for _, id := range ids {
			s.Hub.DispatchGuild(g.ID, "INTEGRATION_UPDATE", map[string]any{
				"guild_id": g.ID, "integration": map[string]any{"id": id},
			})
		}
	}
	c.JSON(map[string]any{"integration_ids_with_app_commands": ids})
}

// handleIntegrationApplicationIDs maps the caller's guilds to the application
// ids attached to their integrations.
func (s *Server) handleIntegrationApplicationIDs(c *Ctx) {
	out, err := s.Store.GuildsWithIntegrationApplications(c.User.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(out)
}
