package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/wavedevgit/waveycord/internal/sqlx"
)

// Message bucket granularity. The storage post partitions by channel plus a
// static time window; one day is the window used here, and partitions cover one
// calendar month each so old months drop without rewriting live data.
const (
	MessageHashPartitions = 8
	bucketMillis          = int64(24 * time.Hour / time.Millisecond)
)

// DayBucket converts a millisecond timestamp into the message bucket value.
func DayBucket(ms int64) int64 { return ms / bucketMillis }

// EnsureMessagePartitions creates the monthly children for every hash partition
// across [from, to]. Existing children and rows already in a DEFAULT partition
// are left alone. SQLite is a no-op.
func EnsureMessagePartitions(ctx context.Context, handle *sqlx.DB, from, to time.Time) error {
	if handle.Dialect != sqlx.Postgres {
		return nil
	}
	conn, err := handle.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return ensurePartitions(ctx, conn, from, to)
}

func ensurePartitions(ctx context.Context, conn *sql.Conn, from, to time.Time) error {
	if from.After(to) {
		return nil
	}
	start := monthStart(from)
	end := monthStart(to).AddDate(0, 1, 0)
	for month := start; month.Before(end); month = month.AddDate(0, 1, 0) {
		next := month.AddDate(0, 1, 0)
		lo, hi := DayBucket(month.UnixMilli()), DayBucket(next.UnixMilli())
		for h := 0; h < MessageHashPartitions; h++ {
			name := fmt.Sprintf("messages_h%d_y%04d%02d", h, month.Year(), int(month.Month()))
			ddl := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF messages_h%d FOR VALUES FROM (%d) TO (%d)`,
				name, h, lo, hi)
			if _, err := conn.ExecContext(ctx, ddl); err != nil {
				// A DEFAULT partition holding rows for this range is the only
				// expected failure; keep the rows where they are.
				if isDefaultPartitionConflict(err) {
					continue
				}
				return fmt.Errorf("db: create partition %s: %w", name, err)
			}
		}
	}
	return nil
}

func isDefaultPartitionConflict(err error) bool {
	msg := err.Error()
	for _, needle := range []string{
		"default partition", "updated partition constraint",
		"would be violated by some row", "moving rows",
	} {
		if containsFold(msg, needle) {
			return true
		}
	}
	return false
}

func containsFold(haystack, needle string) bool {
	h, n := []rune(haystack), []rune(needle)
	if len(n) == 0 || len(h) < len(n) {
		return len(n) == 0
	}
	for i := 0; i+len(n) <= len(h); i++ {
		ok := true
		for j := range n {
			a, b := h[i+j], n[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func monthStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// prepareMessages detects a pre-partitioning messages table and renames it so the
// schema can create the partitioned parent. It returns whether a migration is
// required and the timestamp range of the legacy rows.
func prepareMessages(ctx context.Context, conn *sql.Conn) (bool, int64, int64, error) {
	var kind string
	err := conn.QueryRowContext(ctx, `SELECT c.relkind::text FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'messages' AND n.nspname = current_schema()`).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, 0, nil
	}
	if err != nil {
		return false, 0, 0, err
	}
	if kind == "p" {
		return false, 0, 0, nil
	}
	if kind != "r" {
		return false, 0, 0, fmt.Errorf("db: messages has unexpected relkind %q", kind)
	}
	if _, err := conn.ExecContext(ctx, `ALTER TABLE messages RENAME TO messages_legacy_migrating`); err != nil {
		return false, 0, 0, fmt.Errorf("db: rename legacy messages: %w", err)
	}
	// Index names survive a table rename and would block the partitioned table's
	// indexes, so drop the legacy ones before the schema runs.
	// Old reactions and pins hold a foreign key to messages(id); the partitioned
	// table's key is (channel_id, bucket, id), so those constraints cannot follow.
	_, _ = conn.ExecContext(ctx, `ALTER TABLE message_reactions DROP CONSTRAINT IF EXISTS message_reactions_message_id_fkey`)
	_, _ = conn.ExecContext(ctx, `ALTER TABLE pins DROP CONSTRAINT IF EXISTS pins_message_id_fkey`)
	_, _ = conn.ExecContext(ctx, `ALTER TABLE messages_legacy_migrating DROP CONSTRAINT IF EXISTS messages_pkey`)
	_, _ = conn.ExecContext(ctx, `DROP INDEX IF EXISTS messages_channel`)
	_, _ = conn.ExecContext(ctx, `DROP INDEX IF EXISTS messages_guild`)
	var lo, hi sql.NullInt64
	row := conn.QueryRowContext(ctx, `SELECT MIN(timestamp), MAX(timestamp) FROM messages_legacy_migrating`)
	if err := row.Scan(&lo, &hi); err != nil {
		return false, 0, 0, err
	}
	return true, lo.Int64, hi.Int64, nil
}

// copyLegacyMessages moves rows into the partitioned table with their bucket.
func copyLegacyMessages(ctx context.Context, conn *sql.Conn) error {
	const cols = `id, channel_id, guild_id, author_id, content, timestamp, edited_timestamp,
		tts, mention_everyone, mentions, mention_roles, mention_channels, pinned, type, flags,
		referenced_message_id, webhook_id, application_id, components, attachments, embeds,
		sticker_items, nonce`
	q := `INSERT INTO messages (` + cols + `, bucket)
		SELECT ` + cols + `, timestamp / 86400000 FROM messages_legacy_migrating`
	if _, err := conn.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("db: copy legacy messages: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO message_index (id, channel_id, bucket, created_at)
		SELECT id, channel_id, timestamp / 86400000, timestamp FROM messages_legacy_migrating
		ON CONFLICT (id) DO NOTHING`); err != nil {
		return fmt.Errorf("db: build legacy message index: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `DROP TABLE messages_legacy_migrating`); err != nil {
		return fmt.Errorf("db: drop legacy messages: %w", err)
	}
	return nil
}
