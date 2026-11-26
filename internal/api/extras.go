package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

// --- guild templates ---

func (s *Server) handleGetTemplate(c *Ctx) {
	t, err := s.Store.Template(c.Param("code"))
	if err != nil {
		fail(c, NewError(404, 10000, "Unknown Guild Template"))
		return
	}
	c.JSON(t)
}

func (s *Server) handleUseTemplate(c *Ctx) {
	var req struct {
		Name *string `json:"name"`
	}
	_ = c.Bind(&req)
	if req.Name != nil {
		name := sanitizeName(*req.Name)
		if len([]rune(name)) < 2 || len([]rune(name)) > 100 {
			c.FailForm("name", "Must be between 2 and 100 characters")
			return
		}
		req.Name = &name
	}
	g, err := s.Store.UseTemplate(c.Param("code"), c.User.ID, req.Name)
	if err != nil {
		fail(c, NewError(404, 10000, "Unknown Guild Template"))
		return
	}
	c.JSONStatus(201, g)
}

func (s *Server) handleListTemplates(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	templates, err := s.Store.Templates(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(templates)
}

func (s *Server) handleCreateTemplate(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	name := sanitizeName(req.Name)
	if len([]rune(name)) < 1 || len([]rune(name)) > 100 {
		c.FailForm("name", "Must be between 1 and 100 characters")
		return
	}
	if req.Description != nil && len([]rune(*req.Description)) > 120 {
		c.FailForm("description", "Must be 120 characters or fewer")
		return
	}
	t, err := s.Store.CreateTemplate(g.ID, c.User.ID, name, req.Description)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSONStatus(201, t)
}

func (s *Server) handleSyncTemplate(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	t, err := s.Store.SyncTemplate(g.ID, c.Param("code"))
	if err != nil {
		fail(c, NewError(404, 10000, "Unknown Guild Template"))
		return
	}
	c.JSON(t)
}

func (s *Server) handleUpdateTemplate(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.Name != nil {
		name := sanitizeName(*req.Name)
		if len([]rune(name)) < 1 || len([]rune(name)) > 100 {
			c.FailForm("name", "Must be between 1 and 100 characters")
			return
		}
		req.Name = &name
	}
	t, err := s.Store.UpdateTemplate(g.ID, c.Param("code"), req.Name, req.Description)
	if err != nil {
		fail(c, NewError(404, 10000, "Unknown Guild Template"))
		return
	}
	c.JSON(t)
}

func (s *Server) handleDeleteTemplate(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.DeleteTemplate(g.ID, c.Param("code")); err != nil {
		fail(c, NewError(404, 10000, "Unknown Guild Template"))
		return
	}
	c.NoContent()
}

// --- scheduled events ---

func parseEventTime(raw string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, raw)
	return t, err == nil
}

func (s *Server) handleListScheduledEvents(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	events, err := s.Store.ScheduledEvents(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(events)
}

func (s *Server) handleCreateScheduledEvent(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageEvents)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		Name           string         `json:"name"`
		Description    *string        `json:"description"`
		ChannelID      *string        `json:"channel_id"`
		Start          string         `json:"scheduled_start_time"`
		End            *string        `json:"scheduled_end_time"`
		PrivacyLevel   *int           `json:"privacy_level"`
		EntityType     int            `json:"entity_type"`
		EntityMetadata map[string]any `json:"entity_metadata"`
		Image          *string        `json:"image"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	name := sanitizeName(req.Name)
	if len([]rune(name)) < 1 || len([]rune(name)) > 100 {
		c.FailForm("name", "Must be between 1 and 100 characters")
		return
	}
	if _, ok := parseEventTime(req.Start); !ok {
		c.FailForm("scheduled_start_time", "Must be an ISO8601 timestamp")
		return
	}
	switch req.EntityType {
	case 1:
		if req.ChannelID == nil {
			c.FailForm("channel_id", "Required for stage events")
			return
		}
		ch, err := s.Store.Channel(*req.ChannelID)
		if err != nil || ch.GuildID == nil || *ch.GuildID != g.ID || ch.Type != store.ChannelStage {
			c.FailForm("channel_id", "Must be a stage channel in this guild")
			return
		}
	case 2:
		if req.ChannelID == nil {
			c.FailForm("channel_id", "Required for voice events")
			return
		}
		ch, err := s.Store.Channel(*req.ChannelID)
		if err != nil || ch.GuildID == nil || *ch.GuildID != g.ID || ch.Type != store.ChannelVoice {
			c.FailForm("channel_id", "Must be a voice channel in this guild")
			return
		}
	case 3:
		if req.ChannelID != nil {
			c.FailForm("channel_id", "External events cannot target a channel")
			return
		}
		location, _ := req.EntityMetadata["location"].(string)
		if len([]rune(location)) < 1 || len([]rune(location)) > 100 {
			c.FailForm("entity_metadata", "location is required for external events")
			return
		}
		if req.End == nil {
			c.FailForm("scheduled_end_time", "External events require an end time")
			return
		}
	default:
		c.FailForm("entity_type", "Must be 1, 2 or 3")
		return
	}
	if req.End != nil {
		start, _ := parseEventTime(req.Start)
		end, ok := parseEventTime(*req.End)
		if !ok || !end.After(start) {
			c.FailForm("scheduled_end_time", "Must be after the start time")
			return
		}
	}
	privacy := 2
	if req.PrivacyLevel != nil {
		if *req.PrivacyLevel != 2 {
			c.FailForm("privacy_level", "Only GUILD_ONLY (2) is accepted")
			return
		}
		privacy = *req.PrivacyLevel
	}
	event, err := s.Store.CreateScheduledEvent(g.ID, c.User.ID, store.CreateEventParams{
		Name: name, Description: req.Description, ChannelID: req.ChannelID,
		Start: req.Start, End: req.End, PrivacyLevel: privacy,
		EntityType: req.EntityType, EntityMetadata: req.EntityMetadata, Image: req.Image,
	})
	if err != nil {
		fail(c, err)
		return
	}
	s.audit(c, g.ID, 100, event.ID, nil)
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_SCHEDULED_EVENT_CREATE", event)
	}
	c.JSONStatus(201, event)
}

func (s *Server) handleGetScheduledEvent(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	event, err := s.Store.ScheduledEvent(c.Param("eventID"))
	if err != nil || event.GuildID != g.ID {
		fail(c, NewError(404, 10000, "Unknown Guild Scheduled Event"))
		return
	}
	c.JSON(event)
}

func (s *Server) handleUpdateScheduledEvent(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageEvents)
	if err != nil {
		fail(c, err)
		return
	}
	event, err := s.Store.ScheduledEvent(c.Param("eventID"))
	if err != nil || event.GuildID != g.ID {
		fail(c, NewError(404, 10000, "Unknown Guild Scheduled Event"))
		return
	}
	var req struct {
		Name           *string        `json:"name"`
		Description    *string        `json:"description"`
		ChannelID      *string        `json:"channel_id"`
		Start          *string        `json:"scheduled_start_time"`
		End            *string        `json:"scheduled_end_time"`
		Status         *int           `json:"status"`
		PrivacyLevel   *int           `json:"privacy_level"`
		EntityMetadata map[string]any `json:"entity_metadata"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.Name != nil {
		name := sanitizeName(*req.Name)
		if len([]rune(name)) < 1 || len([]rune(name)) > 100 {
			c.FailForm("name", "Must be between 1 and 100 characters")
			return
		}
		req.Name = &name
	}
	if req.Start != nil {
		if _, ok := parseEventTime(*req.Start); !ok {
			c.FailForm("scheduled_start_time", "Must be an ISO8601 timestamp")
			return
		}
	}
	if req.End != nil {
		if _, ok := parseEventTime(*req.End); !ok {
			c.FailForm("scheduled_end_time", "Must be an ISO8601 timestamp")
			return
		}
	}
	if req.Status != nil && (*req.Status < 1 || *req.Status > 4) {
		c.FailForm("status", "Must be 1-4")
		return
	}
	if req.Status != nil && *req.Status != event.Status &&
		!((event.Status == 1 && (*req.Status == 2 || *req.Status == 4)) ||
			(event.Status == 2 && *req.Status == 3)) {
		c.FailForm("status", "Invalid scheduled event status transition")
		return
	}
	updated, err := s.Store.UpdateScheduledEvent(g.ID, event.ID, store.UpdateEventParams{
		Name: req.Name, Description: req.Description, ChannelID: req.ChannelID,
		Start: req.Start, End: req.End, Status: req.Status,
		PrivacyLevel: req.PrivacyLevel, EntityMetadata: req.EntityMetadata,
	})
	if err != nil {
		fail(c, err)
		return
	}
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_SCHEDULED_EVENT_UPDATE", updated)
	}
	c.JSON(updated)
}

func (s *Server) handleDeleteScheduledEvent(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageEvents)
	if err != nil {
		fail(c, err)
		return
	}
	event, err := s.Store.ScheduledEvent(c.Param("eventID"))
	if err != nil || event.GuildID != g.ID {
		fail(c, NewError(404, 10000, "Unknown Guild Scheduled Event"))
		return
	}
	if err := s.Store.DeleteScheduledEvent(g.ID, event.ID); err != nil {
		fail(c, err)
		return
	}
	if s.Hub != nil {
		s.Hub.DispatchGuild(g.ID, "GUILD_SCHEDULED_EVENT_DELETE", event)
	}
	c.NoContent()
}

// handleEventUsers returns persisted subscriptions with bounded pagination.
func (s *Server) handleEventUsers(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	event, err := s.Store.ScheduledEvent(c.Param("eventID"))
	if err != nil || event.GuildID != g.ID {
		fail(c, NewError(404, 10000, "Unknown Guild Scheduled Event"))
		return
	}
	q := c.R.URL.Query()
	limit := 100
	if value := q.Get("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 100 {
			fail(c, NewError(400, 50035, "Invalid limit"))
			return
		}
		limit = n
	}
	users, err := s.Store.EventUsers(event.ID, g.ID, q.Get("before"), q.Get("after"), limit, q.Get("with_member") == "true")
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(users)
}

func (s *Server) handleEventInterest(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), 0)
	if err != nil {
		fail(c, err)
		return
	}
	event, err := s.Store.ScheduledEvent(c.Param("eventID"))
	if err != nil || event.GuildID != g.ID {
		fail(c, NewError(404, 10000, "Unknown Guild Scheduled Event"))
		return
	}
	interested := c.R.Method == http.MethodPut
	changed, err := s.Store.SetEventInterest(event.ID, c.User.ID, interested)
	if err != nil {
		fail(c, err)
		return
	}
	if changed && s.Hub != nil {
		name := "GUILD_SCHEDULED_EVENT_USER_REMOVE"
		if interested {
			name = "GUILD_SCHEDULED_EVENT_USER_ADD"
		}
		s.Hub.DispatchGuild(g.ID, name, map[string]any{"guild_id": g.ID, "guild_scheduled_event_id": event.ID, "user_id": c.User.ID})
	}
	if !interested {
		c.NoContent()
		return
	}
	c.JSON(store.EventUser{EventID: event.ID, UserID: c.User.ID, Response: 1})
}

// --- stage instances ---

func (s *Server) handleCreateStageInstance(c *Ctx) {
	var req struct {
		ChannelID             string  `json:"channel_id"`
		Topic                 string  `json:"topic"`
		PrivacyLevel          *int    `json:"privacy_level"`
		SendStartNotification *bool   `json:"send_start_notification"`
		GuildScheduledEventID *string `json:"guild_scheduled_event_id"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	ch, err := s.Store.Channel(req.ChannelID)
	if err != nil {
		fail(c, ErrUnknownChannel)
		return
	}
	if ch.GuildID == nil {
		fail(c, ErrGuildOnly)
		return
	}
	if ch.Type != store.ChannelStage {
		fail(c, NewError(400, 50035, "Channel is not a stage channel"))
		return
	}
	if _, err := s.requireChannel(c.User.ID, ch.ID, perm.ManageChannels); err != nil {
		fail(c, err)
		return
	}
	topic := strings.TrimSpace(req.Topic)
	if len([]rune(topic)) < 1 || len([]rune(topic)) > 120 {
		c.FailForm("topic", "Must be between 1 and 120 characters")
		return
	}
	privacy := 2
	if req.PrivacyLevel != nil {
		if *req.PrivacyLevel != 2 {
			c.FailForm("privacy_level", "Only GUILD_ONLY (2) is accepted")
			return
		}
		privacy = *req.PrivacyLevel
	}
	si, err := s.Store.CreateStageInstance(*ch.GuildID, ch.ID, topic, privacy, req.GuildScheduledEventID)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			fail(c, NewError(400, 50035, "A stage instance already exists for this channel"))
			return
		}
		fail(c, err)
		return
	}
	if s.Hub != nil {
		s.Hub.DispatchGuild(*ch.GuildID, "STAGE_INSTANCE_CREATE", si)
	}
	c.JSONStatus(201, si)
}

func (s *Server) handleGetStageInstance(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ViewChannel)
	if err != nil {
		fail(c, err)
		return
	}
	si, err := s.Store.StageInstance(ch.ID)
	if err != nil {
		fail(c, NewError(404, 10000, "Unknown Stage Instance"))
		return
	}
	c.JSON(si)
}

func (s *Server) handleUpdateStageInstance(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ManageChannels)
	if err != nil {
		fail(c, err)
		return
	}
	if _, err := s.Store.StageInstance(ch.ID); err != nil {
		fail(c, NewError(404, 10000, "Unknown Stage Instance"))
		return
	}
	var req struct {
		Topic        *string `json:"topic"`
		PrivacyLevel *int    `json:"privacy_level"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.Topic != nil && (len([]rune(*req.Topic)) < 1 || len([]rune(*req.Topic)) > 120) {
		c.FailForm("topic", "Must be between 1 and 120 characters")
		return
	}
	si, err := s.Store.UpdateStageInstance(ch.ID, req.Topic, req.PrivacyLevel)
	if err != nil {
		fail(c, err)
		return
	}
	if ch.GuildID != nil && s.Hub != nil {
		s.Hub.DispatchGuild(*ch.GuildID, "STAGE_INSTANCE_UPDATE", si)
	}
	c.JSON(si)
}

func (s *Server) handleDeleteStageInstance(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ManageChannels)
	if err != nil {
		fail(c, err)
		return
	}
	si, err := s.Store.StageInstance(ch.ID)
	if err != nil {
		fail(c, NewError(404, 10000, "Unknown Stage Instance"))
		return
	}
	if err := s.Store.DeleteStageInstance(ch.ID); err != nil {
		fail(c, err)
		return
	}
	if ch.GuildID != nil && s.Hub != nil {
		s.Hub.DispatchGuild(*ch.GuildID, "STAGE_INSTANCE_DELETE", si)
	}
	c.NoContent()
}

// --- integrations ---

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
	c.JSON(list)
}

func (s *Server) handleCreateIntegration(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	var req struct {
		Type        int     `json:"type"`
		Name        *string `json:"name"`
		AccountID   string  `json:"account_id"`
		AccountName *string `json:"account_name"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if req.Type != 1 && req.Type != 2 {
		c.FailForm("type", "Only Twitch (1) and YouTube (2) integrations can be created")
		return
	}
	if strings.TrimSpace(req.AccountID) == "" {
		c.FailForm("account_id", "This field is required")
		return
	}
	name := "Twitch"
	if req.Type == 2 {
		name = "YouTube"
	}
	if req.Name != nil {
		name = sanitizeName(*req.Name)
		if len([]rune(name)) < 1 || len([]rune(name)) > 100 {
			c.FailForm("name", "Must be between 1 and 100 characters")
			return
		}
	}
	accountName := req.AccountID
	if req.AccountName != nil && *req.AccountName != "" {
		accountName = *req.AccountName
	}
	in, err := s.Store.CreateIntegration(g.ID, req.Type, name, accountName, req.AccountID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSONStatus(201, in)
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
	if req.ExpireGracePeriod != nil && (*req.ExpireGracePeriod < 0 || *req.ExpireGracePeriod > 180) {
		c.FailForm("expire_grace_period", "Must be between 0 and 180")
		return
	}
	in, err := s.Store.UpdateIntegration(g.ID, c.Param("integrationID"), req.EnableEmoticons, req.ExpireBehavior, req.ExpireGracePeriod)
	if err != nil {
		fail(c, NewError(404, 10000, "Unknown Integration"))
		return
	}
	c.JSON(in)
}

func (s *Server) handleDeleteIntegration(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	botID, err := s.Store.DeleteIntegration(g.ID, c.Param("integrationID"))
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
	c.NoContent()
}

var _ = http.StatusOK
