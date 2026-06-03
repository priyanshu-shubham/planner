package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// backends returns the Store implementations the conformance suite runs against.
// SQLite always runs; Firestore runs only when FIRESTORE_EMULATOR_HOST is set
// (so CI without the emulator stays green). Start one locally with
// `gcloud emulators firestore start` and export the host it prints.
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
	if os.Getenv("FIRESTORE_EMULATOR_HOST") != "" {
		m["firestore"] = func(t *testing.T) Store {
			project := os.Getenv("PLANNER_FIRESTORE_PROJECT")
			if project == "" {
				project = "planner-test"
			}
			s, err := OpenFirestore(context.Background(), project, "(default)")
			if err != nil {
				t.Fatal(err)
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

			p, v1, err := s.CreatePlan("Demo", "line1\nline2\nline3", "/work/demo")
			if err != nil {
				t.Fatal(err)
			}
			if v1.Number != 1 {
				t.Fatalf("want v1, got v%d", v1.Number)
			}

			// anchored comment on v1, with the selected text quoted
			c, err := s.AddComment(v1.ID, 2, 2, "line2", "fix line 2")
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
			v2, err := s.AddVersion(p.ID, "line1 better\nline4")
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
			if _, err := s.AddReply(c.ID, AuthorAgent, "addressed in v2"); err != nil {
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
			if err := s.DeleteComment(carried.ID); err != nil {
				t.Fatal(err)
			}
			if v2all, _ := s.ListComments(v2.ID, false); len(v2all) != 0 {
				t.Fatalf("v2 should have no comments after delete, got %d", len(v2all))
			}
			if err := s.DeleteComment("c_does_not_exist"); !errors.Is(err, ErrNotFound) {
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

			p, _, err := s.CreatePlan("Proj", "body", "/home/me/repo")
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
