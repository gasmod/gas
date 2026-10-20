package s3_test

import (
	"slices"
	"testing"

	"github.com/gasmod/gas/storage"
)

// lsListAll walks every page of in and returns the object keys and common
// prefixes in order.
func lsListAll(t *testing.T, p storage.Provider, in storage.ListObjectsInput) (objs, pfx []string) {
	t.Helper()
	for range 100 {
		out, err := p.ListObjects(t.Context(), &in)
		if err != nil {
			t.Fatalf("ListObjects(%+v) error: %v", in, err)
		}
		for _, o := range out.Objects {
			objs = append(objs, o.Key)
		}
		pfx = append(pfx, out.CommonPrefixes...)
		if !out.IsTruncated {
			return objs, pfx
		}
		in.ContinuationToken = new(out.NextContinuationToken)
	}
	t.Fatal("ListObjects() does not end")
	return nil, nil
}

// TestLocalStack_ListObjects checks listing, delimiters, paging and key
// decoding against LocalStack, with keys that need URL or XML encoding.
//
// LocalStack 4.14 mishandles non-ASCII keys once a listing resumes: with
// encoding-type url it compares the encoded key to StartAfter, and a
// continuation token past such a key fails with a 500. S3 orders keys by
// their UTF-8 bytes in both cases, so the non-ASCII key is only checked in
// a listing that does not resume, in its own bucket.
func TestLocalStack_ListObjects(t *testing.T) {
	t.Parallel()
	p, bucket := lsProviderWithBucket(t, "ls-list")

	keys := []string{"a", "b/1", "b/2", "b/c/3", "c d", "c+d", "c%2Fd", "e\x01f", "q?x&y=z", "z/"}
	for _, k := range keys {
		lsPut(t, p, bucket, k, []byte("k"+k))
	}

	for _, maxKeys := range []*int32{nil, new(int32(2)), new(int32(5000))} {
		objs, pfx := lsListAll(t, p, storage.ListObjectsInput{MaxKeys: maxKeys})
		if want := slices.Sorted(slices.Values(keys)); !slices.Equal(objs, want) || pfx != nil {
			t.Errorf("ListObjects(MaxKeys %v) = %q, %q; want %q", maxKeys, objs, pfx, want)
		}

		objs, pfx = lsListAll(t, p, storage.ListObjectsInput{
			Delimiter: new("/"),
			MaxKeys:   maxKeys,
		})
		wantObjs := []string{"a", "c d", "c%2Fd", "c+d", "e\x01f", "q?x&y=z"}
		wantPfx := []string{"b/", "z/"}
		if !slices.Equal(objs, wantObjs) || !slices.Equal(pfx, wantPfx) {
			t.Errorf("ListObjects(delimiter /, MaxKeys %v) = %q, %q; want %q, %q",
				maxKeys, objs, pfx, wantObjs, wantPfx)
		}
	}

	objs, pfx := lsListAll(t, p, storage.ListObjectsInput{
		Prefix:    new("b/"),
		Delimiter: new("/"),
		MaxKeys:   new(int32(1)),
	})
	if !slices.Equal(objs, []string{"b/1", "b/2"}) || !slices.Equal(pfx, []string{"b/c/"}) {
		t.Errorf("ListObjects(prefix b/) = %q, %q; want [b/1 b/2], [b/c/]", objs, pfx)
	}

	objs, _ = lsListAll(t, p, storage.ListObjectsInput{StartAfter: new("c+d")})
	if want := []string{"e\x01f", "q?x&y=z", "z/"}; !slices.Equal(objs, want) {
		t.Errorf("ListObjects(start after c+d) = %q, want %q", objs, want)
	}

	objs, pfx = lsListAll(t, p, storage.ListObjectsInput{
		StartAfter: new("b/1"),
		Delimiter:  new("/"),
	})
	if want := []string{"c d", "c%2Fd", "c+d", "e\x01f", "q?x&y=z"}; !slices.Equal(objs, want) ||
		!slices.Equal(pfx, []string{"b/", "z/"}) {
		t.Errorf("ListObjects(start after b/1, delimiter /) = %q, %q; want %q, [b/ z/]",
			objs, pfx, want)
	}

	out, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{})
	if err != nil {
		t.Fatalf("ListObjects() error: %v", err)
	}
	for _, o := range out.Objects {
		if o.Size != int64(len("k"+o.Key)) || o.LastModified.IsZero() {
			t.Errorf("object %q = %+v, want Size %d and LastModified set", o.Key, o,
				len("k"+o.Key))
		}
	}

	up, upBucket := lsProviderWithBucket(t, "ls-list-utf8")
	for _, k := range []string{"z", "日本", "a"} {
		lsPut(t, up, upBucket, k, []byte("k"+k))
	}
	objs, _ = lsListAll(t, up, storage.ListObjectsInput{})
	if want := []string{"a", "z", "日本"}; !slices.Equal(objs, want) {
		t.Errorf("ListObjects() of non-ASCII keys = %q, want %q", objs, want)
	}
}

// TestLocalStack_ListObjectsErrors checks the sentinels ListObjects reports
// from LocalStack.
func TestLocalStack_ListObjectsErrors(t *testing.T) {
	t.Parallel()
	p, _ := lsProviderWithBucket(t, "ls-list-err")

	_, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{
		Bucket: new(uniqueBucket("ls-list-missing")),
	})
	assertIs(t, "ListObjects() of a missing bucket", err, storage.ErrBucketNotFound)
}

// TestLocalStack_ListBuckets checks ListBuckets by prefix and page against
// an account no other test uses, so the listing is predictable.
func TestLocalStack_ListBuckets(t *testing.T) {
	t.Parallel()
	const account = "000000000077"
	pfx := uniqueBucket("ls-lb")
	names := []string{pfx + "-b", pfx + "-a1", pfx + "-a2"}
	p := lsProviderAs(t, "us-east-1", names[0], account)
	for _, b := range names {
		if _, err := p.CreateBucket(
			t.Context(),
			&storage.CreateBucketInput{Bucket: b},
		); err != nil {
			t.Fatalf("CreateBucket(%s) error: %v", b, err)
		}
	}

	out, err := p.ListBuckets(t.Context(), &storage.ListBucketsInput{Prefix: new(pfx)})
	if err != nil {
		t.Fatalf("ListBuckets() error: %v", err)
	}
	got := make([]string, 0, len(out.Buckets))
	for _, b := range out.Buckets {
		got = append(got, b.Name)
		if b.CreationDate.IsZero() {
			t.Errorf("bucket %s has no CreationDate", b.Name)
		}
	}
	if want := slices.Sorted(slices.Values(names)); !slices.Equal(got, want) ||
		out.NextContinuationToken != "" {
		t.Errorf("ListBuckets(prefix) = %q, token %q; want %q in one page", got,
			out.NextContinuationToken, want)
	}

	in := storage.ListBucketsInput{Prefix: new(pfx + "-a"), MaxBuckets: new(int32(1))}
	var paged []string
	for range 5 {
		out, err := p.ListBuckets(t.Context(), &in)
		if err != nil {
			t.Fatalf("ListBuckets(%+v) error: %v", in, err)
		}
		for _, b := range out.Buckets {
			paged = append(paged, b.Name)
		}
		if out.NextContinuationToken == "" {
			break
		}
		in.ContinuationToken = new(out.NextContinuationToken)
	}
	if want := []string{pfx + "-a1", pfx + "-a2"}; !slices.Equal(paged, want) {
		t.Errorf("ListBuckets(prefix -a, one per page) = %q, want %q", paged, want)
	}
}
