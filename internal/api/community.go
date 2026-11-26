package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

// --- application command permissions ---

func (s *Server) requireAppGuild(c *Ctx, appID string) (store.Application, model.Guild, bool) {
	app, ok := s.authorizeApplication(c, appID)
	if !ok {
		return app, model.Guild{}, false
	}
	g, err := s.Store.Guild(c.Param("guildID"))
	if err != nil {
		fail(c, ErrUnknownGuild)
		return app, g, false
	}
	return app, g, true
}

func (s *Server) handleListCommandPermissions(c *Ctx) {
	app, g, ok := s.requireAppGuild(c, c.Param("applicationID"))
	if !ok {
		return
	}
	list, err := s.Store.ListCommandPermissions(app.ID, g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(list)
}

func (s *Server) handleGetCommandPermissions(c *Ctx) {
	app, g, ok := s.requireAppGuild(c, c.Param("applicationID"))
	if !ok {
		return
	}
	cmd, err := s.Store.Command(c.Param("commandID"))
	if err != nil || cmd.ApplicationID != app.ID {
		fail(c, NewError(404, 10063, "Unknown application command"))
		return
	}
	perms, err := s.Store.CommandPermissionsFor(app.ID, g.ID, cmd.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(perms)
}

func (s *Server) handlePutCommandPermissions(c *Ctx) {
	app, g, ok := s.requireAppGuild(c, c.Param("applicationID"))
	if !ok {
		return
	}
	cmd, err := s.Store.Command(c.Param("commandID"))
	if err != nil || cmd.ApplicationID != app.ID {
		fail(c, NewError(404, 10063, "Unknown application command"))
		return
	}
	var req struct {
		Permissions []store.CommandPermissionEntry `json:"permissions"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if len(req.Permissions) > 100 {
		c.FailForm("permissions", "Must be 100 or fewer in length")
		return
	}
	seen := map[string]bool{}
	for _, e := range req.Permissions {
		if e.ID == "" || seen[e.ID] {
			c.FailForm("permissions", "Entries must have unique ids")
			return
		}
		seen[e.ID] = true
		switch e.Type {
		case 1:
			if _, err := s.Store.Role(g.ID, e.ID); err != nil {
				c.FailForm("permissions", "Role must belong to the guild")
				return
			}
		case 2:
			if _, err := s.Store.UserByID(e.ID); err != nil {
				c.FailForm("permissions", "User must exist")
				return
			}
		case 3:
			ch, err := s.Store.Channel(e.ID)
			if err != nil || ch.GuildID == nil || *ch.GuildID != g.ID {
				c.FailForm("permissions", "Channel must belong to the guild")
				return
			}
		default:
			c.FailForm("permissions", "type must be 1 (role), 2 (user) or 3 (channel)")
			return
		}
	}
	perms, err := s.Store.PutCommandPermissions(app.ID, g.ID, cmd.ID, req.Permissions)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(perms)
}

// --- onboarding ---

type onboardingOption struct {
	ChannelID *string `json:"channel_id,omitempty"`
	RoleID    *string `json:"role_id,omitempty"`
	Emoji     *struct {
		ID   *string `json:"id,omitempty"`
		Name *string `json:"name,omitempty"`
	} `json:"emoji,omitempty"`
	Title       string  `json:"title"`
	Description *string `json:"description,omitempty"`
}

type onboardingPrompt struct {
	ID           *string            `json:"id,omitempty"`
	Type         int                `json:"type"`
	Options      []onboardingOption `json:"options"`
	Title        string             `json:"title"`
	SingleSelect *bool              `json:"single_select,omitempty"`
	Required     *bool              `json:"required,omitempty"`
	Completed    *bool              `json:"completed,omitempty"`
	InOnboarding *bool              `json:"in_onboarding,omitempty"`
}

func (s *Server) handleGetOnboarding(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	onboarding, err := s.Store.GetOnboarding(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(onboarding)
}

func (s *Server) handlePutOnboarding(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		Prompts           []onboardingPrompt `json:"prompts"`
		DefaultChannelIDs []string           `json:"default_channel_ids"`
		Enabled           bool               `json:"enabled"`
		Mode              int                `json:"mode"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.Mode < 0 || req.Mode > 1 {
		c.FailForm("mode", "Must be 0 or 1")
		return
	}
	if len(req.Prompts) > 4 {
		c.FailForm("prompts", "At most 4 prompts are allowed")
		return
	}
	for _, p := range req.Prompts {
		if p.Type != 0 && p.Type != 1 {
			c.FailForm("prompts", "Prompt type must be 0 or 1")
			return
		}
		if len([]rune(p.Title)) < 1 || len([]rune(p.Title)) > 100 {
			c.FailForm("prompts", "Prompt titles must be 1-100 characters")
			return
		}
		if len(p.Options) == 0 || len(p.Options) > 8 {
			c.FailForm("prompts", "Prompts need 1-8 options")
			return
		}
		for _, o := range p.Options {
			if len([]rune(o.Title)) < 1 || len([]rune(o.Title)) > 50 {
				c.FailForm("prompts", "Option titles must be 1-50 characters")
				return
			}
			if o.Description != nil && len([]rune(*o.Description)) > 100 {
				c.FailForm("prompts", "Option descriptions must be 100 characters or fewer")
				return
			}
			if o.ChannelID != nil {
				ch, err := s.Store.Channel(*o.ChannelID)
				if err != nil || ch.GuildID == nil || *ch.GuildID != g.ID {
					c.FailForm("prompts", "Option channel must belong to the guild")
					return
				}
			}
			if o.RoleID != nil {
				if _, err := s.Store.Role(g.ID, *o.RoleID); err != nil {
					c.FailForm("prompts", "Option role must belong to the guild")
					return
				}
			}
		}
	}
	if len(req.DefaultChannelIDs) > 7 {
		c.FailForm("default_channel_ids", "At most 7 default channels are allowed")
		return
	}
	for _, id := range req.DefaultChannelIDs {
		ch, err := s.Store.Channel(id)
		if err != nil || ch.GuildID == nil || *ch.GuildID != g.ID {
			c.FailForm("default_channel_ids", "Channels must belong to the guild")
			return
		}
	}
	raw, _ := json.Marshal(req.Prompts)
	onboarding, err := s.Store.PutOnboarding(g.ID, raw, req.DefaultChannelIDs, req.Enabled, req.Mode)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(onboarding)
}

// --- welcome screen ---

func (s *Server) handleGetWelcomeScreen(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	ws, err := s.Store.GetWelcomeScreen(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(ws)
}

func (s *Server) handlePatchWelcomeScreen(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	existing, err := s.Store.GetWelcomeScreen(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		Enabled         *bool           `json:"enabled"`
		Description     *string         `json:"description"`
		WelcomeChannels json.RawMessage `json:"welcome_channels"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	enabled := existing.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	description := existing.Description
	if req.Description != nil {
		if len([]rune(*req.Description)) > 140 {
			c.FailForm("description", "Must be 140 characters or fewer")
			return
		}
		description = req.Description
	}
	channels := existing.WelcomeChannels
	if len(req.WelcomeChannels) > 0 {
		var list []struct {
			ChannelID   string  `json:"channel_id"`
			Description string  `json:"description"`
			EmojiID     *string `json:"emoji_id,omitempty"`
			EmojiName   *string `json:"emoji_name,omitempty"`
		}
		if err := json.Unmarshal(req.WelcomeChannels, &list); err != nil {
			c.FailForm("welcome_channels", "Must be an array of channel objects")
			return
		}
		if len(list) > 5 {
			c.FailForm("welcome_channels", "At most 5 channels are allowed")
			return
		}
		seen := map[string]bool{}
		for _, item := range list {
			if seen[item.ChannelID] {
				c.FailForm("welcome_channels", "Channels must be unique")
				return
			}
			seen[item.ChannelID] = true
			ch, err := s.Store.Channel(item.ChannelID)
			if err != nil || ch.GuildID == nil || *ch.GuildID != g.ID {
				c.FailForm("welcome_channels", "Channels must belong to the guild")
				return
			}
			if len([]rune(item.Description)) < 1 || len([]rune(item.Description)) > 100 {
				c.FailForm("welcome_channels", "Descriptions must be 1-100 characters")
				return
			}
		}
		channels = req.WelcomeChannels
	}
	if enabled && (description == nil || strings.TrimSpace(*description) == "") {
		c.FailForm("description", "A description is required when the welcome screen is enabled")
		return
	}
	ws, err := s.Store.PutWelcomeScreen(g.ID, description, channels, enabled)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(ws)
}

// --- webhook execution variants ---

// webhookMessage posts a webhook message and optionally returns it.
func (s *Server) webhookMessage(c *Ctx, w store.Webhook, ch model.Channel, content string, username *string, wait bool) {
	guildID := ""
	if ch.GuildID != nil {
		guildID = *ch.GuildID
	}
	msg, err := s.Store.CreateMessage(store.CreateMessageParams{
		ChannelID: ch.ID, GuildID: guildID, AuthorID: w.ID, Content: content,
	})
	if err != nil {
		fail(c, err)
		return
	}
	if s.Hub != nil && ch.GuildID != nil {
		s.Hub.DispatchGuild(*ch.GuildID, "MESSAGE_CREATE", msg)
	}
	_ = username
	if wait {
		c.JSON(msg)
		return
	}
	c.NoContent()
}

func (s *Server) tokenWebhook(c *Ctx) (store.Webhook, model.Channel, bool) {
	w, err := s.Store.WebhookByToken(c.Param("webhookID"), c.Param("token"))
	if err != nil {
		fail(c, ErrUnknownWebhook)
		return w, model.Channel{}, false
	}
	if w.ChannelID == nil {
		fail(c, ErrUnknownWebhook)
		return w, model.Channel{}, false
	}
	ch, err := s.Store.Channel(*w.ChannelID)
	if err != nil {
		fail(c, ErrUnknownChannel)
		return w, ch, false
	}
	return w, ch, true
}

func (s *Server) handleGetWebhookWithToken(c *Ctx) {
	w, _, ok := s.tokenWebhook(c)
	if !ok {
		return
	}
	c.JSON(w)
}

func (s *Server) handleUpdateWebhookWithToken(c *Ctx) {
	w, _, ok := s.tokenWebhook(c)
	if !ok {
		return
	}
	var req struct {
		Name   *string `json:"name"`
		Avatar *string `json:"avatar"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.Name != nil && (len([]rune(*req.Name)) < 1 || len([]rune(*req.Name)) > 80) {
		c.FailForm("name", "Must be between 1 and 80 characters")
		return
	}
	updated, err := s.Store.UpdateWebhook(w.ID, req.Name, req.Avatar, nil)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(updated)
}

func (s *Server) handleDeleteWebhookWithToken(c *Ctx) {
	w, _, ok := s.tokenWebhook(c)
	if !ok {
		return
	}
	if err := s.Store.DeleteWebhook(w.ID); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

// handleSlackWebhook accepts the Slack-compatible payload.
func (s *Server) handleSlackWebhook(c *Ctx) {
	w, ch, ok := s.tokenWebhook(c)
	if !ok {
		return
	}
	if target, tok := s.webhookTarget(c, w, ch); !tok {
		return
	} else {
		ch = target
	}
	var req struct {
		Text     string  `json:"text"`
		Username *string `json:"username"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		fail(c, ErrEmptyMessage)
		return
	}
	if len([]rune(req.Text)) > 2000 {
		c.FailForm("text", "Must be 2000 characters or fewer")
		return
	}
	s.webhookMessage(c, w, ch, req.Text, req.Username, c.R.URL.Query().Get("wait") == "true")
}

// handleGithubWebhook accepts the GitHub-compatible payload.
func (s *Server) handleGithubWebhook(c *Ctx) {
	w, ch, ok := s.tokenWebhook(c)
	if !ok {
		return
	}
	if target, tok := s.webhookTarget(c, w, ch); !tok {
		return
	} else {
		ch = target
	}
	event := c.R.Header.Get("X-GitHub-Event")
	if event == "" {
		c.FailForm("X-GitHub-Event", "Header is required")
		return
	}
	body, _ := io.ReadAll(http.MaxBytesReader(c.W, c.R.Body, 1<<20))
	content := "GitHub `" + event + "` event"
	var payload struct {
		Action     string `json:"action"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Sender struct {
			Login string `json:"login"`
		} `json:"sender"`
	}
	if json.Unmarshal(body, &payload) == nil {
		if payload.Action != "" {
			content += ": " + payload.Action
		}
		if payload.Repository.FullName != "" {
			content += " in " + payload.Repository.FullName
		}
		if payload.Sender.Login != "" {
			content += " by " + payload.Sender.Login
		}
	}
	if len([]rune(content)) > 2000 {
		content = string([]rune(content)[:2000])
	}
	s.webhookMessage(c, w, ch, content, nil, c.R.URL.Query().Get("wait") == "true")
}

var _ = errors.Is
