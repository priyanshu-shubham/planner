package store

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

// rawDB reaches the underlying *sql.DB of either backend so a test can assert on
// table state directly.
func rawDB(s Store) *sql.DB {
	switch v := s.(type) {
	case *sqliteStore:
		return v.db
	case *sqlStore:
		return v.db
	}
	return nil
}

// mkUser creates (or refreshes) a user keyed on a sub derived from name.
func mkUser(t *testing.T, s Store, name string) User {
	t.Helper()
	u, err := s.UpsertUserByGoogleSub("sub-"+name, name+"@example.com", name, "")
	if err != nil {
		t.Fatalf("UpsertUserByGoogleSub(%s): %v", name, err)
	}
	return u
}

// TestAuthEntities covers user upsert, refresh-token rotation/reuse, and the PAT
// lifecycle across both backends.
func TestAuthEntities(t *testing.T) {
	for name, open := range backends() {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			defer s.Close()

			// Upsert is create-then-refresh, keyed on the Google sub.
			u1, err := s.UpsertUserByGoogleSub("g-1", "a@x.com", "Alice", "p1")
			if err != nil {
				t.Fatal(err)
			}
			u2, err := s.UpsertUserByGoogleSub("g-1", "alice@x.com", "Alice A.", "p2")
			if err != nil {
				t.Fatal(err)
			}
			if u1.ID != u2.ID {
				t.Fatalf("upsert created a new user: %s vs %s", u1.ID, u2.ID)
			}
			if u2.Email != "alice@x.com" || u2.Name != "Alice A." || u2.Picture != "p2" {
				t.Fatalf("upsert did not refresh profile: %+v", u2)
			}
			if got, err := s.GetUser(u1.ID); err != nil || got.Email != "alice@x.com" {
				t.Fatalf("GetUser = %+v, %v", got, err)
			}
			if _, err := s.GetUser("nope"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetUser(missing) = %v, want ErrNotFound", err)
			}

			// Refresh token rotation + reuse detection.
			if err := s.CreateRefreshToken(u1.ID, "hash-A", time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			ru, err := s.RotateRefreshToken("hash-A", "hash-B", time.Now().Add(time.Hour))
			if err != nil || ru.ID != u1.ID {
				t.Fatalf("rotate = %+v, %v", ru, err)
			}
			// The consumed hash is gone: replaying it is reuse → ErrNotFound.
			if _, err := s.RotateRefreshToken("hash-A", "hash-C", time.Now().Add(time.Hour)); !errors.Is(err, ErrNotFound) {
				t.Fatalf("reuse rotate = %v, want ErrNotFound", err)
			}
			// An expired token is refused.
			if err := s.CreateRefreshToken(u1.ID, "hash-exp", time.Now().Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RotateRefreshToken("hash-exp", "hash-D", time.Now().Add(time.Hour)); !errors.Is(err, ErrNotFound) {
				t.Fatalf("expired rotate = %v, want ErrNotFound", err)
			}
			if err := s.DeleteRefreshToken("hash-B"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RotateRefreshToken("hash-B", "hash-E", time.Now().Add(time.Hour)); !errors.Is(err, ErrNotFound) {
				t.Fatalf("rotate after delete = %v, want ErrNotFound", err)
			}

			// PAT lifecycle: create, look up by hash, touch (throttled), list, revoke.
			pat, err := s.CreatePAT(u1.ID, "laptop", "pathash-1")
			if err != nil {
				t.Fatal(err)
			}
			gotU, gotP, err := s.GetUserByPATHash("pathash-1")
			if err != nil || gotU.ID != u1.ID || gotP.ID != pat.ID {
				t.Fatalf("GetUserByPATHash = %+v %+v %v", gotU, gotP, err)
			}
			if _, _, err := s.GetUserByPATHash("nope"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetUserByPATHash(missing) = %v", err)
			}
			when := time.Now().UTC()
			if err := s.TouchPAT(pat.ID, when); err != nil {
				t.Fatal(err)
			}
			pats, err := s.ListPATs(u1.ID)
			if err != nil || len(pats) != 1 || pats[0].LastUsedAt.IsZero() {
				t.Fatalf("ListPATs after touch = %+v, %v", pats, err)
			}
			// A second touch within the throttle window must not advance last_used_at.
			if err := s.TouchPAT(pat.ID, when.Add(10*time.Second)); err != nil {
				t.Fatal(err)
			}
			again, _ := s.ListPATs(u1.ID)
			if !again[0].LastUsedAt.Truncate(time.Second).Equal(pats[0].LastUsedAt.Truncate(time.Second)) {
				t.Fatalf("throttled touch advanced last_used_at: %v -> %v", pats[0].LastUsedAt, again[0].LastUsedAt)
			}
			// One user can't revoke another's PAT; the owner can.
			other := mkUser(t, s, "bob")
			if err := s.DeletePAT(other.ID, pat.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("cross-user DeletePAT = %v, want ErrNotFound", err)
			}
			if err := s.DeletePAT(u1.ID, pat.ID); err != nil {
				t.Fatalf("DeletePAT = %v", err)
			}
			if left, _ := s.ListPATs(u1.ID); len(left) != 0 {
				t.Fatalf("ListPATs after delete = %+v", left)
			}
		})
	}
}

// TestRefreshSweep verifies that a successful rotation also deletes any expired
// refresh-token rows, so abandoned sessions don't accumulate.
func TestRefreshSweep(t *testing.T) {
	for name, open := range backends() {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			defer s.Close()
			u := mkUser(t, s, "alice")

			// An abandoned, expired token plus a live one.
			if err := s.CreateRefreshToken(u.ID, "expired", time.Now().Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
			if err := s.CreateRefreshToken(u.ID, "live", time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RotateRefreshToken("live", "live2", time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}

			// Only the freshly rotated token should remain — the expired row swept
			// and the consumed "live" row replaced.
			var n int
			if err := rawDB(s).QueryRow(`SELECT COUNT(*) FROM refresh_tokens`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Fatalf("refresh_tokens count = %d, want 1 (expired swept)", n)
			}
		})
	}
}

// TestTwoUserIsolation verifies that a store scoped to one user can neither see
// nor mutate another user's plan, versions, comments, or file lists — every
// cross-user access surfaces as ErrNotFound (or an empty result), never an
// existence oracle.
func TestTwoUserIsolation(t *testing.T) {
	for name, open := range backends() {
		t.Run(name, func(t *testing.T) {
			root := open(t)
			defer root.Close()

			alice := mkUser(t, root, "alice")
			bob := mkUser(t, root, "bob")
			as := root.WithOwner(alice.ID)
			bs := root.WithOwner(bob.ID)

			// Alice builds a plan with a version, a comment, a reply, and a file.
			file := FileSnapshot{Path: "a.go", Language: "go", Content: "package a\n"}
			p, v1, err := as.CreatePlan("Alice plan", "l1\nl2", "/work", []FileSnapshot{file})
			if err != nil {
				t.Fatal(err)
			}
			if p.OwnerID != alice.ID {
				t.Fatalf("created plan owner = %q, want %q", p.OwnerID, alice.ID)
			}
			c, err := as.AddComment(v1.ID, 1, 1, "l1", "look here")
			if err != nil {
				t.Fatal(err)
			}

			// Bob sees none of it.
			if list, _ := bs.ListPlans(); len(list) != 0 {
				t.Fatalf("bob ListPlans = %+v, want empty", list)
			}
			if _, err := bs.GetPlan(p.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("bob GetPlan = %v, want ErrNotFound", err)
			}
			if _, err := bs.GetVersion(p.ID, 1); !errors.Is(err, ErrNotFound) {
				t.Fatalf("bob GetVersion = %v, want ErrNotFound", err)
			}
			if cs, _ := bs.ListComments(v1.ID, false); len(cs) != 0 {
				t.Fatalf("bob ListComments = %+v, want empty", cs)
			}
			if refs, _ := bs.GetVersionFileList(v1.ID); len(refs) != 0 {
				t.Fatalf("bob GetVersionFileList = %+v, want empty", refs)
			}

			// Bob can't mutate it either.
			if _, err := bs.AddVersion(p.ID, "sneaky", nil); !errors.Is(err, ErrNotFound) {
				t.Fatalf("bob AddVersion = %v, want ErrNotFound", err)
			}
			if _, err := bs.AddComment(v1.ID, 1, 1, "", "sneaky"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("bob AddComment = %v, want ErrNotFound", err)
			}
			if _, err := bs.AddReply(c.ID, AuthorAgent, "sneaky"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("bob AddReply = %v, want ErrNotFound", err)
			}
			if err := bs.SetCommentStatus(c.ID, StatusResolved); !errors.Is(err, ErrNotFound) {
				t.Fatalf("bob SetCommentStatus = %v, want ErrNotFound", err)
			}
			if err := bs.DeleteComment(c.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("bob DeleteComment = %v, want ErrNotFound", err)
			}
			if err := bs.CarryComment(c.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("bob CarryComment = %v, want ErrNotFound", err)
			}
			if err := bs.SetPlanStatus(p.ID, PlanCompleted); !errors.Is(err, ErrNotFound) {
				t.Fatalf("bob SetPlanStatus = %v, want ErrNotFound", err)
			}
			if err := bs.SetPlanProject(p.ID, "/elsewhere"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("bob SetPlanProject = %v, want ErrNotFound", err)
			}
			if err := bs.DeletePlan(p.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("bob DeletePlan = %v, want ErrNotFound", err)
			}

			// Alice's data is untouched after all of Bob's attempts.
			if got, err := as.GetPlan(p.ID); err != nil || got.Status != PlanActive {
				t.Fatalf("alice GetPlan = %+v, %v", got, err)
			}
			if cs, _ := as.ListComments(v1.ID, true); len(cs) != 1 {
				t.Fatalf("alice should still have 1 open comment, got %d", len(cs))
			}

			// Blob content is intentionally unscoped (the sha is an unguessable
			// capability): Bob can fetch by sha if he somehow knows it.
			refs, _ := as.GetVersionFileList(v1.ID)
			if got, err := bs.GetBlob(refs[0].SHA); err != nil || got != file.Content {
				t.Fatalf("blob should be fetchable by sha regardless of owner: %q %v", got, err)
			}
		})
	}
}

// TestNullOwnerInvisibleUnderScope checks the pre-auth migration story: a plan
// created with no owner (owner_id IS NULL) is invisible to any scoped store but
// fully visible to the unscoped (no-auth) store.
func TestNullOwnerInvisibleUnderScope(t *testing.T) {
	for name, open := range backends() {
		t.Run(name, func(t *testing.T) {
			root := open(t) // unscoped == today's no-auth behavior
			defer root.Close()

			// A pre-auth plan: created by the unscoped store, owner_id stays NULL.
			p, v1, err := root.CreatePlan("Legacy", "body", "/legacy", nil)
			if err != nil {
				t.Fatal(err)
			}
			if p.OwnerID != "" {
				t.Fatalf("unscoped plan owner = %q, want empty", p.OwnerID)
			}

			alice := mkUser(t, root, "alice")
			as := root.WithOwner(alice.ID)
			if list, _ := as.ListPlans(); len(list) != 0 {
				t.Fatalf("scoped ListPlans saw NULL-owner plan: %+v", list)
			}
			if _, err := as.GetPlan(p.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("scoped GetPlan(NULL-owner) = %v, want ErrNotFound", err)
			}
			if _, err := as.GetVersion(p.ID, 1); !errors.Is(err, ErrNotFound) {
				t.Fatalf("scoped GetVersion(NULL-owner) = %v, want ErrNotFound", err)
			}

			// Unscoped still sees everything, including a second user's plan.
			bob := mkUser(t, root, "bob")
			bp, _, err := root.WithOwner(bob.ID).CreatePlan("Bob plan", "x", "/b", nil)
			if err != nil {
				t.Fatal(err)
			}
			ids := map[string]bool{}
			all, err := root.ListPlans()
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range all {
				ids[s.ID] = true
			}
			if !ids[p.ID] || !ids[bp.ID] {
				t.Fatalf("unscoped ListPlans should see all plans; got %v", ids)
			}
			if _, err := root.GetPlan(bp.ID); err != nil {
				t.Fatalf("unscoped GetPlan(owned) = %v", err)
			}
			_ = v1
		})
	}
}
