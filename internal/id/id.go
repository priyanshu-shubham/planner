// Package id generates short, URL-safe, prefixed identifiers.
package id

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

// lowercase base32 alphabet without padding, dropping easily-confused chars is
// not necessary here since the values are machine-generated and never typed.
var enc = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// New returns an id like "pl_a1b2c3d4" using the given prefix.
func New(prefix string) string {
	b := make([]byte, 5) // 5 bytes -> 8 base32 chars
	if _, err := rand.Read(b); err != nil {
		panic("id: crypto/rand failed: " + err.Error())
	}
	return prefix + "_" + strings.ToLower(enc.EncodeToString(b))
}
