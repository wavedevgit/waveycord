// Package search is the message-search boundary. The Postgres implementation is
// a full-text index (tsvector + GIN) that is maintained on message writes; the
// SQLite implementation falls back to substring matching.
//
// This is deliberately not Discord's Elasticsearch cell architecture: there is no
// external cluster, no guild-sharded Lucene index and no PubSub indexing queue.
// The boundary exists so an Elasticsearch adapter can replace the Postgres one
// without touching handlers or permission filtering.
package search

import (
	"context"
	"database/sql"
	"strings"

	"github.com/wavedevgit/waveycord/internal/sqlx"
)

// Doc is one indexed message.
type Doc struct {
	MessageID string
	GuildID   string
	ChannelID string
	AuthorID  string
	Content   string
	CreatedAt int64
}

// Query is a permission-filtered search request.
type Query struct {
	GuildID  string
	Content  string
	AuthorID string
	Channels []string
	Limit    int
}

// Result carries ranked message ids and the unpaged match count.
type Result struct {
	MessageIDs []string
	Total      int
}

// Index is the search service contract.
// DurableIndex acknowledges writes only after the remote index accepts them.
type DurableIndex interface {
	IndexDurable(context.Context, Doc) error
	DeleteDurable(context.Context, string) error
}

type Index interface {
	Index(ctx context.Context, doc Doc) error
	Delete(ctx context.Context, messageID string) error
	Search(ctx context.Context, q Query) (Result, error)
}

// Backfiller is implemented by indexes that can populate themselves from rows
// written before the index existed.
type Backfiller interface {
	Backfill(ctx context.Context, limit int) (int, error)
}

// GuildCounter reports how many documents one guild's index currently holds. It is
// optional; an index without it opts out of drift checks.
type GuildCounter interface {
	GuildDocCount(ctx context.Context, guildID string) (int, error)
}

// GuildResetter drops one guild's index so that guild alone can be rebuilt, the
// big-freaking-guild reindex path.
type GuildResetter interface {
	ResetGuild(ctx context.Context, guildID string) error
}

// BFGPolicy decides when a single guild's index should be rebuilt: either because it
// drifted from the authoritative row count, or because index-size automation says the
// guild has grown enough past the last rebuild to be worth compacting again.
type BFGPolicy struct {
	// Threshold is the minimum number of rows before drift-based rebuilds are
	// considered at all; below it the incremental path is always used.
	Threshold int
	// Drift is the allowed |indexed-rows|/rows ratio. Zero means 0.1.
	Drift float64
	// MaxDocs is the size at which a guild enters the automated rebuild cycle. Zero
	// disables size automation.
	MaxDocs int
	// Growth is the fraction a guild must grow past the size captured at its last
	// rebuild before it is rebuilt again. Zero means 0.25.
	Growth float64
}

// NeedsSizeRebuild reports whether a guild at rows documents should be rebuilt for
// size reasons, given the row count captured at its previous rebuild (0 means it has
// never been rebuilt).
func (p BFGPolicy) NeedsSizeRebuild(rows, lastRebuildRows int) bool {
	if p.MaxDocs <= 0 || rows < p.MaxDocs {
		return false
	}
	if lastRebuildRows <= 0 {
		return true
	}
	growth := p.Growth
	if growth <= 0 {
		growth = 0.25
	}
	return float64(rows) >= float64(lastRebuildRows)*(1+growth)
}

// NeedsRebuild applies the policy to one guild.
func (p BFGPolicy) NeedsRebuild(rows, indexed int) bool {
	if rows <= 0 {
		return false
	}
	if p.Threshold > 0 && rows < p.Threshold {
		return false
	}
	allowed := p.Drift
	if allowed <= 0 {
		allowed = 0.1
	}
	diff := indexed - rows
	if diff < 0 {
		diff = -diff
	}
	return float64(diff) > allowed*float64(rows)
}

// Resetter clears the index so a full reindex can rebuild it.
type Resetter interface {
	Reset(ctx context.Context) error
}

// Open returns the index appropriate for the database dialect. Postgres creates
// the tsvector table and GIN index if they are missing.
func Open(ctx context.Context, db *sqlx.DB) (Index, error) {
	if db.Dialect != sqlx.Postgres {
		return &sqliteIndex{db: db}, nil
	}
	ix := &pgIndex{db: db}
	if err := ix.ensureSchema(ctx); err != nil {
		return nil, err
	}
	return ix, nil
}

// --- Postgres ---

type pgIndex struct{ db *sqlx.DB }

func (p *pgIndex) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS search_documents (
			message_id TEXT PRIMARY KEY,
			guild_id   TEXT NOT NULL,
			channel_id TEXT NOT NULL,
			author_id  TEXT NOT NULL,
			content    TEXT NOT NULL,
			body       tsvector NOT NULL,
			created_at BIGINT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS search_documents_body ON search_documents USING GIN (body)`,
		`CREATE INDEX IF NOT EXISTS search_documents_guild ON search_documents (guild_id)`,
		`CREATE INDEX IF NOT EXISTS search_documents_channel ON search_documents (channel_id)`,
	}
	for _, stmt := range stmts {
		if _, err := p.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (p *pgIndex) Index(ctx context.Context, d Doc) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO search_documents
		(message_id, guild_id, channel_id, author_id, content, body, created_at)
		VALUES (?,?,?,?,?, to_tsvector('simple', ?), ?)
		ON CONFLICT(message_id) DO UPDATE SET
			content = excluded.content, body = excluded.body,
			channel_id = excluded.channel_id, author_id = excluded.author_id`,
		d.MessageID, d.GuildID, d.ChannelID, d.AuthorID, d.Content, d.Content, d.CreatedAt)
	return err
}

func (p *pgIndex) Delete(ctx context.Context, messageID string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM search_documents WHERE message_id = ?`, messageID)
	return err
}

func (p *pgIndex) Search(ctx context.Context, q Query) (Result, error) {
	limit := q.Limit
	if limit <= 0 || limit > 25 {
		limit = 25
	}
	placeholders := make([]string, len(q.Channels))
	args := []any{q.GuildID, q.Content}
	for i, c := range q.Channels {
		placeholders[i] = "?"
		args = append(args, c)
	}
	where := `guild_id = ? AND body @@ plainto_tsquery('simple', ?) AND channel_id IN (` +
		strings.Join(placeholders, ",") + `)`
	if q.AuthorID != "" {
		where += ` AND author_id = ?`
		args = append(args, q.AuthorID)
	}
	var res Result
	if err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM search_documents WHERE `+where, args...).
		Scan(&res.Total); err != nil {
		return res, err
	}
	rows, err := p.db.QueryContext(ctx, `SELECT message_id FROM search_documents WHERE `+where+
		` ORDER BY ts_rank(body, plainto_tsquery('simple', ?)) DESC, message_id DESC LIMIT ?`,
		append(args, q.Content, limit)...)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return res, err
		}
		res.MessageIDs = append(res.MessageIDs, id)
	}
	if res.MessageIDs == nil {
		res.MessageIDs = []string{}
	}
	return res, rows.Err()
}

func (p *pgIndex) Backfill(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	res, err := p.db.ExecContext(ctx, `INSERT INTO search_documents
		(message_id, guild_id, channel_id, author_id, content, body, created_at)
		SELECT m.id, m.guild_id, m.channel_id, m.author_id, m.content,
			to_tsvector('simple', m.content), m.timestamp
		FROM messages m
		WHERE m.guild_id IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM search_documents s WHERE s.message_id = m.id)
		LIMIT ?
		ON CONFLICT (message_id) DO NOTHING`, limit)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// --- SQLite ---

type sqliteIndex struct{ db *sqlx.DB }

func (s *sqliteIndex) Index(context.Context, Doc) error { return nil } // rows are searched directly

func (s *sqliteIndex) Delete(context.Context, string) error { return nil }

func (s *sqliteIndex) Search(ctx context.Context, q Query) (Result, error) {
	limit := q.Limit
	if limit <= 0 || limit > 25 {
		limit = 25
	}
	placeholders := make([]string, len(q.Channels))
	args := []any{q.GuildID, "%" + q.Content + "%"}
	for i, c := range q.Channels {
		placeholders[i] = "?"
		args = append(args, c)
	}
	where := `guild_id = ? AND content LIKE ? AND channel_id IN (` + strings.Join(placeholders, ",") + `)`
	if q.AuthorID != "" {
		where += ` AND author_id = ?`
		args = append(args, q.AuthorID)
	}
	var res Result
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE `+where, args...).Scan(&res.Total); err != nil {
		if err == sql.ErrNoRows {
			return res, nil
		}
		return res, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM messages WHERE `+where+` ORDER BY id DESC LIMIT ?`,
		append(args, limit)...)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return res, err
		}
		res.MessageIDs = append(res.MessageIDs, id)
	}
	if res.MessageIDs == nil {
		res.MessageIDs = []string{}
	}
	return res, rows.Err()
}
