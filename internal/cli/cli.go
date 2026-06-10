// Package cli implements the planner command line — the agent's interface.
// Every command except "serve" is a thin HTTP client against a running
// `planner serve`; the server owns the database. This keeps a single,
// consistent data path shared with the human-facing React app.
package cli

import (
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"planner/internal/store"
	"planner/internal/web"
)

const usage = `planner — AI/human plan review loop

Usage:
  planner serve    [--port 8080] [--backend sqlite|postgres] [--db PATH_OR_DSN] [--auth]
  planner setup    --server URL [--name NAME] [--no-browser]
  planner create   --title TITLE [--file plan.md]      (reads stdin if no --file)
  planner update   PLAN_ID [--file plan.md]            (reads stdin if no --file)
  planner show     PLAN_ID [--version N] [--json]
  planner comments PLAN_ID [--version N] [--status open|all] [--json]
  planner reply    PLAN_ID COMMENT_ID [-m MESSAGE]     (reads stdin if no -m)

Client commands talk to the server configured by 'planner setup', which resolves
as $PLANNER_SERVER > ~/.planner/config.json > http://localhost:8080. Run
'planner setup --server URL' once to point the CLI at a server (and, when that
server requires Google login, to authorize this machine).

Server only:
  --backend KIND         sqlite (default) or postgres; or $PLANNER_BACKEND
  --db PATH_OR_DSN       sqlite: file path (default ~/.planner/planner.db);
                         postgres: connection string. Or $PLANNER_DB.
  --auth                 require Google login + per-user scoping; or $PLANNER_AUTH.
                         Needs $GOOGLE_CLIENT_ID and $GOOGLE_CLIENT_SECRET
                         (optional $PLANNER_AUTH_SECRET to pin the HMAC key).
  $PORT, if set, overrides --port's default (some hosting platforms set it).
`

// Run dispatches a subcommand. args is os.Args[1:].
func Run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "serve":
		err = cmdServe(rest)
	case "setup":
		err = cmdSetup(rest)
	case "create":
		err = cmdCreate(rest)
	case "update":
		err = cmdUpdate(rest)
	case "show":
		err = cmdShow(rest)
	case "comments":
		err = cmdComments(rest)
	case "reply":
		err = cmdReply(rest)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// ---- shared flag/env helpers ----

func dbPath(fs *flag.FlagSet) *string {
	return fs.String("db", defaultDBPath(), "SQLite database file")
}

// defaultDBPath returns $PLANNER_DB if set, otherwise the built-in SQLite path.
func defaultDBPath() string {
	if v := os.Getenv("PLANNER_DB"); v != "" {
		return v
	}
	return sqliteHomeDefault()
}

// sqliteHomeDefault is the built-in SQLite database path (~/.planner/planner.db),
// used when neither --db nor $PLANNER_DB supplies one. Kept separate from
// defaultDBPath so the postgres backend can recognize "no DSN was provided" even
// when $PLANNER_DB is set (in which case defaultDBPath returns that DSN).
func sqliteHomeDefault() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".planner", "planner.db")
	}
	return "planner.db"
}

// defaultServer is the zero-config server URL used when neither $PLANNER_SERVER
// nor a saved config points elsewhere.
const defaultServer = "http://localhost:8080"

// resolveClient builds the HTTP client for a client command, resolving the server
// URL with precedence $PLANNER_SERVER > config.json > the default, and attaching
// the saved token only when it belongs to the resolved server.
func resolveClient() (*client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	server, token := "", ""
	switch {
	case strings.TrimSpace(os.Getenv("PLANNER_SERVER")) != "":
		server = normalizeServer(os.Getenv("PLANNER_SERVER"))
		// Use the saved token only if the env server is the one we set up.
		if cfg != nil && cfg.Token != "" && normalizeServer(cfg.Server) == server {
			token = cfg.Token
		}
	case cfg != nil && cfg.Server != "":
		server = normalizeServer(cfg.Server)
		token = cfg.Token
	}
	if server == "" {
		server = defaultServer
	}
	return newClient(server, token), nil
}

// workingDir is the absolute directory the CLI runs in. Referenced-file paths in
// a plan are written relative to it, so it is the root snapshotFiles reads from.
// Distinct from resolveProject: the snapshot root is always the real cwd, even
// when the plan's recorded project identity is a git remote or another checkout.
func workingDir() string {
	wd, _ := os.Getwd()
	return wd
}

// resolveProject returns the identity the server records so plans group sensibly
// across a repo's worktrees and clones. Precedence: the origin remote (constant
// per repo); else the current branch's tracking remote (covers repos whose
// canonical remote isn't named "origin"); else the main checkout shared by all
// linked worktrees (collapses worktrees even with no remote); else the bare cwd.
// Remote URLs are normalized to host/owner/repo. Any git signal beats the cwd so
// worktrees of one repo don't split into separate projects.
func resolveProject() string {
	wd := workingDir()
	if url, ok := git(wd, "config", "--get", "remote.origin.url"); ok && url != "" {
		return normalizeRemoteURL(url)
	}
	if branch, ok := git(wd, "branch", "--show-current"); ok && branch != "" {
		if remote, ok := git(wd, "config", "--get", "branch."+branch+".remote"); ok && remote != "" {
			if url, ok := git(wd, "config", "--get", "remote."+remote+".url"); ok && url != "" {
				return normalizeRemoteURL(url)
			}
		}
	}
	if dir, ok := git(wd, "rev-parse", "--git-common-dir"); ok && dir != "" {
		// --git-common-dir is the *shared* .git (the main repo's) for every
		// linked worktree, but it may be relative to wd. Its parent is the main
		// checkout root.
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(wd, dir)
		}
		return filepath.Dir(filepath.Clean(dir))
	}
	return wd
}

// git runs `git -C dir args...` and returns its trimmed stdout. ok is false when
// git is absent, dir isn't a repo, or the command otherwise fails — callers fall
// through to the next identity source.
func git(dir string, args ...string) (string, bool) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// normalizeRemoteURL collapses the assorted git remote URL forms to a stable
// host/owner/repo identity, so the same repo reached over https or ssh (or with
// a trailing slash or .git suffix) maps to one project. Examples:
//
//	https://github.com/me/planner.git    -> github.com/me/planner
//	git@github.com:me/planner.git        -> github.com/me/planner
//	ssh://git@github.com:22/me/planner   -> github.com/me/planner
func normalizeRemoteURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 { // drop scheme (https/ssh/git/...)
		s = s[i+3:]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 { // drop userinfo (e.g. git@)
		s = s[i+1:]
	}
	// Split host from path. Normal URLs use '/', scp-like syntax uses
	// host:owner/repo, and ssh:// may carry host:port — turn the first
	// separator into '/' and drop a numeric port.
	if i := strings.IndexAny(s, ":/"); i >= 0 {
		host, rest := s[:i], s[i+1:]
		if s[i] == ':' {
			if j := strings.IndexByte(rest, '/'); j >= 0 && isAllDigits(rest[:j]) {
				rest = rest[j+1:]
			}
		}
		s = host + "/" + rest
	}
	s = strings.TrimRight(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return strings.TrimRight(s, "/")
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// readContent returns the markdown body from --file or stdin.
func readContent(file string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		return string(b), err
	}
	b, err := io.ReadAll(os.Stdin)
	return string(b), err
}

// takePositional pulls the first non-flag token (a bare PLAN_ID/COMMENT_ID) out
// of args so the remaining flags can be parsed in any order around it.
func takePositional(args []string) (string, []string) {
	for i, a := range args {
		if !strings.HasPrefix(a, "-") {
			rest := append([]string{}, args[:i]...)
			rest = append(rest, args[i+1:]...)
			return a, rest
		}
	}
	return "", args
}

func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ---- serve (the only command that opens the database) ----

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := fs.Int("port", 8080, "port to listen on")
	db := dbPath(fs)
	backend := fs.String("backend", envOr("PLANNER_BACKEND", "sqlite"), "storage backend: sqlite|postgres")
	auth := fs.Bool("auth", os.Getenv("PLANNER_AUTH") != "", "require Google login (per-user scoping)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := authConfig(*auth)
	if err != nil {
		return err
	}
	// Directory of cross-compiled CLI binaries to serve at /cli/{platform} (the
	// Docker image sets it; absent for plain local runs).
	cfg.CLIDir = os.Getenv("PLANNER_CLI_DIR")
	// External origin override (e.g. https://planner.example.com).
	cfg.BaseURL = strings.TrimRight(os.Getenv("PLANNER_BASE_URL"), "/")

	// Port precedence: an explicit --port wins; otherwise $PORT (set by Cloud
	// Run) overrides the built-in default; otherwise the default stands.
	if !flagSet(fs, "port") {
		if p := os.Getenv("PORT"); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil {
				return fmt.Errorf("invalid $PORT %q: %w", p, err)
			}
			*port = n
		}
	}

	st, desc, err := openBackend(*backend, *db)
	if err != nil {
		return err
	}
	defer st.Close()

	authDesc := "no auth"
	if cfg.Auth != nil {
		authDesc = "Google login"
	}
	fmt.Printf("planner serving on http://localhost:%d (%s, %s)\n", *port, desc, authDesc)
	return web.Serve(st, fmt.Sprintf("0.0.0.0:%d", *port), cfg)
}

// authConfig builds the web auth configuration. When auth is off it returns the
// zero Config (no login). When on it requires $GOOGLE_CLIENT_ID and
// $GOOGLE_CLIENT_SECRET; $PLANNER_AUTH_SECRET is the optional HMAC key (a random
// per-start key is used when unset — access tokens then expire on restart and the
// SPA recovers silently via the refresh cookie).
func authConfig(on bool) (web.Config, error) {
	if !on {
		return web.Config{}, nil
	}
	id, secret := os.Getenv("GOOGLE_CLIENT_ID"), os.Getenv("GOOGLE_CLIENT_SECRET")
	if id == "" || secret == "" {
		return web.Config{}, fmt.Errorf("--auth requires $GOOGLE_CLIENT_ID and $GOOGLE_CLIENT_SECRET")
	}
	hmacKey := []byte(os.Getenv("PLANNER_AUTH_SECRET"))
	if len(hmacKey) == 0 {
		hmacKey = randomSecret()
	}
	return web.Config{Auth: &web.AuthConfig{
		GoogleClientID:     id,
		GoogleClientSecret: secret,
		Secret:             hmacKey,
	}}, nil
}

// randomSecret returns a 32-byte random HMAC key for signing access tokens when
// the operator did not pin one via $PLANNER_AUTH_SECRET.
func randomSecret() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return b
}

// openBackend opens the selected store and returns it with a human-readable
// description for the startup banner. For postgres, db carries the connection
// string (the --db flag doubles as the DSN).
func openBackend(backend, db string) (store.Store, string, error) {
	switch backend {
	case "sqlite":
		st, err := store.OpenSQLite(db)
		if err != nil {
			return nil, "", err
		}
		abs, _ := filepath.Abs(db)
		return st, "sqlite: " + abs, nil
	case "postgres":
		// A SQLite file path is meaningless for postgres; require a real DSN. The
		// default --db value is the SQLite home path unless --db or $PLANNER_DB
		// supplied one — compare against that path specifically (not defaultDBPath,
		// which returns the DSN itself when $PLANNER_DB is set).
		if db == "" || db == sqliteHomeDefault() {
			return nil, "", fmt.Errorf("postgres backend requires a connection string via --db or $PLANNER_DB")
		}
		st, err := store.OpenPostgres(db)
		if err != nil {
			return nil, "", err
		}
		return st, "postgres: " + redactDSN(db), nil
	default:
		return nil, "", fmt.Errorf("unknown backend %q (want sqlite or postgres)", backend)
	}
}

// redactDSN masks the password in a postgres connection string for the startup
// banner. It handles the URL form (postgres://user:pw@host/db); keyword DSNs
// (host=... password=...) and other forms are returned unchanged.
func redactDSN(dsn string) string {
	i := strings.Index(dsn, "://")
	if i < 0 {
		return dsn
	}
	rest := dsn[i+3:]
	at := strings.IndexByte(rest, '@')
	if at < 0 {
		return dsn
	}
	userinfo := rest[:at]
	if c := strings.IndexByte(userinfo, ':'); c >= 0 {
		userinfo = userinfo[:c] + ":****"
	}
	return dsn[:i+3] + userinfo + rest[at:]
}

// flagSet reports whether the named flag was explicitly provided on the command
// line (as opposed to taking its default).
func flagSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// envOr returns $key if set and non-empty, else def.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ---- client commands ----

func cmdCreate(args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	title := fs.String("title", "", "plan title (required)")
	file := fs.String("file", "", "markdown file (default: stdin)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *title == "" {
		return fmt.Errorf("--title is required")
	}
	content, err := readContent(*file)
	if err != nil {
		return err
	}
	cl, err := resolveClient()
	if err != nil {
		return err
	}
	project := resolveProject()
	files := snapshotFiles(workingDir(), content)
	created, err := cl.createPlan(*title, content, project, files)
	if err != nil {
		return err
	}
	fmt.Printf("%s\n%s/plans/%s/v/%d\n", created.PlanID, cl.base, created.PlanID, created.Number)
	return nil
}

func cmdUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	file := fs.String("file", "", "markdown file (default: stdin)")
	planID, rest := takePositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if planID == "" {
		return fmt.Errorf("usage: planner update PLAN_ID [--file plan.md]")
	}
	content, err := readContent(*file)
	if err != nil {
		return err
	}
	cl, err := resolveClient()
	if err != nil {
		return err
	}
	files := snapshotFiles(workingDir(), content)
	created, err := cl.addVersion(planID, content, files)
	if err != nil {
		return err
	}
	fmt.Printf("v%d\n%s/plans/%s/v/%d\n", created.Number, cl.base, planID, created.Number)
	return nil
}

func cmdShow(args []string) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	version := fs.Int("version", 0, "version number (default: latest)")
	asJSON := fs.Bool("json", false, "JSON output")
	planID, rest := takePositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if planID == "" {
		return fmt.Errorf("usage: planner show PLAN_ID [--version N] [--json]")
	}
	cl, err := resolveClient()
	if err != nil {
		return err
	}
	view, err := cl.versionView(planID, *version)
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(map[string]any{"plan_id": view.PlanID, "version": view.Number, "content": view.Content})
	}
	fmt.Print(view.Content)
	if !strings.HasSuffix(view.Content, "\n") {
		fmt.Println()
	}
	return nil
}

func cmdComments(args []string) error {
	fs := flag.NewFlagSet("comments", flag.ContinueOnError)
	version := fs.Int("version", 0, "version number (default: latest)")
	status := fs.String("status", "open", "open|all")
	asJSON := fs.Bool("json", false, "JSON output")
	planID, rest := takePositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if planID == "" {
		return fmt.Errorf("usage: planner comments PLAN_ID [--version N] [--status open|all] [--json]")
	}
	cl, err := resolveClient()
	if err != nil {
		return err
	}
	view, err := cl.versionView(planID, *version)
	if err != nil {
		return err
	}
	openOnly := *status != "all"
	var cs []apiComment
	for _, c := range view.Comments {
		if openOnly && c.Status != store.StatusOpen {
			continue
		}
		cs = append(cs, c)
	}

	if *asJSON {
		return writeJSON(map[string]any{"plan_id": planID, "version": view.Number, "comments": cs})
	}
	if len(cs) == 0 {
		fmt.Printf("no %scomments on v%d\n", statusLabel(*status), view.Number)
		return nil
	}
	fmt.Printf("comments on v%d:\n", view.Number)
	for _, c := range cs {
		by := ""
		if c.AuthorName != "" {
			by = " — " + c.AuthorName
		}
		fmt.Printf("  [%s] (%s) %s%s\n", c.Status, commentLoc(c), c.ID, by)
		if c.Quote != "" {
			fmt.Printf("      > %s\n", truncate(c.Quote, 80))
		}
		fmt.Printf("      %s\n", indentBody(c.Body))
		for _, rep := range c.Replies {
			fmt.Printf("      ↳ %s: %s\n", replyAuthor(rep), indentReply(rep.Body))
		}
	}
	return nil
}

func cmdReply(args []string) error {
	fs := flag.NewFlagSet("reply", flag.ContinueOnError)
	msg := fs.String("m", "", "reply message (default: stdin)")
	planID, rest := takePositional(args)
	commentID, rest := takePositional(rest)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if planID == "" || commentID == "" {
		return fmt.Errorf("usage: planner reply PLAN_ID COMMENT_ID [-m MESSAGE]")
	}
	body := strings.TrimSpace(*msg)
	if body == "" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		body = strings.TrimSpace(string(b))
	}
	if body == "" {
		return fmt.Errorf("reply message is empty")
	}
	cl, err := resolveClient()
	if err != nil {
		return err
	}
	if err := cl.reply(planID, commentID, body); err != nil {
		return err
	}
	fmt.Printf("replied to %s\n", commentID)
	return nil
}

// ---- formatting helpers ----

func commentLoc(c apiComment) string {
	if c.WholeFile {
		return "whole file"
	}
	if c.LineStart == c.LineEnd {
		return fmt.Sprintf("line %d", c.LineStart)
	}
	return fmt.Sprintf("lines %d-%d", c.LineStart, c.LineEnd)
}

func statusLabel(s string) string {
	if s == "all" {
		return ""
	}
	return s + " "
}

func indentBody(b string) string {
	return strings.ReplaceAll(strings.TrimRight(b, "\n"), "\n", "\n      ")
}

// indentReply aligns a reply's continuation lines under its first line, past the
// "      ↳ author: " prefix.
func indentReply(b string) string {
	return strings.ReplaceAll(strings.TrimRight(b, "\n"), "\n", "\n        ")
}

// replyAuthor labels a reply with its role and, when attributed, the user's
// name — e.g. `human (Jane)`.
func replyAuthor(r apiReply) string {
	if r.AuthorName != "" {
		return r.Author + " (" + r.AuthorName + ")"
	}
	return r.Author
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
