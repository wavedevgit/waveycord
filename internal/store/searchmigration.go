package store

import (
	"context"
	"time"
)

// MigrateSearchIndex incrementally rebuilds every guild's documents, recording a
// per-guild checkpoint so the migration resumes after a restart and can be run by more
// than one instance. Documents are written through the configured index, which routes
// each guild to its assigned search cell; the checkpoint rows make the work resumable
// and observable rather than a single unbounded scan.
func (s *Store) MigrateSearchIndex(ctx context.Context, batch int, budget time.Duration) (int, error) {
	if s.Search == nil {
		return 0, nil
	}
	deadline := time.Now().Add(budget)
	rows, err := s.DB.Query(`SELECT DISTINCT guild_id FROM messages
		WHERE guild_id IS NOT NULL AND guild_id != ''
		  AND guild_id NOT IN (SELECT guild_id FROM search_migration_state WHERE migrated = 1)
		ORDER BY guild_id ASC`)
	if err != nil {
		return 0, mapErr(err)
	}
	var guilds []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			rows.Close()
			return 0, mapErr(err)
		}
		guilds = append(guilds, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, mapErr(err)
	}
	total := 0
	for _, guildID := range guilds {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		if budget > 0 && time.Now().After(deadline) {
			return total, nil
		}
		if s.Search != nil {
			// Per-guild rebuild; skipped guilds stay unmigrated.
			n, err := s.ReindexGuild(ctx, guildID, batch)
			if err != nil {
				return total, err
			}
			total += n
		}
		if _, err := s.DB.Exec(`INSERT INTO search_migration_state (guild_id, migrated, updated_at)
			VALUES (?,?,?)
			ON CONFLICT(guild_id) DO UPDATE SET migrated = 1, updated_at = excluded.updated_at`,
			guildID, true, now()); err != nil {
			return total, mapErr(err)
		}
	}
	return total, nil
}
