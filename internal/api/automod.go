package api

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

var automodNameRe = regexp.MustCompile(`^.{1,100}$`)

func validateAutoMod(in *store.AutoModRuleInput, creating bool) string {
	if creating || in.Name != "" {
		if !automodNameRe.MatchString(strings.TrimSpace(in.Name)) {
			return "name must be between 1 and 100 characters"
		}
	}
	if in.EventType != 0 && in.EventType != store.AutoModEventMessageSend {
		return "event_type must be 1 (MESSAGE_SEND)"
	}
	if creating {
		switch in.TriggerType {
		case store.AutoModTriggerKeyword, store.AutoModTriggerSpam,
			store.AutoModTriggerKeywordPreset, store.AutoModTriggerMentionSpam,
			store.AutoModTriggerMemberProfile, store.AutoModTriggerMentionRaid:
		default:
			return "trigger_type is not a documented value"
		}
		if len(in.Actions) == 0 {
			return "actions must contain at least one action"
		}
	}
	meta := in.TriggerMetadata
	total := 0
	for _, kw := range meta.KeywordFilter {
		total += len([]rune(kw))
		if len([]rune(kw)) < 1 || len([]rune(kw)) > 60 {
			return "keyword_filter entries must be 1-60 characters"
		}
	}
	if total > 1000 {
		return "keyword_filter total length must be 1000 characters or fewer"
	}
	if len(meta.RegexPatterns) > 10 {
		return "regex_patterns must be 10 or fewer"
	}
	for _, pattern := range meta.RegexPatterns {
		if _, err := regexp.Compile(pattern); err != nil {
			return "regex_patterns contains an invalid expression"
		}
	}
	for _, a := range in.Actions {
		switch a.Type {
		case store.AutoModActionBlockMessage:
		case store.AutoModActionSendAlert:
			if a.Metadata == nil || a.Metadata.ChannelID == nil || *a.Metadata.ChannelID == "" {
				return "send_alert_message requires metadata.channel_id"
			}
		case store.AutoModActionTimeout:
			if a.Metadata == nil || a.Metadata.DurationSeconds == nil ||
				*a.Metadata.DurationSeconds < 1 || *a.Metadata.DurationSeconds > 2419200 {
				return "timeout requires metadata.duration_seconds between 1 and 2419200"
			}
		default:
			return "action type is not a documented value"
		}
	}
	return ""
}

func (s *Server) handleListAutoModRules(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	rules, err := s.Store.ListAutoModRules(g.ID)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(rules)
}

func (s *Server) handleGetAutoModRule(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	rule, err := s.Store.AutoModRule(g.ID, c.Param("ruleID"))
	if err != nil {
		fail(c, NewError(404, 10000, "Unknown Auto Moderation Rule"))
		return
	}
	c.JSON(rule)
}

func (s *Server) handleCreateAutoModRule(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	var in store.AutoModRuleInput
	if err := c.Bind(&in); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if msg := validateAutoMod(&in, true); msg != "" {
		c.FailForm("rule", msg)
		return
	}
	if msg := s.validateAutoModRefs(g.ID, in); msg != "" {
		c.FailForm("rule", msg)
		return
	}
	rule, err := s.Store.CreateAutoModRule(g.ID, c.User.ID, in)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSONStatus(201, rule)
}

func (s *Server) validateAutoModRefs(guildID string, in store.AutoModRuleInput) string {
	for _, a := range in.Actions {
		if a.Type == store.AutoModActionSendAlert && a.Metadata != nil && a.Metadata.ChannelID != nil {
			ch, err := s.Store.Channel(*a.Metadata.ChannelID)
			if err != nil || ch.GuildID == nil || *ch.GuildID != guildID {
				return "alert channel must belong to the guild"
			}
		}
	}
	for _, chID := range in.ExemptChannels {
		ch, err := s.Store.Channel(chID)
		if err != nil || ch.GuildID == nil || *ch.GuildID != guildID {
			return "exempt_channels entries must belong to the guild"
		}
	}
	for _, roleID := range in.ExemptRoles {
		if _, err := s.Store.Role(guildID, roleID); err != nil {
			return "exempt_roles entries must belong to the guild"
		}
	}
	return ""
}

func (s *Server) handleUpdateAutoModRule(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	if _, err := s.Store.AutoModRule(g.ID, c.Param("ruleID")); err != nil {
		fail(c, NewError(404, 10000, "Unknown Auto Moderation Rule"))
		return
	}
	var in store.AutoModRuleInput
	if err := c.Bind(&in); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	if msg := validateAutoMod(&in, false); msg != "" {
		c.FailForm("rule", msg)
		return
	}
	if msg := s.validateAutoModRefs(g.ID, in); msg != "" {
		c.FailForm("rule", msg)
		return
	}
	rule, err := s.Store.UpdateAutoModRule(g.ID, c.Param("ruleID"), in)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(rule)
}

func (s *Server) handleDeleteAutoModRule(c *Ctx) {
	g, err := s.requireGuild(c.User.ID, c.Param("guildID"), perm.ManageGuild)
	if err != nil {
		fail(c, err)
		return
	}
	if err := s.Store.DeleteAutoModRule(g.ID, c.Param("ruleID")); err != nil {
		fail(c, NewError(404, 10000, "Unknown Auto Moderation Rule"))
		return
	}
	c.NoContent()
}

// enforceAutoMod evaluates message-send rules before persistence. It returns true
// when the message was blocked and the response has been written.
func (s *Server) enforceAutoMod(c *Ctx, ch model.Channel, content string) bool {
	if ch.GuildID == nil {
		return false
	}
	rules, err := s.Store.ListAutoModRules(*ch.GuildID)
	if err != nil || len(rules) == 0 {
		return false
	}
	member, err := s.Store.Member(*ch.GuildID, c.User.ID)
	var roles []string
	if err == nil {
		roles = member.Roles
	}
	recent := s.rates.hit(*ch.GuildID + "|" + ch.ID + "|" + c.User.ID)
	matches := store.AutoModEvaluate(rules, store.EvalInput{
		Content: content, ChannelID: ch.ID, MemberRoles: roles,
		RecentMessages: recent, SpamThreshold: spamLimit,
	})
	if len(matches) == 0 {
		return false
	}
	blocked := false
	for _, m := range matches {
		if store.HasAutoModBlock(m.Rule) {
			blocked = true
		}
		for _, action := range m.Rule.Actions {
			switch action.Type {
			case store.AutoModActionSendAlert:
				s.sendAutoModAlert(ch, m)
			case store.AutoModActionTimeout:
				if action.Metadata != nil && action.Metadata.DurationSeconds != nil {
					until := time.Now().Add(time.Duration(*action.Metadata.DurationSeconds) * time.Second).UnixMilli()
					_ = s.Store.SetMemberTimeout(*ch.GuildID, c.User.ID, until)
				}
			}
		}
	}
	if blocked {
		c.JSONStatus(400, map[string]any{"code": 200000, "message": "This message was blocked by AutoMod"})
		return true
	}
	return false
}

func (s *Server) sendAutoModAlert(ch model.Channel, match store.AutoModViolation) {
	var alertChannel *string
	custom := ""
	for _, action := range match.Rule.Actions {
		if action.Type == store.AutoModActionSendAlert && action.Metadata != nil {
			alertChannel = action.Metadata.ChannelID
			if action.Metadata.CustomMessage != nil {
				custom = *action.Metadata.CustomMessage
			}
		}
	}
	if alertChannel == nil {
		return
	}
	target, err := s.Store.Channel(*alertChannel)
	if err != nil {
		return
	}
	system, err := s.Store.EnsureSystemUser()
	if err != nil {
		return
	}
	content := custom
	if content == "" {
		content = "AutoMod rule \"" + match.Rule.Name + "\" triggered (" + match.Reason + ")."
	}
	var guildID string
	if target.GuildID != nil {
		guildID = *target.GuildID
	}
	msg, err := s.Store.CreateMessage(store.CreateMessageParams{
		ChannelID: target.ID, GuildID: guildID, AuthorID: system.ID, Content: content,
	})
	if err != nil {
		return
	}
	if s.Hub != nil && target.GuildID != nil {
		s.Hub.DispatchGuild(*target.GuildID, "MESSAGE_CREATE", msg)
	}
	s.cfg.Logger.Info("automod alert", "guild", guildID, "rule", match.Rule.ID, "channel", target.ID)
}

var _ = errors.Is
