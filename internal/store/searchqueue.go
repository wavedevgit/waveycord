package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/wavedevgit/waveycord/internal/search"
	"github.com/wavedevgit/waveycord/internal/sqlx"
)

// Durable search indexing queue. Message writes append a row instead of calling the
// index inline, and a worker drains rows in id order, deleting each only after the
// index accepted it. Message mutations insert their job in the same transaction;
// a failed index call leaves the row for retry.

// Search job operations.
const (
	SearchOpIndex  = "index"
	SearchOpDelete = "delete"
)

// SearchJob is one queued index operation.
type SearchJob struct {
	ID        int64
	Op        string
	MessageID string
	GuildID   string
	ChannelID string
	AuthorID  string
	Content   string
	CreatedAt int64
	Attempts  int
}

// SetDurableSearchQueue switches message indexing from inline calls to the durable
// queue. Deleting a message uses the same path.
func (s *Store) SetDurableSearchQueue(enabled bool) {
	s.queueMu.Lock()
	s.durableQueue = enabled
	s.queueMu.Unlock()
}

// DurableSearchQueue reports whether the queue is enabled.
func (s *Store) DurableSearchQueue() bool {
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	return s.durableQueue
}

// enqueueSearchTx replaces a pending job inside the message mutation transaction.
func (s *Store) enqueueSearchTx(tx *sqlx.Tx, job SearchJob) error {
	if _, err := tx.Exec(`DELETE FROM search_queue WHERE message_id = ?`, job.MessageID); err != nil {
		return mapErr(err)
	}
	if _, err := tx.Exec(`INSERT INTO search_queue
		(op, message_id, guild_id, channel_id, author_id, content, created_at)
		VALUES (?,?,?,?,?,?,?)`,
		job.Op, job.MessageID, job.GuildID, job.ChannelID, job.AuthorID, job.Content, job.CreatedAt); err != nil {
		return mapErr(err)
	}
	if s.DB.Dialect == sqlx.Postgres {
		// NOTIFY is delivered only after the containing transaction commits.
		if _, err := tx.Exec(`SELECT pg_notify('waveycord_search', ?)`, job.MessageID); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// SearchQueueLen reports the pending job count.
func (s *Store) SearchQueueLen() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM search_queue`).Scan(&n)
	return n, mapErr(err)
}

// ProcessSearchQueue drains up to limit jobs. Each accepted job is deleted; a job
// whose index call failed stays queued with its attempt count incremented. It
// returns the number of jobs completed.
func (s *Store) ProcessSearchQueue(ctx context.Context, limit int) (int, error) {
	s.queueProcessMu.Lock()
	defer s.queueProcessMu.Unlock()
	if s.Search == nil {
		return 0, errors.New("store: no search index configured")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, mapErr(err)
	}
	defer tx.Rollback()
	if s.DB.Dialect == sqlx.Postgres {
		var locked bool
		if err := tx.QueryRow(`SELECT pg_try_advisory_xact_lock(782345190124)`).Scan(&locked); err != nil {
			return 0, err
		}
		if !locked {
			return 0, nil
		}
	}
	query := `SELECT id, op, message_id, guild_id, channel_id, author_id, content, created_at, attempts
		FROM search_queue ORDER BY id ASC LIMIT ?`
	if s.DB.Dialect == sqlx.Postgres {
		query += ` FOR UPDATE`
	}
	rows, err := tx.Query(query, limit)
	if err != nil {
		return 0, mapErr(err)
	}
	var jobs []SearchJob
	for rows.Next() {
		var j SearchJob
		if err := rows.Scan(&j.ID, &j.Op, &j.MessageID, &j.GuildID, &j.ChannelID,
			&j.AuthorID, &j.Content, &j.CreatedAt, &j.Attempts); err != nil {
			rows.Close()
			return 0, mapErr(err)
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, mapErr(err)
	}

	done := 0
	for _, j := range jobs {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		var applyErr error
		switch j.Op {
		case SearchOpDelete:
			if durable, ok := s.Search.(search.DurableIndex); ok {
				applyErr = durable.DeleteDurable(ctx, j.MessageID)
			} else {
				applyErr = s.Search.Delete(ctx, j.MessageID)
			}
		default:
			if j.GuildID == "" {
				applyErr = nil
			} else {
				doc := search.Doc{
					MessageID: j.MessageID, GuildID: j.GuildID, ChannelID: j.ChannelID,
					AuthorID: j.AuthorID, Content: j.Content, CreatedAt: j.CreatedAt,
				}
				if durable, ok := s.Search.(search.DurableIndex); ok {
					applyErr = durable.IndexDurable(ctx, doc)
				} else {
					applyErr = s.Search.Index(ctx, doc)
				}
			}
		}
		if applyErr != nil {
			if _, err := tx.Exec(`UPDATE search_queue SET attempts = attempts + 1 WHERE id = ?`, j.ID); err != nil {
				return done, mapErr(err)
			}
			continue
		}
		if _, err := tx.Exec(`DELETE FROM search_queue WHERE id = ?`, j.ID); err != nil {
			return done, mapErr(err)
		}
		done++
	}
	if err := tx.Commit(); err != nil {
		return 0, mapErr(err)
	}
	return done, nil
}

// RunSearchQueue drains the queue until ctx ends.
func (s *Store) RunSearchQueue(ctx context.Context, interval time.Duration, batch int) {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	for {
		if _, err := s.ProcessSearchQueue(ctx, batch); err != nil && ctx.Err() == nil {
			// A transient index failure leaves rows queued; the next tick retries.
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// ErrListenUnsupported is returned when the wake-up listener is requested on a
// non-Postgres handle.
var ErrListenUnsupported = errors.New("store: LISTEN requires postgres")

// ListenSearchQueue opens a dedicated connection, issues LISTEN, and signals the
// returned channel on each notification. The connection is closed when ctx ends, so
// the listener never shares the pooled writer. Notifications are hints only: the
// durable queue row is the guarantee, and a missed notification costs one poll
// interval.
func (s *Store) ListenSearchQueue(ctx context.Context) (<-chan struct{}, error) {
	if s.DB.Dialect != sqlx.Postgres {
		return nil, ErrListenUnsupported
	}
	if s.pgDSN == "" {
		return nil, errors.New("store: postgres dsn not set for LISTEN")
	}
	conn, err := pgx.Connect(ctx, s.pgDSN)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, `LISTEN waveycord_search`); err != nil {
		_ = conn.Close(ctx)
		return nil, err
	}
	ch := make(chan struct{}, 1)
	go func() {
		defer close(ch)
		defer conn.Close(context.Background())
		for {
			if _, err := conn.WaitForNotification(ctx); err != nil {
				return
			}
			select {
			case ch <- struct{}{}:
			default:
				// A wake-up is already pending; coalescing is fine.
			}
		}
	}()
	return ch, nil
}

// RunSearchQueueWithWake drains the queue on every notification and on the poll
// interval, whichever comes first.
func (s *Store) RunSearchQueueWithWake(ctx context.Context, wake <-chan struct{}, interval time.Duration, batch int) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	drain := func() bool {
		limit := batch
		if limit <= 0 {
			limit = 100
		}
		if limit > 1000 {
			limit = 1000
		}
		for ctx.Err() == nil {
			n, err := s.ProcessSearchQueue(ctx, limit)
			if err != nil || n < limit {
				return ctx.Err() == nil
			}
		}
		return false
	}
	if !drain() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-wake:
			if !ok {
				// Listener stopped; fall back to polling until ctx ends.
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if !drain() {
							return
						}
					}
				}
			}
			if !drain() {
				return
			}
		case <-ticker.C:
			if !drain() {
				return
			}
		}
	}
}
