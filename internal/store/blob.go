package store

import (
	"crypto/sha256"
	"encoding/hex"
)

// fileSHA returns the lowercase hex SHA-256 of a file body. Both store backends
// call it so the content-addressing scheme (the blob key) is identical, and the
// CLI/web layers never see hashing — they post plain FileSnapshots.
func fileSHA(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
