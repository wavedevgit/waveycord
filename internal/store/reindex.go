package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/wavedevgit/waveycord/internal/search"
)

// ReindexSearch rebuilds the configured search index from message rows. With
// reset=true and an index implementing search.Resetter, existing index contents
// are dropped first. The scan is id-ordered and bounded so it can run in the
// background without holding a transaction.
func (s *Store) ReindexSearch(ctx context.Context, reset bool, batch int, budget time.Duration) (int, error) {
	if s.Search == nil {
		return 0, errors.New("store: no search index configured")
	}
	if batch <= 0 {
		batch = 500
	}
	if reset {
		if resetter, ok := s.Search.(search.Resetter); ok {
			if err := resetter.Reset(ctx); err != nil {
				return 0, err
			}
		}
	}
	deadline := time.Now().Add(budget)
	after := ""
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if budget > 0 && time.Now().After(deadline) {
			return total, nil
		}
		q := `SELECT id, guild_id, channel_id, author_id, content, timestamp FROM messages
			WHERE guild_id IS NOT NULL`
		args := []any{}
		if after != "" {
			q += ` AND id > ?`
			args = append(args, after)
		}
		q += ` ORDER BY id ASC LIMIT ?`
		args = append(args, batch)
		rows, err := s.DB.Query(q, args...)
		if err != nil {
			return total, mapErr(err)
		}
		type doc struct {
			id, guild, channel, author, content string
			ts                                  int64
		}
		var docs []doc
		for rows.Next() {
			var (
				d       doc
				guild   sql.NullString
				channel sql.NullString
			)
			if err := rows.Scan(&d.id, &guild, &channel, &d.author, &d.content, &d.ts); err != nil {
				rows.Close()
				return total, mapErr(err)
			}
			d.guild = stringOr(guild, "")
			d.channel = stringOr(channel, "")
			docs = append(docs, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return total, mapErr(err)
		}
		if len(docs) == 0 {
			return total, nil
		}
		for _, d := range docs {
			after = d.id
			if d.guild == "" {
				continue
			}
			if err := s.Search.Index(ctx, search.Doc{
				MessageID: d.id, GuildID: d.guild, ChannelID: d.channel,
				AuthorID: d.author, Content: d.content, CreatedAt: d.ts,
			}); err != nil {
				return total, err
			}
			total++
		}
	}
}

// countGuildMessages returns the authoritative message count for one guild.
func (s *Store) countGuildMessages(guildID string) (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE guild_id = ?`, guildID).Scan(&n)
	return n, mapErr(err)
}

// ReindexGuild rebuilds one guild's documents from message rows without touching
// any other guild. The caller decides whether to reset that guild's index first.
func (s *Store) ReindexGuild(ctx context.Context, guildID string, batch int) (int, error) {
	if s.Search == nil {
		return 0, errors.New("store: no search index configured")
	}
	if batch <= 0 {
		batch = 500
	}
	after := ""
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		rows, err := s.DB.Query(`SELECT id, channel_id, author_id, content, timestamp FROM messages
			WHERE guild_id = ? AND id > ? ORDER BY id ASC LIMIT ?`, guildID, after, batch)
		if err != nil {
			return total, mapErr(err)
		}
		type doc struct {
			id, channel, author, content string
			ts                           int64
		}
		var docs []doc
		for rows.Next() {
			var d doc
			if err := rows.Scan(&d.id, &d.channel, &d.author, &d.content, &d.ts); err != nil {
				rows.Close()
				return total, mapErr(err)
			}
			docs = append(docs, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return total, mapErr(err)
		}
		if len(docs) == 0 {
			return total, nil
		}
		for _, d := range docs {
			after = d.id
			if err := s.Search.Index(ctx, search.Doc{
				MessageID: d.id, GuildID: guildID, ChannelID: d.channel,
				AuthorID: d.author, Content: d.content, CreatedAt: d.ts,
			}); err != nil {
				return total, err
			}
			total++
		}
	}
}

// BFGMaintainGuild checks one guild's index against its row count and, when the
// index has drifted past the policy, resets and rebuilds that guild alone. It
// reports whether a rebuild ran and how many documents were indexed.
func (s *Store) BFGMaintainGuild(ctx context.Context, guildID string, policy search.BFGPolicy, batch int) (bool, int, error) {
	if s.Search == nil {
		return false, 0, nil
	}
	counter, ok := s.Search.(search.GuildCounter)
	if !ok {
		return false, 0, nil
	}
	rows, err := s.countGuildMessages(guildID)
	if err != nil {
		return false, 0, err
	}
	indexed, err := counter.GuildDocCount(ctx, guildID)
	if err != nil {
		return false, 0, err
	}
	if !policy.NeedsRebuild(rows, indexed) {
		return false, indexed, nil
	}
	if resetter, ok := s.Search.(search.GuildResetter); ok {
		if err := resetter.ResetGuild(ctx, guildID); err != nil {
			return false, indexed, err
		}
	}
	n, err := s.ReindexGuild(ctx, guildID, batch)
	return true, n, err
}

// SetBFGPolicy enables the write-triggered per-guild drift check. A zero threshold
// disables it. Every `every` writes to one guild, the drift check runs in the
// background, at most one per guild at a time.
func (s *Store) SetBFGPolicy(policy search.BFGPolicy, every int) {
	if every <= 0 {
		every = 250
	}
	s.bfgMu.Lock()
	s.bfgPolicy = policy
	s.bfgEvery = every
	s.bfgMu.Unlock()
}

// noteGuildWrite counts writes per guild and occasionally runs the drift check.
func (s *Store) noteGuildWrite(guildID string) {
	if guildID == "" || s.Search == nil {
		return
	}
	s.bfgMu.Lock()
	if s.bfgPolicy.Threshold <= 0 {
		s.bfgMu.Unlock()
		return
	}
	if s.bfgWrites == nil {
		s.bfgWrites = map[string]int{}
	}
	if s.bfgBusy == nil {
		s.bfgBusy = map[string]bool{}
	}
	s.bfgWrites[guildID]++
	due := s.bfgWrites[guildID]%s.bfgEvery == 0
	if !due || s.bfgBusy[guildID] {
		s.bfgMu.Unlock()
		return
	}
	s.bfgBusy[guildID] = true
	policy := s.bfgPolicy
	s.bfgMu.Unlock()
	go func() {
		defer func() {
			s.bfgMu.Lock()
			s.bfgBusy[guildID] = false
			s.bfgMu.Unlock()
		}()
		_, _, _ = s.BFGMaintainGuild(context.Background(), guildID, policy, 500)
	}()
}
