package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestShareLinkAccess is the share-link authorization matrix: a second signed-in
// user reaches a shared plan through its share id with view+comment+reply
// access and nothing more, attribution lands on the actual commenter, and
// revocation cuts the link off.
func TestShareLinkAccess(t *testing.T) {
	cfg := authCfg()
	srv, st := newTestServer(t, cfg)
	a, _ := st.UpsertUserByGoogleSub("sub-a", "a@x.com", "Alice", "")
	b, _ := st.UpsertUserByGoogleSub("sub-b", "b@x.com", "Bob", "")
	at, _ := mintAccess(cfg.Auth.Secret, a.ID, accessTTL)
	bt, _ := mintAccess(cfg.Auth.Secret, b.ID, accessTTL)

	// Alice creates a plan and shares it.
	resp := do(t, srv, "POST", "/api/plans", at, map[string]any{"title": "A's", "content": "l1\nl2"})
	var created struct {
		PlanID string `json:"plan_id"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	planID := created.PlanID

	share := func(tok string) (int, string) {
		resp := do(t, srv, "POST", "/api/plans/"+planID+"/share", tok, nil)
		defer resp.Body.Close()
		var out struct {
			ShareID string `json:"share_id"`
		}
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out.ShareID
	}
	code, shareID := share(at)
	if code != http.StatusOK || !strings.HasPrefix(shareID, "share_") {
		t.Fatalf("share = %d %q", code, shareID)
	}
	if code, again := share(at); code != http.StatusOK || again != shareID {
		t.Fatalf("re-share = %d %q, want idempotent %q", code, again, shareID)
	}
	// Bob can neither mint nor see a share for someone else's plan.
	if code, _ := share(bt); code != http.StatusNotFound {
		t.Fatalf("bob share on A's plan = %d, want 404", code)
	}

	// The canonical id stays invisible to Bob; the share id opens the plan.
	resp = do(t, srv, "GET", "/api/plans/"+planID, bt, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob canonical read = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	var view struct {
		Role     string `json:"role"`
		ShareID  string `json:"share_id"`
		Comments []struct {
			ID         string `json:"id"`
			AuthorName string `json:"author_name"`
			Replies    []struct {
				Author     string `json:"author"`
				AuthorName string `json:"author_name"`
			} `json:"replies"`
		} `json:"comments"`
	}
	getView := func(id, tok string) (int, bool) {
		resp := do(t, srv, "GET", "/api/plans/"+id+"/v/1", tok, nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return resp.StatusCode, false
		}
		view = struct {
			Role     string `json:"role"`
			ShareID  string `json:"share_id"`
			Comments []struct {
				ID         string `json:"id"`
				AuthorName string `json:"author_name"`
				Replies    []struct {
					Author     string `json:"author"`
					AuthorName string `json:"author_name"`
				} `json:"replies"`
			} `json:"comments"`
		}{}
		json.NewDecoder(resp.Body).Decode(&view)
		return resp.StatusCode, true
	}
	if code, ok := getView(shareID, bt); !ok || code != http.StatusOK {
		t.Fatalf("bob shared view = %d, want 200", code)
	}
	if view.Role != "shared" || view.ShareID != "" {
		t.Fatalf("shared view role=%q share_id=%q, want shared/empty", view.Role, view.ShareID)
	}

	// An unauthenticated request is still rejected: the link is not public.
	resp = do(t, srv, "GET", "/api/plans/"+shareID+"/v/1", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous shared view = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Bob comments and replies through the share id; both attribute to Bob and
	// the wire carries short (unprefixed) ids.
	resp = do(t, srv, "POST", "/api/plans/"+shareID+"/v/1/comments", bt,
		map[string]any{"line_start": 1, "line_end": 1, "quote": "l1", "body": "from bob"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bob comment via share = %d, want 201", resp.StatusCode)
	}
	var comment struct {
		ID         string `json:"id"`
		AuthorName string `json:"author_name"`
		Own        bool   `json:"own"`
	}
	json.NewDecoder(resp.Body).Decode(&comment)
	resp.Body.Close()
	if comment.AuthorName != "Bob" || !comment.Own {
		t.Fatalf("comment author = %q own=%v, want Bob/true", comment.AuthorName, comment.Own)
	}
	if strings.HasPrefix(comment.ID, "pl_") || !strings.HasPrefix(comment.ID, "c_") {
		t.Fatalf("comment wire id = %q, want short c_ form", comment.ID)
	}
	resp = do(t, srv, "POST", "/api/plans/"+shareID+"/comments/"+comment.ID+"/replies", bt,
		map[string]any{"author": "human", "body": "and a reply"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bob reply via share = %d, want 201", resp.StatusCode)
	}
	var bobReply struct {
		ID  string `json:"id"`
		Own bool   `json:"own"`
	}
	json.NewDecoder(resp.Body).Decode(&bobReply)
	resp.Body.Close()
	if !bobReply.Own {
		t.Fatalf("bob's reply own=%v, want true", bobReply.Own)
	}

	// Everything owner-only is a 403 through the share id.
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/plans/" + shareID + "/status", map[string]any{"status": "completed"}},
		{"POST", "/api/plans/" + shareID + "/project", map[string]any{"project": "/x"}},
		{"POST", "/api/plans/" + shareID + "/versions", map[string]any{"content": "x"}},
		{"DELETE", "/api/plans/" + shareID, nil},
		{"POST", "/api/plans/" + shareID + "/comments/" + comment.ID + "/resolve", nil},
		{"POST", "/api/plans/" + shareID + "/comments/" + comment.ID + "/reopen", nil},
		{"POST", "/api/plans/" + shareID + "/comments/" + comment.ID + "/keep", nil},
		{"POST", "/api/plans/" + shareID + "/share", nil},
		{"DELETE", "/api/plans/" + shareID + "/share", nil},
	} {
		resp := do(t, srv, tc.method, tc.path, bt, tc.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s via share = %d, want 403", tc.method, tc.path, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// The owner following their own share link is recognized and upgraded, so
	// the SPA can redirect to the canonical URL.
	if code, ok := getView(shareID, at); !ok || code != http.StatusOK {
		t.Fatalf("alice via share = %d, want 200", code)
	}
	if view.Role != "owner" || view.ShareID != shareID {
		t.Fatalf("alice via share role=%q share_id=%q, want owner/%q", view.Role, view.ShareID, shareID)
	}

	// The owner sees Bob's attributed thread and can resolve it.
	if code, ok := getView(planID, at); !ok || code != http.StatusOK {
		t.Fatalf("alice view = %d, want 200", code)
	}
	if view.Role != "owner" || view.ShareID != shareID {
		t.Fatalf("owner view role=%q share_id=%q, want owner/%q", view.Role, view.ShareID, shareID)
	}
	if len(view.Comments) != 1 || view.Comments[0].AuthorName != "Bob" {
		t.Fatalf("owner sees comments = %+v, want Bob's", view.Comments)
	}
	if rs := view.Comments[0].Replies; len(rs) != 1 || rs[0].AuthorName != "Bob" {
		t.Fatalf("owner sees replies = %+v, want Bob's", view.Comments[0].Replies)
	}
	resp = do(t, srv, "POST", "/api/plans/"+planID+"/comments/"+comment.ID+"/resolve", at, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("owner resolve = %d, want 204", resp.StatusCode)
	}
	resp.Body.Close()

	// A comment id under the wrong plan does not exist.
	resp = do(t, srv, "POST", "/api/plans", at, map[string]any{"title": "other", "content": "x"})
	var other struct {
		PlanID string `json:"plan_id"`
	}
	json.NewDecoder(resp.Body).Decode(&other)
	resp.Body.Close()
	resp = do(t, srv, "POST", "/api/plans/"+other.PlanID+"/comments/"+comment.ID+"/resolve", at, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-plan cid resolve = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// Self-delete: Bob removes only what he authored; Alice's rows 404 for him.
	resp = do(t, srv, "POST", "/api/plans/"+planID+"/v/1/comments", at,
		map[string]any{"line_start": 2, "line_end": 2, "body": "from alice"})
	var aliceComment struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&aliceComment)
	resp.Body.Close()
	resp = do(t, srv, "POST", "/api/plans/"+planID+"/comments/"+comment.ID+"/replies", at,
		map[string]any{"author": "human", "body": "alice replying"})
	var aliceReply struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&aliceReply)
	resp.Body.Close()

	resp = do(t, srv, "DELETE", "/api/plans/"+shareID+"/comments/"+aliceComment.ID, bt, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob delete alice's comment = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, srv, "DELETE", "/api/plans/"+shareID+"/comments/"+comment.ID+"/replies/"+aliceReply.ID, bt, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob delete alice's reply = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	resp = do(t, srv, "DELETE", "/api/plans/"+shareID+"/comments/"+comment.ID+"/replies/"+bobReply.ID, bt, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("bob delete own reply = %d, want 204", resp.StatusCode)
	}
	resp.Body.Close()
	// The owner moderates anything: Alice removes Bob's comment (cascading her
	// own reply on it) through the canonical path.
	resp = do(t, srv, "DELETE", "/api/plans/"+planID+"/comments/"+comment.ID, at, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("alice delete bob's comment = %d, want 204", resp.StatusCode)
	}
	resp.Body.Close()
	// And Bob removes a comment he authored, via the share id.
	resp = do(t, srv, "POST", "/api/plans/"+shareID+"/v/1/comments", bt,
		map[string]any{"line_start": 1, "line_end": 1, "body": "oops, typo"})
	var bobC2 struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&bobC2)
	resp.Body.Close()
	resp = do(t, srv, "DELETE", "/api/plans/"+shareID+"/comments/"+bobC2.ID, bt, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("bob delete own comment = %d, want 204", resp.StatusCode)
	}
	resp.Body.Close()

	// Revocation: the link dies for Bob, Alice's access is untouched.
	resp = do(t, srv, "DELETE", "/api/plans/"+planID+"/share", at, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke = %d, want 204", resp.StatusCode)
	}
	resp.Body.Close()
	if code, _ := getView(shareID, bt); code != http.StatusNotFound {
		t.Fatalf("bob view after revoke = %d, want 404", code)
	}
	if code, _ := getView(planID, at); code != http.StatusOK {
		t.Fatalf("alice view after revoke = %d, want 200", code)
	}
}

// TestCommentRoutesNoAuth: the plan-scoped comment routes work in no-auth mode
// (unscoped store, NULL attribution) — the single-user regression path.
func TestCommentRoutesNoAuth(t *testing.T) {
	srv, _ := newTestServer(t, Config{})

	resp := do(t, srv, "POST", "/api/plans", "", map[string]any{"title": "T", "content": "l1"})
	var created struct {
		PlanID string `json:"plan_id"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	planID := created.PlanID

	resp = do(t, srv, "POST", "/api/plans/"+planID+"/v/1/comments", "",
		map[string]any{"line_start": 1, "line_end": 1, "body": "note"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("comment = %d, want 201", resp.StatusCode)
	}
	var c struct {
		ID         string `json:"id"`
		AuthorName string `json:"author_name"`
	}
	json.NewDecoder(resp.Body).Decode(&c)
	resp.Body.Close()
	if c.AuthorName != "" {
		t.Fatalf("no-auth comment author = %q, want empty", c.AuthorName)
	}

	resp = do(t, srv, "POST", "/api/plans/"+planID+"/comments/"+c.ID+"/replies", "",
		map[string]any{"author": "agent", "body": "ack"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("reply = %d, want 201", resp.StatusCode)
	}
	var rep struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&rep)
	resp.Body.Close()

	steps := []struct {
		method, path string
		body         any
	}{
		{"DELETE", "/api/plans/" + planID + "/comments/" + c.ID + "/replies/" + rep.ID, nil},
		{"POST", "/api/plans/" + planID + "/comments/" + c.ID + "/resolve", nil},
		{"POST", "/api/plans/" + planID + "/comments/" + c.ID + "/reopen", nil},
		{"DELETE", "/api/plans/" + planID + "/comments/" + c.ID, nil},
	}
	for _, s := range steps {
		resp := do(t, srv, s.method, s.path, "", s.body)
		if resp.StatusCode >= 300 {
			t.Fatalf("%s %s = %d", s.method, s.path, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// Sharing still functions without auth (harmless: everything is open anyway).
	resp = do(t, srv, "POST", "/api/plans/"+planID+"/share", "", nil)
	var sh struct {
		ShareID string `json:"share_id"`
	}
	json.NewDecoder(resp.Body).Decode(&sh)
	resp.Body.Close()
	resp = do(t, srv, "GET", "/api/plans/"+sh.ShareID+"/v/1", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("no-auth shared view = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}
