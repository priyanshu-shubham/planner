// Package store is the single source of truth for planner data. The web server
// depends on the Store interface (see interface.go); this file holds the
// dialect-agnostic CRUD shared by every database/sql backend. SQLite
// (sqlite.go) and Postgres (postgres.go) differ only in how a connection is
// opened, the schema DDL, and (for SQLite) close-time WAL checkpointing — they
// reuse every query below.
package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"planner/internal/id"
)

// sqlStore is the database/sql-backed Store implementation shared by the SQLite
// and Postgres backends. rebind adapts a query written with `?` placeholders to
// the driver's placeholder style: identity for SQLite, `?`→`$1,$2,…` for
// Postgres. Queries here contain no literal `?`, so the rewrite is safe.
//
// owner scopes data access to one user (see auth.go: WithOwner). When "" the
// store is unscoped — every query runs unfiltered, which is the single-user,
// no-auth behavior. When set, plan-touching queries gain an owner predicate so a
// user sees and mutates only their own plans (and pre-auth NULL-owner plans stay
// invisible). grantPlan is the alternative scope (WithPlanGrant): access pinned
// to exactly one plan regardless of owner, which is what a share link grants.
// At most one of the two is set. Scope misses surface as ErrNotFound, never as
// an existence oracle.
type sqlStore struct {
	db        *sql.DB
	rebind    func(string) string
	owner     string
	grantPlan string
}

// scoped reports whether any access scope (owner or plan grant) is active.
func (s *sqlStore) scoped() bool { return s.owner != "" || s.grantPlan != "" }

// ownerArg is the value to store for owner_id on insert: the owner id, or SQL
// NULL when the store is unscoped (so no-auth plans keep owner_id IS NULL).
func (s *sqlStore) ownerArg() any {
	if s.owner == "" {
		return nil
	}
	return s.owner
}

// planPred returns a predicate fragment and its args restricting a plans query
// to the store's scope. idCol and ownerCol name the plan-id and owner columns as
// referenced in the query (e.g. "id","owner_id" or "p.id","p.owner_id"). An
// owner scope filters by owner; a plan grant pins the query to exactly the
// granted plan; unscoped stores get ("", nil), leaving the query unchanged.
func (s *sqlStore) planPred(idCol, ownerCol string) (string, []any) {
	switch {
	case s.grantPlan != "":
		return " AND " + idCol + "=?", []any{s.grantPlan}
	case s.owner != "":
		return " AND " + ownerCol + "=?", []any{s.owner}
	}
	return "", nil
}

// accessibleVersionPred returns a predicate fragment (and args) asserting that
// the row belongs, through vcol (a versions-id column reference like
// "comments.version_id"), to a version of a plan within the store's scope —
// owned, or the granted share plan. Unscoped stores get ("", nil).
func (s *sqlStore) accessibleVersionPred(vcol string) (string, []any) {
	pred, args := s.planPred("p.id", "p.owner_id")
	if pred == "" {
		return "", nil
	}
	return " AND EXISTS(SELECT 1 FROM versions v JOIN plans p ON p.id=v.plan_id WHERE v.id=" + vcol + pred + ")", args
}

// identityRebind leaves a query untouched (SQLite uses `?` natively).
func identityRebind(q string) string { return q }

// Close releases the database handle. SQLite overrides this (sqliteStore) to
// checkpoint the WAL first.
func (s *sqlStore) Close() error { return s.db.Close() }

func now() time.Time { return time.Now().UTC() }

// CreatePlan inserts a new plan and its first version, returning both. Any
// referenced-file snapshots are stored content-addressed alongside the version.
func (s *sqlStore) CreatePlan(title, content, project string, files []FileSnapshot) (Plan, Version, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Plan{}, Version{}, err
	}
	defer tx.Rollback()

	p := Plan{ID: id.New("pl"), Title: title, Status: PlanActive, Project: project, OwnerID: s.owner, CreatedAt: now()}
	if _, err := tx.Exec(s.rebind(`INSERT INTO plans(id,title,status,project,owner_id,created_at) VALUES(?,?,?,?,?,?)`),
		p.ID, p.Title, p.Status, p.Project, s.ownerArg(), p.CreatedAt); err != nil {
		return Plan{}, Version{}, err
	}
	v := Version{ID: id.New("v"), PlanID: p.ID, Number: 1, Content: content, CreatedAt: now()}
	if _, err := tx.Exec(s.rebind(`INSERT INTO versions(id,plan_id,number,content,created_at) VALUES(?,?,?,?,?)`),
		v.ID, v.PlanID, v.Number, v.Content, v.CreatedAt); err != nil {
		return Plan{}, Version{}, err
	}
	if err := s.insertFiles(tx, v.ID, files); err != nil {
		return Plan{}, Version{}, err
	}
	if err := tx.Commit(); err != nil {
		return Plan{}, Version{}, err
	}
	return p, v, nil
}

// insertFiles writes each snapshot's body as a content-addressed blob (insert if
// absent) plus a per-version file-list entry, inside the caller's transaction.
func (s *sqlStore) insertFiles(tx *sql.Tx, versionID string, files []FileSnapshot) error {
	for _, f := range files {
		sha := fileSHA(f.Content)
		if _, err := tx.Exec(s.rebind(`INSERT INTO file_blobs(sha256,content) VALUES(?,?) ON CONFLICT(sha256) DO NOTHING`), sha, f.Content); err != nil {
			return err
		}
		if _, err := tx.Exec(s.rebind(`INSERT INTO version_files(version_id,path,language,sha256) VALUES(?,?,?,?)`),
			versionID, f.Path, f.Language, sha); err != nil {
			return err
		}
	}
	return nil
}

// AddVersion appends a new immutable version to an existing plan, storing any
// referenced-file snapshots content-addressed alongside it.
func (s *sqlStore) AddVersion(planID, content string, files []FileSnapshot) (Version, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Version{}, err
	}
	defer tx.Rollback()

	pred, args := s.planPred("id", "owner_id")
	var exists int
	if err := tx.QueryRow(s.rebind(`SELECT COUNT(*) FROM plans WHERE id=?`+pred), append([]any{planID}, args...)...).Scan(&exists); err != nil {
		return Version{}, err
	}
	if exists == 0 {
		return Version{}, ErrNotFound
	}

	var next int
	if err := tx.QueryRow(s.rebind(`SELECT COALESCE(MAX(number),0)+1 FROM versions WHERE plan_id=?`), planID).Scan(&next); err != nil {
		return Version{}, err
	}
	v := Version{ID: id.New("v"), PlanID: planID, Number: next, Content: content, CreatedAt: now()}
	if _, err := tx.Exec(s.rebind(`INSERT INTO versions(id,plan_id,number,content,created_at) VALUES(?,?,?,?,?)`),
		v.ID, v.PlanID, v.Number, v.Content, v.CreatedAt); err != nil {
		return Version{}, err
	}
	if err := s.insertFiles(tx, v.ID, files); err != nil {
		return Version{}, err
	}
	if err := tx.Commit(); err != nil {
		return Version{}, err
	}
	return v, nil
}

// GetVersionFileList returns a version's referenced-file metadata (no content),
// ordered by path.
func (s *sqlStore) GetVersionFileList(versionID string) ([]FileRef, error) {
	pred, args := s.accessibleVersionPred("version_files.version_id")
	rows, err := s.db.Query(s.rebind(`SELECT path,language,sha256 FROM version_files WHERE version_id=?`+pred+` ORDER BY path`),
		append([]any{versionID}, args...)...)
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
func (s *sqlStore) GetBlob(sha string) (string, error) {
	var content string
	err := s.db.QueryRow(s.rebind(`SELECT content FROM file_blobs WHERE sha256=?`), sha).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return content, err
}

// GetPlan returns a plan by id with its version numbers (ascending) filled in.
func (s *sqlStore) GetPlan(planID string) (Plan, error) {
	pred, args := s.planPred("id", "owner_id")
	var p Plan
	var owner, share sql.NullString
	err := s.db.QueryRow(s.rebind(`SELECT id,title,status,project,owner_id,share_id,created_at FROM plans WHERE id=?`+pred),
		append([]any{planID}, args...)...).
		Scan(&p.ID, &p.Title, &p.Status, &p.Project, &owner, &share, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, ErrNotFound
	}
	if err != nil {
		return Plan{}, err
	}
	p.OwnerID = owner.String
	p.ShareID = share.String
	if p.Versions, err = s.versionNumbers(planID); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// versionNumbers returns a plan's version numbers in ascending order.
func (s *sqlStore) versionNumbers(planID string) ([]int, error) {
	rows, err := s.db.Query(s.rebind(`SELECT number FROM versions WHERE plan_id=? ORDER BY number ASC`), planID)
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
func (s *sqlStore) GetVersion(planID string, number int) (Version, error) {
	pred, args := s.planPred("p.id", "p.owner_id")
	if pred != "" {
		pred = ` AND EXISTS(SELECT 1 FROM plans p WHERE p.id=versions.plan_id` + pred + `)`
	}
	return scanVersion(s.db.QueryRow(
		s.rebind(`SELECT id,plan_id,number,content,created_at FROM versions WHERE plan_id=? AND number=?`+pred),
		append([]any{planID, number}, args...)...))
}

// ListPlans returns plan summaries ordered by most recently created first.
func (s *sqlStore) ListPlans() ([]PlanSummary, error) {
	pred, args := s.planPred("p.id", "p.owner_id")
	where := ""
	if pred != "" {
		where = " WHERE" + strings.TrimPrefix(pred, " AND")
	}
	rows, err := s.db.Query(s.rebind(`
		SELECT p.id, p.title, p.status, p.project, p.created_at,
		       COALESCE((SELECT MAX(number) FROM versions v WHERE v.plan_id=p.id), 0),
		       COALESCE((SELECT COUNT(*) FROM comments c
		                 JOIN versions v ON v.id=c.version_id
		                 WHERE v.plan_id=p.id AND c.status='open'), 0)
		FROM plans p`+where+`
		ORDER BY p.created_at DESC`), args...)
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
// authorID is the commenting user ("" stores NULL, the no-auth case).
func (s *sqlStore) AddComment(planID, versionID string, lineStart, lineEnd int, quote, body, authorID string) (Comment, error) {
	// The version must belong to the named plan — the composite id minted below
	// encodes plan membership, so a mismatch would brand the comment with a lying
	// key — and the plan must be within scope (owned, or the granted share plan).
	pred, args := s.planPred("p.id", "p.owner_id")
	var ok bool
	if err := s.db.QueryRow(s.rebind(`SELECT EXISTS(SELECT 1 FROM versions v JOIN plans p ON p.id=v.plan_id WHERE v.id=? AND v.plan_id=?`+pred+`)`),
		append([]any{versionID, planID}, args...)...).Scan(&ok); err != nil {
		return Comment{}, err
	}
	if !ok {
		return Comment{}, ErrNotFound
	}
	c := Comment{
		ID:        planID + "_" + id.New("c"),
		VersionID: versionID,
		LineStart: lineStart,
		LineEnd:   lineEnd,
		Quote:     quote,
		Body:      body,
		Status:    StatusOpen,
		AuthorID:  authorID,
		CreatedAt: now(),
	}
	if _, err := s.db.Exec(s.rebind(`INSERT INTO comments(id,version_id,line_start,line_end,quote,body,status,author_id,created_at)
		VALUES(?,?,?,?,?,?,?,?,?)`), c.ID, c.VersionID, c.LineStart, c.LineEnd, c.Quote, c.Body, c.Status, nullable(c.AuthorID), c.CreatedAt); err != nil {
		return Comment{}, err
	}
	c.AuthorName, c.AuthorPicture = s.authorDisplay(authorID)
	return c, nil
}

// authorDisplay resolves a user's display fields for a freshly written
// comment/reply, so create responses match what list reads hydrate via JOIN.
// Best-effort: an unknown id (or "") just yields empty fields.
func (s *sqlStore) authorDisplay(authorID string) (name, picture string) {
	if authorID == "" {
		return "", ""
	}
	_ = s.db.QueryRow(s.rebind(`SELECT name,picture FROM users WHERE id=?`), authorID).Scan(&name, &picture)
	return name, picture
}

// nullable maps "" to SQL NULL, for optional text columns like author_id.
func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// DeleteComment permanently removes a comment and its replies. A non-empty
// authorID restricts the delete to comments that user authored (a shared
// viewer may remove only their own); "" deletes regardless of author.
func (s *sqlStore) DeleteComment(commentID, authorID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(s.rebind(`DELETE FROM replies WHERE comment_id=?`), commentID); err != nil {
		return err
	}
	pred, args := s.accessibleVersionPred("comments.version_id")
	if authorID != "" {
		pred += ` AND author_id=?`
		args = append(args, authorID)
	}
	res, err := tx.Exec(s.rebind(`DELETE FROM comments WHERE id=?`+pred), append([]any{commentID}, args...)...)
	if err != nil {
		return err
	}
	// A scope or author miss deletes 0 rows here; the deferred rollback undoes
	// the replies delete above, so the thread is never orphaned.
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// DeleteReply permanently removes one reply. A non-empty authorID restricts
// the delete to replies that user authored; "" deletes regardless of author.
func (s *sqlStore) DeleteReply(replyID, authorID string) error {
	vpred, args := s.accessibleVersionPred("comments.version_id")
	pred := ` AND EXISTS(SELECT 1 FROM comments WHERE comments.id=replies.comment_id` + vpred + `)`
	if authorID != "" {
		pred += ` AND author_id=?`
		args = append(args, authorID)
	}
	res, err := s.db.Exec(s.rebind(`DELETE FROM replies WHERE id=?`+pred), append([]any{replyID}, args...)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPlanStatus updates a plan's lifecycle status (active|completed).
func (s *sqlStore) SetPlanStatus(planID, status string) error {
	pred, args := s.planPred("id", "owner_id")
	res, err := s.db.Exec(s.rebind(`UPDATE plans SET status=? WHERE id=?`+pred), append([]any{status, planID}, args...)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPlanProject re-assigns the project a plan is grouped under.
func (s *sqlStore) SetPlanProject(planID, project string) error {
	pred, args := s.planPred("id", "owner_id")
	res, err := s.db.Exec(s.rebind(`UPDATE plans SET project=? WHERE id=?`+pred), append([]any{project, planID}, args...)...)
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
func (s *sqlStore) DeletePlan(planID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Scoped: verify access up front so the cascade below never touches another
	// user's rows (the cascade keys on plan_id alone). A miss is ErrNotFound.
	if s.scoped() {
		pred, args := s.planPred("id", "owner_id")
		var ok bool
		if err := tx.QueryRow(s.rebind(`SELECT EXISTS(SELECT 1 FROM plans WHERE id=?`+pred+`)`), append([]any{planID}, args...)...).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
	}
	if _, err := tx.Exec(s.rebind(`DELETE FROM replies WHERE comment_id IN
		(SELECT c.id FROM comments c JOIN versions v ON v.id=c.version_id WHERE v.plan_id=?)`), planID); err != nil {
		return err
	}
	if _, err := tx.Exec(s.rebind(`DELETE FROM comments WHERE version_id IN
		(SELECT id FROM versions WHERE plan_id=?)`), planID); err != nil {
		return err
	}
	// Drop this plan's version → file links, then sweep any blob those links
	// pointed at that no longer has a referrer. The guarded delete leaves blobs
	// still referenced by another plan's version untouched.
	candidateSHAs, err := s.planBlobSHAs(tx, planID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(s.rebind(`DELETE FROM version_files WHERE version_id IN
		(SELECT id FROM versions WHERE plan_id=?)`), planID); err != nil {
		return err
	}
	for _, sha := range candidateSHAs {
		if _, err := tx.Exec(s.rebind(`DELETE FROM file_blobs WHERE sha256=?
			AND NOT EXISTS (SELECT 1 FROM version_files WHERE sha256=?)`), sha, sha); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(s.rebind(`DELETE FROM versions WHERE plan_id=?`), planID); err != nil {
		return err
	}
	pred, args := s.planPred("id", "owner_id")
	res, err := tx.Exec(s.rebind(`DELETE FROM plans WHERE id=?`+pred), append([]any{planID}, args...)...)
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
func (s *sqlStore) planBlobSHAs(tx *sql.Tx, planID string) ([]string, error) {
	rows, err := tx.Query(s.rebind(`SELECT DISTINCT sha256 FROM version_files
		WHERE version_id IN (SELECT id FROM versions WHERE plan_id=?)`), planID)
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

// AddReply appends a reply to a comment's thread. authorID is the replying user
// ("" stores NULL, the no-auth case); the existence check doubles as the lookup
// of the plan id that prefixes the new reply's composite id.
func (s *sqlStore) AddReply(commentID, author, body, authorID string) (Reply, error) {
	pred, args := s.accessibleVersionPred("comments.version_id")
	var planID string
	err := s.db.QueryRow(s.rebind(`SELECT versions.plan_id FROM comments JOIN versions ON versions.id=comments.version_id WHERE comments.id=?`+pred),
		append([]any{commentID}, args...)...).Scan(&planID)
	if errors.Is(err, sql.ErrNoRows) {
		return Reply{}, ErrNotFound
	}
	if err != nil {
		return Reply{}, err
	}
	r := Reply{ID: planID + "_" + id.New("r"), CommentID: commentID, Author: author, AuthorID: authorID, Body: body, CreatedAt: now()}
	if _, err := s.db.Exec(s.rebind(`INSERT INTO replies(id,comment_id,author,body,author_id,created_at) VALUES(?,?,?,?,?,?)`),
		r.ID, r.CommentID, r.Author, r.Body, nullable(r.AuthorID), r.CreatedAt); err != nil {
		return Reply{}, err
	}
	r.AuthorName, r.AuthorPicture = s.authorDisplay(authorID)
	return r, nil
}

// repliesForVersion returns every reply on every comment of a version, oldest
// first, so ListComments can group them by comment id. Author display fields
// are hydrated from users (LEFT JOIN: attribution is optional).
func (s *sqlStore) repliesForVersion(versionID string) ([]Reply, error) {
	rows, err := s.db.Query(s.rebind(`SELECT r.id, r.comment_id, r.author, r.body, r.author_id, u.name, u.picture, r.created_at
		FROM replies r JOIN comments c ON c.id = r.comment_id
		LEFT JOIN users u ON u.id = r.author_id
		WHERE c.version_id = ?
		ORDER BY r.created_at ASC`), versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reply
	for rows.Next() {
		var r Reply
		var aid, aname, apic sql.NullString
		if err := rows.Scan(&r.ID, &r.CommentID, &r.Author, &r.Body, &aid, &aname, &apic, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.AuthorID, r.AuthorName, r.AuthorPicture = aid.String, aname.String, apic.String
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListComments returns a version's comments, each with its reply thread
// attached. If openOnly, only open ones. Whole-file comments come first, then by
// line, then by creation time.
func (s *sqlStore) ListComments(versionID string, openOnly bool) ([]Comment, error) {
	pred, args := s.accessibleVersionPred("comments.version_id")
	q := `SELECT comments.id,comments.version_id,comments.line_start,comments.line_end,comments.quote,comments.body,comments.status,
	             comments.author_id,u.name,u.picture,comments.created_at
	      FROM comments LEFT JOIN users u ON u.id=comments.author_id
	      WHERE comments.version_id=?` + pred
	if openOnly {
		q += ` AND comments.status='open'`
	}
	q += ` ORDER BY comments.line_start ASC, comments.created_at ASC`
	rows, err := s.db.Query(s.rebind(q), append([]any{versionID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Comment
	for rows.Next() {
		var c Comment
		var aid, aname, apic sql.NullString
		if err := rows.Scan(&c.ID, &c.VersionID, &c.LineStart, &c.LineEnd, &c.Quote, &c.Body, &c.Status, &aid, &aname, &apic, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.AuthorID, c.AuthorName, c.AuthorPicture = aid.String, aname.String, apic.String
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
func (s *sqlStore) SetCommentStatus(commentID, status string) error {
	pred, args := s.accessibleVersionPred("comments.version_id")
	res, err := s.db.Exec(s.rebind(`UPDATE comments SET status=? WHERE id=?`+pred), append([]any{status, commentID}, args...)...)
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
func (s *sqlStore) CarryComment(commentID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	pred, pargs := s.accessibleVersionPred("comments.version_id")
	var orig Comment
	var origAuthor sql.NullString
	err = tx.QueryRow(s.rebind(`SELECT id,version_id,line_start,line_end,quote,body,status,author_id,created_at
		FROM comments WHERE id=?`+pred), append([]any{commentID}, pargs...)...).
		Scan(&orig.ID, &orig.VersionID, &orig.LineStart, &orig.LineEnd, &orig.Quote, &orig.Body, &orig.Status, &origAuthor, &orig.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	orig.AuthorID = origAuthor.String

	var planID string
	if err := tx.QueryRow(s.rebind(`SELECT plan_id FROM versions WHERE id=?`), orig.VersionID).Scan(&planID); err != nil {
		return err
	}
	var latestID string
	if err := tx.QueryRow(s.rebind(`SELECT id FROM versions WHERE plan_id=? ORDER BY number DESC LIMIT 1`), planID).Scan(&latestID); err != nil {
		return err
	}

	copyc := Comment{
		ID:        planID + "_" + id.New("c"),
		VersionID: latestID,
		LineStart: 0,
		LineEnd:   0,
		Quote:     "", // carried comments become whole-file, so the old quote no longer applies
		Body:      orig.Body,
		Status:    StatusOpen,
		AuthorID:  orig.AuthorID, // the copy keeps the original commenter's attribution
		CreatedAt: now(),
	}
	if _, err := tx.Exec(s.rebind(`INSERT INTO comments(id,version_id,line_start,line_end,quote,body,status,author_id,created_at)
		VALUES(?,?,?,?,?,?,?,?,?)`), copyc.ID, copyc.VersionID, copyc.LineStart, copyc.LineEnd, copyc.Quote, copyc.Body, copyc.Status, nullable(copyc.AuthorID), copyc.CreatedAt); err != nil {
		return err
	}

	// Carry the discussion thread forward too, so the prior back-and-forth
	// (including the agent's replies) stays with the comment on the new version.
	// The original keeps its replies as well, preserving the old version's history.
	type carriedReply struct {
		author, body string
		authorID     sql.NullString
		created      time.Time
	}
	var reps []carriedReply
	rrows, err := tx.Query(s.rebind(`SELECT author, body, author_id, created_at FROM replies WHERE comment_id=? ORDER BY created_at ASC`), orig.ID)
	if err != nil {
		return err
	}
	for rrows.Next() {
		var cr carriedReply
		if err := rrows.Scan(&cr.author, &cr.body, &cr.authorID, &cr.created); err != nil {
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
		if _, err := tx.Exec(s.rebind(`INSERT INTO replies(id,comment_id,author,body,author_id,created_at) VALUES(?,?,?,?,?,?)`),
			planID+"_"+id.New("r"), copyc.ID, cr.author, cr.body, nullable(cr.authorID.String), cr.created); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(s.rebind(`UPDATE comments SET status='resolved' WHERE id=?`), orig.ID); err != nil {
		return err
	}
	return tx.Commit()
}
