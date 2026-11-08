package cryptoutil

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

// ErrInvalidLength indicates that RandomString was asked for a negative
// number of bytes.
var ErrInvalidLength = errors.New("invalid random string length")

// SHA256Hex returns the SHA-256 digest of s as lowercase hex.
func SHA256Hex(s string) string {
	h := sha256.Sum256([]byte(s))

	return hex.EncodeToString(h[:])
}

// RandomString returns n random bytes from crypto/rand as unpadded
// URL-safe base64. A negative n is an error wrapping ErrInvalidLength.
func RandomString(n int) (string, error) {
	if n < 0 {
		return "", fmt.Errorf("%w: must not be negative", ErrInvalidLength)
	}

	b := make([]byte, n)

	_, err := rand.Read(b)
	if err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Equal reports whether a and b are the same, in time that does not depend
// on their contents. Strings of different lengths are unequal at once.
func Equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
