package api

import (
	"errors"

	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

func (s *Server) handleGetChannel(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ViewChannel)
	if err != nil {
		fail(c, err)
		return
	}
	if ch.Type == store.ChannelPrivateThread || ch.Type == store.ChannelPublicThread || ch.Type == store.ChannelAnnouncementThread {
		if members, err := s.Store.ThreadMembers(ch.ID); err == nil {
			for i := range members {
				if members[i].UserID != nil && *members[i].UserID == c.User.ID {
					ch.Member = &members[i]
				}
			}
		}
	}
	c.JSON(ch)
}

type updateChannelRequest struct {
	Name                       *string `json:"name"`
	Type                       *int    `json:"type"`
	Topic                      *string `json:"topic"`
	NSFW                       *bool   `json:"nsfw"`
	Bitrate                    *int    `json:"bitrate"`
	UserLimit                  *int    `json:"user_limit"`
	RateLimitPerUser           *int    `json:"rate_limit_per_user"`
	Position                   *int    `json:"position"`
	ParentID                   *string `json:"parent_id"`
	RTCRegion                  *string `json:"rtc_region"`
	VideoQualityMode           *int    `json:"video_quality_mode"`
	Archived                   *bool   `json:"archived"`
	AutoArchiveDuration        *int    `json:"auto_archive_duration"`
	Locked                     *bool   `json:"locked"`
	Invitable                  *bool   `json:"invitable"`
	Flags                      *int    `json:"flags"`
	Icon                       *string `json:"icon"`
	DefaultAutoArchiveDuration *int    `json:"default_auto_archive_duration"`
}

func (s *Server) handleUpdateChannel(c *Ctx) {
	ch, err := s.Store.Channel(c.Param("channelID"))
	if err != nil {
		fail(c, ErrUnknownChannel)
		return
	}
	var req updateChannelRequest
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if ch.GuildID != nil {
		if _, err := s.requireChannel(c.User.ID, ch.ID, perm.ManageChannels); err != nil {
			fail(c, err)
			return
		}
		if req.Type != nil && *req.Type != ch.Type {
			c.FailForm("type", "Channel type cannot be changed")
			return
		}
	} else {
		// Only participants may edit a DM; the name field applies to group DMs.
		if _, err := s.channelPermissions(c.User.ID, ch); err != nil {
			fail(c, err)
			return
		}
		if req.Name != nil && ch.Type != store.ChannelGroupDM {
			c.FailForm("name", "Cannot set name on a direct message channel")
			return
		}
	}
	if req.Name != nil {
		name := sanitizeName(*req.Name)
		if len([]rune(name)) < 1 || len([]rune(name)) > 100 {
			c.FailForm("name", "Must be between 1 and 100 characters")
			return
		}
		req.Name = &name
	}
	if req.RateLimitPerUser != nil && (*req.RateLimitPerUser < 0 || *req.RateLimitPerUser > 21600) {
		c.FailForm("rate_limit_per_user", "Must be between 0 and 21600")
		return
	}
	updated, err := s.Store.UpdateChannel(ch.ID, store.UpdateChannelParams{
		Name: req.Name, Topic: req.Topic, NSFW: req.NSFW, Bitrate: req.Bitrate,
		UserLimit: req.UserLimit, RateLimitPerUser: req.RateLimitPerUser,
		Position: req.Position, ParentID: req.ParentID, RTCRegion: req.RTCRegion,
		VideoQualityMode: req.VideoQualityMode, Archived: req.Archived,
		AutoArchiveDuration: req.AutoArchiveDuration, Locked: req.Locked,
		Invitable: req.Invitable, Flags: req.Flags, Icon: req.Icon,
		DefaultAutoArchiveDuration: req.DefaultAutoArchiveDuration,
	})
	if err != nil {
		fail(c, err)
		return
	}
	if ch.GuildID != nil {
		s.audit(c, *ch.GuildID, store.AuditChannelUpdate, ch.ID, nil)
	}
	if s.Hub != nil && ch.GuildID != nil {
		s.Hub.DispatchGuild(*ch.GuildID, "CHANNEL_UPDATE", updated)
	}
	c.JSON(updated)
}

func (s *Server) handleDeleteChannel(c *Ctx) {
	ch, err := s.Store.Channel(c.Param("channelID"))
	if err != nil {
		fail(c, ErrUnknownChannel)
		return
	}
	if ch.GuildID == nil {
		// Closing a DM removes the caller from it; an empty channel is deleted.
		if _, err := s.channelPermissions(c.User.ID, ch); err != nil {
			fail(c, err)
			return
		}
		if err := s.Store.RemoveRecipient(ch.ID, c.User.ID); err != nil {
			fail(c, err)
			return
		}
		if recipients, err := s.Store.Recipients(ch.ID); err == nil && len(recipients) == 0 {
			_ = s.Store.DeleteChannel(ch.ID)
		}
		c.NoContent()
		return
	}
	if ch.Type == store.ChannelText || ch.Type == store.ChannelVoice || ch.Type == store.ChannelCategory ||
		ch.Type == store.ChannelAnnouncement || ch.Type == store.ChannelStage ||
		ch.Type == store.ChannelForum || ch.Type == store.ChannelMedia {
		if _, err := s.requireChannel(c.User.ID, ch.ID, perm.ManageChannels); err != nil {
			fail(c, err)
			return
		}
	} else {
		// Threads: the thread owner or a manager may delete.
		if ch.OwnerID == nil || (*ch.OwnerID != c.User.ID) {
			if _, err := s.requireChannel(c.User.ID, ch.ID, perm.ManageThreads); err != nil {
				fail(c, err)
				return
			}
		}
	}
	if err := s.Store.DeleteChannel(ch.ID); err != nil {
		fail(c, err)
		return
	}
	s.audit(c, *ch.GuildID, store.AuditChannelDelete, ch.ID, nil)
	if s.Hub != nil {
		s.Hub.DispatchGuild(*ch.GuildID, "CHANNEL_DELETE", ch)
	}
	c.NoContent()
}

func (s *Server) handlePutOverwrite(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ManageRoles)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		Type  *int    `json:"type"`
		Allow *string `json:"allow"`
		Deny  *string `json:"deny"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	typ := perm.TypeRole
	if req.Type != nil {
		typ = *req.Type
	}
	allow, deny := "0", "0"
	if req.Allow != nil {
		if _, err := perm.Parse(*req.Allow); err != nil {
			c.FailForm("allow", "Invalid bitfield")
			return
		}
		allow = *req.Allow
	}
	if req.Deny != nil {
		if _, err := perm.Parse(*req.Deny); err != nil {
			c.FailForm("deny", "Invalid bitfield")
			return
		}
		deny = *req.Deny
	}
	ow := model.Overwrite{ID: c.Param("overwriteID"), Type: typ, Allow: allow, Deny: deny}
	if err := s.Store.PutOverwrite(ch.ID, ow); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

func (s *Server) handleDeleteOverwrite(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ManageRoles)
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.DeleteOverwrite(ch.ID, c.Param("overwriteID")); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

func (s *Server) handleTyping(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.SendMessages)
	if err != nil {
		fail(c, err)
		return
	}
	if s.Hub != nil && ch.GuildID != nil {
		s.Hub.DispatchGuild(*ch.GuildID, "TYPING_START", map[string]any{
			"channel_id": ch.ID,
			"guild_id":   *ch.GuildID,
			"user_id":    c.User.ID,
			"timestamp":  nowUnix(),
		})
	}
	c.NoContent()
}

func (s *Server) handleChannelInvites(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ManageChannels)
	if err != nil {
		fail(c, err)
		return
	}
	invites, err := s.Store.ChannelInvites(ch.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(invites)
}

func (s *Server) handleAddRecipient(c *Ctx) {
	ch, err := s.Store.Channel(c.Param("channelID"))
	if err != nil {
		fail(c, ErrUnknownChannel)
		return
	}
	if err := assertDMChannel(ch); err != nil {
		fail(c, err)
		return
	}
	if ch.Type != store.ChannelGroupDM {
		fail(c, NewError(400, 50020, "This endpoint is only valid for group direct messages"))
		return
	}
	if _, err := s.channelPermissions(c.User.ID, ch); err != nil {
		fail(c, err)
		return
	}
	target := c.Param("userID")
	u, err := s.Store.UserByID(target)
	if err != nil {
		fail(c, ErrUnknownUser)
		return
	}
	var req struct {
		AccessToken *string `json:"access_token"`
		Nick        *string `json:"nick"`
	}
	_ = c.Bind(&req)
	if err := s.Store.AddRecipient(ch.ID, target); err != nil {
		fail(c, err)
		return
	}
	updated, _ := s.Store.Channel(ch.ID)
	if s.Hub != nil {
		s.Hub.DispatchUser(target, "CHANNEL_CREATE", updated)
		s.Hub.DispatchUser(c.User.ID, "CHANNEL_UPDATE", updated)
	}
	_ = u
	c.JSON(u)
}

func (s *Server) handleRemoveRecipient(c *Ctx) {
	ch, err := s.Store.Channel(c.Param("channelID"))
	if err != nil {
		fail(c, ErrUnknownChannel)
		return
	}
	if err := assertDMChannel(ch); err != nil {
		fail(c, err)
		return
	}
	if _, err := s.channelPermissions(c.User.ID, ch); err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.RemoveRecipient(ch.ID, c.Param("userID")); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

type startThreadRequest struct {
	Name                string `json:"name"`
	AutoArchiveDuration *int   `json:"auto_archive_duration"`
	Type                *int   `json:"type"`
	Invitable           *bool  `json:"invitable"`
	RateLimitPerUser    *int   `json:"rate_limit_per_user"`
}

func (s *Server) handleStartThread(c *Ctx) {
	parent, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.CreatePublicThreads)
	if err != nil {
		fail(c, err)
		return
	}
	var req startThreadRequest
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	name := sanitizeName(req.Name)
	if len([]rune(name)) < 1 || len([]rune(name)) > 100 {
		c.FailForm("name", "Must be between 1 and 100 characters")
		return
	}
	typ := store.ChannelPublicThread
	if req.Type != nil {
		typ = *req.Type
	}
	if typ != store.ChannelPublicThread && typ != store.ChannelPrivateThread {
		c.FailForm("type", "Value must be 10, 11 or 12")
		return
	}
	if typ == store.ChannelPrivateThread {
		if _, err := s.requireChannel(c.User.ID, parent.ID, perm.CreatePrivateThreads); err != nil {
			fail(c, err)
			return
		}
	}
	ch, err := s.Store.CreateChannel(*parent.GuildID, store.CreateChannelParams{
		Type: typ, Name: &name, ParentID: &parent.ID,
		AutoArchiveDuration: req.AutoArchiveDuration, Invitable: req.Invitable,
		RateLimitPerUser: req.RateLimitPerUser, OwnerID: &c.User.ID,
	})
	if err != nil {
		fail(c, err)
		return
	}
	_ = s.Store.JoinThread(ch.ID, c.User.ID)
	if s.Hub != nil {
		s.Hub.DispatchGuild(*parent.GuildID, "THREAD_CREATE", ch)
	}
	c.JSONStatus(201, ch)
}

func (s *Server) handleStartThreadFromMessage(c *Ctx) {
	parent, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.CreatePublicThreads)
	if err != nil {
		fail(c, err)
		return
	}
	msgID := c.Param("messageID")
	if _, err := s.Store.Message(msgID); err != nil {
		fail(c, ErrUnknownMessage)
		return
	}
	var req startThreadRequest
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	name := sanitizeName(req.Name)
	if name == "" {
		name = "Thread"
	}
	ch, err := s.Store.CreateChannel(*parent.GuildID, store.CreateChannelParams{
		Type: store.ChannelPublicThread, Name: &name, ParentID: &parent.ID,
		AutoArchiveDuration: req.AutoArchiveDuration,
		RateLimitPerUser:    req.RateLimitPerUser, OwnerID: &c.User.ID,
	})
	if err != nil {
		fail(c, err)
		return
	}
	_ = s.Store.JoinThread(ch.ID, c.User.ID)
	// The message that spawned the thread now carries it.
	if m, err := s.Store.Message(msgID); err == nil {
		m.Thread = &ch
		if s.Hub != nil {
			s.Hub.DispatchGuild(*parent.GuildID, "MESSAGE_UPDATE", m)
		}
	}
	if s.Hub != nil {
		s.Hub.DispatchGuild(*parent.GuildID, "THREAD_CREATE", ch)
	}
	c.JSONStatus(201, ch)
}

func (s *Server) handleJoinThread(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ViewChannel)
	if err != nil {
		fail(c, err)
		return
	}
	if ch.ParentID == nil {
		fail(c, NewError(400, 50020, "This endpoint is only valid for threads"))
		return
	}
	if err := s.Store.JoinThread(ch.ID, c.User.ID); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

func (s *Server) handleAddThreadMember(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ManageThreads)
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.JoinThread(ch.ID, c.Param("userID")); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

func (s *Server) handleLeaveThread(c *Ctx) {
	ch, err := s.Store.Channel(c.Param("channelID"))
	if err != nil {
		fail(c, ErrUnknownChannel)
		return
	}
	if _, err := s.channelPermissions(c.User.ID, ch); err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.LeaveThread(ch.ID, c.User.ID); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

func (s *Server) handleRemoveThreadMember(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ManageThreads)
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.LeaveThread(ch.ID, c.Param("userID")); err != nil {
		fail(c, err)
		return
	}
	c.NoContent()
}

func (s *Server) handleGetThreadMember(c *Ctx) {
	ch, err := s.Store.Channel(c.Param("channelID"))
	if err != nil {
		fail(c, ErrUnknownChannel)
		return
	}
	if _, err := s.channelPermissions(c.User.ID, ch); err != nil {
		fail(c, err)
		return
	}
	members, err := s.Store.ThreadMembers(ch.ID)
	if err != nil {
		fail(c, err)
		return
	}
	for _, m := range members {
		if m.UserID != nil && *m.UserID == c.Param("userID") {
			c.JSON(m)
			return
		}
	}
	fail(c, ErrUnknownMember)
}

func (s *Server) handleListThreadMembers(c *Ctx) {
	ch, err := s.Store.Channel(c.Param("channelID"))
	if err != nil {
		fail(c, ErrUnknownChannel)
		return
	}
	if _, err := s.channelPermissions(c.User.ID, ch); err != nil {
		fail(c, err)
		return
	}
	members, err := s.Store.ThreadMembers(ch.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(members)
}

func (s *Server) handleArchivedPublic(c *Ctx) {
	s.archivedThreads(c, false)
}

func (s *Server) handleArchivedPrivate(c *Ctx) {
	s.archivedThreads(c, true)
}

func (s *Server) archivedThreads(c *Ctx, private bool) {
	ch, err := s.Store.Channel(c.Param("channelID"))
	if err != nil {
		fail(c, ErrUnknownChannel)
		return
	}
	if ch.GuildID == nil {
		fail(c, ErrGuildOnly)
		return
	}
	if _, err := s.channelPermissions(c.User.ID, ch); err != nil {
		fail(c, err)
		return
	}
	threads, err := s.Store.ArchivedThreads(ch.ID, private)
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
	c.JSON(map[string]any{
		"threads":  threads,
		"members":  members,
		"has_more": false,
	})
}

var _ = errors.Is
