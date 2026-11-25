package api

import (
	"errors"
	"strings"

	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

func (s *Server) handleCreateApplication(c *Ctx) {
	var req struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
		BotPublic   *bool   `json:"bot_public"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	name := sanitizeName(req.Name)
	if len([]rune(name)) < 2 || len([]rune(name)) > 50 {
		c.FailForm("name", "Must be between 2 and 50 characters")
		return
	}
	existing, err := s.Store.ApplicationsForOwner(c.User.ID)
	if err != nil {
		fail(c, err)
		return
	}
	if len(existing) >= 25 {
		c.JSONStatus(400, map[string]any{"code": 30009, "message": "Maximum number of applications reached"})
		return
	}
	description := ""
	if req.Description != nil {
		description = *req.Description
	}
	app, err := s.Store.CreateApplication(c.User.ID, name, description)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSONStatus(201, app)
}

func (s *Server) handleListApplications(c *Ctx) {
	apps, err := s.Store.ApplicationsForOwner(c.User.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(apps)
}

// handleGetMyApplication answers for bot tokens (the documented /applications/@me).
func (s *Server) handleGetMyApplication(c *Ctx) {
	app, err := s.Store.ApplicationForBot(c.User.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, NewError(404, 10002, "Unknown Application"))
			return
		}
		fail(c, err)
		return
	}
	c.JSON(app)
}

func (s *Server) handleResetBotToken(c *Ctx) {
	app, err := s.Store.Application(c.Param("applicationID"))
	if err != nil {
		fail(c, NewError(404, 10002, "Unknown Application"))
		return
	}
	if app.OwnerID != c.User.ID {
		fail(c, ErrMissingPerms)
		return
	}
	token, err := s.Store.ResetBotToken(app.ID, c.User.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(map[string]any{"token": token})
}

// handleGuildMessageSearch implements the guild search route. Only channels the
// caller can read are searched.
func (s *Server) handleGuildMessageSearch(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	q := c.R.URL.Query()
	content := q.Get("content")
	if content == "" {
		content = q.Get("query")
	}
	channels, err := s.Store.GuildChannels(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	allowed := make([]string, 0, len(channels))
	for _, ch := range channels {
		if ch.Type != store.ChannelText && ch.Type != store.ChannelAnnouncement &&
			ch.Type != store.ChannelPublicThread && ch.Type != store.ChannelPrivateThread {
			continue
		}
		have, err := s.channelPermissions(c.User.ID, ch)
		if err != nil {
			continue
		}
		if perm.Has(have, perm.ViewChannel|perm.ReadMessageHistory) {
			allowed = append(allowed, ch.ID)
		}
	}
	limit := atoiSafe(q.Get("limit"))
	messages, total, err := s.Store.SearchMessages(g.ID, content, q.Get("author_id"), allowed, limit)
	if err != nil {
		fail(c, err)
		return
	}
	groups := make([][]model.Message, 0, len(messages))
	for _, m := range messages {
		groups = append(groups, []model.Message{m})
	}
	c.JSON(map[string]any{
		"messages":                    groups,
		"total_results":               total,
		"doing_deep_historical_index": false,
	})
}

var _ = strings.TrimSpace
