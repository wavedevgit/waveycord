package api

import (
	"errors"
	"strings"

	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

func (s *Server) handleGuildStickers(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	stickers, err := s.Store.GuildStickers(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(stickers)
}

func (s *Server) handleCreateSticker(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	existing, _ := s.Store.GuildStickers(g.ID)
	if len(existing) >= 20 {
		c.JSONStatus(400, map[string]any{"code": 30039, "message": "Maximum number of stickers reached"})
		return
	}
	var req struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
		Tags        string  `json:"tags"`
		Image       *string `json:"image"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	name := strings.TrimSpace(req.Name)
	if len([]rune(name)) < 2 || len([]rune(name)) > 30 {
		c.FailForm("name", "Must be between 2 and 30 characters")
		return
	}
	if req.Description != nil && len([]rune(*req.Description)) > 100 {
		c.FailForm("description", "Must be 100 characters or fewer")
		return
	}
	tags := strings.TrimSpace(req.Tags)
	if tags == "" || len([]rune(tags)) > 200 {
		c.FailForm("tags", "Must be between 1 and 200 characters")
		return
	}
	sticker, err := s.Store.CreateSticker(g.ID, c.User.ID, name, req.Description, tags, 1)
	if err != nil {
		fail(c, err)
		return
	}
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_STICKERS_UPDATE", map[string]any{"guild_id": g.ID})
	}
	c.JSONStatus(201, sticker)
}

func (s *Server) handleGetSticker(c *Ctx) {
	sticker, err := s.Store.Sticker(c.Param("stickerID"))
	if err != nil {
		fail(c, NewError(404, 10060, "Unknown Sticker"))
		return
	}
	c.JSON(sticker)
}

func (s *Server) handleGetGuildSticker(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	sticker, err := s.Store.Sticker(c.Param("stickerID"))
	if err != nil || sticker.GuildID == nil || *sticker.GuildID != g.ID {
		fail(c, NewError(404, 10060, "Unknown Sticker"))
		return
	}
	c.JSON(sticker)
}

func (s *Server) handleUpdateSticker(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	sticker, err := s.Store.Sticker(c.Param("stickerID"))
	if err != nil || sticker.GuildID == nil || *sticker.GuildID != g.ID {
		fail(c, NewError(404, 10060, "Unknown Sticker"))
		return
	}
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Tags        *string `json:"tags"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.Name != nil && (len([]rune(*req.Name)) < 2 || len([]rune(*req.Name)) > 30) {
		c.FailForm("name", "Must be between 2 and 30 characters")
		return
	}
	if req.Description != nil && len([]rune(*req.Description)) > 100 {
		c.FailForm("description", "Must be 100 characters or fewer")
		return
	}
	if req.Tags != nil && (strings.TrimSpace(*req.Tags) == "" || len([]rune(*req.Tags)) > 200) {
		c.FailForm("tags", "Must be between 1 and 200 characters")
		return
	}
	updated, err := s.Store.UpdateSticker(g.ID, sticker.ID, req.Name, req.Description, req.Tags)
	if err != nil {
		fail(c, err)
		return
	}
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_STICKERS_UPDATE", map[string]any{"guild_id": g.ID})
	}
	c.JSON(updated)
}

func (s *Server) handleDeleteSticker(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.DeleteSticker(g.ID, c.Param("stickerID")); err != nil {
		fail(c, NewError(404, 10060, "Unknown Sticker"))
		return
	}
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_STICKERS_UPDATE", map[string]any{"guild_id": g.ID})
	}
	c.NoContent()
}

// --- guild widget ---

func (s *Server) handleGetWidget(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	enabled, channel, err := s.Store.Widget(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"enabled": enabled, "channel_id": channel})
}

func (s *Server) handleUpdateWidget(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		Enabled   *bool   `json:"enabled"`
		ChannelID *string `json:"channel_id"`
	}
	if err := c.Bind(&req); err != nil && (req.Enabled == nil && req.ChannelID == nil) {
		fail(c, ErrInvalidForm)
		return
	}
	enabled, channel, err := s.Store.Widget(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if req.ChannelID != nil {
		ch, err := s.Store.Channel(*req.ChannelID)
		if err != nil || ch.GuildID == nil || *ch.GuildID != g.ID {
			c.FailForm("channel_id", "Must belong to the guild")
			return
		}
		channel = req.ChannelID
	}
	if err := s.Store.SetWidget(g.ID, enabled, channel); err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"enabled": enabled, "channel_id": channel})
}

// handleWidgetJSON is the public widget payload.
func (s *Server) handleWidgetJSON(c *Ctx) {
	g, err := s.Store.Guild(c.Param("guildID"))
	if err != nil {
		fail(c, ErrUnknownGuild)
		return
	}
	enabled, _, err := s.Store.Widget(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	if !enabled {
		c.JSONStatus(403, map[string]any{"code": 50004, "message": "Widget Disabled"})
		return
	}
	channels, _ := s.Store.GuildChannels(g.ID)
	type widgetChannel struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Position int    `json:"position"`
	}
	list := make([]widgetChannel, 0, len(channels))
	for _, ch := range channels {
		if ch.Name == nil {
			continue
		}
		list = append(list, widgetChannel{ID: ch.ID, Name: *ch.Name, Position: *ch.Position})
	}
	c.JSON(map[string]any{
		"id": g.ID, "name": g.Name, "instant_invite": nil,
		"channels": list, "members": []any{}, "presence_count": 0,
	})
}

// --- bulk command permissions (deprecated endpoint) ---

func (s *Server) validatePermissionEntries(guildID string, entries []store.CommandPermissionEntry) string {
	seen := map[string]bool{}
	for _, e := range entries {
		if e.ID == "" || seen[e.ID] {
			return "Entries must have unique ids"
		}
		seen[e.ID] = true
		switch e.Type {
		case 1:
			if _, err := s.Store.Role(guildID, e.ID); err != nil {
				return "Role must belong to the guild"
			}
		case 2:
			if _, err := s.Store.UserByID(e.ID); err != nil {
				return "User must exist"
			}
		case 3:
			ch, err := s.Store.Channel(e.ID)
			if err != nil || ch.GuildID == nil || *ch.GuildID != guildID {
				return "Channel must belong to the guild"
			}
		default:
			return "type must be 1 (role), 2 (user) or 3 (channel)"
		}
	}
	return ""
}

func (s *Server) handleBulkCommandPermissions(c *Ctx) {
	app, g, ok := s.requireAppGuild(c, c.Param("applicationID"))
	if !ok {
		return
	}
	var req []struct {
		ID          string                         `json:"id"`
		Permissions []store.CommandPermissionEntry `json:"permissions"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	sets := map[string][]store.CommandPermissionEntry{}
	order := []string{}
	for _, item := range req {
		cmd, err := s.Store.Command(item.ID)
		if err != nil || cmd.ApplicationID != app.ID {
			fail(c, NewError(404, 10063, "Unknown application command"))
			return
		}
		if msg := s.validatePermissionEntries(g.ID, item.Permissions); msg != "" {
			c.FailForm("permissions", msg)
			return
		}
		sets[item.ID] = item.Permissions
		order = append(order, item.ID)
	}
	if err := s.Store.ReplaceAllCommandPermissions(app.ID, g.ID, sets); err != nil {
		fail(c, err)
		return
	}
	out := make([]store.CommandPermissions, 0, len(order))
	for _, id := range order {
		perms, err := s.Store.CommandPermissionsFor(app.ID, g.ID, id)
		if err != nil {
			fail(c, err)
			return
		}
		out = append(out, perms)
	}
	c.JSON(out)
}

var _ = errors.Is
