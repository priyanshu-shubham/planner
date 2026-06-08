package cli

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestNormalizeRemoteURL covers the remote URL forms git emits — https, scp-like
// git@host:path, ssh:// (with and without a port), bare host/path — plus trailing
// slash and .git trimming, so the same repo over any transport maps to one id.
func TestNormalizeRemoteURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://github.com/me/planner.git", "github.com/me/planner"},
		{"https://github.com/me/planner", "github.com/me/planner"},
		{"http://github.com/me/planner.git/", "github.com/me/planner"},
		{"git@github.com:me/planner.git", "github.com/me/planner"},
		{"git@github.com:me/planner", "github.com/me/planner"},
		{"ssh://git@github.com/me/planner.git", "github.com/me/planner"},
		{"ssh://git@github.com:22/me/planner.git", "github.com/me/planner"},
		{"git://github.com/me/planner.git", "github.com/me/planner"},
		{"github.com/me/planner", "github.com/me/planner"},
		{"https://gitlab.example.com/group/sub/repo.git", "gitlab.example.com/group/sub/repo"},
		{"", ""},
		{"  ", ""},
	}
	for _, c := range cases {
		if got := normalizeRemoteURL(c.in); got != c.want {
			t.Errorf("normalizeRemoteURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestResolveProject exercises the precedence end-to-end against a real repo:
// origin wins when present; with no origin a linked worktree resolves to the
// SAME identity as its main checkout (the worktree-collapse this feature is for);
// a non-git dir falls back to its own path.
func TestResolveProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(repo, "init", "-q")
	run(repo, "remote", "add", "origin", "git@github.com:me/widget.git")
	run(repo, "commit", "-q", "--allow-empty", "-m", "init")

	// macOS /tmp is a symlink to /private/tmp; compare resolved paths.
	realRepo, _ := filepath.EvalSymlinks(repo)

	// 1) Origin present -> normalized origin identity.
	t.Chdir(repo)
	if got := resolveProject(); got != "github.com/me/widget" {
		t.Fatalf("with origin: resolveProject() = %q, want github.com/me/widget", got)
	}

	// 2) No origin -> main checkout, and a linked worktree collapses to it.
	run(repo, "remote", "remove", "origin")
	wt := filepath.Join(t.TempDir(), "wt")
	run(repo, "worktree", "add", "-q", "-b", "feature", wt)

	t.Chdir(repo)
	mainID := resolveProject()
	if real, _ := filepath.EvalSymlinks(mainID); real != realRepo {
		t.Fatalf("no origin, main checkout: resolveProject() = %q, want %q", mainID, realRepo)
	}
	t.Chdir(wt)
	if real, _ := filepath.EvalSymlinks(resolveProject()); real != realRepo {
		t.Fatalf("worktree did not collapse to main checkout: got %q, want %q", resolveProject(), realRepo)
	}

	// 3) Non-git dir -> its own path.
	plain := t.TempDir()
	t.Chdir(plain)
	realPlain, _ := filepath.EvalSymlinks(plain)
	if real, _ := filepath.EvalSymlinks(resolveProject()); real != realPlain {
		t.Fatalf("non-git dir: resolveProject() = %q, want %q", resolveProject(), realPlain)
	}
}
