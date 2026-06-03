package store

import "time"

// Plan is a logical planning thread that owns an ordered list of versions.
type Plan struct {
	ID        string
	Title     string
	Status    string // "active" | "completed"
	Project   string // absolute path the plan was created from; "No Project" if unknown
	CreatedAt time.Time
	Versions  []int // ascending version numbers; filled by GetPlan only
}

const (
	PlanActive    = "active"
	PlanCompleted = "completed"
	NoProject     = "No Project" // project value when the origin folder is unknown
)

// Version is an immutable snapshot of a plan's markdown content.
type Version struct {
	ID        string
	PlanID    string
	Number    int
	Content   string
	CreatedAt time.Time
}

// Comment is human feedback attached to a specific version. A line_start of 0
// means the comment applies to the whole file rather than a line range. Quote
// is the exact text the reviewer selected in the rendered markdown (empty for
// whole-file comments), giving the agent sub-line precision on top of the line
// range.
type Comment struct {
	ID        string
	VersionID string
	LineStart int
	LineEnd   int
	Quote     string
	Body      string
	Status    string // "open" | "resolved"
	CreatedAt time.Time
	Replies   []Reply // reply thread, oldest first; filled by ListComments only
}

// WholeFile reports whether the comment is unanchored (applies to the file).
func (c Comment) WholeFile() bool { return c.LineStart == 0 }

const (
	StatusOpen     = "open"
	StatusResolved = "resolved"
)

// Reply is a message in a comment's thread, authored by either the human
// reviewer or the AI agent.
type Reply struct {
	ID        string
	CommentID string
	Author    string // "human" | "agent"
	Body      string
	CreatedAt time.Time
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
