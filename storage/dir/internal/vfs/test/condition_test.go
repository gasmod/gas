package test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/gasmod/gas/storage/dir/internal/vfs"
)

// createIf writes key "k" with body under cond.
func createIf(t *testing.T, b *vfs.Bucket, body string, cond vfs.Condition) (*vfs.ObjectStat, error) {
	t.Helper()
	return b.CreateObject(t.Context(), &vfs.CreateObjectInput{
		Key: "k", Body: strings.NewReader(body), Condition: cond,
	})
}

// setMeta replaces the metadata file of key "k" with content.
func setMeta(t *testing.T, dir, content string) {
	t.Helper()
	writeMeta(t, dir, "k", content)
}

// TestCreateObject_Condition checks IfMatch and IfNoneMatch against a
// missing, a current, a legacy and a corrupt object.
func TestCreateObject_Condition(t *testing.T) {
	v, dir := newTestVFS(t)
	b := newTestBucket(t, v)

	if _, err := createIf(
		t,
		b,
		"first",
		vfs.Condition{IfMatch: "abc"},
	); !errors.Is(
		err,
		vfs.ErrObjectNotFound,
	) {
		t.Errorf("IfMatch on a missing key = %v, want ErrObjectNotFound", err)
	}
	first, err := createIf(t, b, "first", vfs.Condition{IfNoneMatch: true})
	if err != nil {
		t.Fatalf("IfNoneMatch on a missing key: %v", err)
	}
	if _, err := createIf(
		t,
		b,
		"x",
		vfs.Condition{IfNoneMatch: true},
	); !errors.Is(
		err,
		vfs.ErrPreconditionFailed,
	) {
		t.Errorf("IfNoneMatch on an existing key = %v, want ErrPreconditionFailed", err)
	}
	if _, err := createIf(
		t,
		b,
		"x",
		vfs.Condition{IfMatch: "stale"},
	); !errors.Is(
		err,
		vfs.ErrPreconditionFailed,
	) {
		t.Errorf("IfMatch stale = %v, want ErrPreconditionFailed", err)
	}
	if body, _ := getObject(t, b, "k"); body != "first" {
		t.Fatalf("object after refused writes = %q, want first", body)
	}
	if _, err := createIf(t, b, "second", vfs.Condition{IfMatch: first.ETag}); err != nil {
		t.Fatalf("IfMatch current: %v", err)
	}

	// Metadata from before ETags were recorded has none, so no IfMatch
	// matches it.
	legacy := `{"Key":"k","DataFile":"` + dataFile(t, b, "k") + `"}`
	setMeta(t, dir, legacy)
	if _, err := createIf(
		t,
		b,
		"x",
		vfs.Condition{IfMatch: first.ETag},
	); !errors.Is(
		err,
		vfs.ErrPreconditionFailed,
	) {
		t.Errorf("IfMatch on legacy metadata = %v, want ErrPreconditionFailed", err)
	}

	// Corrupt metadata holds the key, so neither condition holds.
	setMeta(t, dir, "{")
	for name, cond := range map[string]vfs.Condition{
		"IfMatch": {IfMatch: first.ETag}, "IfNoneMatch": {IfNoneMatch: true},
	} {
		if _, err := createIf(t, b, "x", cond); !errors.Is(err, vfs.ErrPreconditionFailed) {
			t.Errorf("%s on corrupt metadata = %v, want ErrPreconditionFailed", name, err)
		}
	}
}

// TestCreateObject_IfNoneMatchRace checks that the condition is evaluated
// under the key's lock: of many concurrent IfNoneMatch writes of one new
// key, exactly one succeeds.
func TestCreateObject_IfNoneMatchRace(t *testing.T) {
	v, _ := newTestVFS(t)
	b := newTestBucket(t, v)

	const writers = 16
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for range writers {
		wg.Go(func() {
			_, err := b.CreateObject(t.Context(), &vfs.CreateObjectInput{
				Key: "k", Body: strings.NewReader("x"), Condition: vfs.Condition{IfNoneMatch: true},
			})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, vfs.ErrPreconditionFailed):
			t.Errorf("CreateObject = %v, want nil or ErrPreconditionFailed", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d of %d IfNoneMatch writes succeeded, want exactly 1", ok, writers)
	}
}
