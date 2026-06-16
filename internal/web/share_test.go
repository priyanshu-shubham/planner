package web

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
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

// TestShareLinkVersionPolicy covers the HTTP behavior for selected-version and
// all-version share links: shared viewers only see allowed versions, direct
// guesses to hidden versions 404, and old no-body share creation still exposes
// the whole plan.
func TestShareLinkVersionPolicy(t *testing.T) {
	cfg := authCfg()
	srv, st := newTestServer(t, cfg)
	a, _ := st.UpsertUserByGoogleSub("sub-a", "a@x.com", "Alice", "")
	b, _ := st.UpsertUserByGoogleSub("sub-b", "b@x.com", "Bob", "")
	at, _ := mintAccess(cfg.Auth.Secret, a.ID, accessTTL)
	bt, _ := mintAccess(cfg.Auth.Secret, b.ID, accessTTL)

	resp := do(t, srv, "POST", "/api/plans", at, map[string]any{"title": "Selective", "content": "v1"})
	var created struct {
		PlanID string `json:"plan_id"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	planID := created.PlanID
	for _, body := range []string{"v2", "v3"} {
		resp = do(t, srv, "POST", "/api/plans/"+planID+"/versions", at, map[string]any{"content": body})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("add version = %d, want 201", resp.StatusCode)
		}
		resp.Body.Close()
	}

	resp = do(t, srv, "POST", "/api/plans/"+planID+"/share", at, map[string]any{"all_versions": false, "versions": []int{2}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("selected share = %d, want 200", resp.StatusCode)
	}
	var sh struct {
		ShareID string `json:"share_id"`
	}
	json.NewDecoder(resp.Body).Decode(&sh)
	resp.Body.Close()
	if !strings.HasPrefix(sh.ShareID, "share_") {
		t.Fatalf("share id = %q", sh.ShareID)
	}

	var meta struct {
		Versions []int `json:"versions"`
		Latest   int   `json:"latest"`
	}
	resp = do(t, srv, "GET", "/api/plans/"+sh.ShareID, bt, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("shared meta = %d, want 200", resp.StatusCode)
	}
	json.NewDecoder(resp.Body).Decode(&meta)
	resp.Body.Close()
	if meta.Latest != 2 || len(meta.Versions) != 1 || meta.Versions[0] != 2 {
		t.Fatalf("shared meta = %+v, want only v2", meta)
	}

	var sharedView struct {
		Role     string `json:"role"`
		Number   int    `json:"number"`
		Versions []int  `json:"versions"`
	}
	resp = do(t, srv, "GET", "/api/plans/"+sh.ShareID+"/v/2", bt, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("shared v2 view = %d, want 200", resp.StatusCode)
	}
	json.NewDecoder(resp.Body).Decode(&sharedView)
	resp.Body.Close()
	if sharedView.Role != "shared" || sharedView.Number != 2 || len(sharedView.Versions) != 1 || sharedView.Versions[0] != 2 {
		t.Fatalf("shared view = %+v, want shared v2 only", sharedView)
	}
	for _, n := range []int{1, 3} {
		resp = do(t, srv, "GET", "/api/plans/"+sh.ShareID+"/v/"+strconv.Itoa(n), bt, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("shared hidden v%d = %d, want 404", n, resp.StatusCode)
		}
		resp.Body.Close()
		resp = do(t, srv, "POST", "/api/plans/"+sh.ShareID+"/v/"+strconv.Itoa(n)+"/comments", bt,
			map[string]any{"line_start": 1, "line_end": 1, "body": "hidden"})
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("shared comment hidden v%d = %d, want 404", n, resp.StatusCode)
		}
		resp.Body.Close()
	}
	resp = do(t, srv, "POST", "/api/plans/"+sh.ShareID+"/v/2/comments", bt,
		map[string]any{"line_start": 1, "line_end": 1, "body": "visible"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("shared comment v2 = %d, want 201", resp.StatusCode)
	}
	resp.Body.Close()

	var ownerView struct {
		Role             string `json:"role"`
		ShareAllVersions bool   `json:"share_all_versions"`
		ShareVersions    []int  `json:"share_versions"`
		Versions         []int  `json:"versions"`
	}
	resp = do(t, srv, "GET", "/api/plans/"+planID+"/v/1", at, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner view = %d, want 200", resp.StatusCode)
	}
	json.NewDecoder(resp.Body).Decode(&ownerView)
	resp.Body.Close()
	if ownerView.Role != "owner" || ownerView.ShareAllVersions || len(ownerView.ShareVersions) != 1 || ownerView.ShareVersions[0] != 2 || len(ownerView.Versions) != 3 {
		t.Fatalf("owner share policy view = %+v, want selected v2 and all owner versions", ownerView)
	}

	resp = do(t, srv, "POST", "/api/plans/"+planID+"/share", at, map[string]any{"all_versions": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("all-version share = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
	for _, n := range []int{1, 3} {
		resp = do(t, srv, "GET", "/api/plans/"+sh.ShareID+"/v/"+strconv.Itoa(n), bt, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("all-version shared v%d = %d, want 200", n, resp.StatusCode)
		}
		resp.Body.Close()
	}

	resp = do(t, srv, "POST", "/api/plans/"+planID+"/share", at, map[string]any{"all_versions": false, "versions": []int{99}})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("share unknown version = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, srv, "POST", "/api/plans", at, map[string]any{"title": "Legacy", "content": "v1"})
	var legacy struct {
		PlanID string `json:"plan_id"`
	}
	json.NewDecoder(resp.Body).Decode(&legacy)
	resp.Body.Close()
	resp = do(t, srv, "POST", "/api/plans/"+legacy.PlanID+"/versions", at, map[string]any{"content": "v2"})
	resp.Body.Close()
	resp = do(t, srv, "POST", "/api/plans/"+legacy.PlanID+"/share", at, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy no-body share = %d, want 200", resp.StatusCode)
	}
	var legacyShare struct {
		ShareID string `json:"share_id"`
	}
	json.NewDecoder(resp.Body).Decode(&legacyShare)
	resp.Body.Close()
	resp = do(t, srv, "GET", "/api/plans/"+legacyShare.ShareID+"/v/2", bt, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy shared v2 = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	req, err := http.NewRequest("POST", srv.URL+"/api/plans/"+legacy.PlanID+"/share", io.NopCloser(strings.NewReader("")))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer "+at)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy unknown-length no-body share = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
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
