package guidesfiles_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gasmod/gas/storage"
	storagedir "github.com/gasmod/gas/storage/dir"
)

// putAll writes every key with a small body, concurrently.
func putAll(t *testing.T, p storage.Provider, keys []string) {
	t.Helper()
	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)
	errs := make(chan error, len(keys))
	for _, k := range keys {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := p.PutObject(t.Context(), &storage.PutObjectInput{
				Body: strings.NewReader("x"), BaseObjectInput: obj(k),
			}); err != nil {
				errs <- fmt.Errorf("PutObject(%s): %w", k, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// Docs: guides/files.mdx#operations
// "`ListObjects(ctx, *ListObjectsInput)` | `*ListObjectsOutput` | One page of
// a bucket's objects, by key, with `Prefix`, `Delimiter` and `StartAfter`"
func TestListObjectsByKeyWithFilters(t *testing.T) {
	t.Parallel()
	keys := []string{"b/2", "a/1", "c", "a/3", "b/1", "a/2"}
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			putAll(t, b.p, keys)
			all, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{})
			if err != nil {
				t.Fatalf("ListObjects() error: %v", err)
			}
			want := slices.Sorted(slices.Values(keys))
			if got := keysOf(all.Objects); !slices.Equal(got, want) {
				t.Fatalf("ListObjects() keys = %v, want %v (by key)", got, want)
			}
			pre, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{Prefix: ptr("a/")})
			if err != nil {
				t.Fatalf("ListObjects(Prefix) error: %v", err)
			}
			if got := keysOf(pre.Objects); !slices.Equal(got, []string{"a/1", "a/2", "a/3"}) {
				t.Fatalf("ListObjects(Prefix a/) = %v, want [a/1 a/2 a/3]", got)
			}
			sa, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{StartAfter: ptr("a/3")})
			if err != nil {
				t.Fatalf("ListObjects(StartAfter) error: %v", err)
			}
			if got := keysOf(sa.Objects); !slices.Equal(got, []string{"b/1", "b/2", "c"}) {
				t.Fatalf("ListObjects(StartAfter a/3) = %v, want [b/1 b/2 c]", got)
			}
		})
	}
}

// Docs: guides/files.mdx#operations
// "A `Delimiter` such as `/` groups keys under `CommonPrefixes`, the way S3
// consoles show folders."
func TestDelimiterGroupsCommonPrefixes(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			putAll(t, b.p, []string{"docs/a", "docs/b", "img/x/y", "root"})
			out, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{Delimiter: ptr("/")})
			if err != nil {
				t.Fatalf("ListObjects(Delimiter /) error: %v", err)
			}
			if got := keysOf(out.Objects); !slices.Equal(got, []string{"root"}) {
				t.Fatalf("Objects = %v, want [root]", got)
			}
			if !slices.Equal(out.CommonPrefixes, []string{"docs/", "img/"}) {
				t.Fatalf("CommonPrefixes = %v, want [docs/ img/]", out.CommonPrefixes)
			}
		})
	}
}

// Docs: guides/files.mdx#operations
// "A page holds at most `MaxKeys` objects and common prefixes together"
func TestMaxKeysCountsObjectsAndPrefixes(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			putAll(t, b.p, []string{"a/1", "b", "c/1", "d", "e/1"})
			out, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{Delimiter: ptr("/"), MaxKeys: ptr(int32(3))})
			if err != nil {
				t.Fatalf("ListObjects() error: %v", err)
			}
			if n := len(out.Objects) + len(out.CommonPrefixes); n != 3 {
				t.Fatalf("page holds %d objects + prefixes (%v, %v), want 3", n, keysOf(out.Objects), out.CommonPrefixes)
			}
			if len(out.Objects) == 0 || len(out.CommonPrefixes) == 0 {
				t.Fatalf("page = %v, %v, want both objects and prefixes counted", keysOf(out.Objects), out.CommonPrefixes)
			}
		})
	}
}

// Docs: guides/files.mdx#operations
// "The directory backend defaults `MaxKeys` to 1000 and treats a larger value
// as 1000, and defaults `MaxBuckets` to 10000."
func TestDirPageSizes(t *testing.T) {
	t.Parallel()
	t.Run("MaxKeys", func(t *testing.T) {
		t.Parallel()
		keys := make([]string, 1001)
		for i := range keys {
			keys[i] = fmt.Sprintf("k%04d", i)
		}
		d := dirBackend(t)
		putAll(t, d.p, keys)
		for name, mk := range map[string]*int32{"default": nil, "MaxKeys 2000": ptr(int32(2000))} {
			out, err := d.p.ListObjects(t.Context(), &storage.ListObjectsInput{MaxKeys: mk})
			if err != nil {
				t.Fatalf("%s: ListObjects() error: %v", name, err)
			}
			if len(out.Objects) != 1000 || !out.IsTruncated {
				t.Fatalf("%s: page of %d objects, IsTruncated %v, want 1000 and truncated", name, len(out.Objects), out.IsTruncated)
			}
		}
	})
	t.Run("MaxBuckets", func(t *testing.T) {
		t.Parallel()
		p, _ := newDirProvider(t, "")
		const n = 10001
		var wg sync.WaitGroup
		sem := make(chan struct{}, 32)
		errs := make(chan error, n)
		for i := range n {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				name := fmt.Sprintf("b%05d", i)
				if _, err := p.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: name}); err != nil {
					errs <- fmt.Errorf("CreateBucket(%s): %w", name, err)
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		out, err := p.ListBuckets(t.Context(), &storage.ListBucketsInput{})
		if err != nil {
			t.Fatalf("ListBuckets() error: %v", err)
		}
		if len(out.Buckets) != 10000 || out.NextContinuationToken == "" {
			t.Fatalf("default page of %d buckets, NextContinuationToken %q, want 10000 and a token", len(out.Buckets), out.NextContinuationToken)
		}
	})
}

// Docs: guides/files.mdx#operations
// "The S3 backend passes both through as given, so the page size is S3's"
// Assumption: "as given" includes an unset MaxKeys or MaxBuckets, which then
// sends no page size and leaves S3's default in force.
func TestS3PageSizesPassedThrough(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var queries []url.Values
	f := newFakeS3(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.Query())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ListAllMyBucketsResult><Buckets></Buckets></ListAllMyBucketsResult>`))
			return
		}
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`))
	})
	p := f.provider(t, "us-east-1", "b-pages")
	last := func() url.Values {
		mu.Lock()
		defer mu.Unlock()
		return queries[len(queries)-1]
	}
	for _, tc := range []struct {
		name string
		mk   *int32
		want string
	}{
		{"MaxKeys 2000", ptr(int32(2000)), "2000"},
		{"MaxKeys 7", ptr(int32(7)), "7"},
		{"MaxKeys unset", nil, ""},
	} {
		if _, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{MaxKeys: tc.mk}); err != nil {
			t.Fatalf("%s: ListObjects() error: %v", tc.name, err)
		}
		if got := last().Get("max-keys"); got != tc.want {
			t.Errorf("%s: max-keys sent = %q, want %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range []struct {
		name string
		mb   *int32
		want string
	}{
		{"MaxBuckets 10000", ptr(int32(10000)), "10000"},
		{"MaxBuckets 3", ptr(int32(3)), "3"},
		{"MaxBuckets unset", nil, ""},
	} {
		if _, err := p.ListBuckets(t.Context(), &storage.ListBucketsInput{MaxBuckets: tc.mb}); err != nil {
			t.Fatalf("%s: ListBuckets() error: %v", tc.name, err)
		}
		if got := last().Get("max-buckets"); got != tc.want {
			t.Errorf("%s: max-buckets sent = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Docs: guides/files.mdx#operations
// "To read the next page, pass the page's `NextContinuationToken` back in the
// next input as `ContinuationToken`, and stop when it is empty, which happens
// on the last page only (`ListObjects` also reports `IsTruncated`)."
func TestListObjectsPaging(t *testing.T) {
	t.Parallel()
	keys := []string{"k1", "k2", "k3", "k4", "k5"}
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			putAll(t, b.p, keys)
			in := &storage.ListObjectsInput{MaxKeys: ptr(int32(2))}
			var seen []string
			for page := 1; ; page++ {
				out, err := b.p.ListObjects(t.Context(), in)
				if err != nil {
					t.Fatalf("page %d: ListObjects() error: %v", page, err)
				}
				seen = append(seen, keysOf(out.Objects)...)
				last := len(seen) == len(keys)
				if last != (out.NextContinuationToken == "") {
					t.Fatalf("page %d: NextContinuationToken = %q with %d of %d keys seen, want empty on the last page only",
						page, out.NextContinuationToken, len(seen), len(keys))
				}
				if out.IsTruncated == last {
					t.Fatalf("page %d: IsTruncated = %v, want %v", page, out.IsTruncated, !last)
				}
				if last {
					break
				}
				if page > len(keys) {
					t.Fatalf("paging did not end")
				}
				in.ContinuationToken = ptr(out.NextContinuationToken)
			}
			if !slices.Equal(seen, keys) {
				t.Fatalf("keys across pages = %v, want %v", seen, keys)
			}
		})
	}
}

// Docs: guides/files.mdx#operations
// "`ListBuckets(ctx, *ListBucketsInput)` | `*ListBucketsOutput` | One page of
// your buckets, by name; filter with `Prefix`"
// Docs: guides/files.mdx#operations
// "stop when it is empty, which happens on the last page only"
func TestListBucketsByNameAndPaging(t *testing.T) {
	t.Parallel()
	type provider struct {
		name string
		p    storage.Provider
	}
	dp, _ := newDirProvider(t, "")
	ps := []provider{{"s3", newS3Provider(t, "us-east-1", "")}, {"dir", dp}}
	for _, pv := range ps {
		t.Run(pv.name, func(t *testing.T) {
			t.Parallel()
			prefix := uniqueName("lb")
			names := []string{prefix + "-c", prefix + "-a", prefix + "-b"}
			for _, n := range names {
				mustCreateBucket(t, pv.p, n)
			}
			mustCreateBucket(t, pv.p, uniqueName("other"))
			want := slices.Sorted(slices.Values(names))

			out, err := pv.p.ListBuckets(t.Context(), &storage.ListBucketsInput{Prefix: ptr(prefix)})
			if err != nil {
				t.Fatalf("ListBuckets(Prefix) error: %v", err)
			}
			var got []string
			for _, bk := range out.Buckets {
				got = append(got, bk.Name)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("ListBuckets(Prefix) = %v, want %v by name", got, want)
			}
			if out.NextContinuationToken != "" {
				t.Fatalf("single page NextContinuationToken = %q, want empty", out.NextContinuationToken)
			}

			in := &storage.ListBucketsInput{Prefix: ptr(prefix), MaxBuckets: ptr(int32(1))}
			got = nil
			for page := 1; ; page++ {
				out, err := pv.p.ListBuckets(t.Context(), in)
				if err != nil {
					t.Fatalf("page %d: ListBuckets() error: %v", page, err)
				}
				for _, bk := range out.Buckets {
					got = append(got, bk.Name)
				}
				last := len(got) == len(want)
				if last != (out.NextContinuationToken == "") {
					t.Fatalf("page %d: NextContinuationToken = %q with %d of %d buckets seen, want empty on the last page only",
						page, out.NextContinuationToken, len(got), len(want))
				}
				if last {
					break
				}
				if page > len(want) {
					t.Fatalf("paging did not end")
				}
				in.ContinuationToken = ptr(out.NextContinuationToken)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("buckets across pages = %v, want %v", got, want)
			}
		})
	}
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Docs: guides/files.mdx#operations
// "Each page's `Skipped` counts the entries the backend could not read and
// left out, over the whole bucket; the directory backend reports where they
// are to a handler given with `dir.WithSkipHandler`, and S3 never skips."
func TestSkipped(t *testing.T) {
	t.Parallel()
	t.Run("dir", func(t *testing.T) {
		t.Parallel()
		var mu sync.Mutex
		var paths []string
		bucket := uniqueName("skip")
		p, root := newDirProvider(t, bucket, storagedir.WithSkipHandler(func(path string, _ error) {
			mu.Lock()
			paths = append(paths, path)
			mu.Unlock()
		}))
		mustCreateBucket(t, p, bucket)
		putAll(t, p, []string{"a/1", "a/2", "a/3", "z/broken"})
		// Corrupt one object's metadata file, at the location the dir package
		// documents: <bucket-hash>/<key-hash>.gas.json.
		meta := filepath.Join(root, sha(bucket), sha("z/broken")+".gas.json")
		if err := os.WriteFile(meta, []byte("not json"), 0o600); err != nil {
			t.Fatalf("corrupt metadata: %v", err)
		}
		in := &storage.ListObjectsInput{Prefix: ptr("a/"), MaxKeys: ptr(int32(2))}
		for page := 1; ; page++ {
			out, err := p.ListObjects(t.Context(), in)
			if err != nil {
				t.Fatalf("page %d: ListObjects() error: %v", page, err)
			}
			if out.Skipped != 1 {
				t.Fatalf("page %d: Skipped = %d, want 1 (counted over the whole bucket, outside Prefix too)", page, out.Skipped)
			}
			if slices.Contains(keysOf(out.Objects), "z/broken") {
				t.Fatalf("page %d lists the unreadable object", page)
			}
			if out.NextContinuationToken == "" {
				break
			}
			in.ContinuationToken = ptr(out.NextContinuationToken)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(paths) == 0 {
			t.Fatalf("skip handler not called")
		}
		for _, got := range paths {
			if !strings.Contains(got, sha("z/broken")) {
				t.Fatalf("skip handler path = %q, want the unreadable object's location", got)
			}
		}
	})
	t.Run("s3", func(t *testing.T) {
		t.Parallel()
		b := s3Backend(t)
		putAll(t, b.p, []string{"a", "b"})
		out, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{})
		if err != nil {
			t.Fatalf("ListObjects() error: %v", err)
		}
		bl, err := b.p.ListBuckets(t.Context(), &storage.ListBucketsInput{})
		if err != nil {
			t.Fatalf("ListBuckets() error: %v", err)
		}
		if out.Skipped != 0 || bl.Skipped != 0 {
			t.Fatalf("s3 Skipped = %d (objects), %d (buckets), want 0", out.Skipped, bl.Skipped)
		}
	})
}

// Docs: guides/files.mdx#errors-and-testing
// "`ErrInvalidContinuationToken` | A list token that is set but empty, or one
// the directory backend cannot decode. The directory backend cannot tell a
// well-formed token it did not issue, and resumes after the name it decodes
// to; S3 may reject a token it did not issue with an error that carries no
// sentinel"
// The dir token encoding, unpadded base64url of the last name, is the one the
// dir package documentation gives.
func TestContinuationTokens(t *testing.T) {
	t.Parallel()
	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		for _, b := range backends(t) {
			_, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{ContinuationToken: ptr("")})
			wantErr(t, b.name+" ListObjects(empty token)", err, storage.ErrInvalidContinuationToken)
			_, err = b.p.ListBuckets(t.Context(), &storage.ListBucketsInput{ContinuationToken: ptr("")})
			wantErr(t, b.name+" ListBuckets(empty token)", err, storage.ErrInvalidContinuationToken)
		}
	})
	t.Run("dir cannot decode", func(t *testing.T) {
		t.Parallel()
		d := dirBackend(t)
		for _, tok := range []string{"%%%", "!!", "a b"} {
			_, err := d.p.ListObjects(t.Context(), &storage.ListObjectsInput{ContinuationToken: ptr(tok)})
			wantErr(t, fmt.Sprintf("dir ListObjects(token %q)", tok), err, storage.ErrInvalidContinuationToken)
			_, err = d.p.ListBuckets(t.Context(), &storage.ListBucketsInput{ContinuationToken: ptr(tok)})
			wantErr(t, fmt.Sprintf("dir ListBuckets(token %q)", tok), err, storage.ErrInvalidContinuationToken)
		}
	})
	t.Run("dir resumes after the decoded name", func(t *testing.T) {
		t.Parallel()
		p, _ := newDirProvider(t, "")
		bucket := uniqueName("tok")
		for _, name := range []string{bucket, bucket + "-a", bucket + "-b"} {
			mustCreateBucket(t, p, name)
		}
		putAll(t, &bucketScoped{Provider: p, bucket: bucket}, []string{"k1", "k2", "k3"})
		out, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{
			Bucket: ptr(bucket), ContinuationToken: ptr(base64.RawURLEncoding.EncodeToString([]byte("k1"))),
		})
		if err != nil {
			t.Fatalf("ListObjects(token of %q) error: %v", "k1", err)
		}
		if got := keysOf(out.Objects); !slices.Equal(got, []string{"k2", "k3"}) {
			t.Fatalf("ListObjects(token of %q) = %v, want [k2 k3]", "k1", got)
		}
		bl, err := p.ListBuckets(t.Context(), &storage.ListBucketsInput{
			ContinuationToken: ptr(base64.RawURLEncoding.EncodeToString([]byte(bucket + "-a"))),
		})
		if err != nil {
			t.Fatalf("ListBuckets(token of %q) error: %v", bucket+"-a", err)
		}
		var names []string
		for _, b := range bl.Buckets {
			names = append(names, b.Name)
		}
		if !slices.Equal(names, []string{bucket + "-b"}) {
			t.Fatalf("ListBuckets(token of %q) = %v, want [%s]", bucket+"-a", names, bucket+"-b")
		}
	})
	t.Run("s3 token not issued", func(t *testing.T) {
		t.Parallel()
		b := s3Backend(t)
		putAll(t, b.p, []string{"k1", "k2"})
		_, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{ContinuationToken: ptr("not-a-token")})
		if err == nil {
			return // "may reject": acceptance is allowed
		}
		for _, s := range storageSentinels {
			if errors.Is(err, s) {
				t.Fatalf("s3 ListObjects(foreign token) error = %v, want an error carrying no storage sentinel, got %v", err, s)
			}
		}
	})
}

// bucketScoped is a storage.Provider whose PutObject writes into bucket.
type bucketScoped struct {
	storage.Provider
	bucket string
}

func (b *bucketScoped) PutObject(ctx context.Context, in *storage.PutObjectInput) (*storage.ObjectInfo, error) {
	in.Bucket = ptr(b.bucket)
	return b.Provider.PutObject(ctx, in)
}

// storageSentinels is every sentinel the page's error table lists.
var storageSentinels = []error{
	storage.ErrKeyNotFound, storage.ErrNotFound, storage.ErrBucketNotFound, storage.ErrVersionNotFound,
	storage.ErrBucketExists, storage.ErrBucketNotEmpty, storage.ErrAccessDenied, storage.ErrPreconditionFailed,
	storage.ErrInvalidCondition, storage.ErrInvalidHeader, storage.ErrInvalidKey, storage.ErrInvalidBucket,
	storage.ErrInputRequired, storage.ErrInvalidTTL, storage.ErrInvalidLimit, storage.ErrInvalidContinuationToken,
	storage.ErrInvalidRange, storage.ErrNotSupported, storage.ErrContentLengthMismatch, storage.ErrInvalidOption,
	storage.ErrNotInitialized,
}

// Docs: guides/files.mdx#errors-and-testing
// "`ErrInvalidLimit` | A `MaxKeys` under 1, or a `MaxBuckets` outside 1 to
// 10000"
func TestInvalidLimit(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			for _, mk := range []int32{0, -1} {
				_, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{MaxKeys: ptr(mk)})
				wantErr(t, fmt.Sprintf("ListObjects(MaxKeys %d)", mk), err, storage.ErrInvalidLimit)
			}
			for _, mb := range []int32{0, -1, 10001} {
				_, err := b.p.ListBuckets(t.Context(), &storage.ListBucketsInput{MaxBuckets: ptr(mb)})
				wantErr(t, fmt.Sprintf("ListBuckets(MaxBuckets %d)", mb), err, storage.ErrInvalidLimit)
			}
			if _, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{MaxKeys: ptr(int32(1))}); err != nil {
				t.Fatalf("ListObjects(MaxKeys 1) error: %v", err)
			}
			for _, mb := range []int32{1, 10000} {
				if _, err := b.p.ListBuckets(t.Context(), &storage.ListBucketsInput{MaxBuckets: ptr(mb)}); err != nil {
					t.Fatalf("ListBuckets(MaxBuckets %d) error: %v", mb, err)
				}
			}
		})
	}
}
