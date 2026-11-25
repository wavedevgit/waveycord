package api

import (
	"errors"
	"strings"

	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

func (s *Server) handleAck(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ViewChannel)
	if err != nil {
		fail(c, err)
		return
	}
	msg, err := s.Store.Message(c.Param("messageID"))
	if err != nil || msg.ChannelID != ch.ID {
		fail(c, ErrUnknownMessage)
		return
	}
	var req struct {
		LastViewed *string `json:"last_viewed"`
	}
	_ = c.Bind(&req)
	if err := s.Store.AckChannel(c.User.ID, ch.ID, msg.ID, 0); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

func (s *Server) handleListAuditLogs(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ViewAuditLog)
	if err != nil {
		fail(c, err)
		return
	}
	q := c.R.URL.Query()
	action := atoiSafe(q.Get("action_type"))
	entries, err := s.Store.AuditLogs(g.ID, q.Get("user_id"), action, atoiSafe(q.Get("limit")), q.Get("before"))
	if err != nil {
		fail(c, err)
		return
	}
	users := map[string]any{}
	for _, e := range entries {
		if e.ActorID != nil {
			if u, err := s.Store.UserByID(*e.ActorID); err == nil {
				users[*e.ActorID] = u
			}
		}
	}
	c.JSON(map[string]any{
		"audit_log_entries":     entries,
		"users":                 users,
		"integrations":          []any{},
		"webhooks":              []any{},
		"threads":               []any{},
		"application_commands":  []any{},
		"auto_moderation_rules": []any{},
	})
}

func (s *Server) handleCreateEmoji(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuildExpressions)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		Name     string   `json:"name"`
		Image    string   `json:"image"`
		Roles    []string `json:"roles"`
		Animated *bool    `json:"animated"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 32 || strings.ContainsAny(name, " ~`!@#$%^&*()+=[]{}|;:'\",.<>/?\\") {
		c.FailForm("name", "Must be 32 characters or fewer and contain only alphanumerics and underscores")
		return
	}
	animated := false
	if req.Animated != nil {
		animated = *req.Animated
	}
	emoji, err := s.Store.CreateEmoji(g.ID, c.User.ID, name, animated)
	if err != nil {
		fail(c, err)
		return
	}
	if req.Roles != nil {
		emoji, _ = s.Store.UpdateEmoji(g.ID, *emoji.ID, nil, &req.Roles)
	}
	s.audit(c, g.ID, store.AuditEmojiCreate, deref(emoji.ID), nil)
	if s.Hub != nil {
		emojis, _ := s.Store.Emojis(g.ID)
		s.Hub.DispatchGuild(g.ID, "GUILD_EMOJIS_UPDATE", map[string]any{"guild_id": g.ID, "emojis": emojis})
	}
	c.JSONStatus(201, emoji)
}

func (s *Server) handleUpdateEmoji(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuildExpressions)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		Name  *string   `json:"name"`
		Roles *[]string `json:"roles"`
	}
	if err := c.Bind(&req); err != nil && req.Name == nil && req.Roles == nil {
		fail(c, ErrInvalidForm)
		return
	}
	emoji, err := s.Store.UpdateEmoji(g.ID, c.Param("emojiID"), req.Name, req.Roles)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, ErrUnknownEmoji)
			return
		}
		fail(c, err)
		return
	}
	s.audit(c, g.ID, store.AuditEmojiUpdate, deref(emoji.ID), nil)
	c.JSON(emoji)
}

func (s *Server) handleDeleteEmoji(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuildExpressions)
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.DeleteEmoji(g.ID, c.Param("emojiID")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fail(c, ErrUnknownEmoji)
			return
		}
		fail(c, err)
		return
	}
	s.audit(c, g.ID, store.AuditEmojiDelete, c.Param("emojiID"), nil)
	c.NoContent()
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
