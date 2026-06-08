// This file is the SQLite backend: the default for local runs. It opens the
// database, applies/migrates the schema, and overrides Close to checkpoint the
// WAL. All query logic lives on the shared sqlStore (sql.go). WAL mode makes
// concurrent access from two processes on one machine safe.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// sqliteStore is the SQLite-backed Store. It embeds the shared sqlStore for all
// query logic and only overrides Close to checkpoint the WAL on shutdown.
type sqliteStore struct {
	*sqlStore
}

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS plans (
  id         TEXT PRIMARY KEY,
  title      TEXT NOT NULL,
  status     TEXT NOT NULL DEFAULT 'active',
  project    TEXT NOT NULL DEFAULT 'No Project',
  created_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS versions (
  id         TEXT PRIMARY KEY,
  plan_id    TEXT NOT NULL REFERENCES plans(id),
  number     INTEGER NOT NULL,
  content    TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL,
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
  created_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS replies (
  id         TEXT PRIMARY KEY,
  comment_id TEXT NOT NULL REFERENCES comments(id),
  author     TEXT NOT NULL,
  body       TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL
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

// OpenSQLite opens (creating if needed) the SQLite database at path and applies
// the schema. WAL + a busy timeout allow the CLI and server to share the file.
func OpenSQLite(path string) (Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory %s: %w", dir, err)
		}
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &sqliteStore{&sqlStore{db: db, rebind: identityRebind}}, nil
}

// Close checkpoints the WAL into the main database file and releases the
// handle. Checkpointing on close keeps the on-disk .db self-contained after
// every short-lived CLI command, so committed data is never left stranded in a
// WAL that another process might fail to pick up.
func (s *sqliteStore) Close() error {
	// Best-effort: a concurrent reader (e.g. a running server) can prevent a
	// full truncate, but the data is already committed and visible regardless.
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return s.sqlStore.Close()
}

// migrate applies schema changes that "CREATE TABLE IF NOT EXISTS" cannot make
// to a pre-existing table. Each change is guarded by an existence check so a
// real error (rather than the expected "duplicate column") is never swallowed.
func migrate(db *sql.DB) error {
	has, err := columnExists(db, "comments", "quote")
	if err != nil {
		return fmt.Errorf("check for comments.quote: %w", err)
	}
	if !has {
		if _, err := db.Exec(`ALTER TABLE comments ADD COLUMN quote TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add comments.quote: %w", err)
		}
	}
	if err := addColumn(db, "plans", "status", `TEXT NOT NULL DEFAULT 'active'`); err != nil {
		return err
	}
	if err := addColumn(db, "plans", "project", `TEXT NOT NULL DEFAULT 'No Project'`); err != nil {
		return err
	}
	return nil
}

// addColumn adds a column to a pre-existing table if it is not already present.
// The decl is the column type plus any constraints/default.
func addColumn(db *sql.DB, table, column, decl string) error {
	has, err := columnExists(db, table, column)
	if err != nil {
		return fmt.Errorf("check for %s.%s: %w", table, column, err)
	}
	if has {
		return nil
	}
	if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl)); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}

// columnExists reports whether the given table has the named column, using the
// pragma_table_info table-valued function.
func columnExists(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`SELECT 1 FROM pragma_table_info(?) WHERE name = ?`, table, column)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := rows.Next()
	if err := rows.Err(); err != nil {
		return false, err
	}
	return found, nil
}
