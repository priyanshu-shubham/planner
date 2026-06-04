package store

import "errors"

// ErrNotFound is returned by any backend when a requested entity does not exist.
var ErrNotFound = errors.New("not found")

// Store is the storage backend planner runs on. The web server (and through it
// the CLI) depends only on this interface, so the same handlers run against the
// local SQLite file (OpenSQLite) or Firestore on Cloud Run (OpenFirestore).
//
// Conventions:
//   - Creations return the created entity; its generated id/number is consumed
//     by the CLI and tests.
//   - State-mutations whose result no client reads return just error.
//   - Nested collections are hydrated only by the single-item getters: GetPlan
//     fills Plan.Versions, ListComments fills Comment.Replies.
type Store interface {
	// Plans / versions.
	ListPlans() ([]PlanSummary, error)
	CreatePlan(title, content, project string, files []FileSnapshot) (Plan, Version, error)
	GetPlan(planID string) (Plan, error) // Plan.Versions filled, ascending
	SetPlanStatus(planID, status string) error
	AddVersion(planID, content string, files []FileSnapshot) (Version, error)
	GetVersion(planID string, number int) (Version, error)

	// Referenced-file snapshots (content-addressed). The store hashes each
	// snapshot's body server-side, stores one blob per unique body, and keeps a
	// per-version file list of (path, language, sha). GetVersionFileList returns
	// the metadata for a version (no content); GetBlob returns one file's content
	// by sha (ErrNotFound for an unknown sha).
	GetVersionFileList(versionID string) ([]FileRef, error)
	GetBlob(sha string) (string, error)

	// Comments / replies.
	ListComments(versionID string, openOnly bool) ([]Comment, error) // Replies attached
	AddComment(versionID string, lineStart, lineEnd int, quote, body string) (Comment, error)
	SetCommentStatus(commentID, status string) error
	CarryComment(commentID string) error
	DeleteComment(commentID string) error
	AddReply(commentID, author, body string) (Reply, error)

	// Plan deletion / lifecycle.
	DeletePlan(planID string) error
	Close() error
}
