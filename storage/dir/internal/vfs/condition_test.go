package vfs

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// createIf writes key "k" with body under cond.
func createIf(t *testing.T, b *Bucket, body string, cond Condition) (*ObjectStat, error) {
	t.Helper()
	return b.CreateObject(t.Context(), &CreateObjectInput{
		Key: "k", Body: strings.NewReader(body), Condition: cond,
	})
}

// setMeta replaces the metadata file of key "k" with content.
func setMeta(t *testing.T, b *Bucket, content string) {
	t.Helper()
	if err := b.metaRoot.WriteFile(hash("k")+metaFileExt, []byte(content), filePerm); err != nil {
		t.Fatal(err)
	}
}

// TestCreateObject_Condition checks IfMatch and IfNoneMatch against a
// missing, a current, a legacy and a corrupt object.
func TestCreateObject_Condition(t *testing.T) {
	v, _ := newTestVFS(t)
	b := newTestBucket(t, v)

	if _, err := createIf(
		t,
		b,
		"first",
		Condition{IfMatch: "abc"},
	); !errors.Is(
		err,
		ErrObjectNotFound,
	) {
		t.Errorf("IfMatch on a missing key = %v, want ErrObjectNotFound", err)
	}
	first, err := createIf(t, b, "first", Condition{IfNoneMatch: true})
	if err != nil {
		t.Fatalf("IfNoneMatch on a missing key: %v", err)
	}
	if _, err := createIf(
		t,
		b,
		"x",
		Condition{IfNoneMatch: true},
	); !errors.Is(
		err,
		ErrPreconditionFailed,
	) {
		t.Errorf("IfNoneMatch on an existing key = %v, want ErrPreconditionFailed", err)
	}
	if _, err := createIf(
		t,
		b,
		"x",
		Condition{IfMatch: "stale"},
	); !errors.Is(
		err,
		ErrPreconditionFailed,
	) {
		t.Errorf("IfMatch stale = %v, want ErrPreconditionFailed", err)
	}
	if body, _ := getObject(t, b, "k"); body != "first" {
		t.Fatalf("object after refused writes = %q, want first", body)
	}
	if _, err := createIf(t, b, "second", Condition{IfMatch: first.ETag}); err != nil {
		t.Fatalf("IfMatch current: %v", err)
	}

	// Metadata from before ETags were recorded has none, so no IfMatch
	// matches it.
	legacy := `{"Key":"k","DataFile":"` + dataFile(t, b, "k") + `"}`
	setMeta(t, b, legacy)
	if _, err := createIf(
		t,
		b,
		"x",
		Condition{IfMatch: first.ETag},
	); !errors.Is(
		err,
		ErrPreconditionFailed,
	) {
		t.Errorf("IfMatch on legacy metadata = %v, want ErrPreconditionFailed", err)
	}

	// Corrupt metadata holds the key, so neither condition holds.
	setMeta(t, b, "{")
	for name, cond := range map[string]Condition{
		"IfMatch": {IfMatch: first.ETag}, "IfNoneMatch": {IfNoneMatch: true},
	} {
		if _, err := createIf(t, b, "x", cond); !errors.Is(err, ErrPreconditionFailed) {
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
			_, err := b.CreateObject(t.Context(), &CreateObjectInput{
				Key: "k", Body: strings.NewReader("x"), Condition: Condition{IfNoneMatch: true},
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
		case !errors.Is(err, ErrPreconditionFailed):
			t.Errorf("CreateObject = %v, want nil or ErrPreconditionFailed", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d of %d IfNoneMatch writes succeeded, want exactly 1", ok, writers)
	}
}
