// This file is the Postgres backend, used when planner runs on a server whose
// local filesystem is ephemeral or per-instance (so SQLite-on-disk won't do). It
// opens a connection pool, applies the schema, and reuses every query on the
// shared sqlStore (sql.go); only the placeholder style and DDL differ from
// SQLite.
package store

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// postgresSchema mirrors sqliteSchema: identical tables and indexes, but
// created_at is TIMESTAMPTZ (the store writes UTC times via now()). A fresh
// Postgres database already has every column, so there is no migrate step.
const postgresSchema = `
CREATE TABLE IF NOT EXISTS plans (
  id         TEXT PRIMARY KEY,
  title      TEXT NOT NULL,
  status     TEXT NOT NULL DEFAULT 'active',
  project    TEXT NOT NULL DEFAULT 'No Project',
  created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS versions (
  id         TEXT PRIMARY KEY,
  plan_id    TEXT NOT NULL REFERENCES plans(id),
  number     INTEGER NOT NULL,
  content    TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  UNIQUE(plan_id, number)
);

CREATE TABLE IF NOT EXISTS comments (
  id         TEXT PRIMARY KEY,
  version_id TEXT NOT NULL REFERENCES versions(id),
  line_start INTEGER NOT NULL,
  line_end   INTEGER NOT NULL,
  quote      TEXT NOT NULL DEFAULT '',
  body       TEXT NOT NULL,
  status     TEXT NOT NULL DEFAULT 'open',
  created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS replies (
  id         TEXT PRIMARY KEY,
  comment_id TEXT NOT NULL REFERENCES comments(id),
  author     TEXT NOT NULL,
  body       TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL
);

-- Content-addressed store of referenced-file bodies: one row per unique body,
-- keyed by the SHA-256 hex of its content, so identical files across versions or
-- plans collapse to a single row.
CREATE TABLE IF NOT EXISTS file_blobs (
  sha256  TEXT PRIMARY KEY,
  content TEXT NOT NULL
);

-- Per-version list of the files a version references: each entry points a
-- (version, path) at a blob. The blob FK ties the list to the content store.
CREATE TABLE IF NOT EXISTS version_files (
  version_id TEXT NOT NULL REFERENCES versions(id),
  path       TEXT NOT NULL,
  language   TEXT NOT NULL,
  sha256     TEXT NOT NULL REFERENCES file_blobs(sha256),
  PRIMARY KEY(version_id, path)
);

CREATE INDEX IF NOT EXISTS idx_versions_plan ON versions(plan_id);
CREATE INDEX IF NOT EXISTS idx_comments_version ON comments(version_id);
CREATE INDEX IF NOT EXISTS idx_replies_comment ON replies(comment_id);
CREATE INDEX IF NOT EXISTS idx_version_files_version ON version_files(version_id);
CREATE INDEX IF NOT EXISTS idx_version_files_sha ON version_files(sha256);
`

// OpenPostgres connects to the Postgres database named by dsn (a libpq
// connection string or URL, e.g. postgres://user:pw@host:5432/db?sslmode=...),
// verifies the connection, and applies the schema.
func OpenPostgres(dsn string) (Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if _, err := db.Exec(postgresSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &sqlStore{db: db, rebind: pgRebind}, nil
}

// pgRebind rewrites the `?` placeholders the shared queries are written with to
// Postgres's positional `$1, $2, …` form. Our queries contain no literal `?`
// (no JSON operators or quoted question marks), so a straight left-to-right
// substitution is correct.
func pgRebind(q string) string {
	var b strings.Builder
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(q[i])
	}
	return b.String()
}
