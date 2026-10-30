package test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gasmod/gas/storage"
)

// TestSentinelErrors checks that the sentinels are distinct, prefixed, and
// matchable when wrapped.
func TestSentinelErrors(t *testing.T) {
	t.Parallel()

	sentinels := map[string]error{
		"ErrNotInitialized":        storage.ErrNotInitialized,
		"ErrInputRequired":         storage.ErrInputRequired,
		"ErrInvalidKey":            storage.ErrInvalidKey,
		"ErrKeyNotFound":           storage.ErrKeyNotFound,
		"ErrNotFound":              storage.ErrNotFound,
		"ErrInvalidBucket":         storage.ErrInvalidBucket,
		"ErrBucketNotFound":        storage.ErrBucketNotFound,
		"ErrBucketExists":          storage.ErrBucketExists,
		"ErrVersionNotFound":       storage.ErrVersionNotFound,
		"ErrInvalidOption":         storage.ErrInvalidOption,
		"ErrInvalidTTL":            storage.ErrInvalidTTL,
		"ErrNotSupported":          storage.ErrNotSupported,
		"ErrInvalidRange":          storage.ErrInvalidRange,
		"ErrContentLengthMismatch": storage.ErrContentLengthMismatch,
		"ErrInvalidLimit":          storage.ErrInvalidLimit,

		"ErrInvalidContinuationToken": storage.ErrInvalidContinuationToken,
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
	if errors.Is(storage.ErrKeyNotFound, storage.ErrNotFound) || errors.Is(storage.ErrNotFound, storage.ErrKeyNotFound) {
		t.Error("ErrKeyNotFound and ErrNotFound must not match each other")
	}

	// ErrVersionNotFound is a third, distinct not-found: the key exists but
	// the requested version does not.
	for _, other := range []error{storage.ErrKeyNotFound, storage.ErrNotFound} {
		if errors.Is(storage.ErrVersionNotFound, other) || errors.Is(other, storage.ErrVersionNotFound) {
			t.Errorf("ErrVersionNotFound and %v must not match each other", other)
		}
	}
}
