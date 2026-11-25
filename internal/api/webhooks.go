package api

import (
	"errors"

	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

func (s *Server) handleCreateWebhook(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ManageWebhooks)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		Name   string  `json:"name"`
		Avatar *string `json:"avatar"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if len([]rune(req.Name)) < 1 || len([]rune(req.Name)) > 80 {
		c.FailForm("name", "Must be between 1 and 80 characters")
		return
	}
	guildID := ""
	if ch.GuildID != nil {
		guildID = *ch.GuildID
	}
	w, err := s.Store.CreateWebhook(guildID, ch.ID, c.User.ID, req.Name, req.Avatar)
	if err != nil {
		fail(c, err)
		return
	}
	s.audit(c, guildID, store.AuditWebhookCreate, w.ID, nil)
	if s.Hub != nil && ch.GuildID != nil {
		s.Hub.DispatchGuild(*ch.GuildID, "WEBHOOKS_UPDATE", map[string]any{"guild_id": *ch.GuildID, "channel_id": ch.ID})
	}
	c.JSONStatus(201, w)
}

func (s *Server) handleChannelWebhooks(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ManageWebhooks)
	if err != nil {
		fail(c, err)
		return
	}
	hooks, err := s.Store.ChannelWebhooks(ch.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(hooks)
}

func (s *Server) handleGuildWebhooks(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageWebhooks)
	if err != nil {
		fail(c, err)
		return
	}
	hooks, err := s.Store.GuildWebhooks(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(hooks)
}

func (s *Server) handleGetWebhook(c *Ctx) {
	w, err := s.Store.Webhook(c.Param("webhookID"))
	if err != nil {
		fail(c, ErrUnknownWebhook)
		return
	}
	if w.GuildID != nil {
		if _, err := s.requireGuild(c.User.ID, *w.GuildID, 0); err != nil {
			fail(c, err)
			return
		}
	}
	c.JSON(w)
}

func (s *Server) handleUpdateWebhook(c *Ctx) {
	w, err := s.Store.Webhook(c.Param("webhookID"))
	if err != nil {
		fail(c, ErrUnknownWebhook)
		return
	}
	if w.GuildID != nil {
		if _, err := s.requireGuild(c.User.ID, *w.GuildID, perm.ManageWebhooks); err != nil {
			fail(c, err)
			return
		}
	} else if w.User == nil || w.User.ID != c.User.ID {
		fail(c, ErrForbidden)
		return
	}
	var req struct {
		Name      *string `json:"name"`
		Avatar    *string `json:"avatar"`
		ChannelID *string `json:"channel_id"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.Name != nil && (len([]rune(*req.Name)) < 1 || len([]rune(*req.Name)) > 80) {
		c.FailForm("name", "Must be between 1 and 80 characters")
		return
	}
	if req.ChannelID != nil {
		target, err := s.Store.Channel(*req.ChannelID)
		if err != nil {
			fail(c, ErrUnknownChannel)
			return
		}
		if target.GuildID == nil || w.GuildID == nil || *target.GuildID != *w.GuildID {
			c.FailForm("channel_id", "Channel must belong to the same guild")
			return
		}
	}
	updated, err := s.Store.UpdateWebhook(w.ID, req.Name, req.Avatar, req.ChannelID)
	if err != nil {
		fail(c, err)
		return
	}
	if w.GuildID != nil {
		s.audit(c, *w.GuildID, store.AuditWebhookUpdate, w.ID, nil)
	}
	c.JSON(updated)
}

func (s *Server) handleDeleteWebhook(c *Ctx) {
	w, err := s.Store.Webhook(c.Param("webhookID"))
	if err != nil {
		fail(c, ErrUnknownWebhook)
		return
	}
	if w.GuildID != nil {
		if _, err := s.requireGuild(c.User.ID, *w.GuildID, perm.ManageWebhooks); err != nil {
			fail(c, err)
			return
		}
	} else if w.User == nil || w.User.ID != c.User.ID {
		fail(c, ErrForbidden)
		return
	}
	if err := s.Store.DeleteWebhook(w.ID); err != nil {
		fail(c, err)
		return
	}
	if w.GuildID != nil {
		s.audit(c, *w.GuildID, store.AuditWebhookDelete, w.ID, nil)
	}
	c.NoContent()
}

// handleExecuteWebhook posts a message as the webhook. No user auth: the token is
// the credential.
func (s *Server) handleExecuteWebhook(c *Ctx) {
	w, err := s.Store.WebhookByToken(c.Param("webhookID"), c.Param("token"))
	if err != nil {
		fail(c, ErrUnknownWebhook)
		return
	}
	if w.ChannelID == nil {
		fail(c, ErrUnknownWebhook)
		return
	}
	ch, err := s.Store.Channel(*w.ChannelID)
	if err != nil {
		fail(c, ErrUnknownChannel)
		return
	}
	var req struct {
		Content     string            `json:"content"`
		Username    *string           `json:"username"`
		AvatarURL   *string           `json:"avatar_url"`
		TTS         bool              `json:"tts"`
		Embeds      []model.Embed     `json:"embeds"`
		Components  []any             `json:"components"`
		Attachments []attachmentInput `json:"attachments"`
		Flags       int               `json:"flags"`
		ThreadName  *string           `json:"thread_name"`
	}
	if c.R.ContentLength > 0 {
		if err := c.Bind(&req); err != nil {
			fail(c, ErrInvalidForm)
			return
		}
	}
	if len([]rune(req.Content)) > 2000 {
		c.FailForm("content", "Must be 2000 or fewer in length")
		return
	}
	if req.Content == "" && len(req.Embeds) == 0 && len(req.Attachments) == 0 {
		fail(c, ErrEmptyMessage)
		return
	}
	if msg := validateMessageComponents(req.Components); msg != "" {
		c.FailForm("components", msg)
		return
	}
	atts := make([]model.Attachment, 0, len(req.Attachments))
	for _, in := range req.Attachments {
		atts = append(atts, model.Attachment{ID: in.ID, Filename: in.Filename})
	}
	guildID := ""
	if ch.GuildID != nil {
		guildID = *ch.GuildID
	}
	msg, err := s.Store.CreateMessage(store.CreateMessageParams{
		ChannelID: ch.ID, GuildID: guildID, AuthorID: w.ID,
		Content: req.Content, TTS: req.TTS, Embeds: req.Embeds,
		Attachments: atts, Components: req.Components, Flags: req.Flags,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, ErrUnknownChannel)
			return
		}
		fail(c, err)
		return
	}
	if s.Hub != nil && ch.GuildID != nil {
		s.Hub.DispatchGuild(*ch.GuildID, "MESSAGE_CREATE", msg)
	}
	if c.R.URL.Query().Get("wait") == "true" {
		c.JSON(msg)
		return
	}
	c.NoContent()
}
