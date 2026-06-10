package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Google OAuth 2.0 endpoints (code flow + PKCE). userinfo is fetched over TLS
// from Google, so the returned profile needs no separate ID-token verification.
const (
	googleAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL    = "https://oauth2.googleapis.com/token"
	googleUserinfoURL = "https://www.googleapis.com/oauth2/v3/userinfo"
	oauthCookie       = "planner_oauth"
	oauthCookieTTL    = 10 * time.Minute
)

// oauthClient is the HTTP client for the token exchange and userinfo calls.
var oauthClient = &http.Client{Timeout: 15 * time.Second}

// oauthState is the short-lived, HMAC-signed cookie payload carrying the CSRF
// state, the PKCE verifier, and the validated post-login redirect target.
type oauthState struct {
	State    string `json:"s"`
	Verifier string `json:"v"`
	Next     string `json:"n"`
}

// callbackURL is the Google redirect URI for this server, derived from the
// incoming request so it matches whatever host/scheme the SPA was reached on.
func callbackURL(r *http.Request) string {
	scheme := "http"
	if isSecure(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/auth/google/callback"
}

// validateNext keeps the post-login redirect same-origin: a path beginning with
// a single "/" and containing no "\". Anything else collapses to "/".
func validateNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		return "/"
	}
	return next
}

// signCookie returns base64url(json).base64url(HMAC) so the value is tamper-evident.
func (h *handlers) signCookie(v any) string {
	raw, _ := json.Marshal(v)
	body := base64.RawURLEncoding.EncodeToString(raw)
	return body + "." + signHMAC(h.cfg.Auth.Secret, body)
}

// readSignedCookie verifies the signature and decodes the payload into dst.
func (h *handlers) readSignedCookie(value string, dst any) bool {
	i := strings.LastIndexByte(value, '.')
	if i < 0 {
		return false
	}
	body, sig := value[:i], value[i+1:]
	if !hmac.Equal([]byte(sig), []byte(signHMAC(h.cfg.Auth.Secret, body))) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return false
	}
	return json.Unmarshal(raw, dst) == nil
}

// oauthLogin starts the Google login: it stores state + PKCE verifier + the
// validated next target in a signed, short-lived cookie and redirects to Google.
func (h *handlers) oauthLogin(w http.ResponseWriter, r *http.Request) {
	st := oauthState{
		State:    randomString(24),
		Verifier: randomString(48),
		Next:     validateNext(r.URL.Query().Get("next")),
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oauthCookie,
		Value:    h.signCookie(st),
		Path:     "/auth",
		HttpOnly: true,
		Secure:   isSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(oauthCookieTTL.Seconds()),
	})

	challenge := base64.RawURLEncoding.EncodeToString(sha256Sum(st.Verifier))
	q := url.Values{
		"client_id":             {h.cfg.Auth.GoogleClientID},
		"redirect_uri":          {callbackURL(r)},
		"response_type":         {"code"},
		"scope":                 {"openid email profile"},
		"state":                 {st.State},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"access_type":           {"online"},
	}
	http.Redirect(w, r, googleAuthURL+"?"+q.Encode(), http.StatusFound)
}

// oauthCallback completes the login: it checks the state (constant time),
// exchanges the code (with the PKCE verifier), fetches the user's Google profile,
// upserts the user, sets the refresh cookie, and redirects to the saved target.
func (h *handlers) oauthCallback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(oauthCookie)
	if err != nil {
		h.oauthFail(w, r, "login expired or cookie missing")
		return
	}
	var st oauthState
	if !h.readSignedCookie(c.Value, &st) {
		h.oauthFail(w, r, "invalid login state")
		return
	}
	// Clear the one-shot cookie regardless of outcome.
	http.SetCookie(w, &http.Cookie{Name: oauthCookie, Path: "/auth", MaxAge: -1, HttpOnly: true})

	q := r.URL.Query()
	if q.Get("error") != "" {
		h.oauthFail(w, r, "Google sign-in was cancelled")
		return
	}
	if !hmac.Equal([]byte(q.Get("state")), []byte(st.State)) {
		h.oauthFail(w, r, "state mismatch")
		return
	}

	tok, err := h.exchangeCode(r, q.Get("code"), st.Verifier)
	if err != nil {
		h.oauthFail(w, r, "token exchange failed")
		return
	}
	info, err := fetchUserinfo(tok)
	if err != nil || info.Sub == "" {
		h.oauthFail(w, r, "could not read Google profile")
		return
	}

	user, err := h.st.UpsertUserByGoogleSub(info.Sub, info.Email, info.Name, info.Picture)
	if err != nil {
		writeServerError(w, err)
		return
	}
	token, hash := newOpaqueToken("")
	if err := h.st.CreateRefreshToken(user.ID, hash, time.Now().Add(refreshTTL)); err != nil {
		writeServerError(w, err)
		return
	}
	h.setRefreshCookie(w, r, token)
	http.Redirect(w, r, validateNext(st.Next), http.StatusFound)
}

// oauthFail redirects back to the SPA login with a short reason, so the user sees
// a message rather than a raw error page.
func (h *handlers) oauthFail(w http.ResponseWriter, r *http.Request, reason string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(reason), http.StatusFound)
}

// exchangeCode trades the authorization code (plus PKCE verifier) for a Google
// access token.
func (h *handlers) exchangeCode(r *http.Request, code, verifier string) (string, error) {
	form := url.Values{
		"code":          {code},
		"client_id":     {h.cfg.Auth.GoogleClientID},
		"client_secret": {h.cfg.Auth.GoogleClientSecret},
		"redirect_uri":  {callbackURL(r)},
		"grant_type":    {"authorization_code"},
		"code_verifier": {verifier},
	}
	resp, err := oauthClient.PostForm(googleTokenURL, form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errBadStatus
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", errBadStatus
	}
	return out.AccessToken, nil
}

// googleUserinfo is the subset of Google's userinfo response we consume.
type googleUserinfo struct {
	Sub     string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

// fetchUserinfo reads the signed-in user's profile from Google's userinfo
// endpoint using the access token from the exchange.
func fetchUserinfo(accessToken string) (googleUserinfo, error) {
	req, _ := http.NewRequest(http.MethodGet, googleUserinfoURL, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := oauthClient.Do(req)
	if err != nil {
		return googleUserinfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return googleUserinfo{}, errBadStatus
	}
	var info googleUserinfo
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&info)
	return info, err
}

func sha256Sum(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}
