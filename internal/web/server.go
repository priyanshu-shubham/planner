// Package web is the planner server. It owns the SQLite database and exposes a
// small JSON API under /api that BOTH clients use: the React single-page app
// (the human interface) and the planner CLI (the agent interface). Because the
// server is the sole owner of the database, there is one consistent data path
// and no cross-process file contention.
package web

import (
	"compress/gzip"
	"embed"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"planner/internal/store"
)

//go:embed static/*
var staticFS embed.FS

//go:embed index.html
var indexHTML []byte

//go:embed setup.md
var setupMD []byte

// Serve starts the HTTP server and blocks until it stops. cfg.Auth toggles the
// optional Google-login layer; when nil the server runs unauthenticated and
// every request is unscoped (the single-user default).
func Serve(st store.Store, addr string, cfg Config) error {
	h, err := newHandler(st, cfg)
	if err != nil {
		return err
	}
	return http.ListenAndServe(addr, h)
}

// newHandler builds the server's HTTP handler (route table + auth wiring). Serve
// wraps it with ListenAndServe; tests drive it directly via httptest.
func newHandler(st store.Store, cfg Config) (http.Handler, error) {
	h := &handlers{st: st, cfg: cfg}
	mux := http.NewServeMux()

	// api registers a data endpoint, gating it behind Bearer auth when auth is on
	// (and leaving it fully open otherwise — the no-auth regression path).
	api := func(pattern string, fn http.HandlerFunc) {
		if h.authEnabled() {
			mux.Handle(pattern, h.requireAuth(fn))
		} else {
			mux.HandleFunc(pattern, fn)
		}
	}

	// JSON API (shared by the CLI and the React app).
	api("GET /api/plans", h.apiListPlans)
	api("POST /api/plans", h.apiCreatePlan)
	api("GET /api/plans/{id}", h.apiPlanMeta)
	api("DELETE /api/plans/{id}", h.apiDeletePlan)
	api("POST /api/plans/{id}/status", h.apiSetPlanStatus)
	api("POST /api/plans/{id}/project", h.apiSetPlanProject)
	api("POST /api/plans/{id}/versions", h.apiAddVersion)
	api("GET /api/plans/{id}/v/{n}", h.apiVersionView)
	api("POST /api/plans/{id}/v/{n}/comments", h.apiAddComment)
	api("POST /api/comments/{id}/resolve", h.apiResolveComment)
	api("POST /api/comments/{id}/reopen", h.apiReopenComment)
	api("POST /api/comments/{id}/keep", h.apiKeepComment)
	api("POST /api/comments/{id}/replies", h.apiAddReply)
	api("DELETE /api/comments/{id}", h.apiDeleteComment)
	api("GET /api/files/{sha}", h.apiFile) // referenced-file content, by sha

	// Always-public config probe (the SPA reads it before deciding to log in).
	mux.HandleFunc("GET /api/config", h.apiConfig)

	if h.authEnabled() {
		// Cookie-authenticated session endpoints (they read the refresh cookie, not
		// a Bearer token, so they are not behind requireAuth).
		mux.HandleFunc("POST /api/auth/refresh", h.apiRefresh)
		mux.HandleFunc("POST /api/auth/logout", h.apiLogout)
		api("GET /api/me", h.apiMe)

		// PAT management is web-session-only: a PAT can neither mint nor list tokens.
		mux.Handle("GET /api/pats", h.requireWebUser(h.apiListPATs))
		mux.Handle("POST /api/pats", h.requireWebUser(h.apiCreatePAT))
		mux.Handle("DELETE /api/pats/{id}", h.requireWebUser(h.apiDeletePAT))

		// Google OAuth handshake.
		mux.HandleFunc("GET /auth/google/login", h.oauthLogin)
		mux.HandleFunc("GET /auth/google/callback", h.oauthCallback)
	}

	mux.HandleFunc("GET /api/", apiNotFound) // unknown /api GET -> JSON 404, not the SPA shell

	// Static assets (the built React bundle).
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// Agent-facing setup instructions (fetched by Claude Code et al).
	mux.HandleFunc("GET /setup.md", h.serveSetup)

	// Version-matched CLI downloads (public, like setup.md — an agent needs the
	// binary before it can authenticate).
	mux.HandleFunc("GET /cli/{platform}", h.serveCLI)

	// Everything else serves the SPA shell; React Router handles the path.
	mux.HandleFunc("GET /", h.serveIndex)

	return mux, nil
}

func (h *handlers) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

// serveSetup returns the agent setup instructions with the server's actual base
// URL substituted in and the auth-mode-specific blocks resolved, so the commands
// and links point at this running server and match whether login is required.
func (h *handlers) serveSetup(w http.ResponseWriter, r *http.Request) {
	base := "http://" + r.Host
	if isSecure(r) {
		base = "https://" + r.Host
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Write([]byte(renderSetup(string(setupMD), base, h.authEnabled())))
}

// renderSetup substitutes {{BASE}} and keeps or strips the {{IF_AUTH}}…{{END_AUTH}}
// and {{IF_NOAUTH}}…{{END_NOAUTH}} blocks according to the server's auth mode,
// then collapses any blank-line runs the removed blocks left behind.
func renderSetup(md, base string, authOn bool) string {
	md = applyBlock(md, "IF_AUTH", "END_AUTH", authOn)
	md = applyBlock(md, "IF_NOAUTH", "END_NOAUTH", !authOn)
	md = strings.ReplaceAll(md, "{{BASE}}", base)
	for strings.Contains(md, "\n\n\n") {
		md = strings.ReplaceAll(md, "\n\n\n", "\n\n")
	}
	return strings.TrimLeft(md, "\n")
}

// applyBlock keeps the content between each {{begin}}/{{end}} marker (dropping the
// markers) when keep is true, or removes the whole block when keep is false.
func applyBlock(s, begin, end string, keep bool) string {
	bm, em := "{{"+begin+"}}", "{{"+end+"}}"
	for {
		i := strings.Index(s, bm)
		if i < 0 {
			break
		}
		j := strings.Index(s, em)
		if j < 0 || j < i {
			break
		}
		inner := strings.Trim(s[i+len(bm):j], "\n")
		after := s[j+len(em):]
		if keep {
			s = s[:i] + inner + after
		} else {
			s = s[:i] + after
		}
	}
	return s
}

// cliPlatforms is the allowlist of distributable builds, mapping the {platform}
// path value to the binary's filename under Config.CLIDir. Validating against it
// (rather than joining the path value) closes any traversal surface.
var cliPlatforms = map[string]string{
	"linux-amd64":   "planner-linux-amd64",
	"linux-arm64":   "planner-linux-arm64",
	"darwin-amd64":  "planner-darwin-amd64",
	"darwin-arm64":  "planner-darwin-arm64",
	"windows-amd64": "planner-windows-amd64.exe",
	"windows-arm64": "planner-windows-arm64.exe",
}

// serveCLI streams the cross-compiled planner binary for one platform, so agents
// install a CLI that exactly matches the server they will talk to. Binaries are
// stored gzipped (see `make cli-dist`): clients that accept gzip get the
// compressed bytes as-is, everyone else gets them decompressed on the fly, so
// correctness never depends on the client. A server run without a CLI directory
// (e.g. a local `planner serve`) reports that it does not distribute the CLI;
// setup.md then falls back to asking the human.
func (h *handlers) serveCLI(w http.ResponseWriter, r *http.Request) {
	file, ok := cliPlatforms[r.PathValue("platform")]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "unknown platform — want one of: linux-amd64, linux-arm64, darwin-amd64, darwin-arm64, windows-amd64, windows-arm64")
		return
	}
	const notDistributed = "this server does not distribute the CLI — ask the human to install it"
	if h.cfg.CLIDir == "" {
		writeJSONError(w, http.StatusNotFound, notDistributed)
		return
	}
	name := "planner"
	if strings.HasSuffix(file, ".exe") {
		name = "planner.exe"
	}
	download := func() {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}

	if gzPath := filepath.Join(h.cfg.CLIDir, file+".gz"); fileExists(gzPath) {
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			download()
			w.Header().Set("Content-Encoding", "gzip")
			http.ServeFile(w, r, gzPath) // keeps our pre-set Content-Type
			return
		}
		f, err := os.Open(gzPath)
		if err != nil {
			writeServerError(w, err)
			return
		}
		defer f.Close()
		zr, err := gzip.NewReader(f)
		if err != nil {
			writeServerError(w, err)
			return
		}
		defer zr.Close()
		download()
		if _, err := io.Copy(w, zr); err != nil {
			log.Printf("stream CLI %s: %v", file, err) // response already started
		}
		return
	}

	// Uncompressed fallback: a hand-populated $PLANNER_CLI_DIR.
	path := filepath.Join(h.cfg.CLIDir, file)
	if !fileExists(path) {
		writeJSONError(w, http.StatusNotFound, notDistributed)
		return
	}
	download()
	http.ServeFile(w, r, path)
}

// fileExists reports whether path exists as a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func apiNotFound(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusNotFound, "no such endpoint")
}
