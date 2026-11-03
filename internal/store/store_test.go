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
				`TRUNCATE plans, versions, comments, replies, file_blobs, version_files, share_versions, users, refresh_tokens, pats CASCADE`); err != nil {
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

			p, v1, err := s.CreatePlan("Demo", "line1\nline2\nline3", "", "/work/demo", nil)
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
			v2, err := s.AddVersion(p.ID, "line1 better\nline4", "", nil)
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

			p, _, err := s.CreatePlan("Proj", "body", "", "/home/me/repo", nil)
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
			p, v1, err := s.CreatePlan("Files", "see main.go", "", "/work", []FileSnapshot{shared, only})
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
			v2, err := s.AddVersion(p.ID, "still see go.mod", "", []FileSnapshot{shared})
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
			p2, p2v1, err := s.CreatePlan("Files2", "see go.mod", "", "/work", []FileSnapshot{shared})
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

// TestScopedDeleteAndComment verifies that DeletePlan and AddComment work
// correctly under owner scoping — the EXISTS query must scan a boolean on
// Postgres and an integer on SQLite without error.
func TestScopedDeleteAndComment(t *testing.T) {
	for name, open := range backends() {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			defer s.Close()

			scoped := s.WithOwner("user-1")

			// Create a plan owned by user-1.
			p, v, err := scoped.CreatePlan("Scoped", "body", "", "/work", nil)
			if err != nil {
				t.Fatal(err)
			}

			// AddComment under the same owner should succeed (exercises the
			// EXISTS scan in AddComment).
			if _, err := scoped.AddComment(p.ID, v.ID, 0, 0, "", "looks good", "user-1"); err != nil {
				t.Fatalf("AddComment under owner scope: %v", err)
			}

			// A different owner must not be able to add a comment.
			other := s.WithOwner("user-2")
			if _, err := other.AddComment(p.ID, v.ID, 0, 0, "", "nope", "user-2"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("AddComment by wrong owner: want ErrNotFound, got %v", err)
			}

			// A different owner must not be able to delete the plan.
			if err := other.DeletePlan(p.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("DeletePlan by wrong owner: want ErrNotFound, got %v", err)
			}

			// The owning user can delete it (exercises the EXISTS scan in
			// DeletePlan).
			if err := scoped.DeletePlan(p.ID); err != nil {
				t.Fatalf("DeletePlan by owner: %v", err)
			}

			// Verify it's gone.
			if _, err := scoped.GetPlan(p.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetPlan after delete: want ErrNotFound, got %v", err)
			}
		})
	}
}

// TestVersionFormat covers the per-version content format across both backends:
// an explicit "html" round-trips, an empty/unknown format normalizes to
// "markdown" on write, and a plan's versions can mix formats.
func TestVersionFormat(t *testing.T) {
	for name, open := range backends() {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			defer s.Close()

			// v1: explicit html.
			p, v1, err := s.CreatePlan("Fmt", "<h1>hi</h1>", FormatHTML, "/work", nil)
			if err != nil {
				t.Fatal(err)
			}
			if v1.Format != FormatHTML {
				t.Fatalf("CreatePlan format: want %q, got %q", FormatHTML, v1.Format)
			}
			if got, _ := s.GetVersion(p.ID, 1); got.Format != FormatHTML {
				t.Fatalf("GetVersion v1 format: want %q, got %q", FormatHTML, got.Format)
			}

			// v2: empty format normalizes to markdown (mixed-format plan).
			v2, err := s.AddVersion(p.ID, "# hi", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if v2.Format != FormatMarkdown {
				t.Fatalf("AddVersion empty format: want %q, got %q", FormatMarkdown, v2.Format)
			}

			// v3: an unrecognized value also collapses to markdown.
			v3, err := s.AddVersion(p.ID, "plain", "rtf", nil)
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := s.GetVersion(p.ID, v3.Number); got.Format != FormatMarkdown {
				t.Fatalf("GetVersion v3 format: want %q, got %q", FormatMarkdown, got.Format)
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

// TestDeleteVersion covers single-version deletion across both backends: the
// version's comments, replies, share selection and file links go with it; blobs
// still cited by another version survive while orphans are swept; other
// versions keep their numbers; the only remaining version is refused; and a
// freed latest number is reused by the next AddVersion.
func TestDeleteVersion(t *testing.T) {
	for name, open := range backends() {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			defer s.Close()

			shared := FileSnapshot{Path: "go.mod", Content: "module example\n"}
			onlyV3 := FileSnapshot{Path: "main.go", Language: "go", Content: "package main\n"}

			p, v1, err := s.CreatePlan("Versions", "one", "", "/work", []FileSnapshot{shared})
			if err != nil {
				t.Fatal(err)
			}
			v2, err := s.AddVersion(p.ID, "two", "", []FileSnapshot{shared})
			if err != nil {
				t.Fatal(err)
			}
			v3, err := s.AddVersion(p.ID, "three", "", []FileSnapshot{onlyV3})
			if err != nil {
				t.Fatal(err)
			}
			c, err := s.AddComment(p.ID, v2.ID, 0, 0, "", "note", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddReply(c.ID, AuthorAgent, "ack", ""); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SetSharePolicy(p.ID, false, []int{2, 3}); err != nil {
				t.Fatal(err)
			}
			v3refs, err := s.GetVersionFileList(v3.ID)
			if err != nil || len(v3refs) != 1 {
				t.Fatalf("v3 file refs = %d, err=%v", len(v3refs), err)
			}
			sharedRefs, _ := s.GetVersionFileList(v1.ID)

			// Deleting a middle version removes its rows but keeps v1/v3 numbered.
			if err := s.DeleteVersion(p.ID, 2); err != nil {
				t.Fatalf("DeleteVersion(2): %v", err)
			}
			if _, err := s.GetVersion(p.ID, 2); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetVersion(2) after delete = %v, want ErrNotFound", err)
			}
			if cs, _ := s.ListComments(v2.ID, false); len(cs) != 0 {
				t.Fatalf("comments should be gone, got %d", len(cs))
			}
			if refs, _ := s.GetVersionFileList(v2.ID); len(refs) != 0 {
				t.Fatalf("v2 file list should be cleared, got %d", len(refs))
			}
			got, err := s.GetPlan(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Versions) != 2 || got.Versions[0] != 1 || got.Versions[1] != 3 {
				t.Fatalf("versions = %v, want [1 3]", got.Versions)
			}
			if len(got.ShareVersions) != 1 || got.ShareVersions[0] != 3 {
				t.Fatalf("share versions = %v, want [3]", got.ShareVersions)
			}
			db := rawDB(s)
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM comments`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("comment rows = %d, err=%v, want 0", n, err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM replies`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("reply rows = %d, err=%v, want 0", n, err)
			}
			// go.mod is still cited by v1, so its blob survives v2's deletion.
			if _, err := s.GetBlob(sharedRefs[0].SHA); err != nil {
				t.Fatalf("shared blob should survive: %v", err)
			}

			// v3's file has no other referrer, so deleting v3 sweeps its blob.
			if err := s.DeleteVersion(p.ID, 3); err != nil {
				t.Fatalf("DeleteVersion(3): %v", err)
			}
			if _, err := s.GetBlob(v3refs[0].SHA); !errors.Is(err, ErrNotFound) {
				t.Fatalf("orphan blob should be swept, got %v", err)
			}

			// The only remaining version cannot be deleted.
			if err := s.DeleteVersion(p.ID, 1); !errors.Is(err, ErrLastVersion) {
				t.Fatalf("DeleteVersion(last) = %v, want ErrLastVersion", err)
			}
			if _, err := s.GetVersion(p.ID, 1); err != nil {
				t.Fatalf("v1 should remain: %v", err)
			}

			// A freed latest number is reused; numbers are not renumbered.
			nv, err := s.AddVersion(p.ID, "again", "", nil)
			if err != nil || nv.Number != 2 {
				t.Fatalf("AddVersion after deleting latest = %d, err=%v, want 2", nv.Number, err)
			}

			if err := s.DeleteVersion(p.ID, 99); !errors.Is(err, ErrNotFound) {
				t.Fatalf("DeleteVersion(unknown version) = %v, want ErrNotFound", err)
			}
			if err := s.DeleteVersion("nope", 1); !errors.Is(err, ErrNotFound) {
				t.Fatalf("DeleteVersion(unknown plan) = %v, want ErrNotFound", err)
			}
		})
	}
}
