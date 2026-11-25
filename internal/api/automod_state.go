package api

import (
	"sync"
	"time"

	"github.com/wavedevgit/waveycord/internal/store"
)

// Spam detection is stateful: a per-user, per-channel sliding window counts
// messages. Discord's production heuristics are unpublished; this implements the
// documented SPAM trigger with a fixed window and threshold.
const (
	spamWindow  = 5 * time.Second
	spamLimit   = 5
	spamMaxKeys = 100_000
)

type messageRate struct {
	mu   sync.Mutex
	hits map[string][]int64
}

func newMessageRate() *messageRate { return &messageRate{hits: map[string][]int64{}} }

// hit records a message and returns the count inside the window, including this
// message.
func (r *messageRate) hit(key string) int {
	now := time.Now().UnixMilli()
	cutoff := now - spamWindow.Milliseconds()
	r.mu.Lock()
	defer r.mu.Unlock()
	// ponytail: unbounded key growth is capped by dropping the whole map once it
	// exceeds spamMaxKeys; use an LRU if many guilds stay hot simultaneously.
	if len(r.hits) > spamMaxKeys {
		r.hits = map[string][]int64{}
	}
	list := r.hits[key]
	kept := list[:0]
	for _, ts := range list {
		if ts >= cutoff {
			kept = append(kept, ts)
		}
	}
	kept = append(kept, now)
	r.hits[key] = kept
	return len(kept)
}

// enforceProfileAutoMod scans a member's username, display name and nickname
// against MEMBER_PROFILE rules and applies timeout/alert actions. Blocking cannot
// apply to a profile, so block actions are ignored.
func (s *Server) enforceProfileAutoMod(guildID, userID string) {
	rules, err := s.Store.ListAutoModRules(guildID)
	if err != nil || len(rules) == 0 {
		return
	}
	member, err := s.Store.Member(guildID, userID)
	if err != nil || member.User == nil {
		return
	}
	profile := &store.ProfileFields{Username: member.User.Username}
	if member.User.GlobalName != nil {
		profile.GlobalName = *member.User.GlobalName
	}
	if member.Nick != nil {
		profile.Nick = *member.Nick
	}
	matches := store.AutoModEvaluate(rules, store.EvalInput{
		MemberRoles: member.Roles,
		Profile:     profile,
	})
	for _, match := range matches {
		if match.Rule.TriggerType != store.AutoModTriggerMemberProfile {
			continue
		}
		for _, action := range match.Rule.Actions {
			switch action.Type {
			case store.AutoModActionTimeout:
				if action.Metadata != nil && action.Metadata.DurationSeconds != nil {
					until := time.Now().Add(time.Duration(*action.Metadata.DurationSeconds) * time.Second).UnixMilli()
					_ = s.Store.SetMemberTimeout(guildID, userID, until)
				}
			case store.AutoModActionSendAlert:
				s.sendProfileAlert(guildID, match)
			}
		}
	}
}

// sendProfileAlert posts the profile-rule alert into its configured channel.
func (s *Server) sendProfileAlert(guildID string, match store.AutoModViolation) {
	var channelID *string
	custom := ""
	for _, action := range match.Rule.Actions {
		if action.Type == store.AutoModActionSendAlert && action.Metadata != nil {
			channelID = action.Metadata.ChannelID
			if action.Metadata.CustomMessage != nil {
				custom = *action.Metadata.CustomMessage
			}
		}
	}
	if channelID == nil {
		return
	}
	ch, err := s.Store.Channel(*channelID)
	if err != nil {
		return
	}
	system, err := s.Store.EnsureSystemUser()
	if err != nil {
		return
	}
	content := custom
	if content == "" {
		content = "AutoMod rule \"" + match.Rule.Name + "\" triggered (member profile)."
	}
	msg, err := s.Store.CreateMessage(store.CreateMessageParams{
		ChannelID: ch.ID, GuildID: guildID, AuthorID: system.ID, Content: content,
	})
	if err != nil {
		return
	}
	if s.Hub != nil {
		s.Hub.DispatchGuild(guildID, "MESSAGE_CREATE", msg)
	}
	s.cfg.Logger.Info("automod profile alert", "guild", guildID, "rule", match.Rule.ID)
}
