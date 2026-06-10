package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeServer(t *testing.T) {
	cases := map[string]string{
		"http://localhost:8080":          "http://localhost:8080",
		"http://localhost:8080/":         "http://localhost:8080",
		"https://Plan.Example.com/x?y=1": "https://plan.example.com/x", // host lowercased, path kept, query dropped
		"https://host/planner/":          "https://host/planner",       // sub-path preserved
		"localhost:8080":                 "http://localhost:8080",
		"HTTPS://HOST":                   "https://host",
		"  http://h:1  ":                 "http://h:1",
		"":                               "",
	}
	for in, want := range cases {
		if got := normalizeServer(in); got != want {
			t.Errorf("normalizeServer(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfigRoundTripAndPerms(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Absent config loads as (nil, nil).
	if c, err := loadConfig(); err != nil || c != nil {
		t.Fatalf("loadConfig(absent) = %+v, %v; want nil, nil", c, err)
	}

	in := &config{Server: "http://localhost:8080", Token: "pln_secret", Machine: "laptop"}
	if err := saveConfig(in); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfig()
	if err != nil || got == nil || *got != *in {
		t.Fatalf("round-trip = %+v, %v; want %+v", got, err, in)
	}

	// File is 0600, dir is 0700 — it can hold a long-lived token.
	path, _ := configPath()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("config file perm = %o, want 600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Errorf("config dir perm = %o, want 700", di.Mode().Perm())
	}
}

func TestResolveClientPrecedence(t *testing.T) {
	t.Run("default when nothing set", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("PLANNER_SERVER", "")
		cl, err := resolveClient()
		if err != nil {
			t.Fatal(err)
		}
		if cl.base != defaultServer || cl.token != "" {
			t.Fatalf("default = %q/%q, want %q/empty", cl.base, cl.token, defaultServer)
		}
	})

	t.Run("config when no env", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("PLANNER_SERVER", "")
		saveConfig(&config{Server: "https://planner.example.com", Token: "pln_tok"})
		cl, _ := resolveClient()
		if cl.base != "https://planner.example.com" || cl.token != "pln_tok" {
			t.Fatalf("config = %q/%q", cl.base, cl.token)
		}
	})

	t.Run("env overrides server; keeps token when same server", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		saveConfig(&config{Server: "https://planner.example.com", Token: "pln_tok"})
		// Env names the same server (differently cased / trailing slash) → token kept.
		t.Setenv("PLANNER_SERVER", "https://Planner.Example.com/")
		cl, _ := resolveClient()
		if cl.base != "https://planner.example.com" || cl.token != "pln_tok" {
			t.Fatalf("env-same = %q/%q", cl.base, cl.token)
		}
	})

	t.Run("env overrides server; drops token for a different server", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		saveConfig(&config{Server: "https://planner.example.com", Token: "pln_tok"})
		t.Setenv("PLANNER_SERVER", "http://localhost:9999")
		cl, _ := resolveClient()
		if cl.base != "http://localhost:9999" || cl.token != "" {
			t.Fatalf("env-diff = %q/%q, want localhost:9999/empty", cl.base, cl.token)
		}
	})
}
