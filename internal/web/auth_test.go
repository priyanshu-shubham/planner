package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"planner/internal/store"
)

// newTestServer builds an httptest server backed by a fresh SQLite store. cfg
// selects no-auth vs. authed mode. The store is returned so tests can seed users
// and tokens directly.
func newTestServer(t *testing.T, cfg Config) (*httptest.Server, store.Store) {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	h, err := newHandler(st, cfg)
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.Close(); st.Close() })
	return srv, st
}

func authCfg() Config {
	return Config{Auth: &AuthConfig{
		GoogleClientID:     "cid",
		GoogleClientSecret: "csecret",
		Secret:             []byte("test-hmac-secret"),
	}}
}

// do issues a request with optional bearer token and JSON body, returning the
// response (caller closes the body).
func do(t *testing.T, srv *httptest.Server, method, path, bearer string, body any) *http.Response {
	t.Helper()
	var r *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	} else {
		r = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, srv.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestRenderSetup checks the auth-mode blocks are kept/stripped and {{BASE}} is
// substituted.
func TestRenderSetup(t *testing.T) {
	md := "base={{BASE}}\n{{IF_AUTH}}\nAUTHED\n{{END_AUTH}}\n{{IF_NOAUTH}}\nOPEN\n{{END_NOAUTH}}\nend\n"

	authed := renderSetup(md, "https://x", true)
	if !strings.Contains(authed, "AUTHED") || strings.Contains(authed, "OPEN") {
		t.Fatalf("authed render wrong:\n%s", authed)
	}
	if !strings.Contains(authed, "base=https://x") {
		t.Fatalf("BASE not substituted:\n%s", authed)
	}

	open := renderSetup(md, "http://y", false)
	if !strings.Contains(open, "OPEN") || strings.Contains(open, "AUTHED") {
		t.Fatalf("no-auth render wrong:\n%s", open)
	}
	if strings.Contains(open, "{{") {
		t.Fatalf("leftover markers:\n%s", open)
	}
}

// TestNoAuthOpen is the regression guard: with auth off, every endpoint is open
// and /api/config reports "none".
func TestNoAuthOpen(t *testing.T) {
	srv, _ := newTestServer(t, Config{})

	resp := do(t, srv, "POST", "/api/plans", "", map[string]any{"title": "T", "content": "body"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create plan without auth = %d, want 201", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, srv, "GET", "/api/plans", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list plans without auth = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, srv, "GET", "/api/config", "", nil)
	var cfg map[string]string
	json.NewDecoder(resp.Body).Decode(&cfg)
	resp.Body.Close()
	if cfg["auth"] != "none" {
		t.Fatalf("config auth = %q, want none", cfg["auth"])
	}
}

// TestAuthModeGating covers the authed-mode access matrix: 401 without a token,
// 200 with an access token and with a PAT, public paths always reachable.
func TestAuthModeGating(t *testing.T) {
	cfg := authCfg()
	srv, st := newTestServer(t, cfg)
	user, _ := st.UpsertUserByGoogleSub("sub-1", "u@x.com", "U", "")
	access, _ := mintAccess(cfg.Auth.Secret, user.ID, accessTTL)

	// No token → 401.
	resp := do(t, srv, "GET", "/api/plans", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Access token → 200.
	resp = do(t, srv, "GET", "/api/plans", access, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("access token = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// PAT → 200.
	patTok, patHash := newOpaqueToken(patPrefix)
	if _, err := st.CreatePAT(user.ID, "cli", patHash); err != nil {
		t.Fatal(err)
	}
	resp = do(t, srv, "GET", "/api/plans", patTok, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PAT = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// Config + SPA shell + setup.md are public.
	for _, p := range []string{"/api/config", "/", "/setup.md"} {
		resp = do(t, srv, "GET", p, "", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("public path %s = %d, want 200", p, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// config reports google.
	resp = do(t, srv, "GET", "/api/config", "", nil)
	var c map[string]string
	json.NewDecoder(resp.Body).Decode(&c)
	resp.Body.Close()
	if c["auth"] != "google" {
		t.Fatalf("config auth = %q, want google", c["auth"])
	}
}

// TestCrossUserIsolation: one user's plan is invisible (404) to another via the API.
func TestCrossUserIsolation(t *testing.T) {
	cfg := authCfg()
	srv, st := newTestServer(t, cfg)
	a, _ := st.UpsertUserByGoogleSub("sub-a", "a@x.com", "A", "")
	b, _ := st.UpsertUserByGoogleSub("sub-b", "b@x.com", "B", "")
	at, _ := mintAccess(cfg.Auth.Secret, a.ID, accessTTL)
	bt, _ := mintAccess(cfg.Auth.Secret, b.ID, accessTTL)

	resp := do(t, srv, "POST", "/api/plans", at, map[string]any{"title": "A's", "content": "secret"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("A create = %d", resp.StatusCode)
	}
	var created struct {
		PlanID string `json:"plan_id"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	// B cannot read it.
	resp = do(t, srv, "GET", "/api/plans/"+created.PlanID, bt, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("B read A's plan = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	// B's plan list is empty; A's has one.
	resp = do(t, srv, "GET", "/api/plans", bt, nil)
	var bList []any
	json.NewDecoder(resp.Body).Decode(&bList)
	resp.Body.Close()
	if len(bList) != 0 {
		t.Fatalf("B list = %d plans, want 0", len(bList))
	}
}

// TestRefreshRotationAndReuse: a valid refresh cookie mints an access token and
// rotates the cookie; replaying the old cookie is rejected; logout kills it.
func TestRefreshRotationAndReuse(t *testing.T) {
	cfg := authCfg()
	srv, st := newTestServer(t, cfg)
	user, _ := st.UpsertUserByGoogleSub("sub-1", "u@x.com", "U", "")

	rawToken, hash := newOpaqueToken("")
	if err := st.CreateRefreshToken(user.ID, hash, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	refresh := func(cookieVal string) (*http.Response, string) {
		req, _ := http.NewRequest("POST", srv.URL+"/api/auth/refresh", nil)
		req.AddCookie(&http.Cookie{Name: refreshCookie, Value: cookieVal})
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var newCookie string
		for _, c := range resp.Cookies() {
			if c.Name == refreshCookie {
				newCookie = c.Value
			}
		}
		return resp, newCookie
	}

	resp, rotated := refresh(rawToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first refresh = %d, want 200", resp.StatusCode)
	}
	var out struct {
		AccessToken string  `json:"access_token"`
		User        userDTO `json:"user"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.AccessToken == "" || out.User.ID != user.ID {
		t.Fatalf("refresh payload bad: %+v", out)
	}
	if rotated == "" || rotated == rawToken {
		t.Fatalf("cookie not rotated: %q", rotated)
	}

	// Replaying the consumed cookie is reuse → 401.
	resp, _ = refresh(rawToken)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reuse refresh = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// The rotated cookie still works once...
	resp, rotated2 := refresh(rotated)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotated refresh = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// ...then logout kills the latest cookie.
	req, _ := http.NewRequest("POST", srv.URL+"/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: refreshCookie, Value: rotated2})
	resp, _ = srv.Client().Do(req)
	resp.Body.Close()
	resp, _ = refresh(rotated2)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refresh after logout = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestPATCannotManagePATs: PAT management requires a web session — a PAT bearer is
// rejected on /api/pats, while an access token works.
func TestPATCannotManagePATs(t *testing.T) {
	cfg := authCfg()
	srv, st := newTestServer(t, cfg)
	user, _ := st.UpsertUserByGoogleSub("sub-1", "u@x.com", "U", "")
	access, _ := mintAccess(cfg.Auth.Secret, user.ID, accessTTL)
	patTok, patHash := newOpaqueToken(patPrefix)
	st.CreatePAT(user.ID, "cli", patHash)

	// PAT is rejected on the PAT management endpoint.
	resp := do(t, srv, "GET", "/api/pats", patTok, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("PAT on /api/pats = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Access token can list and mint.
	resp = do(t, srv, "GET", "/api/pats", access, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("access on /api/pats = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, srv, "POST", "/api/pats", access, map[string]any{"name": "laptop"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("mint PAT = %d, want 201", resp.StatusCode)
	}
	var minted struct {
		Token string `json:"token"`
		ID    string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&minted)
	resp.Body.Close()
	if minted.Token == "" {
		t.Fatal("mint did not return a token")
	}

	// The minted token authenticates a data request.
	resp = do(t, srv, "GET", "/api/plans", minted.Token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("minted PAT on /api/plans = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()
}
