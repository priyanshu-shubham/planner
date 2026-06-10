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
  owner_id   TEXT,
  share_id   TEXT,
  created_at TIMESTAMP NOT NULL
);

-- Auth tables. Present on fresh databases; pre-auth databases gain plans.owner_id
-- (and these tables) via migrate(). A user signs in with Google; refresh_tokens
-- and pats hold only SHA-256 hashes of the actual credentials.
CREATE TABLE IF NOT EXISTS users (
  id         TEXT PRIMARY KEY,
  google_sub TEXT NOT NULL UNIQUE,
  email      TEXT NOT NULL,
  name       TEXT NOT NULL,
  picture    TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS refresh_tokens (
  token_hash TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(id),
  expires_at TIMESTAMP NOT NULL,
  created_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS pats (
  id           TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES users(id),
  name         TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,
  created_at   TIMESTAMP NOT NULL,
  last_used_at TIMESTAMP
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
  author_id  TEXT,
  created_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS replies (
  id         TEXT PRIMARY KEY,
  comment_id TEXT NOT NULL REFERENCES comments(id),
  author     TEXT NOT NULL,
  body       TEXT NOT NULL,
  author_id  TEXT,
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
CREATE INDEX IF NOT EXISTS idx_refresh_user ON refresh_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_pats_user ON pats(user_id);
`

// idx_plans_owner is created in migrate(), not here: a pre-auth database does
// not yet have plans.owner_id when this schema runs, so the index on that column
// must wait until migrate() has added the column.

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
	if err := addColumn(db, "plans", "owner_id", `TEXT`); err != nil {
		return err
	}
	// Safe now that owner_id is guaranteed to exist (fresh DBs get it from the
	// CREATE TABLE; pre-auth DBs from the addColumn above).
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_plans_owner ON plans(owner_id)`); err != nil {
		return fmt.Errorf("create idx_plans_owner: %w", err)
	}
	if err := addColumn(db, "plans", "share_id", `TEXT`); err != nil {
		return err
	}
	if err := addColumn(db, "comments", "author_id", `TEXT`); err != nil {
		return err
	}
	if err := addColumn(db, "replies", "author_id", `TEXT`); err != nil {
		return err
	}
	// Unique: one plan per share link. SQLite treats NULLs as distinct in unique
	// indexes, so any number of unshared (NULL share_id) plans coexist.
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_plans_share ON plans(share_id)`); err != nil {
		return fmt.Errorf("create idx_plans_share: %w", err)
	}
	return migrateChildIDs(db)
}

// migrateChildIDs rewrites pre-composite comment/reply ids ("c_…", "r_…") to
// the plan-prefixed form ("pl_<plan>_c_…", "pl_<plan>_r_…") that AddComment and
// AddReply mint today. The positive match on the old prefix makes a second run
// a no-op. The rewrite necessarily passes through states where replies point at
// not-yet-renamed comments, and this connection runs with foreign_keys ON, so
// FK checks are deferred to the transaction's commit.
func migrateChildIDs(db *sql.DB) error {
	var pending int
	if err := db.QueryRow(`SELECT (SELECT COUNT(*) FROM comments WHERE id LIKE 'c\_%' ESCAPE '\')
		+ (SELECT COUNT(*) FROM replies WHERE id LIKE 'r\_%' ESCAPE '\')`).Scan(&pending); err != nil {
		return fmt.Errorf("check for pre-composite ids: %w", err)
	}
	if pending == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`PRAGMA defer_foreign_keys = ON`); err != nil {
		return fmt.Errorf("defer foreign keys: %w", err)
	}
	// Reply rows first, while replies.comment_id still joins to the old
	// comments.id; the comments rewrite last restores FK consistency at commit.
	for _, q := range []string{
		`UPDATE replies SET id = v.plan_id || '_' || replies.id
		   FROM comments c, versions v
		  WHERE c.id = replies.comment_id AND v.id = c.version_id AND replies.id LIKE 'r\_%' ESCAPE '\'`,
		`UPDATE replies SET comment_id = v.plan_id || '_' || replies.comment_id
		   FROM comments c, versions v
		  WHERE c.id = replies.comment_id AND v.id = c.version_id AND replies.comment_id LIKE 'c\_%' ESCAPE '\'`,
		`UPDATE comments SET id = v.plan_id || '_' || comments.id
		   FROM versions v
		  WHERE v.id = comments.version_id AND comments.id LIKE 'c\_%' ESCAPE '\'`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return fmt.Errorf("rewrite child ids: %w", err)
		}
	}
	return tx.Commit()
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
