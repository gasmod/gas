package modulesstorage_test

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gasmod/gas/storage"
	storagedir "github.com/gasmod/gas/storage/dir"
)

// Docs: modules/storage.mdx#operations
// "`ListObjects(ctx, *ListObjectsInput)` | `*ListObjectsOutput` | One page of a
// bucket's objects, by key, with `Prefix`, `Delimiter` and `StartAfter`"
// Docs: modules/storage.mdx#listing
// "entries in byte order of their UTF-8 names"
func TestListObjectsByteOrderAndFilters(t *testing.T) {
	t.Parallel()
	keys := []string{"b/2", "a/1", "c", "a/3", "B", "é", "~", "a/2", "Z"}
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			putAll(t, b.p, keys)
			all, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{})
			if err != nil {
				t.Fatalf("ListObjects() error: %v", err)
			}
			want := slices.Sorted(slices.Values(keys)) // Go sorts strings by bytes
			if got := keysOf(all.Objects); !slices.Equal(got, want) {
				t.Fatalf("ListObjects() keys = %q, want byte order %q", got, want)
			}
			pre, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{Prefix: ptr("a/")})
			if err != nil {
				t.Fatalf("ListObjects(Prefix) error: %v", err)
			}
			if got := keysOf(pre.Objects); !slices.Equal(got, []string{"a/1", "a/2", "a/3"}) {
				t.Fatalf("ListObjects(Prefix a/) = %v, want [a/1 a/2 a/3]", got)
			}
			// StartAfter is checked within ASCII keys: LocalStack drops "é" after
			// an ASCII StartAfter, which says nothing about the backend.
			sa, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{Prefix: ptr("a/"), StartAfter: ptr("a/1")})
			if err != nil {
				t.Fatalf("ListObjects(StartAfter) error: %v", err)
			}
			if got := keysOf(sa.Objects); !slices.Equal(got, []string{"a/2", "a/3"}) {
				t.Fatalf("ListObjects(Prefix a/, StartAfter a/1) = %q, want [a/2 a/3]", got)
			}
		})
	}
}

// Docs: modules/storage.mdx#pagination
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
			if n := len(out.Objects) + len(out.CommonPrefixes); n != 3 || len(out.Objects) == 0 || len(out.CommonPrefixes) == 0 {
				t.Fatalf("page = %v + %v, want 3 entries counting both objects and prefixes", keysOf(out.Objects), out.CommonPrefixes)
			}
		})
	}
}

// Docs: modules/storage.mdx#pagination
// "A `MaxKeys` under 1, or a `MaxBuckets` outside 1 to 10000, fails with
// `storage.ErrInvalidLimit`."
func TestInvalidLimit(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	d, _ := newDirProvider(t, "bkt")
	for name, p := range map[string]storage.Provider{"s3": f.provider(t, "us-east-1", "bkt"), "dir": d} {
		for _, mk := range []int32{0, -1} {
			_, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{MaxKeys: ptr(mk)})
			wantErr(t, fmt.Sprintf("%s ListObjects(MaxKeys %d)", name, mk), err, storage.ErrInvalidLimit)
		}
		for _, mb := range []int32{0, -1, 10001} {
			_, err := p.ListBuckets(t.Context(), &storage.ListBucketsInput{MaxBuckets: ptr(mb)})
			wantErr(t, fmt.Sprintf("%s ListBuckets(MaxBuckets %d)", name, mb), err, storage.ErrInvalidLimit)
		}
	}
	if n := f.requests.Load(); n != 0 {
		t.Fatalf("invalid limits sent %d requests, want 0", n)
	}
	mustCreateBucket(t, d, "bkt")
	if _, err := d.ListObjects(t.Context(), &storage.ListObjectsInput{MaxKeys: ptr(int32(1))}); err != nil {
		t.Fatalf("ListObjects(MaxKeys 1) error: %v", err)
	}
	for _, mb := range []int32{1, 10000} {
		if _, err := d.ListBuckets(t.Context(), &storage.ListBucketsInput{MaxBuckets: ptr(mb)}); err != nil {
			t.Fatalf("ListBuckets(MaxBuckets %d) error: %v", mb, err)
		}
	}
}

// Docs: modules/storage.mdx#pagination
// "The directory backend defaults `MaxKeys` to 1000 and treats a larger value as
// 1000, and defaults `MaxBuckets` to 10000."
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
			t.Fatalf("default page of %d buckets, token %q, want 10000 and a token", len(out.Buckets), out.NextContinuationToken)
		}
	})
}

// Docs: modules/storage.mdx#pagination
// "The s3 backend passes both through as given, so the page size is S3's"
// Assumption: "as given" includes an unset MaxKeys or MaxBuckets, which then
// sends no page size.
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
		mk   *int32
		want string
	}{{ptr(int32(2000)), "2000"}, {ptr(int32(7)), "7"}, {nil, ""}} {
		if _, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{MaxKeys: tc.mk}); err != nil {
			t.Fatalf("ListObjects() error: %v", err)
		}
		if got := last().Get("max-keys"); got != tc.want {
			t.Errorf("max-keys sent = %q, want %q", got, tc.want)
		}
	}
	for _, tc := range []struct {
		mb   *int32
		want string
	}{{ptr(int32(10000)), "10000"}, {ptr(int32(3)), "3"}, {nil, ""}} {
		if _, err := p.ListBuckets(t.Context(), &storage.ListBucketsInput{MaxBuckets: tc.mb}); err != nil {
			t.Fatalf("ListBuckets() error: %v", err)
		}
		if got := last().Get("max-buckets"); got != tc.want {
			t.Errorf("max-buckets sent = %q, want %q", got, tc.want)
		}
	}
}

// Docs: modules/storage.mdx#listing
// "Pass the page's `NextContinuationToken` back as `ContinuationToken`, and stop
// when it is empty, which happens on the last page only. `ListObjects` also
// reports `IsTruncated`, as S3 does."
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
				if last != (out.NextContinuationToken == "") || out.IsTruncated == last {
					t.Fatalf("page %d: token %q, IsTruncated %v with %d of %d keys seen, want a token and truncation on every page but the last",
						page, out.NextContinuationToken, out.IsTruncated, len(seen), len(keys))
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

// Docs: modules/storage.mdx#operations
// "`ListBuckets(ctx, *ListBucketsInput)` | `*ListBucketsOutput` | One page of
// your buckets, by name; filter with `Prefix`"
// Docs: modules/storage.mdx#listing
// "stop when it is empty, which happens on the last page only"
// Docs: modules/storage.mdx#pagination
// "`ListBuckets` lists the buckets the credentials own."
func TestListBucketsByNameAndPaging(t *testing.T) {
	t.Parallel()
	dp, _ := newDirProvider(t, "")
	for name, p := range map[string]storage.Provider{"s3": newS3Provider(t, "us-east-1", ""), "dir": dp} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			prefix := uniqueName("lb")
			names := []string{prefix + "-c", prefix + "-a", prefix + ".b", prefix + "0"}
			for _, n := range names {
				mustCreateBucket(t, p, n)
			}
			mustCreateBucket(t, p, uniqueName("other"))
			want := slices.Sorted(slices.Values(names))

			out, err := p.ListBuckets(t.Context(), &storage.ListBucketsInput{Prefix: ptr(prefix)})
			if err != nil {
				t.Fatalf("ListBuckets(Prefix) error: %v", err)
			}
			if got := namesOf(out.Buckets); !slices.Equal(got, want) {
				t.Fatalf("ListBuckets(Prefix) = %v, want %v in byte order", got, want)
			}
			if out.NextContinuationToken != "" {
				t.Fatalf("single page token = %q, want empty", out.NextContinuationToken)
			}

			in := &storage.ListBucketsInput{Prefix: ptr(prefix), MaxBuckets: ptr(int32(1))}
			var got []string
			for page := 1; ; page++ {
				out, err := p.ListBuckets(t.Context(), in)
				if err != nil {
					t.Fatalf("page %d: ListBuckets() error: %v", page, err)
				}
				got = append(got, namesOf(out.Buckets)...)
				last := len(got) == len(want)
				if last != (out.NextContinuationToken == "") {
					t.Fatalf("page %d: token %q with %d of %d buckets seen, want empty on the last page only",
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

// Docs: modules/storage.mdx#pagination
// "A `ContinuationToken` that is set but empty fails with
// `storage.ErrInvalidContinuationToken`."
// Docs: modules/storage.mdx#errors
// "`ErrInvalidContinuationToken` | A list token that is set but empty, or one
// the directory backend cannot decode. The directory backend cannot tell a
// well-formed token it did not issue, and resumes after the name it decodes to;
// S3 may reject a token it did not issue with an error that carries no
// sentinel"
func TestContinuationTokens(t *testing.T) {
	t.Parallel()
	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		d, _ := newDirProvider(t, "bkt")
		for name, p := range map[string]storage.Provider{"s3": f.provider(t, "us-east-1", "bkt"), "dir": d} {
			_, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{ContinuationToken: ptr("")})
			wantErr(t, name+" ListObjects(empty token)", err, storage.ErrInvalidContinuationToken)
			_, err = p.ListBuckets(t.Context(), &storage.ListBucketsInput{ContinuationToken: ptr("")})
			wantErr(t, name+" ListBuckets(empty token)", err, storage.ErrInvalidContinuationToken)
		}
	})
	t.Run("dir cannot decode", func(t *testing.T) {
		t.Parallel()
		d := dirBackend(t)
		for _, tok := range []string{"%%%", "!!", "a b", "YQ=="} {
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
		// "k10" is not a key: the backend resumes after the name anyway.
		out, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{
			Bucket: ptr(bucket), ContinuationToken: ptr(base64.RawURLEncoding.EncodeToString([]byte("k10"))),
		})
		if err != nil {
			t.Fatalf("ListObjects(token of k10) error: %v", err)
		}
		if got := keysOf(out.Objects); !slices.Equal(got, []string{"k2", "k3"}) {
			t.Fatalf("ListObjects(token of k10) = %v, want [k2 k3]", got)
		}
		bl, err := p.ListBuckets(t.Context(), &storage.ListBucketsInput{
			Prefix: ptr(bucket), ContinuationToken: ptr(base64.RawURLEncoding.EncodeToString([]byte(bucket + "-a"))),
		})
		if err != nil {
			t.Fatalf("ListBuckets(token) error: %v", err)
		}
		if got := namesOf(bl.Buckets); !slices.Equal(got, []string{bucket + "-b"}) {
			t.Fatalf("ListBuckets(token of %s-a) = %v, want [%s-b]", bucket, got, bucket)
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
				t.Fatalf("s3 ListObjects(foreign token) error = %v, matches %v, want no sentinel", err, s)
			}
		}
	})
}

// Docs: modules/storage.mdx#listing
// "A `Delimiter` such as `/` rolls every key that holds it after `Prefix` up into
// a common prefix, listed once across all pages in `CommonPrefixes`, the way S3
// consoles show folders."
// Docs: modules/storage.mdx#pagination
// "**StartAfter.** `StartAfter` applies to keys before they are rolled up, and
// need not name an existing key."
func TestDelimiterAndStartAfter(t *testing.T) {
	t.Parallel()
	keys := []string{"p/docs/a", "p/docs/b", "p/docs/c", "p/img/x/y", "p/img/z", "p/root", "q/other"}
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			putAll(t, b.p, keys)
			// One entry per page, so a prefix spanning several keys is paged over.
			in := &storage.ListObjectsInput{Prefix: ptr("p/"), Delimiter: ptr("/"), MaxKeys: ptr(int32(1))}
			var objs, prefixes []string
			for page := 1; ; page++ {
				out, err := b.p.ListObjects(t.Context(), in)
				if err != nil {
					t.Fatalf("page %d: ListObjects() error: %v", page, err)
				}
				objs = append(objs, keysOf(out.Objects)...)
				prefixes = append(prefixes, out.CommonPrefixes...)
				if out.NextContinuationToken == "" {
					break
				}
				if page > len(keys) {
					t.Fatalf("paging did not end")
				}
				in.ContinuationToken = ptr(out.NextContinuationToken)
			}
			if !slices.Equal(objs, []string{"p/root"}) || !slices.Equal(prefixes, []string{"p/docs/", "p/img/"}) {
				t.Fatalf("objects %v, prefixes %v across pages, want [p/root] and [p/docs/ p/img/] each once", objs, prefixes)
			}

			// "p/docs/a0" is not a key, and is compared with keys before roll-up:
			// p/docs/b and p/docs/c come after it, so p/docs/ is still listed.
			out, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{
				Prefix: ptr("p/"), Delimiter: ptr("/"), StartAfter: ptr("p/docs/a0"),
			})
			if err != nil {
				t.Fatalf("ListObjects(StartAfter) error: %v", err)
			}
			if !slices.Equal(out.CommonPrefixes, []string{"p/docs/", "p/img/"}) || !slices.Equal(keysOf(out.Objects), []string{"p/root"}) {
				t.Fatalf("StartAfter p/docs/a0: objects %v, prefixes %v, want [p/root] and [p/docs/ p/img/]", keysOf(out.Objects), out.CommonPrefixes)
			}
			out, err = b.p.ListObjects(t.Context(), &storage.ListObjectsInput{
				Prefix: ptr("p/"), Delimiter: ptr("/"), StartAfter: ptr("p/docs/c"),
			})
			if err != nil {
				t.Fatalf("ListObjects(StartAfter) error: %v", err)
			}
			if !slices.Equal(out.CommonPrefixes, []string{"p/img/"}) {
				t.Fatalf("StartAfter p/docs/c: prefixes %v, want [p/img/] (no key of p/docs/ after it)", out.CommonPrefixes)
			}
		})
	}
}

// Docs: modules/storage.mdx#listing
// "Each page's `Skipped` counts the entries the backend could not read and left
// out, over the whole bucket (or every bucket), so every page repeats it. The
// entries returned are correct; a caller that ignores `Skipped` sees a short
// listing and no error. The s3 backend never skips."
func TestSkipped(t *testing.T) {
	t.Parallel()
	t.Run("dir objects", func(t *testing.T) {
		t.Parallel()
		bucket := uniqueName("skip")
		p, root := newDirProvider(t, bucket)
		mustCreateBucket(t, p, bucket)
		putAll(t, p, []string{"a/1", "a/2", "a/3", "z/broken"})
		if err := os.WriteFile(metaPath(root, bucket, "z/broken"), []byte("not json"), 0o600); err != nil {
			t.Fatalf("corrupt metadata: %v", err)
		}
		in := &storage.ListObjectsInput{Prefix: ptr("a/"), MaxKeys: ptr(int32(2))}
		var seen []string
		for page := 1; ; page++ {
			out, err := p.ListObjects(t.Context(), in)
			if err != nil {
				t.Fatalf("page %d: ListObjects() error = %v, want none", page, err)
			}
			if out.Skipped != 1 {
				t.Fatalf("page %d: Skipped = %d, want 1 (over the whole bucket, outside Prefix too)", page, out.Skipped)
			}
			seen = append(seen, keysOf(out.Objects)...)
			if out.NextContinuationToken == "" {
				break
			}
			in.ContinuationToken = ptr(out.NextContinuationToken)
		}
		if !slices.Equal(seen, []string{"a/1", "a/2", "a/3"}) {
			t.Fatalf("listed %v, want the correct entries [a/1 a/2 a/3]", seen)
		}
	})
	t.Run("dir buckets", func(t *testing.T) {
		t.Parallel()
		p, root := newDirProvider(t, "")
		prefix := uniqueName("sk")
		for _, n := range []string{prefix + "-a", prefix + "-b", prefix + "-c", "zz-" + prefix} {
			mustCreateBucket(t, p, n)
		}
		if err := os.Remove(root + "/.gas/buckets/" + sha("zz-"+prefix)); err != nil {
			t.Fatalf("remove name record: %v", err)
		}
		in := &storage.ListBucketsInput{Prefix: ptr(prefix), MaxBuckets: ptr(int32(2))}
		for page := 1; ; page++ {
			out, err := p.ListBuckets(t.Context(), in)
			if err != nil {
				t.Fatalf("page %d: ListBuckets() error: %v", page, err)
			}
			if out.Skipped != 1 {
				t.Fatalf("page %d: Skipped = %d, want 1 (over every bucket, outside Prefix too)", page, out.Skipped)
			}
			if out.NextContinuationToken == "" {
				break
			}
			in.ContinuationToken = ptr(out.NextContinuationToken)
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

// Docs: modules/storage.mdx#pagination
// "`ListObjects` of a missing bucket is `storage.ErrBucketNotFound`."
func TestListObjectsMissingBucket(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		_, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{Bucket: ptr(uniqueName("nobucket"))})
		wantErr(t, b.name+" ListObjects(missing bucket)", err, storage.ErrBucketNotFound)
	}
}

// Docs: modules/storage.mdx#pagination
// "On s3, `ListObjects` uses ListObjectsV2 with URL encoding, so keys holding
// characters XML 1.0 cannot carry are listed intact"
func TestS3ListObjectsV2URLEncoding(t *testing.T) {
	t.Parallel()
	t.Run("request", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`))
		})
		if _, err := f.provider(t, "us-east-1", "bkt").ListObjects(t.Context(), &storage.ListObjectsInput{}); err != nil {
			t.Fatalf("ListObjects() error: %v", err)
		}
		q, _ := url.ParseQuery(strings.SplitN(f.paths()[0], "?", 2)[1])
		if q.Get("list-type") != "2" || q.Get("encoding-type") != "url" {
			t.Fatalf("ListObjects query = %v, want list-type=2 and encoding-type=url", q)
		}
	})
	t.Run("localstack", func(t *testing.T) {
		t.Parallel()
		b := s3Backend(t)
		keys := []string{"ctl\x01char", "tab\tkey", "plain"}
		putAll(t, b.p, keys)
		out, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{})
		if err != nil {
			t.Fatalf("ListObjects() error: %v", err)
		}
		if got, want := keysOf(out.Objects), slices.Sorted(slices.Values(keys)); !slices.Equal(got, want) {
			t.Fatalf("ListObjects() = %q, want %q intact", got, want)
		}
	})
}

// Docs: modules/storage.mdx#the-directory-backend
// "**Listing cost.** Every `ListObjects` call reads the metadata of every object
// in the bucket, so a page costs time in proportion to the bucket, not the
// page."
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
	if err := os.WriteFile(metaPath(root, bucket, "z/broken"), []byte("not json"), 0o600); err != nil {
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
			t.Fatalf("call %d: skip handler heard %d entries, want 1 (read on this call)", call, got)
		}
		if out.NextContinuationToken == "" {
			break
		}
		in.ContinuationToken = ptr(out.NextContinuationToken)
	}
}
