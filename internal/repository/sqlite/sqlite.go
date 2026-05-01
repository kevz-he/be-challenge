// Package sqlite is the SQLite implementation of repository.Repository.
package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// timeFormat is the canonical format we use to serialize timestamps in
// SQLite. It is RFC3339 with nanoseconds and always UTC, which lets
// lexicographic comparisons (>=, <=) work correctly.
const timeFormat = "2006-01-02T15:04:05.999999999Z"

// Repo is the SQLite implementation of repository.
type Repo struct {
	db *sql.DB
}

// Open opens the database with the given DSN and returns a Repo. A typical
// DSN is:
//
//	file:yuno.db?_pragma=journal_mode(WAL)
//
// Tests use ":memory:".
func Open(dsn string) (*Repo, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys = ON;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pragma foreign_keys: %w", err)
	}
	return &Repo{db: db}, nil
}

func (r *Repo) Close() error { return r.db.Close() }

// DB exposes the underlying handle for advanced usage (e.g. healthchecks).
func (r *Repo) DB() *sql.DB { return r.db }

// Migrate applies the embedded schema. It is idempotent (CREATE TABLE/INDEX IF NOT EXISTS).
func (r *Repo) Migrate(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

func formatTime(t time.Time) string { return t.UTC().Format(timeFormat) }

func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(timeFormat, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("parse time %q", s)
}
