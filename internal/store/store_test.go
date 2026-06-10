package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// backends returns the Store implementations the conformance suite runs against.
// SQLite always runs; Postgres runs only when PLANNER_TEST_POSTGRES_DSN is set
// (so CI without a database stays green). Because Postgres is a shared database
// rather than a fresh temp file, the factory truncates every table on open so
// each subtest starts from a clean slate. Run one locally with, e.g.:
//
//	docker run --rm -e POSTGRES_PASSWORD=pw -p 5432:5432 postgres:16
//	export PLANNER_TEST_POSTGRES_DSN='postgres://postgres:pw@localhost:5432/postgres?sslmode=disable'
func backends() map[string]func(t *testing.T) Store {
	m := map[string]func(t *testing.T) Store{
		"sqlite": func(t *testing.T) Store {
			s, err := OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
	}
	if dsn := os.Getenv("PLANNER_TEST_POSTGRES_DSN"); dsn != "" {
		m["postgres"] = func(t *testing.T) Store {
			s, err := OpenPostgres(dsn)
			if err != nil {
				t.Fatal(err)
			}
			ps, ok := s.(*sqlStore)
			if !ok {
				t.Fatalf("OpenPostgres returned %T, want *sqlStore", s)
			}
			if _, err := ps.db.Exec(
				`TRUNCATE plans, versions, comments, replies, file_blobs, version_files, users, refresh_tokens, pats CASCADE`); err != nil {
				s.Close()
				t.Fatalf("truncate postgres tables: %v", err)
			}
			return s
		}
	}
	return m
}

// findComment returns the comment with the given id from cs, or nil.
func findComment(cs []Comment, id string) *Comment {
	for i := range cs {
		if cs[i].ID == id {
			return &cs[i]
		}
	}
	return nil
}

func TestRoundTrip(t *testing.T) {
	for name, open := range backends() {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			defer s.Close()

			p, v1, err := s.CreatePlan("Demo", "line1\nline2\nline3", "/work/demo", nil)
			if err != nil {
				t.Fatal(err)
			}
			if v1.Number != 1 {
				t.Fatalf("want v1, got v%d", v1.Number)
			}

			// anchored comment on v1, with the selected text quoted
			c, err := s.AddComment(p.ID, v1.ID, 2, 2, "line2", "fix line 2", "")
			if err != nil {
				t.Fatal(err)
			}
			// re-read via ListComments (there is no GetComment) to confirm persistence
			v1all, err := s.ListComments(v1.ID, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := findComment(v1all, c.ID); got == nil || got.Quote != "line2" {
				t.Fatalf("quote not persisted: %+v", got)
			}
			open1, err := s.ListComments(v1.ID, true)
			if err != nil || len(open1) != 1 {
				t.Fatalf("want 1 open comment, got %d (err=%v)", len(open1), err)
			}

			// post v2
			v2, err := s.AddVersion(p.ID, "line1 better\nline4", nil)
			if err != nil {
				t.Fatal(err)
			}
			if v2.Number != 2 {
				t.Fatalf("want v2, got v%d", v2.Number)
			}
			// GetPlan hydrates the version list; latest is the last entry
			plan, err := s.GetPlan(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Versions) != 2 || plan.Versions[len(plan.Versions)-1] != 2 {
				t.Fatalf("plan versions want [1 2], got %v", plan.Versions)
			}

			// a reply on the v1 comment
			if _, err := s.AddReply(c.ID, AuthorAgent, "addressed in v2", ""); err != nil {
				t.Fatal(err)
			}

			// carry the v1 comment forward: it becomes a whole-file comment on v2,
			// the original is resolved, and the reply thread travels with it.
			if err := s.CarryComment(c.ID); err != nil {
				t.Fatal(err)
			}
			v2all, err := s.ListComments(v2.ID, false)
			if err != nil || len(v2all) != 1 {
				t.Fatalf("v2 should have 1 comment after carry, got %d (err=%v)", len(v2all), err)
			}
			carried := v2all[0]
			if !carried.WholeFile() {
				t.Fatalf("carried comment should be whole-file, got %d-%d", carried.LineStart, carried.LineEnd)
			}
			if carried.VersionID != v2.ID {
				t.Fatal("carried comment should attach to v2")
			}
			if len(carried.Replies) != 1 {
				t.Fatalf("carried comment should have 1 reply, got %d", len(carried.Replies))
			}
			if r := carried.Replies[0]; r.Body != "addressed in v2" || r.Author != AuthorAgent {
				t.Fatalf("carried reply mismatch: %+v", r)
			}

			// the original v1 comment is now resolved (no open comments on v1)
			if open, _ := s.ListComments(v1.ID, true); len(open) != 0 {
				t.Fatalf("v1 should have no open comments after carry, got %d", len(open))
			}
			if v1all, _ := s.ListComments(v1.ID, false); findComment(v1all, c.ID).Status != StatusResolved {
				t.Fatalf("original should be resolved")
			}
			if open, _ := s.ListComments(v2.ID, true); len(open) != 1 {
				t.Fatalf("v2 should have 1 open comment after carry, got %d", len(open))
			}

			// resolve via status setter (error-only signature)
			if err := s.SetCommentStatus(carried.ID, StatusResolved); err != nil {
				t.Fatal(err)
			}
			summaries, err := s.ListPlans()
			if err != nil {
				t.Fatal(err)
			}
			var sum *PlanSummary
			for i := range summaries {
				if summaries[i].ID == p.ID {
					sum = &summaries[i]
				}
			}
			if sum == nil {
				t.Fatal("created plan missing from ListPlans")
			}
			if sum.LatestVersion != 2 || sum.OpenComments != 0 {
				t.Fatalf("summary mismatch: %+v", *sum)
			}

			// delete the carried comment
			if err := s.DeleteComment(carried.ID, ""); err != nil {
				t.Fatal(err)
			}
			if v2all, _ := s.ListComments(v2.ID, false); len(v2all) != 0 {
				t.Fatalf("v2 should have no comments after delete, got %d", len(v2all))
			}
			if err := s.DeleteComment("c_does_not_exist", ""); !errors.Is(err, ErrNotFound) {
				t.Fatalf("delete of missing comment should be ErrNotFound, got %v", err)
			}
		})
	}
}

// TestPlanStatusAndProject covers the plan lifecycle status and the project
// origin field across both backends: new plans default to active, the project
// round-trips through GetPlan and ListPlans, and SetPlanStatus flips the status
// (with ErrNotFound for an unknown plan).
func TestPlanStatusAndProject(t *testing.T) {
	for name, open := range backends() {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			defer s.Close()

			p, _, err := s.CreatePlan("Proj", "body", "/home/me/repo", nil)
			if err != nil {
				t.Fatal(err)
			}

			// New plans default to active and keep their project on GetPlan.
			got, err := s.GetPlan(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != PlanActive {
				t.Fatalf("new plan status: want %q, got %q", PlanActive, got.Status)
			}
			if got.Project != "/home/me/repo" {
				t.Fatalf("project not persisted: got %q", got.Project)
			}

			// ListPlans surfaces status + project too.
			sum := findSummary(t, s, p.ID)
			if sum.Status != PlanActive || sum.Project != "/home/me/repo" {
				t.Fatalf("summary status/project mismatch: %+v", sum)
			}

			// Complete, then reopen.
			if err := s.SetPlanStatus(p.ID, PlanCompleted); err != nil {
				t.Fatal(err)
			}
			if got, _ := s.GetPlan(p.ID); got.Status != PlanCompleted {
				t.Fatalf("after complete: want %q, got %q", PlanCompleted, got.Status)
			}
			if err := s.SetPlanStatus(p.ID, PlanActive); err != nil {
				t.Fatal(err)
			}
			if got, _ := s.GetPlan(p.ID); got.Status != PlanActive {
				t.Fatalf("after reopen: want %q, got %q", PlanActive, got.Status)
			}

			// Unknown plan is ErrNotFound.
			if err := s.SetPlanStatus("pl_does_not_exist", PlanCompleted); !errors.Is(err, ErrNotFound) {
				t.Fatalf("SetPlanStatus on missing plan should be ErrNotFound, got %v", err)
			}
		})
	}
}

// TestFileSnapshots covers the content-addressed referenced-file store across
// both backends: snapshots round-trip via GetVersionFileList + GetBlob; identical
// content under two versions collapses to one blob but keeps two list entries;
// and DeletePlan clears a plan's list entries while sweeping only the blobs that
// no surviving plan still references.
func TestFileSnapshots(t *testing.T) {
	for name, open := range backends() {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			defer s.Close()

			shared := FileSnapshot{Path: "go.mod", Language: "", Content: "module example\n\ngo 1.22\n"}
			only := FileSnapshot{Path: "main.go", Language: "go", Content: "package main\n"}

			// (a) round-trip: a snapshot's content is retrievable via GetBlob for the
			// sha returned by GetVersionFileList.
			p, v1, err := s.CreatePlan("Files", "see main.go", "/work", []FileSnapshot{shared, only})
			if err != nil {
				t.Fatal(err)
			}
			refs, err := s.GetVersionFileList(v1.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(refs) != 2 {
				t.Fatalf("want 2 file refs, got %d", len(refs))
			}
			byPath := map[string]FileRef{}
			for _, r := range refs {
				byPath[r.Path] = r
			}
			if r, ok := byPath["main.go"]; !ok || r.Language != "go" {
				t.Fatalf("main.go ref missing/wrong: %+v", r)
			}
			gotContent, err := s.GetBlob(byPath["go.mod"].SHA)
			if err != nil || gotContent != shared.Content {
				t.Fatalf("blob round-trip mismatch: %q err=%v", gotContent, err)
			}
			if _, err := s.GetBlob("deadbeef"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetBlob of unknown sha should be ErrNotFound, got %v", err)
			}

			// (b) dedup: the same content under a second version yields one blob but
			// two list entries, both resolvable to the same sha.
			v2, err := s.AddVersion(p.ID, "still see go.mod", []FileSnapshot{shared})
			if err != nil {
				t.Fatal(err)
			}
			v2refs, err := s.GetVersionFileList(v2.ID)
			if err != nil || len(v2refs) != 1 {
				t.Fatalf("v2 want 1 file ref, got %d (err=%v)", len(v2refs), err)
			}
			if v2refs[0].SHA != byPath["go.mod"].SHA {
				t.Fatalf("identical content should share a sha: %s vs %s", v2refs[0].SHA, byPath["go.mod"].SHA)
			}

			// (c) sweep: a second plan re-cites go.mod (same blob). Deleting the first
			// plan keeps the shared blob (still referenced by plan 2) but removes the
			// now-unreferenced main.go blob and plan 1's list entries.
			mainSHA := byPath["main.go"].SHA
			sharedSHA := byPath["go.mod"].SHA
			p2, p2v1, err := s.CreatePlan("Files2", "see go.mod", "/work", []FileSnapshot{shared})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.DeletePlan(p.ID); err != nil {
				t.Fatal(err)
			}
			// plan 1's version file lists are gone.
			if refs, _ := s.GetVersionFileList(v1.ID); len(refs) != 0 {
				t.Fatalf("plan 1 file list should be cleared, got %d", len(refs))
			}
			// main.go had only plan 1 as referrer → swept.
			if _, err := s.GetBlob(mainSHA); !errors.Is(err, ErrNotFound) {
				t.Fatalf("unreferenced blob should be swept, got %v", err)
			}
			// go.mod still referenced by plan 2 → survives.
			if _, err := s.GetBlob(sharedSHA); err != nil {
				t.Fatalf("shared blob should survive while plan 2 references it: %v", err)
			}
			if refs, _ := s.GetVersionFileList(p2v1.ID); len(refs) != 1 {
				t.Fatalf("plan 2 should still reference go.mod, got %d", len(refs))
			}
			// Deleting the last referrer removes the shared blob too.
			if err := s.DeletePlan(p2.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetBlob(sharedSHA); !errors.Is(err, ErrNotFound) {
				t.Fatalf("blob should be swept once its last referrer is gone, got %v", err)
			}
		})
	}
}

// findSummary returns the ListPlans summary for planID, failing the test if absent.
func findSummary(t *testing.T, s Store, planID string) PlanSummary {
	t.Helper()
	summaries, err := s.ListPlans()
	if err != nil {
		t.Fatal(err)
	}
	for _, sum := range summaries {
		if sum.ID == planID {
			return sum
		}
	}
	t.Fatalf("plan %s missing from ListPlans", planID)
	return PlanSummary{}
}
