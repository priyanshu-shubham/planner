package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"planner/internal/store"
)

// maxSnapshotBytes is the size ceiling for a captured file. Files at or above it
// are skipped entirely (their reference tokens stay plain text), which also keeps
// any single Firestore blob doc well under the 1 MiB limit.
const maxSnapshotBytes = 50 * 1024

// knownBasenames are extensionless filenames common enough to treat as refs even
// though they have no extension to match on. Case-sensitive on purpose, so prose
// words like "license" or "todo" don't match.
const knownBasenames = `Makefile|makefile|GNUmakefile|Dockerfile|Containerfile|Jenkinsfile|Vagrantfile|Procfile|Gemfile|Rakefile|Brewfile|Justfile|Caddyfile|LICENSE|LICENCE|README|CHANGELOG|NOTICE|AUTHORS|CONTRIBUTORS|CODEOWNERS|COPYING|INSTALL|TODO`

// refLineSpec matches an optional trailing line spec: `:120`, `:120-140`, or
// comma groups `:51-61, 176-222`. The ranges are discarded by the CLI (only the
// path drives storage); the frontend re-parses them for display.
const refLineSpec = `:\d+(?:-\d+)?(?:,\s*\d+(?:-\d+)?)*`

// refToken matches a `file:line`-style code reference in plan markdown. The path
// is captured in group 1 (forms that stand on their own) or group 3 (a bare
// extensionless token, which only counts when a line spec follows so prose words
// don't match). The accepted path forms are:
//   - anything ending in an extension          (internal/web/handlers.go)
//   - a known extensionless basename           (Makefile, build/Dockerfile)
//   - a path-separated extensionless token     (scripts/deploy)
//   - a bare extensionless token + line spec   (deploy:12)
//
// This pattern is the Go twin of REF_RE/detectRefs() in the frontend's
// markdown.js; keep the two in sync. Over-matching of path-like prose is expected
// and absorbed by the snapshot-presence filter (a token whose file wasn't
// snapshotted never decorates).
var refToken = regexp.MustCompile(
	`(` +
		`[\w./-]+\.\w+` + // with an extension
		`|(?:[\w.-]+/)*(?:` + knownBasenames + `)\b` + // known extensionless basename, optional dir prefix
		`|[\w.-]+(?:/[\w.-]+)+` + // path-separated extensionless
		`)(` + refLineSpec + `)?` +
		`|(` + `[\w.-]+` + `)(` + refLineSpec + `)`) // bare extensionless, requires a line spec

// snapshotFiles scans plan markdown for code references and returns a deduped
// (by path) snapshot of each referenced file that resolves inside root, is a
// regular text file under maxSnapshotBytes, and reads cleanly. Detection only
// chooses which files to read — line ranges are ignored. Best-effort: a token
// whose file is absent, oversized, binary, escapes root, or errors mid-read is
// simply omitted, so its token renders as plain text.
func snapshotFiles(root, content string) []store.FileSnapshot {
	if root == "" {
		return nil
	}
	var out []store.FileSnapshot
	seen := map[string]bool{}
	for _, m := range refToken.FindAllStringSubmatch(content, -1) {
		path := m[1] // group 1 (standalone forms) or group 3 (bare + line spec)
		if path == "" {
			path = m[3]
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		snap, ok := readSnapshot(root, path)
		if ok {
			out = append(out, snap)
		}
	}
	return out
}

// readSnapshot resolves path against root and, if it is a safe regular text file
// under the size cap, returns its whole-file snapshot.
func readSnapshot(root, path string) (store.FileSnapshot, bool) {
	if filepath.IsAbs(path) {
		return store.FileSnapshot{}, false
	}
	joined := filepath.Join(root, path)
	rel, err := filepath.Rel(root, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return store.FileSnapshot{}, false // escapes the project root
	}
	info, err := os.Stat(joined)
	if err != nil || !info.Mode().IsRegular() || info.Size() >= maxSnapshotBytes {
		return store.FileSnapshot{}, false
	}
	data, err := os.ReadFile(joined)
	if err != nil {
		return store.FileSnapshot{}, false
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return store.FileSnapshot{}, false // binary
	}
	return store.FileSnapshot{Path: path, Language: hljsLanguage(path), Content: string(data)}, true
}

// hljsLanguage maps a file extension to a highlight.js language id, or "" when
// unknown (the frontend then renders the preview without syntax highlighting).
func hljsLanguage(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".py":
		return "python"
	case ".css":
		return "css"
	case ".json":
		return "json"
	case ".md", ".markdown":
		return "markdown"
	case ".sh", ".bash":
		return "bash"
	case ".html", ".htm", ".xml":
		return "xml"
	case ".yml", ".yaml":
		return "yaml"
	case ".sql":
		return "sql"
	case ".rs":
		return "rust"
	case ".java":
		return "java"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".hpp":
		return "cpp"
	case ".rb":
		return "ruby"
	case ".toml":
		return "toml"
	default:
		return ""
	}
}
