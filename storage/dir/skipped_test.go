package dir

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas/storage"
)

// skipLog records what a provider reports to its skip handler.
type skipLog struct {
	paths []string
	errs  []error
	mu    sync.Mutex
}

// handler returns the function to give WithSkipHandler.
func (l *skipLog) handler() func(string, error) {
	return func(path string, err error) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.paths = append(l.paths, path)
		l.errs = append(l.errs, err)
	}
}

// newSkipProvider returns a provider with testBucket created and a skip
// handler recording into the returned log, and its root dir.
func newSkipProvider(t *testing.T) (*Provider, *skipLog, string) {
	t.Helper()
	log := &skipLog{}
	root, dir := openRoot(t)
	p, err := NewProvider(WithRoot(root), WithSkipHandler(log.handler()))(newConfig(t, testBucket))
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}
	createBucket(t, p, testBucket)
	return p, log, dir
}

// listPage returns the keys of one ListObjects page of in, and the page.
func listPage(
	t *testing.T,
	p *Provider,
	in *storage.ListObjectsInput,
) ([]string, *storage.ListObjectsOutput) {
	t.Helper()
	out, err := p.ListObjects(context.Background(), in)
	if err != nil {
		t.Fatalf("ListObjects() error: %v", err)
	}
	keys := make([]string, 0, len(out.Objects))
	for _, o := range out.Objects {
		keys = append(keys, o.Key)
	}
	return keys, out
}

// TestListObjects_SkipsUnreadable checks that an object whose metadata
// cannot be used, or whose data file is gone, is left out of the listing
// and counted in Skipped, instead of failing it, and that the skip handler
// gets the path of each, relative to the root, with an error matching
// ErrCorrupt.
func TestListObjects_SkipsUnreadable(t *testing.T) {
	t.Parallel()

	tests := map[string]func(t *testing.T, dir string){
		"undecodable metadata": func(t *testing.T, dir string) {
			writeFile(t, metaFile(dir, "bad"), []byte("{not json"))
		},
		"metadata holding another key": func(t *testing.T, dir string) {
			data, err := os.ReadFile(metaFile(dir, "good"))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, metaFile(dir, "bad"), data)
		},
		"data file gone": func(t *testing.T, dir string) {
			if err := os.Remove(dataFile(t, dir, "bad")); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, damage := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p, log, dir := newSkipProvider(t)
			put(t, p, "good", []byte("v"))
			put(t, p, "bad", []byte("v"))
			damage(t, dir)

			keys, out := listPage(t, p, &storage.ListObjectsInput{})
			if !slices.Equal(keys, []string{"good"}) || out.Skipped != 1 {
				t.Errorf("ListObjects() = %q, Skipped %d; want [good], 1", keys, out.Skipped)
			}
			wantPath := filepath.Join(sha(testBucket), sha("bad")+".gas.json")
			if len(log.paths) != 1 || log.paths[0] != wantPath ||
				!errors.Is(log.errs[0], ErrCorrupt) {
				t.Errorf("skip handler got %q, %v; want [%s] matching ErrCorrupt",
					log.paths, log.errs, wantPath)
			}
			for _, err := range log.errs {
				if strings.Contains(err.Error(), "bad") || strings.Contains(err.Error(), "good") {
					t.Errorf("skip handler error %q quotes a key", err)
				}
			}
		})
	}
}

// TestListObjects_SkippedCoversTheWholeBucket checks that Skipped counts the
// whole bucket, whatever the Prefix or page, so every page of a listing
// repeats it, and so does the skip handler.
func TestListObjects_SkippedCoversTheWholeBucket(t *testing.T) {
	t.Parallel()
	p, log, dir := newSkipProvider(t)
	for _, k := range []string{"a/1", "a/2", "z"} {
		put(t, p, k, []byte("v"))
	}
	writeFile(t, metaFile(dir, "zz"), []byte("{"))

	in := &storage.ListObjectsInput{Prefix: new("a/"), MaxKeys: new(int32(1))}
	for page := 1; ; page++ {
		_, out := listPage(t, p, in)
		if out.Skipped != 1 {
			t.Errorf("page %d Skipped = %d, want 1", page, out.Skipped)
		}
		if !out.IsTruncated {
			if page != 2 {
				t.Errorf("listing took %d pages, want 2", page)
			}
			break
		}
		in.ContinuationToken = new(out.NextContinuationToken)
	}
	if len(log.paths) != 2 {
		t.Errorf("skip handler called %d times over two pages, want 2", len(log.paths))
	}
}

// TestListObjects_IOErrorStillFails checks that an I/O fault, as opposed to
// unusable metadata, still fails the listing rather than being skipped.
func TestListObjects_IOErrorStillFails(t *testing.T) {
	t.Parallel()
	p, _, dir := newSkipProvider(t)
	put(t, p, "k", []byte("v"))
	// A symlink in place of a metadata file, pointing out of the bucket's
	// directory, is one os.Root refuses to follow: reading it is an I/O
	// error, not corrupt metadata, and a listing must not hide it.
	if err := os.Remove(metaFile(dir, "k")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "nowhere"), metaFile(dir, "k")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, err := p.ListObjects(context.Background(), &storage.ListObjectsInput{}); err == nil {
		t.Error("ListObjects() with an unreadable metadata file = nil, want an error")
	}
}

// TestListBuckets_ReportsUnnamed checks that a bucket ListBuckets leaves out
// for want of a name is counted in Skipped and reported with the path of
// its name record and an error matching ErrUnnamedBucket.
func TestListBuckets_ReportsUnnamed(t *testing.T) {
	t.Parallel()
	p, log, dir := newSkipProvider(t)
	createBucket(t, p, "other-bucket")
	if err := os.Remove(nameRecord(dir, testBucket)); err != nil {
		t.Fatal(err)
	}

	out, err := p.ListBuckets(context.Background(), &storage.ListBucketsInput{})
	if err != nil {
		t.Fatalf("ListBuckets() error: %v", err)
	}
	if len(out.Buckets) != 1 || out.Skipped != 1 {
		t.Errorf("ListBuckets() = %+v, want other-bucket and Skipped 1", out)
	}
	wantPath := filepath.Join(".gas", "buckets", sha(testBucket))
	if len(log.paths) != 1 || log.paths[0] != wantPath ||
		!errors.Is(log.errs[0], ErrUnnamedBucket) {
		t.Errorf("skip handler got %q, %v; want [%s] matching ErrUnnamedBucket",
			log.paths, log.errs, wantPath)
	}
}

// TestDeleteBucket_ReportsDebrisAndLeftovers checks that DeleteBucket
// reports to the skip handler the debris it removes with the bucket and a
// leftover it must leave.
func TestDeleteBucket_ReportsDebrisAndLeftovers(t *testing.T) {
	t.Parallel()
	p, log, dir := newSkipProvider(t)
	writeFile(t, metaFile(dir, "bad"), []byte("{"))

	if err := p.DeleteBucket(context.Background(),
		&storage.DeleteBucketInput{Bucket: testBucket}); err != nil {
		t.Fatalf("DeleteBucket() error: %v", err)
	}
	wantPath := filepath.Join(sha(testBucket), sha("bad")+".gas.json")
	if !slices.Contains(log.paths, wantPath) {
		t.Errorf("skip handler got %q, want %s among them", log.paths, wantPath)
	}
	for i, path := range log.paths {
		if path == wantPath && !errors.Is(log.errs[i], ErrCorrupt) {
			t.Errorf("skip handler error for %s = %v, want ErrCorrupt", path, log.errs[i])
		}
	}
}

// TestProvider_CorruptReadsMatchErrCorrupt checks that HeadObject and
// GetObject of an object whose metadata cannot be used, including metadata
// holding another key, return an error matching ErrCorrupt, which callers
// could not match before: Get agrees with ListObjects, which skips them.
func TestProvider_CorruptReadsMatchErrCorrupt(t *testing.T) {
	t.Parallel()
	for name, damage := range map[string]func(t *testing.T, dir string){
		"undecodable": func(t *testing.T, dir string) {
			writeFile(t, metaFile(dir, "k"), []byte("{"))
		},
		"holds another key": func(t *testing.T, dir string) {
			// The key's own data file, but another key in the record.
			data, err := os.ReadFile(metaFile(dir, "k"))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, metaFile(dir, "k"),
				[]byte(strings.Replace(string(data), `"Key":"k"`, `"Key":"other"`, 1)))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p, dir := newTestProvider(t)
			put(t, p, "k", []byte("v"))
			damage(t, dir)

			if _, err := head(p, "k"); !errors.Is(err, ErrCorrupt) {
				t.Errorf("HeadObject() = %v, want ErrCorrupt", err)
			}
			if err := getErr(p, "k"); !errors.Is(err, ErrCorrupt) {
				t.Errorf("GetObject() = %v, want ErrCorrupt", err)
			}
		})
	}
}

// TestDeleteBucket_SkipHandlerMayCallTheProvider checks that DeleteBucket
// calls the skip handler only after giving up its locks: a handler that
// creates the same bucket again, which takes the bucket's lifecycle lock,
// would otherwise wait on DeleteBucket forever.
func TestDeleteBucket_SkipHandlerMayCallTheProvider(t *testing.T) {
	t.Parallel()
	root, dir := openRoot(t)
	var p *Provider
	var handlerErr error
	calls := 0
	handler := func(string, error) {
		calls++
		if calls > 1 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, handlerErr = p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: testBucket})
	}
	var err error
	p, err = NewProvider(WithRoot(root), WithSkipHandler(handler))(newConfig(t, testBucket))
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}
	createBucket(t, p, testBucket)
	writeFile(t, metaFile(dir, "bad"), []byte("{"))

	if err := p.DeleteBucket(context.Background(),
		&storage.DeleteBucketInput{Bucket: testBucket}); err != nil {
		t.Fatalf("DeleteBucket() error: %v", err)
	}
	if calls == 0 || handlerErr != nil {
		t.Errorf(
			"skip handler called %d times; its CreateBucket returned %v, want nil",
			calls,
			handlerErr,
		)
	}
}
