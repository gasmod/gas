// Package cryptoutil provides shared cryptographic helpers used across
// gas/auth sub-packages.
package cryptoutil

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// SHA256Hex returns the hex-encoded SHA-256 hash of s.
func SHA256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// RandomString generates n random bytes and returns them as a
// URL-safe base64 string (no padding).
func RandomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("cryptoutil: random: %w", err)
	}
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(b), nil
}
