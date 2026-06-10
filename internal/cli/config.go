package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// config is the CLI's saved connection settings, owned by `planner setup` and
// stored at ~/.planner/config.json. token is empty against a no-auth server.
type config struct {
	Server  string `json:"server"`            // normalized base URL
	Token   string `json:"token,omitempty"`   // PAT for authed servers
	Machine string `json:"machine,omitempty"` // the name this machine's PAT was minted under
}

// configPath returns the path to the CLI config file (~/.planner/config.json).
func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".planner", "config.json"), nil
}

// loadConfig reads the saved config. A missing file returns (nil, nil): the CLI
// then falls back to its zero-config defaults.
func loadConfig() (*config, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &c, nil
}

// saveConfig writes the config with restrictive permissions (dir 0700, file
// 0600) since it can hold a long-lived token.
func saveConfig(c *config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// normalizeServer canonicalizes a server URL so a config entry and an env/CLI
// value compare equal: it lowercases the scheme and host, drops any query or
// fragment, and trims a trailing slash. A sub-path is preserved (path is
// case-sensitive), so a server mounted under e.g. /planner can still be targeted.
// Bare hosts are assumed http. Returns "" if the input has no host.
func normalizeServer(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.Path, "/")
}
