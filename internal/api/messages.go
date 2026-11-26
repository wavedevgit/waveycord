package api

import (
	"encoding/json"
	"strings"

	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

func nowUnix() int64 { return nowMillisFn() }

func (s *Server) handleListMessages(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ReadMessageHistory|perm.ViewChannel)
	if err != nil {
		fail(c, err)
		return
	}
	q := c.R.URL.Query()
	key := "messages|" + ch.ID + "|" + q.Get("before") + "|" + q.Get("after") + "|" + q.Get("around") + "|" + q.Get("limit")
	v, err, _ := s.reads.Do(key, func() (any, error) {
		messages, err := s.Store.MessagesPage(ch.ID, q.Get("before"), q.Get("after"), q.Get("around"), atoiSafe(q.Get("limit")))
		if err != nil {
			return nil, err
		}
		return json.Marshal(messages)
	})
	if err != nil {
		fail(c, err)
		return
	}
	c.JSONStatus(200, json.RawMessage(v.([]byte)))
}

type createMessageRequest struct {
	Content          string         `json:"content"`
	Nonce            *string        `json:"nonce"`
	TTS              bool           `json:"tts"`
	Embeds           []model.Embed  `json:"embeds"`
	AllowedMentions  map[string]any `json:"allowed_mentions"`
	MessageReference *struct {
		MessageID       *string `json:"message_id"`
		ChannelID       *string `json:"channel_id"`
		GuildID         *string `json:"guild_id"`
		FailIfNotExists *bool   `json:"fail_if_not_exists"`
	} `json:"message_reference"`
	Components  []any              `json:"components"`
	StickerIDs  []string           `json:"sticker_ids"`
	Attachments []model.Attachment `json:"attachments"`
	Flags       int                `json:"flags"`
	Pinned      bool               `json:"pinned"`
	Poll        *pollCreateRequest `json:"poll"`
}

func (s *Server) handleCreateMessage(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.SendMessages|perm.ViewChannel)
	if err != nil {
		fail(c, err)
		return
	}
	var req createMessageRequest
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if len([]rune(req.Content)) > 2000 {
		c.FailForm("content", "Must be 2000 or fewer in length")
		return
	}
	if len(req.Embeds) > 10 {
		c.FailForm("embeds", "Must be 10 or fewer in length")
		return
	}
	var poll model.Poll
	var pollExpiry int64
	if req.Poll != nil {
		var formErr string
		poll, pollExpiry, formErr = validatePoll(req.Poll)
		if formErr != "" {
			c.FailForm("poll", formErr)
			return
		}
		if strings.TrimSpace(req.Content) == "" && len(req.Embeds) == 0 && len(req.Attachments) == 0 {
			// A poll message without text is valid; the empty-message check below
			// must not reject it.
			req.Content = ""
		}
		if len(req.Attachments) > 0 {
			c.FailForm("poll", "Polls cannot be combined with attachments")
			return
		}
	}
	if msg := validateMessageComponentsWithFlags(req.Components, req.Flags); msg != "" {
		c.FailForm("components", msg)
		return
	}
	if req.Flags&flagComponentsV2 != 0 {
		if strings.TrimSpace(req.Content) != "" {
			c.FailForm("content", "content is not allowed on components v2 messages")
			return
		}
		if len(req.Embeds) > 0 {
			c.FailForm("embeds", "embeds are not allowed on components v2 messages")
			return
		}
		if req.Poll != nil {
			c.FailForm("poll", "polls are not allowed on components v2 messages")
			return
		}
		if len(req.StickerIDs) > 0 {
			c.FailForm("sticker_ids", "stickers are not allowed on components v2 messages")
			return
		}
	}
	componentsOnly := req.Flags&flagComponentsV2 != 0 && len(req.Components) > 0
	if strings.TrimSpace(req.Content) == "" && len(req.Embeds) == 0 && len(req.Attachments) == 0 && req.Poll == nil && !componentsOnly {
		fail(c, ErrEmptyMessage)
		return
	}
	have, err := s.channelPermissions(c.User.ID, ch)
	if err != nil {
		fail(c, err)
		return
	}
	if len(req.Embeds) > 0 && !perm.Has(have, perm.EmbedLinks) {
		fail(c, ErrMissingPerms)
		return
	}
	if len(req.Attachments) > 0 && !perm.Has(have, perm.AttachFiles) {
		fail(c, ErrMissingPerms)
		return
	}
	var refID *string
	if req.MessageReference != nil {
		refID = req.MessageReference.MessageID
		if refID != nil {
			if ref, err := s.Store.Message(*refID); err != nil || ref.ChannelID != ch.ID {
				fail(c, ErrUnknownMessage)
				return
			}
		}
	}
	if s.enforceAutoMod(c, ch, req.Content) {
		return
	}
	var guildID string
	if ch.GuildID != nil {
		guildID = *ch.GuildID
	}
	// Attach ids to attachments that arrived without one, matching the documented
	// flow where a client uploads first and references the returned id.
	for i := range req.Attachments {
		if req.Attachments[i].ID == "" {
			req.Attachments[i].ID = s.Store.NewID()
		}
		if req.Attachments[i].Filename == "" {
			req.Attachments[i].Filename = "file"
		}
		if u, err := s.Store.Upload(req.Attachments[i].ID); err == nil && u.ChannelID == ch.ID {
			req.Attachments[i].Size = u.Size
			if req.Attachments[i].ContentType == nil && u.ContentType != nil {
				req.Attachments[i].ContentType = u.ContentType
			}
			req.Attachments[i].URL = s.cfg.CDNBase + "/cdn/attachments/" + ch.ID + "/" + u.ID + "/" + urlEscape(req.Attachments[i].Filename)
			req.Attachments[i].ProxyURL = req.Attachments[i].URL
		}
	}
	msgType := 0
	if req.Poll != nil {
		msgType = 32
	}
	msg, err := s.Store.CreateMessage(store.CreateMessageParams{
		ChannelID: ch.ID, GuildID: guildID, AuthorID: c.User.ID,
		Content: req.Content, TTS: req.TTS, Embeds: req.Embeds,
		Attachments: req.Attachments, Components: req.Components,
		Nonce: req.Nonce, ReferencedMessageID: refID, Flags: req.Flags, Pinned: req.Pinned,
		Type: msgType,
	})
	if err != nil {
		fail(c, err)
		return
	}
	if req.Poll != nil {
		if err := s.Store.CreatePoll(msg.ID, ch.ID, poll, pollExpiry, poll.AllowMultiselect, poll.LayoutType); err != nil {
			// The message is already written; remove it so a failed poll insert
			// cannot leave a type-32 message without a poll.
			_, _ = s.Store.DeleteMessage(msg.ID)
			fail(c, err)
			return
		}
		if refreshed, err := s.Store.Message(msg.ID); err == nil {
			msg = refreshed
		}
	}
	if s.Hub != nil {
		if ch.GuildID != nil {
			s.Hub.DispatchGuild(*ch.GuildID, "MESSAGE_CREATE", msg)
		} else {
			for _, r := range ch.Recipients {
				s.Hub.DispatchUser(r.ID, "MESSAGE_CREATE", msg)
			}
			s.Hub.DispatchUser(c.User.ID, "MESSAGE_CREATE", msg)
		}
	}
	c.JSON(msg)
}

func (s *Server) handleGetMessage(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ViewChannel|perm.ReadMessageHistory)
	if err != nil {
		fail(c, err)
		return
	}
	msg, err := s.Store.Message(c.Param("messageID"))
	if err != nil || msg.ChannelID != ch.ID {
		fail(c, ErrUnknownMessage)
		return
	}
	c.JSON(msg)
}

type updateMessageRequest struct {
	Content    *string        `json:"content"`
	Embeds     *[]model.Embed `json:"embeds"`
	Flags      *int           `json:"flags"`
	Components *[]any         `json:"components"`
}

func (s *Server) handleUpdateMessage(c *Ctx) {
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
	if msg.Author.ID != c.User.ID {
		have, err := s.channelPermissions(c.User.ID, ch)
		if err != nil || !perm.Has(have, perm.ManageMessages) {
			fail(c, ErrMissingPerms)
			return
		}
	}
	var req updateMessageRequest
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.Content != nil && len([]rune(*req.Content)) > 2000 {
		c.FailForm("content", "Must be 2000 or fewer in length")
		return
	}
	if req.Embeds != nil && len(*req.Embeds) > 10 {
		c.FailForm("embeds", "Must be 10 or fewer in length")
		return
	}
	flags := msg.Flags
	if req.Flags != nil {
		flags = *req.Flags
	}
	if req.Components != nil {
		if msg := validateMessageComponentsWithFlags(*req.Components, flags); msg != "" {
			c.FailForm("components", msg)
			return
		}
	}
	if flags&flagComponentsV2 != 0 {
		if msg.Flags&flagComponentsV2 != 0 && req.Content != nil {
			c.FailForm("content", "content cannot be edited on components v2 messages")
			return
		}
		if req.Content != nil && strings.TrimSpace(*req.Content) != "" {
			c.FailForm("content", "content is not allowed on components v2 messages")
			return
		}
		if req.Embeds != nil && len(*req.Embeds) > 0 {
			c.FailForm("embeds", "embeds are not allowed on components v2 messages")
			return
		}
	}
	updated, err := s.Store.UpdateMessage(msg.ID, req.Content, req.Embeds, req.Flags, req.Components)
	if err != nil {
		fail(c, ErrUnknownMessage)
		return
	}
	if s.Hub != nil {
		if ch.GuildID != nil {
			s.Hub.DispatchGuild(*ch.GuildID, "MESSAGE_UPDATE", updated)
		} else {
			s.Hub.DispatchUser(c.User.ID, "MESSAGE_UPDATE", updated)
			for _, r := range ch.Recipients {
				s.Hub.DispatchUser(r.ID, "MESSAGE_UPDATE", updated)
			}
		}
	}
	c.JSON(updated)
}

func (s *Server) handleDeleteMessage(c *Ctx) {
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
	if msg.Author.ID != c.User.ID {
		have, err := s.channelPermissions(c.User.ID, ch)
		if err != nil || !perm.Has(have, perm.ManageMessages) {
			fail(c, ErrMissingPerms)
			return
		}
	}
	if _, err := s.Store.DeleteMessage(msg.ID); err != nil {
		fail(c, err)
		return
	}
	if ch.GuildID != nil {
		s.audit(c, *ch.GuildID, store.AuditMessageDelete, ch.ID, nil)
	}
	s.dispatchDelete(ch, msg.ID)
	c.NoContent()
}

func (s *Server) dispatchDelete(ch model.Channel, messageID string) {
	if s.Hub == nil {
		return
	}
	payload := map[string]any{"id": messageID, "channel_id": ch.ID}
	if ch.GuildID != nil {
		payload["guild_id"] = *ch.GuildID
		s.Hub.DispatchGuild(*ch.GuildID, "MESSAGE_DELETE", payload)
		return
	}
	s.Hub.DispatchUser(chOwner(ch, s), "MESSAGE_DELETE", payload)
	for _, r := range ch.Recipients {
		s.Hub.DispatchUser(r.ID, "MESSAGE_DELETE", payload)
	}
}

func chOwner(ch model.Channel, s *Server) string {
	if ch.OwnerID != nil {
		return *ch.OwnerID
	}
	return ""
}

func (s *Server) handleBulkDelete(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ManageMessages|perm.ViewChannel)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		Messages []string `json:"messages"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if len(req.Messages) < 2 || len(req.Messages) > 100 {
		c.FailForm("messages", "Must be between 2 and 100 in length")
		return
	}
	filtered := make([]string, 0, len(req.Messages))
	for _, idv := range req.Messages {
		msg, err := s.Store.Message(idv)
		if err != nil || msg.ChannelID != ch.ID {
			continue
		}
		if msg.Pinned {
			continue
		}
		filtered = append(filtered, idv)
	}
	if err := s.Store.DeleteMessages(ch.ID, filtered); err != nil {
		fail(c, err)
		return
	}
	if ch.GuildID != nil {
		s.audit(c, *ch.GuildID, store.AuditMessageBulkDelete, ch.ID, []store.AuditChange{{Key: "count", NewValue: len(filtered)}})
	}
	for _, idv := range filtered {
		s.dispatchDelete(ch, idv)
	}
	c.NoContent()
}

func (s *Server) handleCrosspost(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.SendMessages)
	if err != nil {
		fail(c, err)
		return
	}
	if ch.Type != store.ChannelAnnouncement {
		fail(c, NewError(400, 50020, "This endpoint is only valid for announcement channels"))
		return
	}
	msg, err := s.Store.Message(c.Param("messageID"))
	if err != nil || msg.ChannelID != ch.ID {
		fail(c, ErrUnknownMessage)
		return
	}
	c.JSON(msg)
}

// parseEmoji converts a path parameter into an emoji object.
func parseEmoji(raw string) model.Emoji {
	if raw == "" {
		return model.Emoji{}
	}
	if strings.Contains(raw, ":") {
		parts := strings.SplitN(raw, ":", 2)
		name, idv := parts[0], parts[1]
		animated := strings.HasPrefix(name, "a:")
		if animated {
			name = strings.TrimPrefix(name, "a:")
		}
		e := model.Emoji{ID: &idv, Animated: animated}
		if name != "" {
			e.Name = &name
		}
		return e
	}
	name := raw
	return model.Emoji{Name: &name}
}

func (s *Server) messageForReaction(c *Ctx) (model.Channel, model.Message, error) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ViewChannel|perm.ReadMessageHistory)
	if err != nil {
		return ch, model.Message{}, err
	}
	msg, err := s.Store.Message(c.Param("messageID"))
	if err != nil || msg.ChannelID != ch.ID {
		return ch, msg, ErrUnknownMessage
	}
	return ch, msg, nil
}

func (s *Server) handleAddReaction(c *Ctx) {
	ch, msg, err := s.messageForReaction(c)
	if err != nil {
		fail(c, err)
		return
	}
	if _, err := s.requireChannel(c.User.ID, ch.ID, perm.AddReactions); err != nil {
		fail(c, err)
		return
	}
	emoji := parseEmoji(c.Param("emoji"))
	if ReactionKeyOf(emoji) == "" {
		fail(c, ErrUnknownEmoji)
		return
	}
	if err := s.Store.AddReaction(msg.ID, ch.ID, c.User.ID, emoji); err != nil {
		fail(c, err)
		return
	}
	if s.Hub != nil && ch.GuildID != nil {
		s.Hub.DispatchGuild(*ch.GuildID, "MESSAGE_REACTION_ADD", map[string]any{
			"user_id": c.User.ID, "channel_id": ch.ID, "message_id": msg.ID,
			"guild_id": *ch.GuildID, "emoji": emoji,
		})
	}
	c.NoContent()
}

func ReactionKeyOf(e model.Emoji) string { return store.ReactionKey(e) }

func (s *Server) handleRemoveOwnReaction(c *Ctx) {
	_, msg, err := s.messageForReaction(c)
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.RemoveReaction(msg.ID, c.User.ID, store.ReactionKey(parseEmoji(c.Param("emoji")))); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

func (s *Server) handleRemoveUserReaction(c *Ctx) {
	ch, msg, err := s.messageForReaction(c)
	if err != nil {
		fail(c, err)
		return
	}
	if _, err := s.requireChannel(c.User.ID, ch.ID, perm.ManageMessages); err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.RemoveReaction(msg.ID, c.Param("userID"), store.ReactionKey(parseEmoji(c.Param("emoji")))); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

func (s *Server) handleDeleteEmojiReactions(c *Ctx) {
	ch, msg, err := s.messageForReaction(c)
	if err != nil {
		fail(c, err)
		return
	}
	if _, err := s.requireChannel(c.User.ID, ch.ID, perm.ManageMessages); err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.RemoveEmojiReactions(msg.ID, store.ReactionKey(parseEmoji(c.Param("emoji")))); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

func (s *Server) handleDeleteAllReactions(c *Ctx) {
	ch, msg, err := s.messageForReaction(c)
	if err != nil {
		fail(c, err)
		return
	}
	if _, err := s.requireChannel(c.User.ID, ch.ID, perm.ManageMessages); err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.RemoveAllReactions(msg.ID); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

func (s *Server) handleReactionUsers(c *Ctx) {
	_, msg, err := s.messageForReaction(c)
	if err != nil {
		fail(c, err)
		return
	}
	key := store.ReactionKey(parseEmoji(c.Param("emoji")))
	users, err := s.Store.ReactionUsers(msg.ID, key, atoiSafe(c.R.URL.Query().Get("limit")))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(users)
}

func (s *Server) handleListPins(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ViewChannel|perm.ReadMessageHistory)
	if err != nil {
		fail(c, err)
		return
	}
	pins, err := s.Store.PinnedMessages(ch.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(pins)
}

func (s *Server) handlePin(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ManageMessages)
	if err != nil {
		fail(c, err)
		return
	}
	msg, err := s.Store.Message(c.Param("messageID"))
	if err != nil || msg.ChannelID != ch.ID {
		fail(c, ErrUnknownMessage)
		return
	}
	if err := s.Store.Pin(ch.ID, msg.ID); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

func (s *Server) handleUnpin(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ManageMessages)
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.Unpin(ch.ID, c.Param("messageID")); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}
