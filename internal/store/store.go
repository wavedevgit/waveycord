// Package store holds every SQL statement in the server. Rows are converted to
// model objects here so the HTTP layer never touches SQL.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wavedevgit/waveycord/internal/db"
	"github.com/wavedevgit/waveycord/internal/id"
)

// Sentinel errors mapped onto documented JSON error codes by the API layer.
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
)

// Store is the aggregate root for persistence.
type Store struct {
	DB  *sql.DB
	Gen *id.Generator
}

// New wraps an open database handle.
func New(handle *sql.DB) *Store { return &Store{DB: handle, Gen: id.New()} }

// NewID returns the next snowflake.
func (s *Store) NewID() string { return s.Gen.MustNext() }

// iso renders a millisecond timestamp the way the API does.
func iso(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000000+00:00")
}

func isoPtr(v sql.NullInt64) *string {
	if !v.Valid {
		return nil
	}
	s := iso(v.Int64)
	return &s
}

func ns(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func stringOr(v sql.NullString, def string) string {
	if v.Valid {
		return v.String
	}
	return def
}

// now is separated so tests can reason about ordering.
func now() int64 { return db.NowMillis() }

// paging describes a before/after/around message query.
type paging struct {
	before, after, around string
	limit                 int
}

func (p paging) where() (string, []any) {
	switch {
	case p.around != "":
		return "id >= ? OR id <= ?", nil // handled separately
	case p.before != "" && p.after != "":
		return "id < ? AND id > ?", []any{p.before, p.after}
	case p.before != "":
		return "id < ?", []any{p.before}
	case p.after != "":
		return "id > ?", []any{p.after}
	}
	return "", nil
}

// clampLimit applies the documented 1..100 default of 50.
func clampLimit(n int) int {
	if n <= 0 {
		return 50
	}
	if n > 100 {
		return 100
	}
	return n
}

// parseList splits a JSON array column (ids) into strings.
func parseJSONStrings(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return []string{}
	}
	raw = strings.TrimPrefix(raw, "[")
	raw = strings.TrimSuffix(raw, "]")
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// jsonStrings renders ids for storage.
func jsonStrings(ids []string) string {
	if len(ids) == 0 {
		return "[]"
	}
	q := make([]string, len(ids))
	for i, v := range ids {
		q[i] = `"` + strings.ReplaceAll(v, `"`, ``) + `"`
	}
	return "[" + strings.Join(q, ",") + "]"
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func itoa(n int) string { return strconv.Itoa(n) }

// mapErr converts driver errors to sentinels.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	msg := err.Error()
	if strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "constraint failed") {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}
