// Package web is the planner server. It owns the SQLite database and exposes a
// small JSON API under /api that BOTH clients use: the React single-page app
// (the human interface) and the planner CLI (the agent interface). Because the
// server is the sole owner of the database, there is one consistent data path
// and no cross-process file contention.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"planner/internal/store"
)

//go:embed static/*
var staticFS embed.FS

//go:embed index.html
var indexHTML []byte

//go:embed setup.md
var setupMD []byte

// Serve starts the HTTP server and blocks until it stops.
func Serve(st store.Store, addr string) error {
	h := &handlers{st: st}
	mux := http.NewServeMux()

	// JSON API (shared by the CLI and the React app).
	mux.HandleFunc("GET /api/plans", h.apiListPlans)
	mux.HandleFunc("POST /api/plans", h.apiCreatePlan)
	mux.HandleFunc("GET /api/plans/{id}", h.apiPlanMeta)
	mux.HandleFunc("DELETE /api/plans/{id}", h.apiDeletePlan)
	mux.HandleFunc("POST /api/plans/{id}/status", h.apiSetPlanStatus)
	mux.HandleFunc("POST /api/plans/{id}/versions", h.apiAddVersion)
	mux.HandleFunc("GET /api/plans/{id}/v/{n}", h.apiVersionView)
	mux.HandleFunc("POST /api/plans/{id}/v/{n}/comments", h.apiAddComment)
	mux.HandleFunc("POST /api/comments/{id}/resolve", h.apiResolveComment)
	mux.HandleFunc("POST /api/comments/{id}/reopen", h.apiReopenComment)
	mux.HandleFunc("POST /api/comments/{id}/keep", h.apiKeepComment)
	mux.HandleFunc("POST /api/comments/{id}/replies", h.apiAddReply)
	mux.HandleFunc("DELETE /api/comments/{id}", h.apiDeleteComment)
	mux.HandleFunc("GET /api/files/{sha}", h.apiFile) // referenced-file content, by sha
	mux.HandleFunc("GET /api/", apiNotFound)          // unknown /api GET -> JSON 404, not the SPA shell

	// Static assets (the built React bundle).
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return err
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// Agent-facing setup instructions (fetched by Claude Code et al).
	mux.HandleFunc("GET /setup.md", h.serveSetup)

	// Everything else serves the SPA shell; React Router handles the path.
	mux.HandleFunc("GET /", h.serveIndex)

	return http.ListenAndServe(addr, mux)
}

func (h *handlers) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

// serveSetup returns the agent setup instructions with the server's actual base
// URL substituted in, so the commands and links point at this running server.
func (h *handlers) serveSetup(w http.ResponseWriter, r *http.Request) {
	base := "http://" + r.Host
	if r.TLS != nil {
		base = "https://" + r.Host
	}
	body := strings.ReplaceAll(string(setupMD), "{{BASE}}", base)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Write([]byte(body))
}

func apiNotFound(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusNotFound, "no such endpoint")
}
