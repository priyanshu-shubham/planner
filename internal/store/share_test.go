package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestShareIDLifecycle covers minting, idempotency, resolution, revocation, and
// owner scoping of plan share ids, plus NULL-coexistence under the unique index.
func TestShareIDLifecycle(t *testing.T) {
	for name, open := range backends() {
		t.Run(name, func(t *testing.T) {
			root := open(t)
			defer root.Close()

			alice := mkUser(t, root, "alice")
			bob := mkUser(t, root, "bob")
			as := root.WithOwner(alice.ID)
			bs := root.WithOwner(bob.ID)

			p, _, err := as.CreatePlan("P", "l1", "/w", nil)
			if err != nil {
				t.Fatal(err)
			}
			// Two unshared plans coexist (NULL share_id must not collide).
			if _, _, err := as.CreatePlan("Q", "l1", "/w", nil); err != nil {
				t.Fatalf("second unshared plan: %v", err)
			}

			sid, err := as.EnsureShareID(p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(sid, "share_") || len(sid) < len("share_")+20 {
				t.Fatalf("share id %q: want share_ prefix and high entropy", sid)
			}
			again, err := as.EnsureShareID(p.ID)
			if err != nil || again != sid {
				t.Fatalf("EnsureShareID not idempotent: %q vs %q (err=%v)", again, sid, err)
			}

			if got, err := root.ResolveShareID(sid); err != nil || got != p.ID {
				t.Fatalf("ResolveShareID = %q, %v; want %q", got, err, p.ID)
			}
			if plan, err := as.GetPlan(p.ID); err != nil || plan.ShareID != sid {
				t.Fatalf("GetPlan.ShareID = %q, %v; want %q", plan.ShareID, err, sid)
			}

			// Only the owner manages the share.
			if _, err := bs.EnsureShareID(p.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("bob EnsureShareID = %v, want ErrNotFound", err)
			}
			if err := bs.ClearShareID(p.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("bob ClearShareID = %v, want ErrNotFound", err)
			}

			// Revoke, then the id no longer resolves; revoking again is fine.
			if err := as.ClearShareID(p.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := root.ResolveShareID(sid); !errors.Is(err, ErrNotFound) {
				t.Fatalf("ResolveShareID after revoke = %v, want ErrNotFound", err)
			}
			if err := as.ClearShareID(p.ID); err != nil {
				t.Fatalf("second revoke = %v, want nil", err)
			}

			// Re-sharing mints a fresh id (revoke-then-share rotates).
			rotated, err := as.EnsureShareID(p.ID)
			if err != nil || rotated == sid {
				t.Fatalf("rotated share id %q (err=%v), want a new id", rotated, err)
			}
		})
	}
}

// TestPlanGrantScope verifies a WithPlanGrant store reaches exactly the granted
// plan — reads and comment/reply writes work there and nowhere else — and that
// writes attribute to the acting user, not the owner.
func TestPlanGrantScope(t *testing.T) {
	for name, open := range backends() {
		t.Run(name, func(t *testing.T) {
			root := open(t)
			defer root.Close()

			alice := mkUser(t, root, "alice")
			bob := mkUser(t, root, "bob")
			as := root.WithOwner(alice.ID)

			pA, vA, err := as.CreatePlan("A", "l1\nl2", "/w", nil)
			if err != nil {
				t.Fatal(err)
			}
			pB, vB, err := as.CreatePlan("B", "l1", "/w", nil)
			if err != nil {
				t.Fatal(err)
			}

			g := root.WithPlanGrant(pA.ID)

			if _, err := g.GetPlan(pA.ID); err != nil {
				t.Fatalf("grant GetPlan(granted) = %v", err)
			}
			if _, err := g.GetPlan(pB.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("grant GetPlan(other) = %v, want ErrNotFound", err)
			}
			if _, err := g.GetVersion(pB.ID, 1); !errors.Is(err, ErrNotFound) {
				t.Fatalf("grant GetVersion(other) = %v, want ErrNotFound", err)
			}
			if list, _ := g.ListPlans(); len(list) != 1 || list[0].ID != pA.ID {
				t.Fatalf("grant ListPlans = %+v, want just %s", list, pA.ID)
			}

			// Comment + reply on the granted plan, attributed to bob.
			c, err := g.AddComment(pA.ID, vA.ID, 1, 1, "l1", "from bob", bob.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(c.ID, pA.ID+"_c_") {
				t.Fatalf("comment id %q: want %s_c_ prefix", c.ID, pA.ID)
			}
			rep, err := g.AddReply(c.ID, AuthorHuman, "more", bob.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(rep.ID, pA.ID+"_r_") {
				t.Fatalf("reply id %q: want %s_r_ prefix", rep.ID, pA.ID)
			}
			cs, err := g.ListComments(vA.ID, false)
			if err != nil || len(cs) != 1 {
				t.Fatalf("grant ListComments = %d comments (err=%v), want 1", len(cs), err)
			}
			if cs[0].AuthorID != bob.ID || cs[0].AuthorName != "bob" {
				t.Fatalf("comment attribution = %q/%q, want bob", cs[0].AuthorID, cs[0].AuthorName)
			}
			if len(cs[0].Replies) != 1 || cs[0].Replies[0].AuthorName != "bob" {
				t.Fatalf("reply attribution = %+v, want bob", cs[0].Replies)
			}

			// No reach into the other plan.
			if _, err := g.AddComment(pB.ID, vB.ID, 1, 1, "", "sneaky", bob.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("grant AddComment(other) = %v, want ErrNotFound", err)
			}
			if cs, _ := g.ListComments(vB.ID, false); len(cs) != 0 {
				t.Fatalf("grant ListComments(other) = %+v, want empty", cs)
			}

			// A version of one plan cannot be commented under another plan's id
			// (the composite id would lie about membership).
			if _, err := as.AddComment(pB.ID, vA.ID, 1, 1, "", "mismatch", alice.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("AddComment(plan/version mismatch) = %v, want ErrNotFound", err)
			}

			// Self-delete: the author constraint lets bob remove only his own
			// comment/reply; alice's stay out of reach even within the grant.
			aliceC, err := as.AddComment(pA.ID, vA.ID, 2, 2, "l2", "from alice", alice.ID)
			if err != nil {
				t.Fatal(err)
			}
			aliceR, err := as.AddReply(c.ID, AuthorAgent, "agent note", alice.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := g.DeleteComment(aliceC.ID, bob.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("bob delete alice's comment = %v, want ErrNotFound", err)
			}
			if err := g.DeleteReply(aliceR.ID, bob.ID); !errors.Is(err, ErrNotFound) {
				t.Fatalf("bob delete alice's reply = %v, want ErrNotFound", err)
			}
			if err := g.DeleteReply(rep.ID, bob.ID); err != nil {
				t.Fatalf("bob delete own reply = %v", err)
			}
			if err := g.DeleteComment(c.ID, bob.ID); err != nil {
				t.Fatalf("bob delete own comment = %v", err)
			}
			// The owner deletes anyone's rows unconstrained.
			bobR2, err := g.AddReply(aliceC.ID, AuthorHuman, "bob again", bob.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := as.DeleteReply(bobR2.ID, ""); err != nil {
				t.Fatalf("owner delete bob's reply = %v", err)
			}
			if err := as.DeleteComment(aliceC.ID, ""); err != nil {
				t.Fatalf("owner delete = %v", err)
			}
		})
	}
}

// TestCarryCommentAttribution: the carried copy and its replies keep author_id
// and get fresh plan-prefixed ids.
func TestCarryCommentAttribution(t *testing.T) {
	for name, open := range backends() {
		t.Run(name, func(t *testing.T) {
			root := open(t)
			defer root.Close()

			alice := mkUser(t, root, "alice")
			as := root.WithOwner(alice.ID)
			p, v1, err := as.CreatePlan("P", "l1", "/w", nil)
			if err != nil {
				t.Fatal(err)
			}
			c, err := as.AddComment(p.ID, v1.ID, 1, 1, "l1", "note", alice.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := as.AddReply(c.ID, AuthorAgent, "ack", alice.ID); err != nil {
				t.Fatal(err)
			}
			v2, err := as.AddVersion(p.ID, "l1 v2", nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := as.CarryComment(c.ID); err != nil {
				t.Fatal(err)
			}
			cs, err := as.ListComments(v2.ID, false)
			if err != nil || len(cs) != 1 {
				t.Fatalf("carried comments = %d (err=%v), want 1", len(cs), err)
			}
			got := cs[0]
			if !strings.HasPrefix(got.ID, p.ID+"_c_") || got.ID == c.ID {
				t.Fatalf("carried id %q: want fresh plan-prefixed id", got.ID)
			}
			if got.AuthorID != alice.ID || got.AuthorName != "alice" {
				t.Fatalf("carried attribution = %q/%q, want alice", got.AuthorID, got.AuthorName)
			}
			if len(got.Replies) != 1 || got.Replies[0].AuthorID != alice.ID || !strings.HasPrefix(got.Replies[0].ID, p.ID+"_r_") {
				t.Fatalf("carried replies = %+v, want alice-attributed plan-prefixed reply", got.Replies)
			}
		})
	}
}

// TestSQLiteChildIDMigration: a database with pre-composite comment/reply ids is
// rewritten in place on open, and a second open is a no-op.
func TestSQLiteChildIDMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	p, v1, err := s.CreatePlan("Old", "l1", "/w", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Seed old-format rows directly, as a pre-upgrade binary would have written.
	db := rawDB(s)
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO comments(id,version_id,line_start,line_end,quote,body,status,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		"c_old11111", v1.ID, 1, 1, "l1", "old comment", StatusOpen, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO replies(id,comment_id,author,body,created_at) VALUES(?,?,?,?,?)`,
		"r_old22222", "c_old11111", AuthorAgent, "old reply", now); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	for _, pass := range []string{"migrating open", "idempotent reopen"} {
		s, err = OpenSQLite(path)
		if err != nil {
			t.Fatalf("%s: %v", pass, err)
		}
		cs, err := s.ListComments(v1.ID, false)
		if err != nil || len(cs) != 1 {
			t.Fatalf("%s: ListComments = %d (err=%v), want 1", pass, len(cs), err)
		}
		wantC := p.ID + "_c_old11111"
		if cs[0].ID != wantC {
			t.Fatalf("%s: comment id = %q, want %q", pass, cs[0].ID, wantC)
		}
		if len(cs[0].Replies) != 1 || cs[0].Replies[0].ID != p.ID+"_r_old22222" {
			t.Fatalf("%s: replies = %+v, want plan-prefixed reply id", pass, cs[0].Replies)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
