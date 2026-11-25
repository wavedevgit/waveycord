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
	_ "github.com/mattn/go-sqlite3"

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
	_, schemaErr := conn.ExecContext(ctx, postgresSchema)
	_, _ = conn.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext('waveycord_schema'))`)
	conn.Close()
	if schemaErr != nil {
		handle.Close()
		return nil, fmt.Errorf("db: apply postgres schema: %w", schemaErr)
	}
	return &sqlx.DB{DB: handle, Dialect: sqlx.Postgres}, nil
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

// NowMillis is the single clock used for row timestamps.
func NowMillis() int64 { return time.Now().UnixMilli() }
