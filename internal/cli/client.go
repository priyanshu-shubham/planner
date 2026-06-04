package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// client is a thin HTTP client for the planner JSON API. The CLI is the agent's
// interface and talks to a running `planner serve` rather than the database
// directly, so the server stays the single owner of the data.
type client struct {
	base string
	http *http.Client
}

func newClient(base string) *client {
	return &client{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 30 * time.Second}}
}

// apiReply mirrors the server's replyDTO.
type apiReply struct {
	ID     string `json:"id"`
	Author string `json:"author"`
	Body   string `json:"body"`
}

// apiComment mirrors the server's commentDTO.
type apiComment struct {
	ID        string     `json:"id"`
	LineStart int        `json:"line_start"`
	LineEnd   int        `json:"line_end"`
	WholeFile bool       `json:"whole_file"`
	Quote     string     `json:"quote"`
	Body      string     `json:"body"`
	Status    string     `json:"status"`
	Replies   []apiReply `json:"replies"`
}

type apiVersionView struct {
	PlanID   string       `json:"plan_id"`
	Title    string       `json:"title"`
	Number   int          `json:"number"`
	Content  string       `json:"content"`
	Versions []int        `json:"versions"`
	Latest   int          `json:"latest"`
	Comments []apiComment `json:"comments"`
}

type apiCreated struct {
	PlanID string `json:"plan_id"`
	Number int    `json:"number"`
}

// do performs a request, JSON-encoding body (if non-nil) and decoding the
// response into out (if non-nil). Non-2xx responses become Go errors carrying
// the server's error message.
func (c *client) do(method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, reqBody)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) || isConnErr(err) {
			return fmt.Errorf("could not reach planner server at %s — is `planner serve` running?", c.base)
		}
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return fmt.Errorf("server: %s (%d)", e.Error, resp.StatusCode)
		}
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func isConnErr(err error) bool {
	var ue *url.Error
	if errors.As(err, &ue) {
		return strings.Contains(ue.Error(), "connection refused") || strings.Contains(ue.Error(), "connect")
	}
	return false
}

// ---- typed endpoint wrappers ----

func (c *client) createPlan(title, content, project string) (apiCreated, error) {
	var out apiCreated
	err := c.do(http.MethodPost, "/api/plans",
		map[string]string{"title": title, "content": content, "project": project}, &out)
	return out, err
}

func (c *client) addVersion(planID, content string) (apiCreated, error) {
	var out apiCreated
	err := c.do(http.MethodPost, "/api/plans/"+planID+"/versions", map[string]string{"content": content}, &out)
	return out, err
}

// versionView fetches a version; pass number<=0 for the latest.
func (c *client) versionView(planID string, number int) (apiVersionView, error) {
	n := "latest"
	if number > 0 {
		n = fmt.Sprintf("%d", number)
	}
	var out apiVersionView
	err := c.do(http.MethodGet, "/api/plans/"+planID+"/v/"+n, nil, &out)
	return out, err
}

// reply posts a reply to a comment as the agent.
func (c *client) reply(commentID, body string) error {
	return c.do(http.MethodPost, "/api/comments/"+commentID+"/replies",
		map[string]string{"author": "agent", "body": body}, nil)
}
