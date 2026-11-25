package store

import (
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"
)

// Auto moderation objects and message evaluation. This covers the rule surface
// and the execution path for message-send rules: keyword, regex, keyword preset
// and mention-spam triggers with block, alert and timeout actions.

// AutoModTriggerMetadata is the documented trigger_metadata object.
type AutoModTriggerMetadata struct {
	KeywordFilter                []string `json:"keyword_filter,omitempty"`
	RegexPatterns                []string `json:"regex_patterns,omitempty"`
	Presets                      []string `json:"presets,omitempty"`
	AllowList                    []string `json:"allow_list,omitempty"`
	MentionTotalLimit            *int     `json:"mention_total_limit,omitempty"`
	MentionRaidProtectionEnabled bool     `json:"mention_raid_protection_enabled,omitempty"`
}

// AutoModAction is one action entry.
type AutoModAction struct {
	Type     int                    `json:"type"`
	Metadata *AutoModActionMetadata `json:"metadata,omitempty"`
}

// AutoModActionMetadata carries the alert channel, timeout duration or custom
// block message.
type AutoModActionMetadata struct {
	ChannelID       *string `json:"channel_id,omitempty"`
	DurationSeconds *int    `json:"duration_seconds,omitempty"`
	CustomMessage   *string `json:"custom_message,omitempty"`
}

// AutoModRule is the auto moderation rule object.
type AutoModRule struct {
	ID              string                 `json:"id"`
	GuildID         string                 `json:"guild_id"`
	CreatorID       *string                `json:"creator_id"`
	Name            string                 `json:"name"`
	EventType       int                    `json:"event_type"`
	TriggerType     int                    `json:"trigger_type"`
	TriggerMetadata AutoModTriggerMetadata `json:"trigger_metadata"`
	Actions         []AutoModAction        `json:"actions"`
	Enabled         bool                   `json:"enabled"`
	ExemptRoles     []string               `json:"exempt_roles"`
	ExemptChannels  []string               `json:"exempt_channels"`
}

// AutoMod rule constants from the docs.
const (
	AutoModEventMessageSend = 1

	AutoModTriggerKeyword       = 1
	AutoModTriggerSpam          = 2
	AutoModTriggerKeywordPreset = 3
	AutoModTriggerMentionSpam   = 4
	AutoModTriggerMemberProfile = 5
	AutoModTriggerMentionRaid   = 6

	AutoModActionBlockMessage = 1
	AutoModActionSendAlert    = 2
	AutoModActionTimeout      = 3
)

const autoModColumns = `id, guild_id, creator_id, name, event_type, trigger_type,
	trigger_metadata, actions, enabled, exempt_roles, exempt_channels`

func scanRule(row rowScanner) (AutoModRule, error) {
	var (
		r        AutoModRule
		creator  sql.NullString
		meta     string
		actions  string
		roles    string
		channels string
	)
	err := row.Scan(&r.ID, &r.GuildID, &creator, &r.Name, &r.EventType, &r.TriggerType,
		&meta, &actions, &r.Enabled, &roles, &channels)
	if err != nil {
		return r, err
	}
	r.CreatorID = ns(creator)
	r.ExemptRoles = parseJSONStrings(roles)
	r.ExemptChannels = parseJSONStrings(channels)
	r.Actions = []AutoModAction{}
	_ = json.Unmarshal([]byte(actions), &r.Actions)
	_ = json.Unmarshal([]byte(meta), &r.TriggerMetadata)
	return r, nil
}

// ListAutoModRules returns every rule in a guild.
func (s *Store) ListAutoModRules(guildID string) ([]AutoModRule, error) {
	rows, err := s.DB.Query(`SELECT `+autoModColumns+` FROM auto_moderation_rules
		WHERE guild_id = ? ORDER BY id ASC`, guildID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []AutoModRule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, mapErr(err)
		}
		out = append(out, r)
	}
	return out, mapErr(rows.Err())
}

// AutoModRule loads one rule.
func (s *Store) AutoModRule(guildID, ruleID string) (AutoModRule, error) {
	r, err := scanRule(s.DB.QueryRow(`SELECT `+autoModColumns+` FROM auto_moderation_rules
		WHERE guild_id = ? AND id = ?`, guildID, ruleID))
	return r, mapErr(err)
}

// AutoModRuleInput is the create/patch payload.
type AutoModRuleInput struct {
	Name            string                 `json:"name"`
	EventType       int                    `json:"event_type"`
	TriggerType     int                    `json:"trigger_type"`
	TriggerMetadata AutoModTriggerMetadata `json:"trigger_metadata"`
	Actions         []AutoModAction        `json:"actions"`
	Enabled         *bool                  `json:"enabled"`
	ExemptRoles     []string               `json:"exempt_roles"`
	ExemptChannels  []string               `json:"exempt_channels"`
}

func marshalOr(v any, fallback string) string {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) == 0 {
		return fallback
	}
	return string(raw)
}

// CreateAutoModRule inserts a rule.
func (s *Store) CreateAutoModRule(guildID, creatorID string, in AutoModRuleInput) (AutoModRule, error) {
	id := s.NewID()
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	_, err := s.DB.Exec(`INSERT INTO auto_moderation_rules
		(id, guild_id, creator_id, name, event_type, trigger_type, trigger_metadata, actions,
		 enabled, exempt_roles, exempt_channels, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, guildID, nullIfEmpty(creatorID), in.Name, in.EventType, in.TriggerType,
		marshalOr(in.TriggerMetadata, "{}"), marshalOr(in.Actions, "[]"), enabled,
		jsonStrings(in.ExemptRoles), jsonStrings(in.ExemptChannels), now())
	if err != nil {
		return AutoModRule{}, mapErr(err)
	}
	return s.AutoModRule(guildID, id)
}

// UpdateAutoModRule applies a rule patch.
func (s *Store) UpdateAutoModRule(guildID, ruleID string, in AutoModRuleInput) (AutoModRule, error) {
	set := []string{}
	args := []any{}
	if in.Name != "" {
		set = append(set, "name = ?")
		args = append(args, in.Name)
	}
	if in.EventType != 0 {
		set = append(set, "event_type = ?")
		args = append(args, in.EventType)
	}
	if in.TriggerType != 0 {
		set = append(set, "trigger_type = ?")
		args = append(args, in.TriggerType)
	}
	if in.TriggerMetadata.MentionTotalLimit != nil || len(in.TriggerMetadata.KeywordFilter) > 0 ||
		len(in.TriggerMetadata.RegexPatterns) > 0 || len(in.TriggerMetadata.Presets) > 0 ||
		len(in.TriggerMetadata.AllowList) > 0 {
		set = append(set, "trigger_metadata = ?")
		args = append(args, marshalOr(in.TriggerMetadata, "{}"))
	}
	if in.Actions != nil {
		set = append(set, "actions = ?")
		args = append(args, marshalOr(in.Actions, "[]"))
	}
	if in.Enabled != nil {
		set = append(set, "enabled = ?")
		args = append(args, *in.Enabled)
	}
	if in.ExemptRoles != nil {
		set = append(set, "exempt_roles = ?")
		args = append(args, jsonStrings(in.ExemptRoles))
	}
	if in.ExemptChannels != nil {
		set = append(set, "exempt_channels = ?")
		args = append(args, jsonStrings(in.ExemptChannels))
	}
	if len(set) == 0 {
		return s.AutoModRule(guildID, ruleID)
	}
	args = append(args, guildID, ruleID)
	res, err := s.DB.Exec(`UPDATE auto_moderation_rules SET `+strings.Join(set, ", ")+`
		WHERE guild_id = ? AND id = ?`, args...)
	if err != nil {
		return AutoModRule{}, mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return AutoModRule{}, ErrNotFound
	}
	return s.AutoModRule(guildID, ruleID)
}

// DeleteAutoModRule removes a rule.
func (s *Store) DeleteAutoModRule(guildID, ruleID string) error {
	res, err := s.DB.Exec(`DELETE FROM auto_moderation_rules WHERE guild_id = ? AND id = ?`, guildID, ruleID)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// autoModPresets are the documented preset names with the terms this deployment
// treats as matching. Discord's production lists are larger and unpublished.
var autoModPresets = map[string][]string{
	"profanity":      {"damn", "hell", "shit", "fuck"},
	"sexual_content": {"porn", "nsfw", "xxx"},
	"slurs":          {"slur1", "slur2"},
}

var mentionRe = regexp.MustCompile(`<@!?[0-9]{17,20}>`)

// raidMentionRe counts user and role mentions for the MENTION_RAID trigger.
var raidMentionRe = regexp.MustCompile(`<@!?[0-9]{17,20}>|<@&[0-9]{17,20}>`)

// CountMentions returns the number of user/role mentions in a message, plus one
// for @everyone/@here.
func CountMentions(content string) int {
	n := len(raidMentionRe.FindAllString(content, -1))
	lower := strings.ToLower(content)
	if strings.Contains(lower, "@everyone") || strings.Contains(lower, "@here") {
		n++
	}
	return n
}

// AutoModViolation describes why a message was flagged.
type AutoModViolation struct {
	Rule   AutoModRule
	Action AutoModAction
	Reason string
}

// ProfileFields is the member profile scanned by the MEMBER_PROFILE trigger.
type ProfileFields struct {
	Username   string
	GlobalName string
	Nick       string
}

// EvalInput carries everything the triggers may inspect.
type EvalInput struct {
	Content        string
	ChannelID      string
	MemberRoles    []string
	RecentMessages int // messages from this user in this channel inside the window
	SpamThreshold  int // messages above this count trip the SPAM trigger
	// RecentRaidMentions is the guild-wide mention count inside the raid window;
	// RaidThreshold is the count above which MENTION_RAID trips.
	RecentRaidMentions int
	RaidThreshold      int
	Profile            *ProfileFields
}

// AutoModMatches returns every enabled message-send rule that matches, with the
// actions the caller must carry out. Blocking is the caller's decision so alert
// and timeout actions still run when a block action is absent.
func AutoModMatches(rules []AutoModRule, content, channelID string, memberRoles []string) []AutoModViolation {
	return AutoModEvaluate(rules, EvalInput{Content: content, ChannelID: channelID, MemberRoles: memberRoles})
}

// AutoModEvaluate evaluates message, spam and profile triggers.
func AutoModEvaluate(rules []AutoModRule, in EvalInput) []AutoModViolation {
	lower := strings.ToLower(in.Content)
	mentions := len(mentionRe.FindAllString(in.Content, -1))
	if strings.Contains(lower, "@everyone") || strings.Contains(lower, "@here") {
		mentions++
	}
	threshold := in.SpamThreshold
	if threshold <= 0 {
		threshold = 5
	}
	raidThreshold := in.RaidThreshold
	if raidThreshold <= 0 {
		raidThreshold = 10
	}
	out := []AutoModViolation{}
	for _, rule := range rules {
		if !rule.Enabled || rule.EventType != AutoModEventMessageSend {
			continue
		}
		if containsID(rule.ExemptChannels, in.ChannelID) || intersects(rule.ExemptRoles, in.MemberRoles) {
			continue
		}
		var matched bool
		var reason string
		switch rule.TriggerType {
		case AutoModTriggerSpam:
			matched = in.RecentMessages > threshold
			reason = "spam"
		case AutoModTriggerMentionRaid:
			matched = in.RecentRaidMentions > raidThreshold
			reason = "mention_raid"
		case AutoModTriggerMemberProfile:
			if in.Profile == nil {
				continue
			}
			haystack := strings.ToLower(in.Profile.Username + "\n" + in.Profile.GlobalName + "\n" + in.Profile.Nick)
			matched, reason = textMatches(rule.TriggerMetadata, haystack)
			reason = "member_profile"
		default:
			matched, reason = ruleMatches(rule, lower, mentions)
		}
		if matched {
			out = append(out, AutoModViolation{Rule: rule, Reason: reason})
		}
	}
	return out
}

// textMatches applies keyword/regex/preset terms to an arbitrary profile string.
func textMatches(meta AutoModTriggerMetadata, text string) (bool, string) {
	for _, kw := range meta.KeywordFilter {
		if kw == "" {
			continue
		}
		if containsWithAllowList(text, strings.ToLower(kw), meta.AllowList) {
			return true, "keyword"
		}
	}
	for _, pattern := range meta.RegexPatterns {
		re, err := regexp.Compile(pattern)
		if err == nil && re.MatchString(text) {
			return true, "regex"
		}
	}
	for _, preset := range meta.Presets {
		for _, term := range autoModPresets[strings.ToLower(preset)] {
			if containsWithAllowList(text, term, meta.AllowList) {
				return true, "preset"
			}
		}
	}
	return false, ""
}

// HasAutoModBlock reports whether a matched rule blocks the message.
func HasAutoModBlock(rule AutoModRule) bool {
	for _, a := range rule.Actions {
		if a.Type == AutoModActionBlockMessage {
			return true
		}
	}
	return false
}

func ruleMatches(rule AutoModRule, lowerContent string, mentions int) (bool, string) {
	meta := rule.TriggerMetadata
	switch rule.TriggerType {
	case AutoModTriggerKeyword:
		for _, kw := range meta.KeywordFilter {
			if kw == "" {
				continue
			}
			if containsWithAllowList(lowerContent, strings.ToLower(kw), meta.AllowList) {
				return true, "keyword"
			}
		}
		for _, pattern := range meta.RegexPatterns {
			re, err := regexp.Compile(pattern)
			if err == nil && re.MatchString(lowerContent) {
				return true, "regex"
			}
		}
	case AutoModTriggerKeywordPreset:
		for _, preset := range meta.Presets {
			for _, term := range autoModPresets[strings.ToLower(preset)] {
				if containsWithAllowList(lowerContent, term, meta.AllowList) {
					return true, "preset:" + preset
				}
			}
		}
	case AutoModTriggerMentionSpam:
		if meta.MentionTotalLimit != nil && mentions > *meta.MentionTotalLimit {
			return true, "mention_spam"
		}
	}
	return false, ""
}

func containsWithAllowList(content, needle string, allow []string) bool {
	if !strings.Contains(content, needle) {
		return false
	}
	for _, a := range allow {
		if a != "" && strings.Contains(content, strings.ToLower(a)) {
			return false
		}
	}
	return true
}

func containsID(list []string, id string) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}

func intersects(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}
