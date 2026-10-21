package modulesstorage_test

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gasmod/gas/storage"
	storagedir "github.com/gasmod/gas/storage/dir"
)

// hexName matches a name the dir backend derives from a hex SHA-256.
var hexName = regexp.MustCompile(`^[0-9a-f]{64}`)

// Docs: modules/storage.mdx#the-directory-backend
// "The directory backend keeps every bucket and object under the root, named by
// the hex SHA-256 of its name, so a key is an opaque string as on S3: `../x`,
// `/abs`, and keys that differ only in case or Unicode normalization are
// distinct objects on every filesystem, and no key can name a path."
func TestDirKeysAreOpaque(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	path := filepath.Join(parent, "root")
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	p, err := storagedir.NewProvider(storagedir.WithRoot(root))(mockConfig(t, map[string]any{"bucket": "opaque"}))
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}
	mustCreateBucket(t, p, "opaque")
	keys := []string{"../x", "/abs", "Case", "case", "CASE", "é", "é", "a/b/", "../../escape"}
	for i, k := range keys {
		mustPut(t, p, k, string(rune('A'+i)))
	}
	for i, k := range keys {
		if got := readObject(t, p, k); got != string(rune('A'+i)) {
			t.Errorf("key %q read back %q, want its own body %q", k, got, string(rune('A'+i)))
		}
	}
	// Nothing landed outside the root, and every name under it is a hash or
	// the backend's own directory.
	if entries, _ := os.ReadDir(parent); len(entries) != 1 {
		t.Fatalf("parent of the root holds %d entries, want only the root", len(entries))
	}
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == path {
			return err
		}
		rel, _ := filepath.Rel(path, p)
		first := strings.Split(rel, string(filepath.Separator))[0]
		if first == ".gas" {
			return nil
		}
		if !hexName.MatchString(d.Name()) {
			t.Errorf("entry %s is not named by a hash", rel)
		}
		return nil
	})
	if _, err := os.Stat(filepath.Join(path, "abs")); err == nil {
		t.Errorf("key /abs named a path in the root")
	}
}

// Docs: modules/storage.mdx#the-directory-backend
// "All access goes through the `os.Root`, so symlinks in the storage directory
// cannot lead outside it."
func TestDirSymlinkCannotEscape(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	bucket := uniqueName("sym")
	p, path := newDirProvider(t, bucket)
	mustCreateBucket(t, p, bucket)
	outside := t.TempDir()
	bucketDir := filepath.Join(path, sha(bucket))
	if err := os.RemoveAll(bucketDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, bucketDir); err != nil {
		t.Fatal(err)
	}
	_, _ = p.PutObject(t.Context(), &storage.PutObjectInput{Body: strings.NewReader("x"), BaseObjectInput: obj("k")})
	_, _ = p.ListObjects(t.Context(), &storage.ListObjectsInput{})
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("directory outside the root holds %d entries after writes through a symlink, want none", len(entries))
	}
}

// Docs: modules/storage.mdx#the-directory-backend
// "**Writes are atomic.** A `PutObject` writes a new data file, then renames a
// new metadata file over the old one. Readers see the old object or the new
// one, never a mix, and a failed write leaves the previous object as it was."
func TestDirWritesAtomic(t *testing.T) {
	t.Parallel()
	d := dirBackend(t)
	a, b := strings.Repeat("a", 64<<10), strings.Repeat("b", 64<<10)
	mustPut(t, d.p, "k", a)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			body := a
			if i%2 == 0 {
				body = b
			}
			mustPut(t, d.p, "k", body)
		}
		close(stop)
	}()
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				o, err := d.p.GetObject(t.Context(), getIn("k"))
				if err != nil {
					t.Errorf("GetObject() during writes error: %v", err)
					return
				}
				got, _ := io.ReadAll(o.Body)
				_ = o.Body.Close()
				if s := string(got); s != a && s != b {
					t.Errorf("read a mix of %d bytes, want the old or the new object", len(got))
					return
				}
				if o.Size != int64(len(got)) {
					t.Errorf("Size %d for a %d-byte body", o.Size, len(got))
					return
				}
			}
		}()
	}
	wg.Wait()

	// A write whose body fails part way leaves the previous object.
	before := readObject(t, d.p, "k")
	_, err := d.p.PutObject(t.Context(), &storage.PutObjectInput{
		Body: io.MultiReader(strings.NewReader("partial"), errReader{}), BaseObjectInput: obj("k"),
	})
	if err == nil {
		t.Fatalf("PutObject with a failing body error = nil, want an error")
	}
	if got := readObject(t, d.p, "k"); got != before {
		t.Fatalf("object after a failed write changed, want the previous object")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("body failed") }

// Docs: modules/storage.mdx#the-directory-backend
// "When the replaced data file cannot be removed afterwards, `PutObject` returns
// the `ObjectInfo` together with `dir.ErrStaleData`: the object reads back as
// written, and the old data file stays on disk."
// Docs: modules/storage.mdx#errors
// "`dir.ErrStaleData` | A `PutObject` that stored the object but could not
// remove the data file it replaced. It is returned with the `ObjectInfo`"
// The old data file is made unremovable by swapping it for a non-empty
// directory of the same name, at the location the dir package documentation
// gives: .gas/objects/<bucket-hash>/<key-hash>_<uuid>.
func TestDirErrStaleData(t *testing.T) {
	t.Parallel()
	bucket := uniqueName("stale")
	p, root := newDirProvider(t, bucket)
	mustCreateBucket(t, p, bucket)
	mustPut(t, p, "k", "old")
	dataDir := filepath.Join(root, ".gas", "objects", sha(bucket))
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatalf("read data directory: %v", err)
	}
	var old string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), sha("k")+"_") {
			old = filepath.Join(dataDir, e.Name())
		}
	}
	if old == "" {
		t.Fatalf("no data file for k in %s", dataDir)
	}
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(old, "pin"), 0o750); err != nil {
		t.Fatal(err)
	}
	info, err := p.PutObject(t.Context(), &storage.PutObjectInput{Body: strings.NewReader("new"), BaseObjectInput: obj("k")})
	if !errors.Is(err, storagedir.ErrStaleData) {
		t.Fatalf("PutObject() error = %v, want dir.ErrStaleData", err)
	}
	if info == nil || info.ETag == "" || info.Size != 3 {
		t.Fatalf("PutObject() ObjectInfo = %+v, want the new object's info with the error", info)
	}
	if got := readObject(t, p, "k"); got != "new" {
		t.Fatalf("object = %q, want it to read back as written", got)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("old data file: %v, want it to stay on disk", err)
	}
}

// Docs: modules/storage.mdx#the-directory-backend
// "**One writer per key.** Operations on one key are serialized"
// Checked in process: concurrent conditional creates of one key, of which
// exactly one may win if they are serialized.
func TestDirOneWriterPerKey(t *testing.T) {
	t.Parallel()
	d := dirBackend(t)
	const n = 16
	var wg sync.WaitGroup
	wins := make(chan int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := d.p.PutObject(t.Context(), &storage.PutObjectInput{
				Body: strings.NewReader("x"), WriteCondition: storage.WriteCondition{IfNoneMatch: ptr("*")}, BaseObjectInput: obj("once"),
			})
			switch {
			case err == nil:
				wins <- i
			case !errors.Is(err, storage.ErrPreconditionFailed):
				t.Errorf("PutObject() error = %v, want nil or ErrPreconditionFailed", err)
			}
		}()
	}
	wg.Wait()
	close(wins)
	if got := len(wins); got != 1 {
		t.Fatalf("%d concurrent IfNoneMatch writes succeeded, want exactly 1", got)
	}
}

// Docs: modules/storage.mdx#the-directory-backend
// "A `GetObject` body keeps reading the data it opened even if the object is
// overwritten or deleted meanwhile (not on Windows)."
func TestDirBodySurvivesOverwriteAndDelete(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("not on Windows, per the page")
	}
	d := dirBackend(t)
	body := strings.Repeat("old ", 1<<14)
	for _, then := range []string{"overwrite", "delete"} {
		mustPut(t, d.p, "k", body)
		o, err := d.p.GetObject(t.Context(), getIn("k"))
		if err != nil {
			t.Fatalf("GetObject() error: %v", err)
		}
		head := make([]byte, 4)
		if _, err := io.ReadFull(o.Body, head); err != nil {
			t.Fatal(err)
		}
		if then == "overwrite" {
			mustPut(t, d.p, "k", "new")
		} else if _, err := d.p.DeleteObject(t.Context(), &storage.DeleteObjectInput{BaseObjectInput: obj("k")}); err != nil {
			t.Fatal(err)
		}
		rest, err := io.ReadAll(o.Body)
		_ = o.Body.Close()
		if err != nil || string(head)+string(rest) != body {
			t.Fatalf("%s: body read %d bytes, %v, want the %d bytes it opened", then, len(head)+len(rest), err, len(body))
		}
	}
}

// Docs: modules/storage.mdx#the-directory-backend
// "**No versions or parts.** `Version` on `HeadObject`, `GetObject` or
// `DeleteObject`, and `PartNumber` on `HeadObject` or `GetObject`, fail with
// `storage.ErrNotSupported`. The `Response*` fields of reads are ignored, and
// `ObjectInfo` carries no `Version`."
func TestDirNoVersionsOrParts(t *testing.T) {
	t.Parallel()
	b := dirBackend(t)
	put := mustPut(t, b.p, "k", "x")
	ctx := t.Context()
	for name, in := range map[string]storage.HeadObjectInput{
		"Version":    {Version: ptr("v1"), BaseObjectInput: obj("k")},
		"PartNumber": {PartNumber: ptr(int32(1)), BaseObjectInput: obj("k")},
	} {
		_, err := b.p.HeadObject(ctx, &in)
		wantErr(t, "HeadObject("+name+")", err, storage.ErrNotSupported)
		_, err = b.p.GetObject(ctx, &storage.GetObjectInput{HeadObjectInput: in})
		wantErr(t, "GetObject("+name+")", err, storage.ErrNotSupported)
	}
	_, err := b.p.DeleteObject(ctx, &storage.DeleteObjectInput{Version: ptr("v1"), BaseObjectInput: obj("k")})
	wantErr(t, "DeleteObject(Version)", err, storage.ErrNotSupported)
	if got := readObject(t, b.p, "k"); got != "x" {
		t.Fatalf("body after DeleteObject(Version) = %q, want unchanged", got)
	}

	in := getIn("k")
	in.ResponseContentType = ptr("application/x-override")
	in.ResponseCacheControl = ptr("max-age=1")
	in.ResponseContentDisposition = ptr("attachment")
	o, err := b.p.GetObject(ctx, in)
	if err != nil {
		t.Fatalf("GetObject(Response*) error: %v", err)
	}
	_ = o.Body.Close()
	if o.ContentType == "application/x-override" || o.CacheControl != "" || o.ContentDisposition != "" {
		t.Fatalf("GetObject(Response*) info = %+v, want the Response* fields ignored", o.ObjectInfo)
	}
	head, err := b.p.HeadObject(ctx, headIn("k"))
	if err != nil {
		t.Fatal(err)
	}
	if put.Version != "" || head.Version != "" || o.Version != "" {
		t.Fatalf("Version put %q, head %q, get %q, want none", put.Version, head.Version, o.Version)
	}
}

// Docs: modules/storage.mdx#the-directory-backend
// "**Damaged entries.** An object whose metadata cannot be used is reported by
// `HeadObject`, `GetObject` and `DeleteObject` as an error wrapping
// `dir.ErrCorrupt`, which never means the object is missing; a `PutObject` of
// the key replaces it, and `ListObjects` leaves it out."
// Docs: modules/storage.mdx#errors
// "`dir.ErrCorrupt` | An object it cannot read, from `HeadObject`, `GetObject`
// and `DeleteObject`. It never means the object is missing, and a `PutObject`
// of the key replaces it"
func TestDirErrCorrupt(t *testing.T) {
	t.Parallel()
	bucket := uniqueName("corrupt")
	p, root := newDirProvider(t, bucket)
	mustCreateBucket(t, p, bucket)
	mustPut(t, p, "k", "old")
	mustPut(t, p, "fine", "x")
	if err := os.WriteFile(metaPath(root, bucket, "k"), []byte("not json"), 0o600); err != nil {
		t.Fatalf("corrupt metadata: %v", err)
	}
	notMissing := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, storagedir.ErrCorrupt) {
			t.Errorf("%s(corrupt) error = %v, want errors.Is dir.ErrCorrupt", what, err)
		}
		for _, s := range []error{storage.ErrNotFound, storage.ErrKeyNotFound, storage.ErrBucketNotFound} {
			if errors.Is(err, s) {
				t.Errorf("%s(corrupt) error = %v, matches %v, want it not to mean missing", what, err, s)
			}
		}
	}
	_, err := p.HeadObject(t.Context(), headIn("k"))
	notMissing("HeadObject", err)
	o, err := p.GetObject(t.Context(), getIn("k"))
	if err == nil {
		_ = o.Body.Close()
	}
	notMissing("GetObject", err)
	_, err = p.DeleteObject(t.Context(), &storage.DeleteObjectInput{BaseObjectInput: obj("k")})
	notMissing("DeleteObject", err)

	out, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{})
	if err != nil {
		t.Fatalf("ListObjects() error: %v", err)
	}
	if got := keysOf(out.Objects); !slices.Equal(got, []string{"fine"}) {
		t.Fatalf("ListObjects() = %v, want the corrupt object left out", got)
	}

	mustPut(t, p, "k", "new")
	if got := readObject(t, p, "k"); got != "new" {
		t.Fatalf("GetObject after PutObject over the corrupt object = %q, want %q", got, "new")
	}
}

// Docs: modules/storage.mdx#the-directory-backend
// "A bucket whose name record is missing or wrong (`dir.ErrUnnamedBucket`) is
// left out of `ListBuckets` until `CreateBucket` of it runs again; its objects
// stay reachable meanwhile."
// Docs: modules/storage.mdx#errors
// "`dir.ErrUnnamedBucket` | A bucket whose name record is missing or wrong.
// `ListBuckets` leaves it out, and the skip handler receives an error wrapping
// it"
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
			scoped := &bucketScoped{Provider: p, bucket: victim}
			mustPut(t, scoped, "k", "kept")
			tc.damage(t, root, victim, other)

			out, err := p.ListBuckets(t.Context(), &storage.ListBucketsInput{})
			if err != nil {
				t.Fatalf("ListBuckets() error: %v", err)
			}
			if got := namesOf(out.Buckets); !slices.Equal(got, []string{other}) {
				t.Errorf("ListBuckets() = %v, want only [%s]", got, other)
			}
			rec.mu.Lock()
			if len(rec.errs) != 1 || !errors.Is(rec.errs[0], storagedir.ErrUnnamedBucket) {
				t.Errorf("skip handler errors = %v, want one wrapping dir.ErrUnnamedBucket", rec.errs)
			}
			rec.mu.Unlock()

			o, err := p.GetObject(t.Context(), &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{
				BaseObjectInput: storage.BaseObjectInput{Bucket: ptr(victim), Key: "k"},
			}})
			if err != nil {
				t.Fatalf("GetObject in the unnamed bucket error = %v, want its objects reachable", err)
			}
			_ = o.Body.Close()

			mustCreateBucket(t, p, victim)
			out, err = p.ListBuckets(t.Context(), &storage.ListBucketsInput{})
			if err != nil {
				t.Fatalf("ListBuckets() error: %v", err)
			}
			if got := namesOf(out.Buckets); !slices.Contains(got, victim) {
				t.Fatalf("ListBuckets() after CreateBucket again = %v, want %s listed", got, victim)
			}
		})
	}
}

// Docs: modules/storage.mdx#the-directory-backend
// "`DeleteBucket` removes such debris with the bucket, so it can never make a
// bucket undeletable."
// Docs: modules/storage.mdx#the-directory-backend
// "`DeleteBucket` also reports the debris it removes and anything it leaves
// behind."
func TestDirDeleteBucketRemovesDebris(t *testing.T) {
	t.Parallel()
	rec := &skipRecorder{}
	bucket := uniqueName("debris")
	p, root := newDirProvider(t, bucket, storagedir.WithSkipHandler(rec.handle))
	mustCreateBucket(t, p, bucket)
	mustPut(t, p, "k", "x")
	if err := os.WriteFile(metaPath(root, bucket, "k"), []byte("not json"), 0o600); err != nil {
		t.Fatalf("corrupt metadata: %v", err)
	}
	if err := p.DeleteBucket(t.Context(), &storage.DeleteBucketInput{Bucket: bucket}); err != nil {
		t.Fatalf("DeleteBucket(bucket holding only debris) error = %v, want nil", err)
	}
	if rec.calls() == 0 {
		t.Fatalf("skip handler heard nothing from DeleteBucket, want the debris it removed")
	}
	_, err := p.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: bucket})
	wantErr(t, "HeadBucket after DeleteBucket", err, storage.ErrBucketNotFound)
}

// Docs: modules/storage.mdx#the-directory-backend
// "**Skip handler.** `WithSkipHandler(fn)` is called, synchronously and once per
// entry per call, with the path of each entry a listing leaves out, relative to
// the root, and an error wrapping `dir.ErrCorrupt` or `dir.ErrUnnamedBucket`."
// Docs: modules/storage.mdx#the-directory-backend
// "A paged listing reports the same entry on every page."
// Docs: modules/storage.mdx#wiring
// "`WithSkipHandler(fn)` receives the entries listings leave out"
func TestDirSkipHandler(t *testing.T) {
	t.Parallel()
	rec := &skipRecorder{}
	bucket := uniqueName("skiph")
	p, root := newDirProvider(t, bucket, storagedir.WithSkipHandler(rec.handle))
	mustCreateBucket(t, p, bucket)
	putAll(t, p, []string{"a", "b", "c", "broken1", "broken2"})
	for _, k := range []string{"broken1", "broken2"} {
		if err := os.WriteFile(metaPath(root, bucket, k), []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	in := &storage.ListObjectsInput{MaxKeys: ptr(int32(1))}
	pages := 0
	for {
		before := rec.calls()
		out, err := p.ListObjects(t.Context(), in)
		if err != nil {
			t.Fatalf("ListObjects() error: %v", err)
		}
		pages++
		// Synchronous: the calls have happened by the time ListObjects returns.
		if got := rec.calls() - before; got != 2 {
			t.Fatalf("page %d: handler called %d times by return, want 2 (once per entry)", pages, got)
		}
		if out.NextContinuationToken == "" {
			break
		}
		in.ContinuationToken = ptr(out.NextContinuationToken)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	want := map[string]int{}
	for i, path := range rec.paths {
		if filepath.IsAbs(path) {
			t.Errorf("skip path %q is absolute, want relative to the root", path)
		}
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			t.Errorf("skip path %q does not name an entry under the root: %v", path, err)
		}
		if !errors.Is(rec.errs[i], storagedir.ErrCorrupt) {
			t.Errorf("skip error = %v, want it wrapping dir.ErrCorrupt", rec.errs[i])
		}
		want[path]++
	}
	if len(want) != 2 {
		t.Fatalf("skip paths = %v, want two distinct entries", rec.paths)
	}
	for path, n := range want {
		if n != pages {
			t.Errorf("entry %s reported %d times over %d pages, want once per page", path, n, pages)
		}
	}
}

// Docs: modules/storage.mdx#the-directory-backend
// "The provider holds no open files between calls and has nothing to close."
// Nothing to close is checked here; open files are counted in dir_unix_test.go.
func TestDirNothingToClose(t *testing.T) {
	t.Parallel()
	d, _ := newDirProvider(t, "")
	if _, ok := reflect.TypeOf(d).MethodByName("Close"); ok {
		t.Fatalf("*dir.Provider has a Close method, want nothing to close")
	}
}
