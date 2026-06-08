// Package store is the single source of truth for planner data. The web server
// depends on the Store interface (see interface.go); this file is the SQLite
// implementation used by default for local runs. WAL mode makes concurrent
// access from two processes on one machine safe.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"planner/internal/id"

	_ "modernc.org/sqlite"
)

// sqliteStore is the SQLite-backed Store implementation. It wraps the database
// handle.
type sqliteStore struct {
	db *sql.DB
}

const schema = `
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
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &sqliteStore{db: db}, nil
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

// Close checkpoints the WAL into the main database file and releases the
// handle. Checkpointing on close keeps the on-disk .db self-contained after
// every short-lived CLI command, so committed data is never left stranded in a
// WAL that another process might fail to pick up.
func (s *sqliteStore) Close() error {
	// Best-effort: a concurrent reader (e.g. a running server) can prevent a
	// full truncate, but the data is already committed and visible regardless.
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return s.db.Close()
}

func now() time.Time { return time.Now().UTC() }

// CreatePlan inserts a new plan and its first version, returning both. Any
// referenced-file snapshots are stored content-addressed alongside the version.
func (s *sqliteStore) CreatePlan(title, content, project string, files []FileSnapshot) (Plan, Version, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Plan{}, Version{}, err
	}
	defer tx.Rollback()

	p := Plan{ID: id.New("pl"), Title: title, Status: PlanActive, Project: project, CreatedAt: now()}
	if _, err := tx.Exec(`INSERT INTO plans(id,title,status,project,created_at) VALUES(?,?,?,?,?)`,
		p.ID, p.Title, p.Status, p.Project, p.CreatedAt); err != nil {
		return Plan{}, Version{}, err
	}
	v := Version{ID: id.New("v"), PlanID: p.ID, Number: 1, Content: content, CreatedAt: now()}
	if _, err := tx.Exec(`INSERT INTO versions(id,plan_id,number,content,created_at) VALUES(?,?,?,?,?)`,
		v.ID, v.PlanID, v.Number, v.Content, v.CreatedAt); err != nil {
		return Plan{}, Version{}, err
	}
	if err := insertFiles(tx, v.ID, files); err != nil {
		return Plan{}, Version{}, err
	}
	if err := tx.Commit(); err != nil {
		return Plan{}, Version{}, err
	}
	return p, v, nil
}

// insertFiles writes each snapshot's body as a content-addressed blob (insert if
// absent) plus a per-version file-list entry, inside the caller's transaction.
func insertFiles(tx *sql.Tx, versionID string, files []FileSnapshot) error {
	for _, f := range files {
		sha := fileSHA(f.Content)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO file_blobs(sha256,content) VALUES(?,?)`, sha, f.Content); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO version_files(version_id,path,language,sha256) VALUES(?,?,?,?)`,
			versionID, f.Path, f.Language, sha); err != nil {
			return err
		}
	}
	return nil
}

// AddVersion appends a new immutable version to an existing plan, storing any
// referenced-file snapshots content-addressed alongside it.
func (s *sqliteStore) AddVersion(planID, content string, files []FileSnapshot) (Version, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Version{}, err
	}
	defer tx.Rollback()

	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM plans WHERE id=?`, planID).Scan(&exists); err != nil {
		return Version{}, err
	}
	if exists == 0 {
		return Version{}, ErrNotFound
	}

	var next int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(number),0)+1 FROM versions WHERE plan_id=?`, planID).Scan(&next); err != nil {
		return Version{}, err
	}
	v := Version{ID: id.New("v"), PlanID: planID, Number: next, Content: content, CreatedAt: now()}
	if _, err := tx.Exec(`INSERT INTO versions(id,plan_id,number,content,created_at) VALUES(?,?,?,?,?)`,
		v.ID, v.PlanID, v.Number, v.Content, v.CreatedAt); err != nil {
		return Version{}, err
	}
	if err := insertFiles(tx, v.ID, files); err != nil {
		return Version{}, err
	}
	if err := tx.Commit(); err != nil {
		return Version{}, err
	}
	return v, nil
}

// GetVersionFileList returns a version's referenced-file metadata (no content),
// ordered by path.
func (s *sqliteStore) GetVersionFileList(versionID string) ([]FileRef, error) {
	rows, err := s.db.Query(`SELECT path,language,sha256 FROM version_files WHERE version_id=? ORDER BY path`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileRef
	for rows.Next() {
		var f FileRef
		if err := rows.Scan(&f.Path, &f.Language, &f.SHA); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetBlob returns a referenced file's content by its sha, or ErrNotFound.
func (s *sqliteStore) GetBlob(sha string) (string, error) {
	var content string
	err := s.db.QueryRow(`SELECT content FROM file_blobs WHERE sha256=?`, sha).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return content, err
}

// GetPlan returns a plan by id with its version numbers (ascending) filled in.
func (s *sqliteStore) GetPlan(planID string) (Plan, error) {
	var p Plan
	err := s.db.QueryRow(`SELECT id,title,status,project,created_at FROM plans WHERE id=?`, planID).
		Scan(&p.ID, &p.Title, &p.Status, &p.Project, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, ErrNotFound
	}
	if err != nil {
		return Plan{}, err
	}
	if p.Versions, err = s.versionNumbers(planID); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// versionNumbers returns a plan's version numbers in ascending order.
func (s *sqliteStore) versionNumbers(planID string) ([]int, error) {
	rows, err := s.db.Query(`SELECT number FROM versions WHERE plan_id=? ORDER BY number ASC`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nums []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		nums = append(nums, n)
	}
	return nums, rows.Err()
}

func scanVersion(row interface{ Scan(...any) error }) (Version, error) {
	var v Version
	err := row.Scan(&v.ID, &v.PlanID, &v.Number, &v.Content, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, ErrNotFound
	}
	return v, err
}

// GetVersion returns a specific version number of a plan.
func (s *sqliteStore) GetVersion(planID string, number int) (Version, error) {
	return scanVersion(s.db.QueryRow(
		`SELECT id,plan_id,number,content,created_at FROM versions WHERE plan_id=? AND number=?`,
		planID, number))
}

// ListPlans returns plan summaries ordered by most recently created first.
func (s *sqliteStore) ListPlans() ([]PlanSummary, error) {
	rows, err := s.db.Query(`
		SELECT p.id, p.title, p.status, p.project, p.created_at,
		       COALESCE((SELECT MAX(number) FROM versions v WHERE v.plan_id=p.id), 0),
		       COALESCE((SELECT COUNT(*) FROM comments c
		                 JOIN versions v ON v.id=c.version_id
		                 WHERE v.plan_id=p.id AND c.status='open'), 0)
		FROM plans p
		ORDER BY p.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanSummary
	for rows.Next() {
		var s PlanSummary
		if err := rows.Scan(&s.ID, &s.Title, &s.Status, &s.Project, &s.CreatedAt, &s.LatestVersion, &s.OpenComments); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// AddComment attaches a comment to a version. lineStart=0 means whole-file.
func (s *sqliteStore) AddComment(versionID string, lineStart, lineEnd int, quote, body string) (Comment, error) {
	c := Comment{
		ID:        id.New("c"),
		VersionID: versionID,
		LineStart: lineStart,
		LineEnd:   lineEnd,
		Quote:     quote,
		Body:      body,
		Status:    StatusOpen,
		CreatedAt: now(),
	}
	_, err := s.db.Exec(`INSERT INTO comments(id,version_id,line_start,line_end,quote,body,status,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, c.ID, c.VersionID, c.LineStart, c.LineEnd, c.Quote, c.Body, c.Status, c.CreatedAt)
	return c, err
}

// DeleteComment permanently removes a comment and its replies.
func (s *sqliteStore) DeleteComment(commentID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM replies WHERE comment_id=?`, commentID); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM comments WHERE id=?`, commentID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// SetPlanStatus updates a plan's lifecycle status (active|completed).
func (s *sqliteStore) SetPlanStatus(planID, status string) error {
	res, err := s.db.Exec(`UPDATE plans SET status=? WHERE id=?`, status, planID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPlanProject re-assigns the project a plan is grouped under.
func (s *sqliteStore) SetPlanProject(planID, project string) error {
	res, err := s.db.Exec(`UPDATE plans SET project=? WHERE id=?`, project, planID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePlan permanently removes a plan and everything under it: all versions,
// their comments, and those comments' replies.
func (s *sqliteStore) DeletePlan(planID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM replies WHERE comment_id IN
		(SELECT c.id FROM comments c JOIN versions v ON v.id=c.version_id WHERE v.plan_id=?)`, planID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM comments WHERE version_id IN
		(SELECT id FROM versions WHERE plan_id=?)`, planID); err != nil {
		return err
	}
	// Drop this plan's version → file links, then sweep any blob those links
	// pointed at that no longer has a referrer. The guarded delete leaves blobs
	// still referenced by another plan's version untouched.
	candidateSHAs, err := planBlobSHAs(tx, planID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM version_files WHERE version_id IN
		(SELECT id FROM versions WHERE plan_id=?)`, planID); err != nil {
		return err
	}
	for _, sha := range candidateSHAs {
		if _, err := tx.Exec(`DELETE FROM file_blobs WHERE sha256=?
			AND NOT EXISTS (SELECT 1 FROM version_files WHERE sha256=?)`, sha, sha); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM versions WHERE plan_id=?`, planID); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM plans WHERE id=?`, planID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// planBlobSHAs returns the distinct blob shas referenced by any of a plan's
// versions — the sweep candidates collected before the plan's file links are
// deleted.
func planBlobSHAs(tx *sql.Tx, planID string) ([]string, error) {
	rows, err := tx.Query(`SELECT DISTINCT sha256 FROM version_files
		WHERE version_id IN (SELECT id FROM versions WHERE plan_id=?)`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			return nil, err
		}
		out = append(out, sha)
	}
	return out, rows.Err()
}

// AddReply appends a reply to a comment's thread.
func (s *sqliteStore) AddReply(commentID, author, body string) (Reply, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM comments WHERE id=?`, commentID).Scan(&n); err != nil {
		return Reply{}, err
	}
	if n == 0 {
		return Reply{}, ErrNotFound
	}
	r := Reply{ID: id.New("r"), CommentID: commentID, Author: author, Body: body, CreatedAt: now()}
	_, err := s.db.Exec(`INSERT INTO replies(id,comment_id,author,body,created_at) VALUES(?,?,?,?,?)`,
		r.ID, r.CommentID, r.Author, r.Body, r.CreatedAt)
	return r, err
}

// repliesForVersion returns every reply on every comment of a version, oldest
// first, so ListComments can group them by comment id.
func (s *sqliteStore) repliesForVersion(versionID string) ([]Reply, error) {
	rows, err := s.db.Query(`SELECT r.id, r.comment_id, r.author, r.body, r.created_at
		FROM replies r JOIN comments c ON c.id = r.comment_id
		WHERE c.version_id = ?
		ORDER BY r.created_at ASC`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reply
	for rows.Next() {
		var r Reply
		if err := rows.Scan(&r.ID, &r.CommentID, &r.Author, &r.Body, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListComments returns a version's comments, each with its reply thread
// attached. If openOnly, only open ones. Whole-file comments come first, then by
// line, then by creation time.
func (s *sqliteStore) ListComments(versionID string, openOnly bool) ([]Comment, error) {
	q := `SELECT id,version_id,line_start,line_end,quote,body,status,created_at
	      FROM comments WHERE version_id=?`
	if openOnly {
		q += ` AND status='open'`
	}
	q += ` ORDER BY line_start ASC, created_at ASC`
	rows, err := s.db.Query(q, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Comment
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.VersionID, &c.LineStart, &c.LineEnd, &c.Quote, &c.Body, &c.Status, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Attach replies. We fetch all replies on the version once and group them by
	// comment id (the join in repliesForVersion ignores status, so reply threads
	// are returned even for comments filtered out by openOnly — those are simply
	// not in byComment, which is harmless).
	replies, err := s.repliesForVersion(versionID)
	if err != nil {
		return nil, err
	}
	byComment := map[string][]Reply{}
	for _, r := range replies {
		byComment[r.CommentID] = append(byComment[r.CommentID], r)
	}
	for i := range out {
		out[i].Replies = byComment[out[i].ID]
	}
	return out, nil
}

// SetCommentStatus updates a comment's status.
func (s *sqliteStore) SetCommentStatus(commentID, status string) error {
	res, err := s.db.Exec(`UPDATE comments SET status=? WHERE id=?`, status, commentID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CarryComment copies an existing comment onto the plan's latest version as a
// whole-file comment and marks the original resolved. Used when a human chooses
// to keep a prior version's comment after a new version is posted.
func (s *sqliteStore) CarryComment(commentID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var orig Comment
	err = tx.QueryRow(`SELECT id,version_id,line_start,line_end,quote,body,status,created_at
		FROM comments WHERE id=?`, commentID).
		Scan(&orig.ID, &orig.VersionID, &orig.LineStart, &orig.LineEnd, &orig.Quote, &orig.Body, &orig.Status, &orig.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	var planID string
	if err := tx.QueryRow(`SELECT plan_id FROM versions WHERE id=?`, orig.VersionID).Scan(&planID); err != nil {
		return err
	}
	var latestID string
	if err := tx.QueryRow(`SELECT id FROM versions WHERE plan_id=? ORDER BY number DESC LIMIT 1`, planID).Scan(&latestID); err != nil {
		return err
	}

	copyc := Comment{
		ID:        id.New("c"),
		VersionID: latestID,
		LineStart: 0,
		LineEnd:   0,
		Quote:     "", // carried comments become whole-file, so the old quote no longer applies
		Body:      orig.Body,
		Status:    StatusOpen,
		CreatedAt: now(),
	}
	if _, err := tx.Exec(`INSERT INTO comments(id,version_id,line_start,line_end,quote,body,status,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, copyc.ID, copyc.VersionID, copyc.LineStart, copyc.LineEnd, copyc.Quote, copyc.Body, copyc.Status, copyc.CreatedAt); err != nil {
		return err
	}

	// Carry the discussion thread forward too, so the prior back-and-forth
	// (including the agent's replies) stays with the comment on the new version.
	// The original keeps its replies as well, preserving the old version's history.
	type carriedReply struct {
		author, body string
		created      time.Time
	}
	var reps []carriedReply
	rrows, err := tx.Query(`SELECT author, body, created_at FROM replies WHERE comment_id=? ORDER BY created_at ASC`, orig.ID)
	if err != nil {
		return err
	}
	for rrows.Next() {
		var cr carriedReply
		if err := rrows.Scan(&cr.author, &cr.body, &cr.created); err != nil {
			rrows.Close()
			return err
		}
		reps = append(reps, cr)
	}
	rrows.Close() // must close before further statements on the same tx
	if err := rrows.Err(); err != nil {
		return err
	}
	for _, cr := range reps {
		if _, err := tx.Exec(`INSERT INTO replies(id,comment_id,author,body,created_at) VALUES(?,?,?,?,?)`,
			id.New("r"), copyc.ID, cr.author, cr.body, cr.created); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(`UPDATE comments SET status='resolved' WHERE id=?`, orig.ID); err != nil {
		return err
	}
	return tx.Commit()
}
