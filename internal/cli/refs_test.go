package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSnapshotFiles covers path detection across the four token shapes, the
// whole-file snapshot under the size cap, the oversize skip, bare-path handling,
// rejection of non-existent and root-escaping paths, dedupe by path, and the
// binary skip.
func TestSnapshotFiles(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("internal/web/handlers.go", "package web\n")
	write("go.mod", "module example\n")
	write("cli.go", "package cli\n")
	write("Makefile", "all:\n\techo hi\n")                 // known extensionless basename
	write("Dockerfile", "FROM scratch\n")                  // known extensionless basename
	write("scripts/deploy", "#!/bin/sh\necho deploy\n")    // path-separated extensionless
	write("big.go", strings.Repeat("x", maxSnapshotBytes)) // at the cap → skipped
	write("bin.dat", "ok\x00nul")                          // binary → skipped

	// One token of each shape, plus extensionless forms, a bare path, a dupe, an
	// oversize, a binary, a missing file, and an escape attempt. "license"
	// (lowercase) must NOT match — the known list is case-sensitive.
	content := strings.Join([]string{
		"see internal/web/handlers.go:120 and cli.go:293-310",
		"multi internal/web/handlers.go:51-61, 176-222 (dupe path)",
		"bare go.mod here",
		"the Makefile and Makefile:1 and build Dockerfile",
		"run scripts/deploy:2 to ship",
		"prose license should not match",
		"big big.go:1 should skip",
		"binary bin.dat:1 should skip",
		"missing does/not/exist.go:1",
		"escape ../outside.go:1",
	}, "\n")

	snaps := snapshotFiles(root, content)
	got := map[string]string{}
	for _, s := range snaps {
		if _, dup := got[s.Path]; dup {
			t.Fatalf("path %q snapshotted twice", s.Path)
		}
		got[s.Path] = s.Content
	}

	// handlers.go (detected with line specs and a duplicate), cli.go (range),
	// go.mod (bare), the known extensionless Makefile/Dockerfile, and the
	// path-separated extensionless scripts/deploy are captured exactly once each.
	want := []string{
		"internal/web/handlers.go", "cli.go", "go.mod",
		"Makefile", "Dockerfile", "scripts/deploy",
	}
	for _, p := range want {
		if _, ok := got[p]; !ok {
			t.Errorf("expected snapshot for %q, missing", p)
		}
	}
	if got["go.mod"] != "module example\n" {
		t.Errorf("go.mod content mismatch: %q", got["go.mod"])
	}
	if len(got) != len(want) {
		t.Errorf("want %d snapshots, got %d: %v", len(want), len(got), keys(got))
	}

	// Skips: oversize, binary, missing, escape, and the lowercase prose "license"
	// (not in the case-sensitive known list, so it never even resolves).
	for _, p := range []string{"big.go", "bin.dat", "does/not/exist.go", "../outside.go", "license"} {
		if _, ok := got[p]; ok {
			t.Errorf("path %q should have been skipped", p)
		}
	}

	// Language detection.
	for _, s := range snaps {
		if s.Path == "cli.go" && s.Language != "go" {
			t.Errorf("cli.go language: want go, got %q", s.Language)
		}
		if s.Path == "go.mod" && s.Language != "" {
			t.Errorf("go.mod language: want empty, got %q", s.Language)
		}
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
