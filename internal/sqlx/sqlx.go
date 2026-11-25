// Package sqlx is a thin dialect wrapper over database/sql. Queries are written
// with `?` placeholders once; the wrapper rewrites them to `$n` for Postgres and
// converts bool arguments to integers so one set of statements serves both
// engines.
package sqlx

import (
	"context"
	"database/sql"
	"strings"
)

// Dialect identifies the SQL engine behind the handle.
type Dialect int

const (
	SQLite Dialect = iota
	Postgres
)

func (d Dialect) String() string {
	if d == Postgres {
		return "postgres"
	}
	return "sqlite"
}

// DB wraps *sql.DB with placeholder rewriting.
type DB struct {
	*sql.DB
	Dialect Dialect
}

// Tx wraps *sql.Tx with placeholder rewriting.
type Tx struct {
	*sql.Tx
	dialect Dialect
}

// Exec runs a statement.
func (d *DB) Exec(query string, args ...any) (sql.Result, error) {
	q, a := d.prepare(query, args)
	return d.DB.Exec(q, a...)
}

// ExecContext runs a statement with a context.
func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	q, a := d.prepare(query, args)
	return d.DB.ExecContext(ctx, q, a...)
}

// Query runs a query.
func (d *DB) Query(query string, args ...any) (*sql.Rows, error) {
	q, a := d.prepare(query, args)
	return d.DB.Query(q, a...)
}

// QueryRow runs a single-row query.
func (d *DB) QueryRow(query string, args ...any) *sql.Row {
	q, a := d.prepare(query, args)
	return d.DB.QueryRow(q, a...)
}

// Begin starts a transaction.
func (d *DB) Begin() (*Tx, error) {
	tx, err := d.DB.Begin()
	if err != nil {
		return nil, err
	}
	return &Tx{Tx: tx, dialect: d.Dialect}, nil
}

func (d *DB) prepare(query string, args []any) (string, []any) {
	return Rewrite(query, d.Dialect), convertArgs(args, d.Dialect)
}

// Exec runs a statement inside the transaction.
func (t *Tx) Exec(query string, args ...any) (sql.Result, error) {
	q, a := Rewrite(query, t.dialect), convertArgs(args, t.dialect)
	return t.Tx.Exec(q, a...)
}

// Query runs a query inside the transaction.
func (t *Tx) Query(query string, args ...any) (*sql.Rows, error) {
	q, a := Rewrite(query, t.dialect), convertArgs(args, t.dialect)
	return t.Tx.Query(q, a...)
}

// QueryRow runs a single-row query inside the transaction.
func (t *Tx) QueryRow(query string, args ...any) *sql.Row {
	q, a := Rewrite(query, t.dialect), convertArgs(args, t.dialect)
	return t.Tx.QueryRow(q, a...)
}

// Rewrite converts `?` placeholders to `$1..$n` for Postgres. Quoted literals
// and already-numbered placeholders are left alone.
func Rewrite(query string, d Dialect) string {
	if d != Postgres || !strings.ContainsRune(query, '?') {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	inSingle := false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		switch {
		case inSingle:
			b.WriteByte(ch)
			if ch == '\'' {
				if i+1 < len(query) && query[i+1] == '\'' {
					b.WriteByte('\'')
					i++
				} else {
					inSingle = false
				}
			}
		case ch == '\'':
			inSingle = true
			b.WriteByte(ch)
		case ch == '?':
			n++
			b.WriteByte('$')
			b.WriteString(itoa(n))
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}

// convertArgs maps Go bools to 0/1 for Postgres columns declared SMALLINT; the
// schema keeps boolean-ish flags as small integers so both engines agree.
func convertArgs(args []any, d Dialect) []any {
	if d != Postgres {
		return args
	}
	anyBool := false
	for _, a := range args {
		if _, ok := a.(bool); ok {
			anyBool = true
			break
		}
	}
	if !anyBool {
		return args
	}
	out := make([]any, len(args))
	for i, a := range args {
		if v, ok := a.(bool); ok {
			if v {
				out[i] = int64(1)
			} else {
				out[i] = int64(0)
			}
		} else {
			out[i] = a
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
