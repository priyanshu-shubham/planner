package web

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeCLI populates a temp CLI directory with gzipped fake binaries (the
// `make cli-dist` layout) for the given platform filenames and returns it. The
// uncompressed content of each is "fake-binary:<name>".
func writeFakeCLI(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		zw.Write([]byte("fake-binary:" + n))
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, n+".gz"), buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// getCLI requests /cli/{platform} with an explicit Accept-Encoding (setting the
// header manually disables Go's transparent decompression, so tests see the wire
// bytes and the Content-Encoding header as sent).
func getCLI(t *testing.T, srv *httptest.Server, platform, acceptEncoding string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest("GET", srv.URL+"/cli/"+platform, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", acceptEncoding)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, body
}

// gunzip decompresses b or fails the test.
func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestServeCLI covers the download endpoint: gzip negotiation both ways, windows
// naming, the platform allowlist, and the no-distribution fallbacks.
func TestServeCLI(t *testing.T) {
	dir := writeFakeCLI(t, "planner-linux-amd64", "planner-windows-amd64.exe")
	srv, _ := newTestServer(t, Config{CLIDir: dir})

	// gzip-capable client: pre-compressed bytes passed through as-is.
	resp, body := getCLI(t, srv, "linux-amd64", "gzip")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("linux-amd64 = %d, want 200", resp.StatusCode)
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", ce)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="planner"`) {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	if got := gunzip(t, body); string(got) != "fake-binary:planner-linux-amd64" {
		t.Fatalf("decompressed body = %q", got)
	}

	// Client that does not accept gzip: decompressed on the fly.
	resp, body = getCLI(t, srv, "linux-amd64", "identity")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("identity linux-amd64 = %d, want 200", resp.StatusCode)
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "" {
		t.Fatalf("identity Content-Encoding = %q, want none", ce)
	}
	if string(body) != "fake-binary:planner-linux-amd64" {
		t.Fatalf("identity body = %q", body)
	}

	// Windows build maps to the .exe artifact and download name.
	resp, _ = getCLI(t, srv, "windows-amd64", "gzip")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("windows-amd64 = %d, want 200", resp.StatusCode)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="planner.exe"`) {
		t.Fatalf("windows Content-Disposition = %q", cd)
	}

	// Unknown platform: rejected by the allowlist (no path is ever joined).
	resp, _ = getCLI(t, srv, "plan9-mips", "gzip")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown platform = %d, want 404", resp.StatusCode)
	}

	// Allowlisted platform whose artifact is absent from the dir.
	resp, _ = getCLI(t, srv, "darwin-arm64", "gzip")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing binary = %d, want 404", resp.StatusCode)
	}
}

// TestServeCLIUncompressedFallback: a hand-populated CLI dir holding a raw (not
// gzipped) binary is still served.
func TestServeCLIUncompressedFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "planner-linux-amd64"), []byte("raw-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv, _ := newTestServer(t, Config{CLIDir: dir})

	resp, body := getCLI(t, srv, "linux-amd64", "gzip")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("raw fallback = %d, want 200", resp.StatusCode)
	}
	if string(body) != "raw-binary" {
		t.Fatalf("raw fallback body = %q", body)
	}
}

// TestServeCLINoDir: a server without a CLI directory reports it does not
// distribute the CLI.
func TestServeCLINoDir(t *testing.T) {
	srv, _ := newTestServer(t, Config{})
	resp := do(t, srv, "GET", "/cli/linux-amd64", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("no dir = %d, want 404", resp.StatusCode)
	}
	var e struct {
		Error string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&e)
	if !strings.Contains(e.Error, "does not distribute") {
		t.Fatalf("error = %q, want friendly no-distribution message", e.Error)
	}
}

// TestServeCLIPublicInAuthMode: the download must work without credentials — an
// agent needs the binary before it can authenticate.
func TestServeCLIPublicInAuthMode(t *testing.T) {
	dir := writeFakeCLI(t, "planner-linux-amd64")
	cfg := authCfg()
	cfg.CLIDir = dir
	srv, _ := newTestServer(t, cfg)

	resp := do(t, srv, "GET", "/cli/linux-amd64", "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unauthenticated CLI download in auth mode = %d, want 200", resp.StatusCode)
	}
}

// TestPlanPostBodyCap: plan posts accept bodies past the ordinary 1 MiB cap (file
// snapshots), while other endpoints keep the small cap.
func TestPlanPostBodyCap(t *testing.T) {
	srv, _ := newTestServer(t, Config{})
	big := strings.Repeat("x", 2<<20) // 2 MiB payload: over 1 MiB, well under 8 MiB

	// Create with a large snapshot succeeds.
	resp := do(t, srv, "POST", "/api/plans", "", map[string]any{
		"title":   "big",
		"content": "see web/big.txt",
		"files":   []map[string]string{{"path": "web/big.txt", "language": "", "content": big}},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("large create = %d, want 201", resp.StatusCode)
	}

	// A non-plan endpoint still enforces the 1 MiB cap: the decoder hits the
	// limit mid-body and the request fails as invalid JSON.
	resp = do(t, srv, "POST", "/api/plans", "", map[string]any{"title": "t", "content": "c"})
	var created struct {
		PlanID string `json:"plan_id"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	resp = do(t, srv, "POST", "/api/plans/"+created.PlanID+"/v/1/comments", "", map[string]any{
		"line_start": 0, "line_end": 0, "quote": "", "body": big,
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized comment = %d, want 400", resp.StatusCode)
	}
}
