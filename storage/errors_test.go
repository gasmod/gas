package storage

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestSentinelErrors checks that the sentinels are distinct, prefixed, and
// matchable when wrapped.
func TestSentinelErrors(t *testing.T) {
	t.Parallel()

	sentinels := map[string]error{
		"ErrNotInitialized":  ErrNotInitialized,
		"ErrInputRequired":   ErrInputRequired,
		"ErrInvalidKey":      ErrInvalidKey,
		"ErrKeyNotFound":     ErrKeyNotFound,
		"ErrNotFound":        ErrNotFound,
		"ErrInvalidBucket":   ErrInvalidBucket,
		"ErrBucketNotFound":  ErrBucketNotFound,
		"ErrBucketExists":    ErrBucketExists,
		"ErrVersionNotFound": ErrVersionNotFound,
		"ErrInvalidOption":   ErrInvalidOption,
		"ErrInvalidTTL":      ErrInvalidTTL,
		"ErrNotSupported":    ErrNotSupported,
		"ErrInvalidRange":    ErrInvalidRange,
	}

	messages := map[string]string{}
	for name, err := range sentinels {
		if err == nil {
			t.Fatalf("%s is nil", name)
		}
		if !strings.HasPrefix(err.Error(), "storage: ") {
			t.Errorf("%s = %q, want storage: prefix", name, err)
		}
		if other, dup := messages[err.Error()]; dup {
			t.Errorf("%s and %s share message %q", name, other, err)
		}
		messages[err.Error()] = name

		for otherName, other := range sentinels {
			if otherName != name && errors.Is(err, other) {
				t.Errorf("errors.Is(%s, %s) = true, sentinels must be distinct", name, otherName)
			}
		}

		wrapped := fmt.Errorf("gas/storage/x: op: %w", err)
		if !errors.Is(wrapped, err) {
			t.Errorf("wrapped %s not matched by errors.Is", name)
		}
	}

	// ErrKeyNotFound and ErrNotFound are deliberately distinct: HEAD cannot
	// distinguish a missing key from a missing bucket.
	if errors.Is(ErrKeyNotFound, ErrNotFound) || errors.Is(ErrNotFound, ErrKeyNotFound) {
		t.Error("ErrKeyNotFound and ErrNotFound must not match each other")
	}

	// ErrVersionNotFound is a third, distinct not-found: the key exists but
	// the requested version does not.
	for _, other := range []error{ErrKeyNotFound, ErrNotFound} {
		if errors.Is(ErrVersionNotFound, other) || errors.Is(other, ErrVersionNotFound) {
			t.Errorf("ErrVersionNotFound and %v must not match each other", other)
		}
	}
}
