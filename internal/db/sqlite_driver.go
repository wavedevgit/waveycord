//go:build !nosqlite

// Package db registers the cgo SQLite driver for the default single-process
// build. Postgres-only builds pass -tags nosqlite and stay static.
package db

import _ "github.com/mattn/go-sqlite3"
