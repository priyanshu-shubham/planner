// Package web is the planner server. It owns the SQLite database and exposes a
// small JSON API under /api that BOTH clients use: the React single-page app
// (the human interface) and the planner CLI (the agent interface). Because the
// server is the sole owner of the database, there is one consistent data path
// and no cross-process file contention.
package web

import (
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
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
	// Comment/reply actions live under the plan path: {id} (a plan or share id)
	// is the access credential each request is authorized against, and {cid} is
	// the short comment id the wire uses (see fullCommentID).
	api("POST /api/plans/{id}/comments/{cid}/resolve", h.apiResolveComment)
	api("POST /api/plans/{id}/comments/{cid}/reopen", h.apiReopenComment)
	api("POST /api/plans/{id}/comments/{cid}/keep", h.apiKeepComment)
	api("POST /api/plans/{id}/comments/{cid}/replies", h.apiAddReply)
	api("DELETE /api/plans/{id}/comments/{cid}", h.apiDeleteComment)
	api("DELETE /api/plans/{id}/comments/{cid}/replies/{rid}", h.apiDeleteReply)
	api("POST /api/plans/{id}/share", h.apiCreateShare)
	api("DELETE /api/plans/{id}/share", h.apiRevokeShare)
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
	mux.Handle("GET /static/", staticHandler(staticSub))

	// Agent-facing setup instructions (fetched by Claude Code et al).
	mux.HandleFunc("GET /setup.md", h.serveSetup)

	// Version-matched CLI downloads (public, like setup.md — an agent needs the
	// binary before it can authenticate).
	mux.HandleFunc("GET /cli/{platform}", h.serveCLI)

	// Everything else serves the SPA shell; React Router handles the path.
	mux.HandleFunc("GET /", h.serveIndex)

	// Compress responses for clients that accept it. The React bundle is ~3.5 MB
	// uncompressed (~1 MB gzipped); over a high-latency link that transfer, not the
	// server, dominates page load.
	return gzipMiddleware(mux), nil
}

// staticHandler serves the embedded React bundle with caching tuned to esbuild's
// output: content-hashed chunks (chunk-*.js) never change, so they are cached for
// a year as immutable; the stable-named entrypoints (bundle.js/.css) get an ETag
// so a browser revalidates and receives a 304 until the bytes change on a deploy.
// The files are flat under static/, so the leaf name is the path minus "/static/".
func staticHandler(staticSub fs.FS) http.Handler {
	etags := fileETags(staticSub)
	fileServer := http.FileServer(http.FS(staticSub))
	return http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if strings.HasPrefix(name, "chunk-") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
		}
		if et := etags[name]; et != "" {
			// ServeContent (inside FileServer) honors a pre-set ETag for
			// If-None-Match, so revalidation returns 304 with no body.
			w.Header().Set("Etag", et)
		}
		fileServer.ServeHTTP(w, r)
	}))
}

// fileETags precomputes a content ETag for every embedded static file (the bundle
// is fixed at build time, so this runs once at startup).
func fileETags(fsys fs.FS) map[string]string {
	etags := map[string]string{}
	fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(b)
		etags[p] = `"` + hex.EncodeToString(sum[:16]) + `"`
		return nil
	})
	return etags
}

// gzipMiddleware compresses responses for clients that send Accept-Encoding: gzip,
// leaving alone responses a handler already encoded (e.g. /cli/{platform}) and
// incompressible content types. It drops any Range request header first so a
// compressed response is never a partial one.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		r.Header.Del("Range")
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

// gzipResponseWriter defers the compress/passthrough decision until the headers
// are known (Content-Type, any handler-set Content-Encoding, status code).
type gzipResponseWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	decided bool
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	if w.decided {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.decided = true
	h := w.Header()
	h.Add("Vary", "Accept-Encoding")
	if h.Get("Content-Encoding") == "" && bodyAllowedForStatus(status) && compressibleType(h.Get("Content-Type")) {
		h.Del("Content-Length") // the length changes once compressed
		h.Set("Content-Encoding", "gzip")
		w.gz = gzip.NewWriter(w.ResponseWriter)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if !w.decided {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", http.DetectContentType(b))
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.gz != nil {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// Flush lets streaming handlers (if any) push bytes through the gzip writer.
func (w *gzipResponseWriter) Flush() {
	if w.gz != nil {
		w.gz.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *gzipResponseWriter) close() {
	if w.gz != nil {
		w.gz.Close()
	}
}

// compressibleType reports whether a Content-Type is worth gzipping. Already
// compressed binaries (images, the gzipped CLI, octet-streams) are left alone.
func compressibleType(ct string) bool {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	switch strings.TrimSpace(strings.ToLower(ct)) {
	case "application/javascript", "application/json", "application/xml",
		"image/svg+xml":
		return true
	}
	return strings.HasPrefix(ct, "text/")
}

// bodyAllowedForStatus mirrors net/http: 1xx, 204 and 304 carry no body.
func bodyAllowedForStatus(status int) bool {
	switch {
	case status >= 100 && status < 200:
		return false
	case status == http.StatusNoContent, status == http.StatusNotModified:
		return false
	}
	return true
}

func (h *handlers) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

// serveSetup returns the agent setup instructions with the server's actual base
// URL substituted in and the auth-mode-specific blocks resolved, so the commands
// and links point at this running server and match whether login is required.
func (h *handlers) serveSetup(w http.ResponseWriter, r *http.Request) {
	base := h.externalBase(r)
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
