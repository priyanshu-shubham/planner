package cli

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// cmdSetup points the CLI at a planner server, the one way to configure where
// client commands talk to. Against a no-auth server it just records the URL.
// Against an authed server it runs a browser handoff: the SPA mints a PAT named
// after this machine and redirects it back to a one-shot local listener.
func cmdSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	server := fs.String("server", "", "planner server base URL (required)")
	name := fs.String("name", defaultMachineName(), "name for this machine's token")
	noBrowser := fs.Bool("no-browser", false, "print the URL and paste the token instead of opening a browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	base := normalizeServer(*server)
	if base == "" {
		return fmt.Errorf("usage: planner setup --server URL [--name NAME] [--no-browser]")
	}

	cl := newClient(base, "")
	mode, err := cl.serverConfig()
	if err != nil {
		return err
	}
	if mode == "none" {
		// No auth: just remember the server so client commands can drop --server.
		if err := saveConfig(&config{Server: base}); err != nil {
			return err
		}
		fmt.Printf("configured planner CLI for %s (no authentication)\n", base)
		return nil
	}

	token, err := obtainPAT(base, *name, *noBrowser)
	if err != nil {
		return err
	}
	// Verify the token works and learn who we are before saving.
	authed := newClient(base, token)
	email, err := authed.me()
	if err != nil {
		return fmt.Errorf("token did not authenticate: %w", err)
	}
	if err := saveConfig(&config{Server: base, Token: token, Machine: *name}); err != nil {
		return err
	}
	fmt.Printf("authenticated as %s — planner CLI configured for %s\n", email, base)
	return nil
}

// obtainPAT runs the local-listener handoff (the default) or the paste fallback.
//
// The default flow starts a one-shot loopback listener, prints the authorization
// URL, and waits for the browser to redirect the minted token back to it. It does
// not require this process to drive the browser — printing the URL lets a human
// (or an agent relaying it) open the page — so it works when an agent runs setup
// in the background. A GUI browser is opened automatically only when attached to a
// terminal, so an agent never spawns a window. The loopback callback is reachable
// only from the same machine; for a remote box use --no-browser (the paste flow).
func obtainPAT(base, name string, noBrowser bool) (string, error) {
	if noBrowser {
		return headlessPAT(base)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	state := randomToken()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	setupURL := base + "/cli-setup?" + url.Values{
		"redirect": {redirect},
		"state":    {state},
		"name":     {name},
	}.Encode()

	result := make(chan string, 1)
	srv := &http.Server{Handler: callbackHandler(state, result)}
	go srv.Serve(ln)
	defer srv.Close()

	fmt.Printf("To authorize this machine, open this URL in a browser and sign in:\n\n  %s\n\n", setupURL)
	fmt.Println("Waiting for authorization… (Ctrl-C to cancel; pass --no-browser to paste a token instead)")
	// Convenience for a human running this directly — never for an agent with
	// captured stdout, so a background run doesn't pop a window. The printed URL
	// above is the source of truth either way.
	if isTerminal(os.Stdout) {
		_ = openBrowser(setupURL)
	}

	select {
	case token := <-result:
		return token, nil
	case <-time.After(10 * time.Minute):
		return "", fmt.Errorf("timed out waiting for authorization; re-run, or use --no-browser to paste a token")
	}
}

// isTerminal reports whether f is a character device (an interactive terminal),
// used to decide whether auto-opening a browser is appropriate.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// callbackHandler is the one-shot local handler the SPA redirects the token to.
// It checks the state (constant time) and, on a match, forwards the token.
func callbackHandler(state string, result chan<- string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		token := q.Get("token")
		if token == "" {
			http.Error(w, "missing token", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!doctype html><title>planner</title><body style=\"font-family:system-ui;padding:2rem\">"+
			"<h2>planner CLI authorized</h2><p>You can close this tab and return to your terminal.</p></body>")
		select {
		case result <- token:
		default:
		}
	}
}

// headlessPAT prints the setup URL and reads a pasted token from stdin — the
// fallback when no browser is available (--no-browser, a launch failure, or a
// timeout).
func headlessPAT(base string) (string, error) {
	fmt.Printf("Open this URL in a browser, authorize, and copy the token:\n  %s/cli-setup\n", base)
	fmt.Print("Paste token: ")
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return "", fmt.Errorf("no token entered")
	}
	token := strings.TrimSpace(sc.Text())
	if token == "" {
		return "", fmt.Errorf("no token entered")
	}
	return token, nil
}

// defaultMachineName labels this machine's token, defaulting to its hostname.
func defaultMachineName() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "cli"
}

// randomToken returns a short URL-safe random string for the callback state.
func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hex[c>>4]
		out[i*2+1] = hex[c&0x0f]
	}
	return string(out)
}

// openBrowser launches the platform's default browser at url.
func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	return exec.Command(cmd, append(args, url)...).Start()
}
