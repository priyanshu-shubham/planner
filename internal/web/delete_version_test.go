package web

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestDeleteVersionRoute covers DELETE /api/plans/{id}/v/{n}: 204 on success,
// 400 for a non-numeric or "latest" version, 404 for an unknown version, 409
// for the only remaining version, 403 through a share link, and 404 for a
// different signed-in user.
func TestDeleteVersionRoute(t *testing.T) {
	cfg := authCfg()
	srv, st := newTestServer(t, cfg)
	a, _ := st.UpsertUserByGoogleSub("sub-a", "a@x.com", "Alice", "")
	b, _ := st.UpsertUserByGoogleSub("sub-b", "b@x.com", "Bob", "")
	at, _ := mintAccess(cfg.Auth.Secret, a.ID, accessTTL)
	bt, _ := mintAccess(cfg.Auth.Secret, b.ID, accessTTL)

	resp := do(t, srv, "POST", "/api/plans", at, map[string]any{"title": "T", "content": "one"})
	var created struct {
		PlanID string `json:"plan_id"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	planID := created.PlanID
	resp = do(t, srv, "POST", "/api/plans/"+planID+"/versions", at, map[string]any{"content": "two"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add version = %d, want 201", resp.StatusCode)
	}
	resp.Body.Close()

	del := func(tok, id, n string) int {
		resp := do(t, srv, "DELETE", "/api/plans/"+id+"/v/"+n, tok, nil)
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// Bob cannot see Alice's plan, so the delete is a 404, not a 403.
	if got := del(bt, planID, "2"); got != http.StatusNotFound {
		t.Fatalf("bob delete = %d, want 404", got)
	}

	// A share link is view+comment only.
	resp = do(t, srv, "POST", "/api/plans/"+planID+"/share", at, nil)
	var sh struct {
		ShareID string `json:"share_id"`
	}
	json.NewDecoder(resp.Body).Decode(&sh)
	resp.Body.Close()
	if got := del(at, sh.ShareID, "2"); got != http.StatusForbidden {
		t.Fatalf("delete via share id = %d, want 403", got)
	}

	if got := del(at, planID, "abc"); got != http.StatusBadRequest {
		t.Fatalf("non-numeric version = %d, want 400", got)
	}
	if got := del(at, planID, "latest"); got != http.StatusBadRequest {
		t.Fatalf("latest = %d, want 400", got)
	}
	if got := del(at, planID, "99"); got != http.StatusNotFound {
		t.Fatalf("unknown version = %d, want 404", got)
	}
	if got := del(at, planID, "2"); got != http.StatusNoContent {
		t.Fatalf("delete v2 = %d, want 204", got)
	}
	resp = do(t, srv, "GET", "/api/plans/"+planID+"/v/2", at, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET deleted version = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
	if got := del(at, planID, "1"); got != http.StatusConflict {
		t.Fatalf("delete only version = %d, want 409", got)
	}
}
