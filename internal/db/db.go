// Package db opens the SQL database and applies the schema. SQLite in WAL mode
// is the default; any database/sql driver with placeholders rewritten by
// dialect could be substituted, which is why every query in internal/store uses
// the `?` form.
package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schema string

// Open opens (creating if needed) the database at path and applies the schema.
func Open(path string) (*sql.DB, error) {
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
	// ponytail: one writer keeps SQLite from returning SQLITE_BUSY under load;
	// move to a pool-per-core plus retry loop if throughput matters.
	handle.SetMaxOpenConns(1)
	handle.SetMaxIdleConns(1)
	handle.SetConnMaxLifetime(0)
	if err := handle.Ping(); err != nil {
		handle.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	if _, err := handle.Exec(schema); err != nil {
		handle.Close()
		return nil, fmt.Errorf("db: apply schema: %w", err)
	}
	return handle, nil
}

// NowMillis is the single clock used for row timestamps.
func NowMillis() int64 { return time.Now().UnixMilli() }
