package store

import "time"

// Plan is a logical planning thread that owns an ordered list of versions.
type Plan struct {
	ID      string
	Title   string
	Status  string // "active" | "completed" | "stashed"
	Project string // absolute path the plan was created from; "No Project" if unknown
	OwnerID string // owning user's id; "" when the plan predates auth (owner_id IS NULL)
	ShareID string // capability id granting view+comment access; "" when not shared
	// ShareAllVersions controls what a share link exposes. When false, only
	// ShareVersions are visible through the share link; owners always see all
	// versions.
	ShareAllVersions bool
	ShareVersions    []int
	CreatedAt        time.Time
	Versions         []int // ascending version numbers; filled by GetPlan only
}

const (
	PlanActive    = "active"
	PlanCompleted = "completed"
	PlanStashed   = "stashed"
	NoProject     = "No Project" // project value when the origin folder is unknown
)

// ValidPlanStatus reports whether s is one of the plan lifecycle statuses. The
// status endpoint takes a client-supplied value, so it whitelists with this.
func ValidPlanStatus(s string) bool {
	return s == PlanActive || s == PlanCompleted || s == PlanStashed
}

// Version is an immutable snapshot of a plan's markdown content.
type Version struct {
	ID        string
	PlanID    string
	Number    int
	Content   string
	CreatedAt time.Time
}

// FileSnapshot is a whole referenced file captured by the CLI at post time and
// posted to the server (the wire + input type). The server splits it into a
// content-addressed blob plus a per-version FileRef; content-addressing details
// (SHA computation) are server-side internals and never appear on this type.
type FileSnapshot struct {
	Path     string `json:"path"`     // path as written in the plan, relative to the project root
	Language string `json:"language"` // hljs language id for syntax highlighting ("" if unknown)
	Content  string `json:"content"`  // the whole file body (captured only for files < 200 KB)
}

// FileRef is one entry in a version's file list: the metadata the frontend needs
// to decorate a reference token and fetch its content by SHA. It carries no
// content — content is fetched lazily via GetBlob.
type FileRef struct {
	Path     string
	Language string
	SHA      string // lowercase hex SHA-256 of the file body; the blob key
}

// Comment is human feedback attached to a specific version. A line_start of 0
// means the comment applies to the whole file rather than a line range. Quote
// is the exact text the reviewer selected in the rendered markdown (empty for
// whole-file comments), giving the agent sub-line precision on top of the line
// range.
type Comment struct {
	ID            string // composite: "<plan_id>_c_<local>", so the key encodes plan membership
	VersionID     string
	LineStart     int
	LineEnd       int
	Quote         string
	Body          string
	Status        string // "open" | "resolved"
	AuthorID      string // commenting user's id; "" in no-auth mode or for pre-attribution rows
	AuthorName    string // display fields hydrated from users by ListComments ("" when AuthorID is "")
	AuthorPicture string
	CreatedAt     time.Time
	Replies       []Reply // reply thread, oldest first; filled by ListComments only
}

// WholeFile reports whether the comment is unanchored (applies to the file).
func (c Comment) WholeFile() bool { return c.LineStart == 0 }

const (
	StatusOpen     = "open"
	StatusResolved = "resolved"
)

// Reply is a message in a comment's thread, authored by either a human
// reviewer or the AI agent.
type Reply struct {
	ID            string // composite: "<plan_id>_r_<local>", like Comment.ID
	CommentID     string
	Author        string // "human" | "agent"
	AuthorID      string // replying user's id; for agent replies, the PAT's user. "" in no-auth mode
	AuthorName    string // display fields hydrated from users ("" when AuthorID is "")
	AuthorPicture string
	Body          string
	CreatedAt     time.Time
}

const (
	AuthorHuman = "human"
	AuthorAgent = "agent"
)

// PlanSummary is a Plan plus aggregate counts for list views.
type PlanSummary struct {
	Plan
	LatestVersion int
	OpenComments  int
}

// User is a person who has signed in with Google. The store keys users by their
// Google subject (the stable per-account id), and every owned plan points back
// at a user id.
type User struct {
	ID        string
	GoogleSub string // Google's stable subject claim; the upsert key
	Email     string
	Name      string
	Picture   string // avatar URL from Google userinfo ("" if none)
	CreatedAt time.Time
}

// RefreshToken is a stored, rotating session credential. Only the SHA-256 hash
// of the opaque token is persisted; the token itself lives in the user's
// httpOnly cookie. Presenting a hash with no row is treated as reuse/theft.
type RefreshToken struct {
	TokenHash string
	UserID    string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// PAT is a personal access token: a long-lived bearer credential the CLI uses.
// As with refresh tokens, only the hash is stored. LastUsedAt is touched at most
// once a minute so an active token does not write on every request.
type PAT struct {
	ID         string
	UserID     string
	Name       string // human label naming the machine the token lives on
	TokenHash  string
	CreatedAt  time.Time
	LastUsedAt time.Time // zero value when never used
}
