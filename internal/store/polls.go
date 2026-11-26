package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/wavedevgit/waveycord/internal/model"
)

// MaxPollAnswers matches the documented limit.
const MaxPollAnswers = 10

// PollExpiry layout for the ISO8601 field.
func isoTime(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000000+00:00")
}

// CreatePoll stores a poll for an existing message. Answers must already carry
// their sequential answer_id.
func (s *Store) CreatePoll(messageID, channelID string, p model.Poll, expiryMs int64, allowMultiselect bool, layout int) error {
	if len(p.Answers) == 0 || len(p.Answers) > MaxPollAnswers {
		return errors.New("store: invalid poll answer count")
	}
	if layout == 0 {
		layout = 1
	}
	raw, err := json.Marshal(p.Answers)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT INTO polls
		(message_id, channel_id, question, answers, expiry, allow_multiselect, layout_type, finalized, created_at)
		VALUES (?,?,?,?,?,?,?,0,?)`,
		messageID, channelID, p.Question.Text, string(raw), expiryMs, allowMultiselect, layout, now())
	return mapErr(err)
}

// Poll loads a poll with tallies and no viewer context.
func (s *Store) Poll(messageID string) (model.Poll, error) {
	return s.PollFor(messageID, "")
}

// PollFor loads a poll with tallies relative to one viewer: answer_counts carry
// me_voted for that user. Counts are always included, matching the documented
// "results may be present while a poll is in progress" note.
func (s *Store) PollFor(messageID, viewer string) (model.Poll, error) {
	p, snap, err := s.pollBase(messageID)
	if err != nil {
		return p, err
	}
	voted, err := s.pollVotedAnswers(messageID, viewer)
	if err != nil {
		return p, err
	}
	counts := snap
	if !p.Results.IsFinalized {
		// Live counts while the poll is open; once finalized the stored snapshot is
		// authoritative so a later vote-row change cannot move the tally.
		counts, err = s.pollCounts(messageID)
		if err != nil {
			return p, err
		}
	}
	for i := range counts {
		counts[i].MeVoted = voted[counts[i].ID]
	}
	p.Results.AnswerCounts = counts
	return p, nil
}

// pollVotedAnswers returns the answer ids the viewer selected.
func (s *Store) pollVotedAnswers(messageID, viewer string) (map[int]bool, error) {
	out := map[int]bool{}
	if viewer == "" {
		return out, nil
	}
	rows, err := s.DB.Query(`SELECT answer_id FROM poll_votes WHERE message_id = ? AND user_id = ?`, messageID, viewer)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, mapErr(err)
		}
		out[id] = true
	}
	return out, mapErr(rows.Err())
}

// pollBase loads the poll row, finalizes it lazily when expired, and returns the
// persisted final tally when one exists.
func (s *Store) pollBase(messageID string) (model.Poll, []model.PollAnswerCount, error) {
	var (
		p           model.Poll
		question    string
		answers     string
		finalCounts string
		expiryMs    int64
		layout      int
		finalized   bool
	)
	err := s.DB.QueryRow(`SELECT question, answers, expiry, allow_multiselect, layout_type, finalized, final_counts
		FROM polls WHERE message_id = ?`, messageID).
		Scan(&question, &answers, &expiryMs, &p.AllowMultiselect, &layout, &finalized, &finalCounts)
	if err != nil {
		return p, nil, mapErr(err)
	}
	p.Question = model.PollMedia{Text: question}
	if err := json.Unmarshal([]byte(answers), &p.Answers); err != nil {
		return p, nil, err
	}
	if p.Answers == nil {
		p.Answers = []model.PollAnswer{}
	}
	p.Expiry = isoTime(expiryMs)
	p.LayoutType = layout
	if !finalized && now() >= expiryMs {
		if changed, err := s.finalizePoll(messageID); err == nil && changed {
			finalized = true
			if row := s.DB.QueryRow(`SELECT final_counts FROM polls WHERE message_id = ?`, messageID); row != nil {
				_ = row.Scan(&finalCounts)
			}
		}
	}
	snap := []model.PollAnswerCount{}
	if finalized && finalCounts != "" {
		_ = json.Unmarshal([]byte(finalCounts), &snap)
	}
	p.Results = &model.PollResults{IsFinalized: finalized}
	return p, snap, nil
}

// finalizePoll freezes the exact tallies into final_counts and marks the poll
// finalized. It reports whether this call performed the finalization; a poll that
// was already finalized is left untouched.
func (s *Store) finalizePoll(messageID string) (bool, error) {
	counts, err := s.pollCounts(messageID)
	if err != nil {
		return false, err
	}
	raw, err := json.Marshal(counts)
	if err != nil {
		return false, err
	}
	res, err := s.DB.Exec(`UPDATE polls SET finalized = 1, final_counts = ? WHERE message_id = ? AND finalized = 0`,
		string(raw), messageID)
	if err != nil {
		return false, mapErr(err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// pollCounts returns one entry per answer that has at least one vote.
func (s *Store) pollCounts(messageID string) ([]model.PollAnswerCount, error) {
	rows, err := s.DB.Query(`SELECT answer_id, COUNT(*) FROM poll_votes WHERE message_id = ? GROUP BY answer_id`, messageID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []model.PollAnswerCount{}
	for rows.Next() {
		var c model.PollAnswerCount
		if err := rows.Scan(&c.ID, &c.Count); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, mapErr(rows.Err())
}

// PollChannel returns the owning channel of a poll message.
func (s *Store) PollChannel(messageID string) (string, error) {
	var ch string
	err := s.DB.QueryRow(`SELECT channel_id FROM polls WHERE message_id = ?`, messageID).Scan(&ch)
	return ch, mapErr(err)
}

// PollExists reports whether the message carries a poll.
func (s *Store) PollExists(messageID string) bool {
	var one int
	err := s.DB.QueryRow(`SELECT 1 FROM polls WHERE message_id = ?`, messageID).Scan(&one)
	return err == nil
}

// SetPollVotes replaces one voter's selection. An empty list retracts the vote. A
// single-select poll accepts at most one answer. Voting twice with the same set is
// idempotent.
func (s *Store) SetPollVotes(messageID, userID string, answerIDs []int, multiselect bool) error {
	var expiryMs int64
	var finalized bool
	var answers string
	if err := s.DB.QueryRow(`SELECT expiry, finalized, answers FROM polls WHERE message_id = ?`, messageID).
		Scan(&expiryMs, &finalized, &answers); err != nil {
		return mapErr(err)
	}
	if finalized || now() >= expiryMs {
		return ErrPollEnded
	}
	if !multiselect && len(answerIDs) > 1 {
		return ErrPollAnswerNotFound
	}
	var parsed []model.PollAnswer
	if err := json.Unmarshal([]byte(answers), &parsed); err != nil {
		return err
	}
	valid := map[int]bool{}
	for _, a := range parsed {
		valid[a.AnswerID] = true
	}
	for _, id := range answerIDs {
		if !valid[id] {
			return ErrPollAnswerNotFound
		}
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM poll_votes WHERE message_id = ? AND user_id = ?`, messageID, userID); err != nil {
		return mapErr(err)
	}
	for _, id := range answerIDs {
		if _, err := tx.Exec(`INSERT INTO poll_votes (message_id, answer_id, user_id, created_at)
			VALUES (?,?,?,?) ON CONFLICT(message_id, answer_id, user_id) DO NOTHING`,
			messageID, id, userID, now()); err != nil {
			return mapErr(err)
		}
	}
	return mapErr(tx.Commit())
}

// ExpirePoll finalizes a poll immediately.
func (s *Store) ExpirePoll(messageID string) error {
	if !s.PollExists(messageID) {
		return ErrNotFound
	}
	_, err := s.finalizePoll(messageID)
	return err
}

// PollVoters lists voters for one answer in id order.
func (s *Store) PollVoters(messageID string, answerID int, after string, limit int) ([]string, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	q := `SELECT user_id FROM poll_votes WHERE message_id = ? AND answer_id = ?`
	args := []any{messageID, answerID}
	if after != "" {
		q += ` AND user_id > ?`
		args = append(args, after)
	}
	q += ` ORDER BY user_id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, uid)
	}
	return out, mapErr(rows.Err())
}

// PollAuthor returns the message author id, used to authorize ending a poll.
func (s *Store) PollAuthor(messageID string) (string, error) {
	var author sql.NullString
	err := s.DB.QueryRow(`SELECT author_id FROM messages WHERE id = ?`, messageID).Scan(&author)
	if err != nil {
		return "", mapErr(err)
	}
	return author.String, nil
}

// ExpiringPoll identifies a poll that has passed its expiry.
type ExpiringPoll struct {
	MessageID string
	ChannelID string
}

// FinalizeExpiredPolls marks up to limit expired polls finalized and returns them,
// so the caller can push an update. A poll finalized by a read is not returned.
func (s *Store) FinalizeExpiredPolls(nowMs int64, limit int) ([]ExpiringPoll, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.DB.Query(`SELECT message_id, channel_id FROM polls
		WHERE finalized = 0 AND expiry <= ? LIMIT ?`, nowMs, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	var out []ExpiringPoll
	for rows.Next() {
		var e ExpiringPoll
		if err := rows.Scan(&e.MessageID, &e.ChannelID); err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	finalized := make([]ExpiringPoll, 0, len(out))
	for _, e := range out {
		changed, err := s.finalizePoll(e.MessageID)
		if err != nil {
			return finalized, err
		}
		if changed {
			finalized = append(finalized, e)
		}
	}
	return finalized, nil
}
