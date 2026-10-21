package guidesfiles_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/gasmod/gas/storage"
	storagedir "github.com/gasmod/gas/storage/dir"
)

// The tests below damage a dir root at the locations the dir package
// documentation gives under "Layout":
//
//	<bucket-hash>/<key-hash>.gas.json   metadata of each object
//	.gas/buckets/<bucket-hash>          name of each bucket

// skipRecorder collects what a dir provider reports to its skip handler.
type skipRecorder struct {
	mu    sync.Mutex
	paths []string
	errs  []error
}

func (r *skipRecorder) handle(path string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, path)
	r.errs = append(r.errs, err)
}

func (r *skipRecorder) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.paths)
}

// Docs: guides/files.mdx#operations
// "The directory backend reads the metadata of every object in the bucket on
// each `ListObjects` call, so a page costs time in proportion to the bucket,
// not the page"
// The cost itself is not measured; what is observable is that metadata outside
// the requested prefix and page is read on every call, which an unreadable
// object there reveals through the skip handler.
func TestDirListReadsEveryObjectEachCall(t *testing.T) {
	t.Parallel()
	rec := &skipRecorder{}
	bucket := uniqueName("cost")
	p, root := newDirProvider(t, bucket, storagedir.WithSkipHandler(rec.handle))
	mustCreateBucket(t, p, bucket)
	putAll(t, p, []string{"a/1", "a/2", "a/3", "z/broken"})
	meta := filepath.Join(root, sha(bucket), sha("z/broken")+".gas.json")
	if err := os.WriteFile(meta, []byte("not json"), 0o600); err != nil {
		t.Fatalf("corrupt metadata: %v", err)
	}
	in := &storage.ListObjectsInput{Prefix: ptr("a/"), MaxKeys: ptr(int32(1))}
	for call := 1; ; call++ {
		before := rec.calls()
		out, err := p.ListObjects(t.Context(), in)
		if err != nil {
			t.Fatalf("call %d: ListObjects() error: %v", call, err)
		}
		if got := rec.calls() - before; got != 1 {
			t.Fatalf("call %d: skip handler heard %d entries, want 1 (z/broken, outside the prefix and page, read on this call)", call, got)
		}
		if out.NextContinuationToken == "" {
			break
		}
		in.ContinuationToken = ptr(out.NextContinuationToken)
	}
}

// Docs: guides/files.mdx#errors-and-testing
// "`dir.ErrCorrupt` is an object it cannot read, from `HeadObject` and
// `GetObject`; it never means the object is missing, and a `PutObject` of the
// key replaces it."
func TestDirErrCorrupt(t *testing.T) {
	t.Parallel()
	bucket := uniqueName("corrupt")
	p, root := newDirProvider(t, bucket)
	mustCreateBucket(t, p, bucket)
	mustPut(t, p, "k", "old")
	meta := filepath.Join(root, sha(bucket), sha("k")+".gas.json")
	if err := os.WriteFile(meta, []byte("not json"), 0o600); err != nil {
		t.Fatalf("corrupt metadata: %v", err)
	}

	_, err := p.HeadObject(t.Context(), &storage.HeadObjectInput{BaseObjectInput: obj("k")})
	if !errors.Is(err, storagedir.ErrCorrupt) {
		t.Errorf("HeadObject(corrupt) error = %v, want errors.Is dir.ErrCorrupt", err)
	}
	if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrKeyNotFound) {
		t.Errorf("HeadObject(corrupt) error = %v, want it not to mean the object is missing", err)
	}
	o, err := p.GetObject(t.Context(), &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: obj("k")}})
	if err == nil {
		_ = o.Body.Close()
	}
	if !errors.Is(err, storagedir.ErrCorrupt) {
		t.Errorf("GetObject(corrupt) error = %v, want errors.Is dir.ErrCorrupt", err)
	}
	if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrKeyNotFound) {
		t.Errorf("GetObject(corrupt) error = %v, want it not to mean the object is missing", err)
	}

	mustPut(t, p, "k", "new")
	if got := readObject(t, p, "k"); got != "new" {
		t.Fatalf("GetObject after PutObject over the corrupt object = %q, want %q", got, "new")
	}
}

// Docs: guides/files.mdx#errors-and-testing
// "`dir.ErrUnnamedBucket` is a bucket whose name record is missing or wrong;
// `ListBuckets` leaves it out, and the skip handler receives an error wrapping
// it."
func TestDirErrUnnamedBucket(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		damage func(t *testing.T, root, victim, other string)
	}{
		{"missing", func(t *testing.T, root, victim, _ string) {
			if err := os.Remove(filepath.Join(root, ".gas", "buckets", sha(victim))); err != nil {
				t.Fatalf("remove name record: %v", err)
			}
		}},
		{"wrong", func(t *testing.T, root, victim, other string) {
			// Another bucket's well-formed record holds a name that is not
			// the victim's.
			b, err := os.ReadFile(filepath.Join(root, ".gas", "buckets", sha(other)))
			if err != nil {
				t.Fatalf("read name record: %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, ".gas", "buckets", sha(victim)), b, 0o600); err != nil {
				t.Fatalf("overwrite name record: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &skipRecorder{}
			p, root := newDirProvider(t, "", storagedir.WithSkipHandler(rec.handle))
			victim, other := uniqueName("victim"), uniqueName("other")
			mustCreateBucket(t, p, victim)
			mustCreateBucket(t, p, other)
			tc.damage(t, root, victim, other)

			out, err := p.ListBuckets(t.Context(), &storage.ListBucketsInput{})
			if err != nil {
				t.Fatalf("ListBuckets() error: %v", err)
			}
			var names []string
			for _, b := range out.Buckets {
				names = append(names, b.Name)
			}
			if !slices.Equal(names, []string{other}) {
				t.Errorf("ListBuckets() = %v, want only [%s] (the unnamed bucket left out)", names, other)
			}
			rec.mu.Lock()
			defer rec.mu.Unlock()
			if len(rec.errs) != 1 || !errors.Is(rec.errs[0], storagedir.ErrUnnamedBucket) {
				t.Fatalf("skip handler errors = %v, want one wrapping dir.ErrUnnamedBucket", rec.errs)
			}
		})
	}
}
