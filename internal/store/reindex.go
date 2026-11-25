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
