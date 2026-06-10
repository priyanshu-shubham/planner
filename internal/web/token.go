package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// patPrefix marks a personal access token, distinguishing it from a web access
// token in the Authorization header (web tokens are base64url.base64url).
const patPrefix = "pln_"

// accessClaims is the payload of a web access token: the user id and an absolute
// expiry. Access tokens are short-lived and held in SPA memory only.
type accessClaims struct {
	UID string `json:"uid"`
	Exp int64  `json:"exp"`
}

// mintAccess returns a signed access token (base64url(claims).base64url(HMAC))
// for uid valid for ttl, plus its expiry as a Unix timestamp.
func mintAccess(secret []byte, uid string, ttl time.Duration) (string, int64) {
	exp := time.Now().Add(ttl).Unix()
	payload, _ := json.Marshal(accessClaims{UID: uid, Exp: exp})
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + signHMAC(secret, body), exp
}

// verifyAccess checks an access token's signature and expiry, returning the
// user id on success. The signature is compared with hmac.Equal (constant time).
func verifyAccess(secret []byte, token string) (string, bool) {
	i := strings.LastIndexByte(token, '.')
	if i < 0 {
		return "", false
	}
	body, sig := token[:i], token[i+1:]
	if !hmac.Equal([]byte(sig), []byte(signHMAC(secret, body))) {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", false
	}
	var c accessClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", false
	}
	if time.Now().Unix() >= c.Exp {
		return "", false
	}
	return c.UID, true
}

// signHMAC returns the base64url HMAC-SHA256 of msg under secret.
func signHMAC(secret []byte, msg string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// newOpaqueToken returns a fresh 32-byte random token (with the given prefix,
// lowercase base32 body) and its SHA-256 hash. Only the hash is ever stored.
func newOpaqueToken(prefix string) (token, hash string) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic("crypto/rand: " + err.Error()) // a failing CSPRNG is unrecoverable
	}
	body := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf[:]))
	token = prefix + body
	return token, hashToken(token)
}

// hashToken returns the lowercase hex SHA-256 of a token — the form persisted for
// refresh tokens and PATs, so a database read never exposes a usable credential.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// randomString returns n bytes of base64url randomness, used for OAuth state and
// PKCE verifiers (values that are compared/forwarded but never stored as hashes).
func randomString(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
