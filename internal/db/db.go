// Package db opens the SQL database and applies the schema. SQLite is the
// zero-dependency default; PostgreSQL is selected when the DSN carries a
// postgres:// or postgresql:// URL.
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/wavedevgit/waveycord/internal/sqlx"
)

//go:embed schema.sql
var sqliteSchema string

//go:embed schema_postgres.sql
var postgresSchema string

// Options tunes the connection pool.
type Options struct {
	// MaxOpenConns defaults to 1 for SQLite (single writer) and 16 for Postgres.
	MaxOpenConns int
}

// Open opens the database named by dsn and applies the matching schema.
func Open(dsn string) (*sqlx.DB, error) { return OpenWithOptions(dsn, Options{}) }

// OpenWithOptions is Open with pool settings.
func OpenWithOptions(dsn string, opts Options) (*sqlx.DB, error) {
	if isPostgres(dsn) {
		return openPostgres(dsn, opts)
	}
	return openSQLite(dsn, opts)
}

func isPostgres(dsn string) bool {
	lower := strings.ToLower(dsn)
	return strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://")
}

func openSQLite(path string, opts Options) (*sqlx.DB, error) {
	if !hasDriver("sqlite3") {
		return nil, fmt.Errorf("db: sqlite driver not built (rebuild without -tags nosqlite, or use a postgres DSN)")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("db: create directory: %w", err)
		}
	}
	dsn := fmt.Sprintf("file:%s?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on&_synchronous=NORMAL", path)
	handle, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	maxConns := opts.MaxOpenConns
	if maxConns <= 0 {
		// ponytail: one writer keeps SQLite from returning SQLITE_BUSY under load;
		// use Postgres when write concurrency matters.
		maxConns = 1
	}
	handle.SetMaxOpenConns(maxConns)
	handle.SetMaxIdleConns(maxConns)
	handle.SetConnMaxLifetime(0)
	if err := handle.Ping(); err != nil {
		handle.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	if _, err := handle.Exec(sqliteSchema); err != nil {
		handle.Close()
		return nil, fmt.Errorf("db: apply schema: %w", err)
	}
	// Existing deployments predate mfa_secret; SQLite has no ADD COLUMN IF NOT
	// EXISTS, so the duplicate-column error means the column already exists.
	_, _ = handle.Exec(`ALTER TABLE users ADD COLUMN mfa_secret TEXT`)
	_, _ = handle.Exec(`ALTER TABLE polls ADD COLUMN final_counts TEXT NOT NULL DEFAULT '[]'`)
	return &sqlx.DB{DB: handle, Dialect: sqlx.SQLite}, nil
}

func openPostgres(dsn string, opts Options) (*sqlx.DB, error) {
	handle, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open postgres: %w", err)
	}
	maxConns := opts.MaxOpenConns
	if maxConns <= 0 {
		maxConns = envInt("WAVEYCORD_PG_MAX_CONNS", 16)
	}
	handle.SetMaxOpenConns(maxConns)
	handle.SetMaxIdleConns(maxConns / 2)
	handle.SetConnMaxIdleTime(5 * time.Minute)
	if err := handle.Ping(); err != nil {
		handle.Close()
		return nil, fmt.Errorf("db: ping postgres: %w", err)
	}
	// Concurrent startups (several instances, or parallel tests) would otherwise
	// collide on CREATE TABLE ... IF NOT EXISTS and can deadlock in the catalog.
	ctx := context.Background()
	conn, err := handle.Conn(ctx)
	if err != nil {
		handle.Close()
		return nil, fmt.Errorf("db: acquire schema connection: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext('waveycord_schema'))`); err != nil {
		conn.Close()
		handle.Close()
		return nil, fmt.Errorf("db: schema lock: %w", err)
	}
	// A pre-partitioning deployment gets its table renamed first, then the
	// partitioned parent is created by the schema, filled, and the old table
	// dropped, all inside the advisory lock.
	migrating, legacyMin, legacyMax, prepErr := prepareMessages(ctx, conn)
	var schemaErr error
	if prepErr == nil {
		_, schemaErr = conn.ExecContext(ctx, postgresSchema)
	}
	if schemaErr == nil {
		// Existing deployments predate mfa_secret.
		_, _ = conn.ExecContext(ctx, `ALTER TABLE users ADD COLUMN IF NOT EXISTS mfa_secret TEXT`)
		_, _ = conn.ExecContext(ctx, `ALTER TABLE polls ADD COLUMN IF NOT EXISTS final_counts TEXT NOT NULL DEFAULT '[]'`)
		from := time.Now().AddDate(0, -1, 0)
		to := time.Now().AddDate(0, 2, 0)
		if migrating && legacyMin > 0 {
			legacyStart := time.UnixMilli(legacyMin)
			legacyEnd := time.UnixMilli(legacyMax)
			if legacyStart.Before(from) {
				from = legacyStart
			}
			if legacyEnd.After(to) {
				to = legacyEnd
			}
		}
		schemaErr = ensurePartitions(ctx, conn, from, to)
	}
	if schemaErr == nil && migrating {
		schemaErr = copyLegacyMessages(ctx, conn)
	}
	_, _ = conn.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext('waveycord_schema'))`)
	conn.Close()
	if prepErr != nil {
		handle.Close()
		return nil, preperrwrap(prepErr)
	}
	if schemaErr != nil {
		handle.Close()
		return nil, fmt.Errorf("db: apply postgres schema: %w", schemaErr)
	}
	return &sqlx.DB{DB: handle, Dialect: sqlx.Postgres}, nil
}

func hasDriver(name string) bool {
	for _, d := range sql.Drivers() {
		if d == name {
			return true
		}
	}
	return false
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

// OpenReplica opens a read-only connection pool against a replica. The schema is
// assumed to already exist on that host.
func OpenReplica(dsn string) (*sqlx.DB, error) {
	if !isPostgres(dsn) {
		return nil, fmt.Errorf("db: replica DSN must be a postgres URL")
	}
	handle, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open replica: %w", err)
	}
	maxConns := envInt("WAVEYCORD_PG_MAX_CONNS", 16)
	handle.SetMaxOpenConns(maxConns)
	handle.SetMaxIdleConns(maxConns / 2)
	if err := handle.Ping(); err != nil {
		handle.Close()
		return nil, fmt.Errorf("db: ping replica: %w", err)
	}
	return &sqlx.DB{DB: handle, Dialect: sqlx.Postgres}, nil
}

func preperrwrap(err error) error { return fmt.Errorf("db: prepare partitioned messages: %w", err) }

// NowMillis is the single clock used for row timestamps.
func NowMillis() int64 { return time.Now().UnixMilli() }
