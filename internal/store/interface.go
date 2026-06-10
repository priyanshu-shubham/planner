package store

import (
	"errors"
	"time"
)

// ErrNotFound is returned by any backend when a requested entity does not exist.
var ErrNotFound = errors.New("not found")

// Store is the storage backend planner runs on. The web server (and through it
// the CLI) depends only on this interface, so the same handlers run against the
// local SQLite file (OpenSQLite) or a Postgres server (OpenPostgres).
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
	SetPlanProject(planID, project string) error
	AddVersion(planID, content string, files []FileSnapshot) (Version, error)
	GetVersion(planID string, number int) (Version, error)

	// Referenced-file snapshots (content-addressed). The store hashes each
	// snapshot's body server-side, stores one blob per unique body, and keeps a
	// per-version file list of (path, language, sha). GetVersionFileList returns
	// the metadata for a version (no content); GetBlob returns one file's content
	// by sha (ErrNotFound for an unknown sha).
	GetVersionFileList(versionID string) ([]FileRef, error)
	GetBlob(sha string) (string, error)

	// Comments / replies. Comment and reply ids are composite —
	// "<plan_id>_c_<local>" / "<plan_id>_r_<local>" — so handlers pass full ids
	// reassembled from the path's plan id + short id, and a short id presented
	// under the wrong plan forms a key that simply does not exist. authorID is
	// the acting user ("" in no-auth mode); it travels as a parameter rather
	// than on the store because under a share grant the actor is not the owner.
	// For the two deletes, authorID constrains the delete to rows the given
	// user authored (shared-role self-delete); "" deletes regardless of author
	// (the owner's moderation power, and the no-auth mode).
	ListComments(versionID string, openOnly bool) ([]Comment, error) // Replies attached
	AddComment(planID, versionID string, lineStart, lineEnd int, quote, body, authorID string) (Comment, error)
	SetCommentStatus(commentID, status string) error
	CarryComment(commentID string) error
	DeleteComment(commentID, authorID string) error
	AddReply(commentID, author, body, authorID string) (Reply, error)
	DeleteReply(replyID, authorID string) error

	// Sharing. A plan's share id is a capability: anyone authenticated who
	// presents it gets view+comment access via WithPlanGrant. EnsureShareID
	// creates one if absent and returns it (idempotent); ClearShareID revokes by
	// nulling it; ResolveShareID maps a share id to its plan id (ErrNotFound for
	// unknown/revoked) and is deliberately unscoped — the share id is the authz.
	EnsureShareID(planID string) (string, error)
	ClearShareID(planID string) error
	ResolveShareID(shareID string) (string, error)

	// Plan deletion / lifecycle.
	DeletePlan(planID string) error
	Close() error

	// Auth & scoping. WithOwner returns a copy of this store scoped to one user
	// (ownerID == "" is unscoped, the no-auth default); WithPlanGrant returns a
	// copy scoped to exactly one plan regardless of owner (share-link access).
	// The remaining methods back the optional Google-login / PAT auth layer.
	// See auth.go.
	WithOwner(ownerID string) Store
	WithPlanGrant(planID string) Store
	UpsertUserByGoogleSub(sub, email, name, picture string) (User, error)
	GetUser(userID string) (User, error)
	CreateRefreshToken(userID, tokenHash string, expiresAt time.Time) error
	RotateRefreshToken(oldHash, newHash string, expiresAt time.Time) (User, error) // ErrNotFound = reuse/unknown
	DeleteRefreshToken(tokenHash string) error
	CreatePAT(userID, name, tokenHash string) (PAT, error)
	ListPATs(userID string) ([]PAT, error)
	GetUserByPATHash(tokenHash string) (User, PAT, error)
	TouchPAT(patID string, when time.Time) error
	DeletePAT(userID, patID string) error
}
