package api

import (
	"context"
	"strings"
	"time"

	"github.com/wavedevgit/waveycord/internal/model"
	"github.com/wavedevgit/waveycord/internal/perm"
	"github.com/wavedevgit/waveycord/internal/store"
)

// pollCreateRequest is the documented create-message poll payload.
type pollCreateRequest struct {
	Question *struct {
		Text string `json:"text"`
	} `json:"question"`
	Answers []struct {
		PollMedia *struct {
			Text  string           `json:"text"`
			Emoji *model.PollEmoji `json:"emoji"`
		} `json:"poll_media"`
	} `json:"answers"`
	Duration         *int `json:"duration"`
	AllowMultiselect bool `json:"allow_multiselect"`
	LayoutType       *int `json:"layout_type"`
}

// validatePoll turns the request into a stored poll shape and expiry.
func validatePoll(req *pollCreateRequest) (model.Poll, int64, string) {
	var p model.Poll
	if req == nil || req.Question == nil {
		return p, 0, "Must include a question"
	}
	question := strings.TrimSpace(req.Question.Text)
	if question == "" || len([]rune(question)) > 300 {
		return p, 0, "Question must be 1-300 characters"
	}
	if len(req.Answers) == 0 || len(req.Answers) > store.MaxPollAnswers {
		return p, 0, "Must include 1-10 answers"
	}
	answers := make([]model.PollAnswer, 0, len(req.Answers))
	for i, a := range req.Answers {
		if a.PollMedia == nil {
			return p, 0, "Answer must include poll_media"
		}
		text := strings.TrimSpace(a.PollMedia.Text)
		if text == "" || len([]rune(text)) > 55 {
			return p, 0, "Answer text must be 1-55 characters"
		}
		if e := a.PollMedia.Emoji; e != nil && (e.ID == nil || *e.ID == "") && (e.Name == nil || *e.Name == "") {
			return p, 0, "Answer emoji must include an id or name"
		}
		answers = append(answers, model.PollAnswer{
			AnswerID:  i + 1,
			PollMedia: model.PollMedia{Text: text, Emoji: a.PollMedia.Emoji},
		})
	}
	hours := 24
	if req.Duration != nil {
		hours = *req.Duration
	}
	if hours < 1 || hours > 768 {
		return p, 0, "Duration must be 1-768 hours"
	}
	layout := 1
	if req.LayoutType != nil {
		layout = *req.LayoutType
	}
	if layout != 1 {
		return p, 0, "Layout type must be 1"
	}
	p.Question = model.PollMedia{Text: question}
	p.Answers = answers
	p.AllowMultiselect = req.AllowMultiselect
	p.LayoutType = layout
	return p, nowMillisFn() + int64(hours)*3600*1000, ""
}

// loadPollMessage resolves the channel, verifies the message carries a poll and
// returns it.
func (s *Server) loadPollMessage(c *Ctx, ch model.Channel) (string, error) {
	messageID := c.Param("messageID")
	if chID, err := s.Store.PollChannel(messageID); err != nil || chID != ch.ID {
		return "", ErrUnknownMessage
	}
	return messageID, nil
}

// handleSetPollVotes replaces the caller's selection. An empty answer_ids retracts
// the vote. This route is undocumented in the public reference but is what the
// client uses (`PUT .../answers/@me` with `{answer_ids}`).
func (s *Server) handleSetPollVotes(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ViewChannel|perm.ReadMessageHistory)
	if err != nil {
		fail(c, err)
		return
	}
	messageID, err := s.loadPollMessage(c, ch)
	if err != nil {
		fail(c, err)
		return
	}
	if c.User.Bot {
		fail(c, ErrBotsCannotVote)
		return
	}
	var req struct {
		AnswerIDs []int `json:"answer_ids"`
	}
	if err := c.Bind(&req); err != nil {
		fail(c, ErrInvalidForm)
		return
	}
	poll, err := s.Store.Poll(messageID)
	if err != nil {
		fail(c, ErrUnknownMessage)
		return
	}
	err = s.Store.SetPollVotes(messageID, c.User.ID, req.AnswerIDs, poll.AllowMultiselect)
	switch err {
	case nil:
		s.dispatchPollUpdate(ch, messageID)
		c.JSONStatus(204, nil)
	case store.ErrPollEnded:
		fail(c, ErrPollEnded)
	case store.ErrPollAnswerNotFound:
		fail(c, ErrPollAnswerNotFound)
	default:
		fail(c, err)
	}
}

// handlePollVoters lists users who voted for one answer.
func (s *Server) handlePollVoters(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ViewChannel|perm.ReadMessageHistory)
	if err != nil {
		fail(c, err)
		return
	}
	messageID, err := s.loadPollMessage(c, ch)
	if err != nil {
		fail(c, err)
		return
	}
	answerID, ok := atoiParam(c.Param("answerID"))
	if !ok {
		fail(c, ErrPollAnswerNotFound)
		return
	}
	poll, err := s.Store.Poll(messageID)
	if err != nil {
		fail(c, ErrUnknownMessage)
		return
	}
	found := false
	for _, a := range poll.Answers {
		if a.AnswerID == answerID {
			found = true
			break
		}
	}
	if !found {
		fail(c, ErrPollAnswerNotFound)
		return
	}
	q := c.R.URL.Query()
	ids, err := s.Store.PollVoters(messageID, answerID, q.Get("after"), atoiSafe(q.Get("limit")))
	if err != nil {
		fail(c, err)
		return
	}
	users := make([]model.User, 0, len(ids))
	for _, uid := range ids {
		if u, err := s.Store.UserByID(uid); err == nil {
			users = append(users, u)
		}
	}
	c.JSON(map[string]any{"users": users})
}

// handleExpirePoll ends a poll. Only its author may end it.
func (s *Server) handleExpirePoll(c *Ctx) {
	ch, err := s.requireChannel(c.User.ID, c.Param("channelID"), perm.ViewChannel|perm.ReadMessageHistory)
	if err != nil {
		fail(c, err)
		return
	}
	messageID, err := s.loadPollMessage(c, ch)
	if err != nil {
		fail(c, err)
		return
	}
	author, err := s.Store.PollAuthor(messageID)
	if err != nil {
		fail(c, ErrUnknownMessage)
		return
	}
	if author != c.User.ID {
		fail(c, ErrMissingPerms)
		return
	}
	if err := s.Store.ExpirePoll(messageID); err != nil {
		fail(c, ErrUnknownMessage)
		return
	}
	s.dispatchPollUpdate(ch, messageID)
	msg, err := s.Store.MessageFor(messageID, c.User.ID)
	if err != nil {
		fail(c, ErrUnknownMessage)
		return
	}
	c.JSON(msg)
}

// dispatchPollUpdate emits a MESSAGE_UPDATE with the refreshed poll, serialized
// per recipient so poll results carry the viewer's own me_voted flags. Guild fanout
// is O(members) because a single shared payload cannot be viewer-aware; polls are
// low-frequency, so this is preferred over shipping wrong per-user state.
func (s *Server) dispatchPollUpdate(ch model.Channel, messageID string) {
	if s.Hub == nil {
		return
	}
	sendTo := func(userID string) {
		if userID == "" {
			return
		}
		msg, err := s.Store.MessageFor(messageID, userID)
		if err != nil {
			return
		}
		s.Hub.DispatchUser(userID, "MESSAGE_UPDATE", msg)
	}
	if ch.GuildID != nil {
		ids, err := s.Store.GuildMemberIDs(*ch.GuildID)
		if err != nil {
			if msg, merr := s.Store.Message(messageID); merr == nil {
				s.Hub.DispatchGuild(*ch.GuildID, "MESSAGE_UPDATE", msg)
			}
			return
		}
		for _, uid := range ids {
			sendTo(uid)
		}
		return
	}
	for _, r := range ch.Recipients {
		sendTo(r.ID)
	}
	sendTo(msgAuthorID(s, messageID))
}

// msgAuthorID resolves the author for DM fanout.
func msgAuthorID(s *Server, messageID string) string {
	msg, err := s.Store.Message(messageID)
	if err != nil {
		return ""
	}
	return msg.Author.ID
}

// SweepExpiredPolls finalizes polls whose expiry has passed and dispatches a
// MESSAGE_UPDATE for each. It returns the finalized message ids. main runs it on a
// ticker; tests call it directly.
func (s *Server) SweepExpiredPolls() []string {
	// Stale remote-auth handshakes are removed on the same cadence.
	_, _ = s.Store.ExpireRemoteAuthSessions(nowMillisFn())
	expired, err := s.Store.FinalizeExpiredPolls(nowMillisFn(), 200)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(expired))
	for _, e := range expired {
		ch, err := s.Store.Channel(e.ChannelID)
		if err != nil {
			continue
		}
		s.dispatchPollUpdate(ch, e.MessageID)
		ids = append(ids, e.MessageID)
	}
	return ids
}

// StartPollSweeper ticks the expiry sweep until ctx is done.
func (s *Server) StartPollSweeper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.SweepExpiredPolls()
			}
		}
	}()
}

// atoiParam parses a non-negative integer path parameter.
func atoiParam(v string) (int, bool) {
	if v == "" {
		return 0, false
	}
	n := 0
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0, false
		}
		if n > (1<<31-1)/10 {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}
