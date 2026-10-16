package vfs

import (
	"crypto/sha256"
	"encoding/hex"
)

// hash returns the hex SHA-256 of s. Bucket names and keys are stored under
// their hash, so no user string ever becomes a path.
func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
