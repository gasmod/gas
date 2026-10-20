package s3_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/gasmod/gas/storage"
)

// TestLocalStack_ConditionalWrites checks IfNoneMatch and IfMatch against
// LocalStack: a create of a missing key, a refused create of an existing
// one, a replace with the current ETag, a refused replace with a stale one,
// and an IfMatch of a missing key.
func TestLocalStack_ConditionalWrites(t *testing.T) {
	t.Parallel()
	p, bucket := lsProviderWithBucket(t, "ls-cond")
	put := func(key, body string, cond storage.WriteCondition) (*storage.ObjectInfo, error) {
		return p.PutObject(t.Context(), &storage.PutObjectInput{
			BaseObjectInput: lsObj(
				bucket,
				key,
			),
			Body:           strings.NewReader(body),
			WriteCondition: cond,
		})
	}

	first, err := put("k", "first", storage.WriteCondition{IfNoneMatch: new("*")})
	if err != nil {
		t.Fatalf("PutObject(IfNoneMatch *) of a missing key error: %v", err)
	}
	if _, err := put(
		"k",
		"x",
		storage.WriteCondition{IfNoneMatch: new("*")},
	); !errors.Is(
		err,
		storage.ErrPreconditionFailed,
	) {
		t.Errorf(
			"PutObject(IfNoneMatch *) of an existing key error = %v, want ErrPreconditionFailed",
			err,
		)
	}
	if _, err := put("k", "second", storage.WriteCondition{IfMatch: new(first.ETag)}); err != nil {
		t.Errorf("PutObject(IfMatch current) error: %v", err)
	}
	if _, err := put(
		"k",
		"x",
		storage.WriteCondition{IfMatch: new(first.ETag)},
	); !errors.Is(
		err,
		storage.ErrPreconditionFailed,
	) {
		t.Errorf("PutObject(IfMatch stale) error = %v, want ErrPreconditionFailed", err)
	}
	if _, err := put(
		"missing",
		"x",
		storage.WriteCondition{IfMatch: new(first.ETag)},
	); !errors.Is(
		err,
		storage.ErrKeyNotFound,
	) {
		t.Errorf("PutObject(IfMatch) of a missing key error = %v, want ErrKeyNotFound", err)
	}
	if got, err := lsGet(t, p, bucket, "k", nil); err != nil || string(got) != "second" {
		t.Errorf("GetObject() = %q, %v; want second", got, err)
	}
}
