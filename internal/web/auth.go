package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"planner/internal/store"
)

// Config configures the server. A nil Auth means the no-auth, single-user mode
// (the default): every request is unscoped and no login is required. CLIDir, when
// set, is a directory of cross-compiled planner binaries served at /cli/{platform}
// (the Docker image ships one); when empty the endpoint reports that this server
// does not distribute the CLI.
type Config struct {
	Auth   *AuthConfig
	CLIDir string
}

// AuthConfig holds the settings for Google-login auth. Secret is the HMAC key for
// access tokens and signed cookies (random per start when the operator does not
// supply one — access tokens then die on restart and the SPA recovers via the
// refresh cookie).
type AuthConfig struct {
	GoogleClientID     string
	GoogleClientSecret string
	Secret             []byte
}

const (
	accessTTL     = 15 * time.Minute
	refreshTTL    = 30 * 24 * time.Hour
	refreshCookie = "planner_refresh"
	refreshPath   = "/api/auth"
)

// userIDKey types the request-context key carrying the authenticated user id.
type userIDKey struct{}

// authEnabled reports whether the server is running in authed mode.
func (h *handlers) authEnabled() bool { return h.cfg.Auth != nil }

// store returns the data store scoped to the request's authenticated user. With
// auth off (or no user in context) it returns the unscoped store — today's
// single-user behavior.
func (h *handlers) store(r *http.Request) store.Store {
	if uid, ok := r.Context().Value(userIDKey{}).(string); ok && uid != "" {
		return h.st.WithOwner(uid)
	}
	return h.st
}

// requireAuth wraps an API handler so it runs only for an authenticated request,
// putting the user id in context for h.store. A Bearer access token or PAT both
// satisfy it; anything else is a 401.
func (h *handlers) requireAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := h.authenticate(r)
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userIDKey{}, uid)))
	})
}

// authenticate resolves the request's Bearer credential to a user id. A token
// with the PAT prefix is looked up by hash (and its last-used time touched);
// otherwise it is verified as a web access token.
func (h *handlers) authenticate(r *http.Request) (string, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return "", false
	}
	if strings.HasPrefix(token, patPrefix) {
		user, pat, err := h.st.GetUserByPATHash(hashToken(token))
		if err != nil {
			return "", false
		}
		_ = h.st.TouchPAT(pat.ID, time.Now().UTC()) // best-effort; throttled in the store
		return user.ID, true
	}
	return verifyAccess(h.cfg.Auth.Secret, token)
}

// requireWebUser is like requireAuth but rejects PATs: only a web access token is
// accepted. PAT management endpoints use it so a stolen PAT can neither mint nor
// enumerate tokens.
func (h *handlers) requireWebUser(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" || strings.HasPrefix(token, patPrefix) {
			writeJSONError(w, http.StatusUnauthorized, "web session required")
			return
		}
		uid, ok := verifyAccess(h.cfg.Auth.Secret, token)
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "web session required")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userIDKey{}, uid)))
	})
}

// isSecure reports whether the request arrived over HTTPS, directly or via a
// terminating proxy, so cookies can carry the Secure attribute when appropriate.
func isSecure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// setRefreshCookie stores the raw refresh token in an httpOnly cookie scoped to
// the refresh/logout endpoints.
func (h *handlers) setRefreshCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookie,
		Value:    token,
		Path:     refreshPath,
		HttpOnly: true,
		Secure:   isSecure(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(refreshTTL),
		MaxAge:   int(refreshTTL.Seconds()),
	})
}

// clearRefreshCookie expires the refresh cookie (logout / failed rotation).
func (h *handlers) clearRefreshCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookie,
		Value:    "",
		Path:     refreshPath,
		HttpOnly: true,
		Secure:   isSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// ---- user DTO ----

type userDTO struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

func toUserDTO(u store.User) userDTO {
	return userDTO{ID: u.ID, Email: u.Email, Name: u.Name, Picture: u.Picture}
}

// ---- auth endpoints ----

// apiConfig advertises the server's auth mode. Always public — the SPA fetches it
// before anything else to decide whether to show a login screen.
func (h *handlers) apiConfig(w http.ResponseWriter, r *http.Request) {
	mode := "none"
	if h.authEnabled() {
		mode = "google"
	}
	writeJSON(w, http.StatusOK, map[string]string{"auth": mode})
}

// apiRefresh rotates the refresh cookie and returns a fresh access token plus the
// user. A missing/unknown/expired cookie is a 401 (and the cookie is cleared) —
// rotation also gives reuse detection (a replayed token no longer exists).
func (h *handlers) apiRefresh(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(refreshCookie)
	if err != nil || c.Value == "" {
		writeJSONError(w, http.StatusUnauthorized, "no session")
		return
	}
	newToken, newHash := newOpaqueToken("")
	user, err := h.st.RotateRefreshToken(hashToken(c.Value), newHash, time.Now().Add(refreshTTL))
	if err != nil {
		h.clearRefreshCookie(w, r)
		writeJSONError(w, http.StatusUnauthorized, "session expired")
		return
	}
	h.setRefreshCookie(w, r, newToken)
	access, exp := mintAccess(h.cfg.Auth.Secret, user.ID, accessTTL)
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access,
		"expires_in":   exp - time.Now().Unix(),
		"user":         toUserDTO(user),
	})
}

// apiLogout deletes the presented refresh token and clears the cookie. Idempotent.
func (h *handlers) apiLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(refreshCookie); err == nil && c.Value != "" {
		_ = h.st.DeleteRefreshToken(hashToken(c.Value))
	}
	h.clearRefreshCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// apiMe returns the authenticated user.
func (h *handlers) apiMe(w http.ResponseWriter, r *http.Request) {
	uid, _ := r.Context().Value(userIDKey{}).(string)
	user, err := h.st.GetUser(uid)
	if err != nil {
		writeNotFoundOr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserDTO(user))
}

// ---- PAT endpoints (web-session-only) ----

type patDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at"` // RFC3339, or "" if never used
}

func toPATDTO(p store.PAT) patDTO {
	last := ""
	if !p.LastUsedAt.IsZero() {
		last = p.LastUsedAt.UTC().Format(time.RFC3339)
	}
	return patDTO{ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339), LastUsedAt: last}
}

func (h *handlers) apiListPATs(w http.ResponseWriter, r *http.Request) {
	uid, _ := r.Context().Value(userIDKey{}).(string)
	pats, err := h.st.ListPATs(uid)
	if err != nil {
		writeServerError(w, err)
		return
	}
	out := make([]patDTO, 0, len(pats))
	for _, p := range pats {
		out = append(out, toPATDTO(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// apiCreatePAT mints a PAT and returns it once (the only time the raw token is
// ever exposed). The caller must store it; only its hash is kept server-side.
func (h *handlers) apiCreatePAT(w http.ResponseWriter, r *http.Request) {
	uid, _ := r.Context().Value(userIDKey{}).(string)
	var in struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "unnamed"
	}
	token, hash := newOpaqueToken(patPrefix)
	pat, err := h.st.CreatePAT(uid, name, hash)
	if err != nil {
		writeServerError(w, err)
		return
	}
	dto := toPATDTO(pat)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         dto.ID,
		"name":       dto.Name,
		"created_at": dto.CreatedAt,
		"token":      token,
	})
}

func (h *handlers) apiDeletePAT(w http.ResponseWriter, r *http.Request) {
	uid, _ := r.Context().Value(userIDKey{}).(string)
	if err := h.st.DeletePAT(uid, r.PathValue("id")); err != nil {
		writeNotFoundOr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
