package modulesstorage_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gasmod/gas/storage"
)

// nilInputCalls calls every Provider method with a nil input.
func nilInputCalls(p storage.Provider) map[string]error {
	ctx := context.Background()
	calls := map[string]error{}
	_, calls["CreateBucket"] = p.CreateBucket(ctx, nil)
	_, calls["HeadBucket"] = p.HeadBucket(ctx, nil)
	calls["DeleteBucket"] = p.DeleteBucket(ctx, nil)
	_, calls["ListBuckets"] = p.ListBuckets(ctx, nil)
	_, calls["PutObject"] = p.PutObject(ctx, nil)
	_, calls["GetObject"] = p.GetObject(ctx, nil)
	_, calls["HeadObject"] = p.HeadObject(ctx, nil)
	_, calls["DeleteObject"] = p.DeleteObject(ctx, nil)
	_, calls["ListObjects"] = p.ListObjects(ctx, nil)
	return calls
}

// Docs: modules/storage.mdx#inputs
// "Every method takes a pointer to an input struct; a nil input fails with
// `storage.ErrInputRequired`."
func TestNilInput(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	d, _ := newDirProvider(t, "bkt")
	for name, p := range map[string]storage.Provider{"s3": f.provider(t, "us-east-1", "bkt"), "dir": d} {
		for m, err := range nilInputCalls(p) {
			wantErr(t, name+" "+m+"(nil)", err, storage.ErrInputRequired)
		}
	}
	pre := f.presigner(t, "us-east-1", "bkt")
	_, err := pre.PresignUploadURL(t.Context(), nil)
	wantErr(t, "PresignUploadURL(nil)", err, storage.ErrInputRequired)
	_, err = pre.PresignDownloadURL(t.Context(), nil)
	wantErr(t, "PresignDownloadURL(nil)", err, storage.ErrInputRequired)
	if n := f.requests.Load(); n != 0 {
		t.Fatalf("nil inputs sent %d requests, want 0", n)
	}
}

// keyChecks runs every object method, and both presign methods when pre is
// set, with key and returns each error.
func keyChecks(p storage.Provider, pre storage.Presigner, in storage.BaseObjectInput) map[string]error {
	ctx := context.Background()
	calls := map[string]error{}
	_, calls["PutObject"] = p.PutObject(ctx, &storage.PutObjectInput{Body: strings.NewReader("x"), BaseObjectInput: in})
	_, calls["GetObject"] = p.GetObject(ctx, &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: in}})
	_, calls["HeadObject"] = p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: in})
	_, calls["DeleteObject"] = p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: in})
	if pre != nil {
		_, calls["PresignUploadURL"] = pre.PresignUploadURL(ctx, &storage.PresignUploadURLInput{
			PutObjectInput: storage.PutObjectInput{BaseObjectInput: in},
		})
		_, calls["PresignDownloadURL"] = pre.PresignDownloadURL(ctx, &storage.PresignDownloadURLInput{
			GetObjectInput: storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: in}},
		})
	}
	return calls
}

// Docs: modules/storage.mdx#inputs
// "Every backend rejects, with `storage.ErrInvalidKey`, an empty key, a key
// longer than 1024 bytes, a key that is not valid UTF-8, and a key whose last
// path element is `.` or that cleans to `.`."
// Docs: modules/storage.mdx#inputs
// "Input is validated before anything is sent to the backend."
func TestInvalidKeys(t *testing.T) {
	t.Parallel()
	bad := map[string]string{
		"empty":            "",
		"1025 bytes":       strings.Repeat("k", 1025),
		"invalid UTF-8":    "a\xffb",
		"last element .":   "a/.",
		"bare .":           ".",
		"cleans to . (..)": "a/..",
		"cleans to . (./)": "./",
		"cleans to . deep": "a/b/../..",
	}
	f := newFakeS3(t, nil)
	d, _ := newDirProvider(t, "bkt")
	type target struct {
		p   storage.Provider
		pre storage.Presigner
	}
	for name, tg := range map[string]target{
		"s3":  {f.provider(t, "us-east-1", "bkt"), f.presigner(t, "us-east-1", "bkt")},
		"dir": {d, nil},
	} {
		for kind, key := range bad {
			for m, err := range keyChecks(tg.p, tg.pre, obj(key)) {
				wantErr(t, name+" "+m+" key "+kind, err, storage.ErrInvalidKey)
			}
		}
	}
	if n := f.requests.Load(); n != 0 {
		t.Fatalf("invalid keys sent %d requests, want 0", n)
	}
}

// Docs: modules/storage.mdx#inputs
// "Only `/` separates path elements, on every platform, as on S3: a backslash
// is an ordinary byte, so `a\.` is valid and `a/.` is not."
// Docs: modules/storage.mdx#inputs
// "a key longer than 1024 bytes"
// The boundary itself, 1024 bytes, is accepted.
func TestValidKeysRoundTrip(t *testing.T) {
	t.Parallel()
	keys := []string{`a\.`, `a\b\.`, strings.Repeat("k", 1024), "a/b", "./a", "a/./b"}
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			for i, k := range keys {
				body := string(rune('A' + i))
				mustPut(t, b.p, k, body)
				if got := readObject(t, b.p, k); got != body {
					t.Errorf("key %.20q read back %q, want %q", k, got, body)
				}
			}
			_, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{Body: strings.NewReader("x"), BaseObjectInput: obj("a/.")})
			wantErr(t, "PutObject(a/.)", err, storage.ErrInvalidKey)
		})
	}
}

// Docs: modules/storage.mdx#inputs
// "The key is checked before the bucket."
func TestKeyCheckedBeforeBucket(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	d, _ := newDirProvider(t, "")
	in := storage.BaseObjectInput{Key: "", Bucket: ptr("Bad_Bucket!")}
	for name, p := range map[string]storage.Provider{"s3": f.provider(t, "us-east-1", ""), "dir": d} {
		var pre storage.Presigner
		if name == "s3" {
			pre = f.presigner(t, "us-east-1", "")
		}
		for m, err := range keyChecks(p, pre, in) {
			wantErr(t, name+" "+m, err, storage.ErrInvalidKey)
			if errors.Is(err, storage.ErrInvalidBucket) {
				t.Errorf("%s %s error = %v, also matches ErrInvalidBucket, want the key reported", name, m, err)
			}
		}
	}
}

// Docs: modules/storage.mdx#inputs
// "`Bucket` is optional. Nil selects the configured bucket (`storage.bucket`)."
func TestNilBucketMeansConfigured(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			mustPut(t, b.p, "k", "x")
			out, err := b.p.ListObjects(t.Context(), &storage.ListObjectsInput{Bucket: ptr(b.bucket)})
			if err != nil {
				t.Fatalf("ListObjects(%s) error: %v", b.bucket, err)
			}
			if got := keysOf(out.Objects); len(got) != 1 || got[0] != "k" {
				t.Fatalf("objects in configured bucket = %v, want [k]", got)
			}
		})
	}
}

// Docs: modules/storage.mdx#inputs
// "A non-nil pointer must name a valid S3 bucket name; an empty string is not a
// fallback. An invalid bucket, or a nil `Bucket` with no valid configured
// bucket, fails with `storage.ErrInvalidBucket`."
func TestInvalidBucketInputs(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	dConfigured, _ := newDirProvider(t, "configured")
	dNone, _ := newDirProvider(t, "")
	mustCreateBucket(t, dConfigured, "configured")
	type target struct {
		p   storage.Provider
		pre storage.Presigner
	}
	cases := map[string]struct {
		tg target
		in storage.BaseObjectInput
	}{
		"s3 empty string":   {target{f.provider(t, "us-east-1", "configured"), f.presigner(t, "us-east-1", "configured")}, storage.BaseObjectInput{Key: "k", Bucket: ptr("")}},
		"s3 invalid":        {target{f.provider(t, "us-east-1", "configured"), f.presigner(t, "us-east-1", "configured")}, storage.BaseObjectInput{Key: "k", Bucket: ptr("Bad_Bucket!")}},
		"s3 nil, none set":  {target{f.provider(t, "us-east-1", ""), f.presigner(t, "us-east-1", "")}, storage.BaseObjectInput{Key: "k"}},
		"dir empty string":  {target{dConfigured, nil}, storage.BaseObjectInput{Key: "k", Bucket: ptr("")}},
		"dir invalid":       {target{dConfigured, nil}, storage.BaseObjectInput{Key: "k", Bucket: ptr("ab")}},
		"dir nil, none set": {target{dNone, nil}, storage.BaseObjectInput{Key: "k"}},
	}
	for name, c := range cases {
		for m, err := range keyChecks(c.tg.p, c.tg.pre, c.in) {
			wantErr(t, name+" "+m, err, storage.ErrInvalidBucket)
		}
		_, err := c.tg.p.ListObjects(t.Context(), &storage.ListObjectsInput{Bucket: c.in.Bucket})
		wantErr(t, name+" ListObjects", err, storage.ErrInvalidBucket)
	}
	if n := f.requests.Load(); n != 0 {
		t.Fatalf("invalid buckets sent %d requests, want 0", n)
	}
	// The empty string did not fall back: nothing was written to the
	// configured bucket.
	out, err := dConfigured.ListObjects(t.Context(), &storage.ListObjectsInput{})
	if err != nil || len(out.Objects) != 0 {
		t.Fatalf("configured bucket after rejected inputs = %v, %v, want empty", out, err)
	}
}

// Docs: modules/storage.mdx#operations
// "The bucket methods take the bucket name as a plain `Bucket` string, and the
// configured bucket is not a fallback for them."
func TestBucketMethodsNoFallback(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	d, _ := newDirProvider(t, "configured")
	mustCreateBucket(t, d, "configured")
	for name, p := range map[string]storage.Provider{"s3": f.provider(t, "us-east-1", "configured"), "dir": d} {
		_, err := p.CreateBucket(t.Context(), &storage.CreateBucketInput{})
		wantErr(t, name+" CreateBucket(\"\")", err, storage.ErrInvalidBucket)
		_, err = p.HeadBucket(t.Context(), &storage.HeadBucketInput{})
		wantErr(t, name+" HeadBucket(\"\")", err, storage.ErrInvalidBucket)
		err = p.DeleteBucket(t.Context(), &storage.DeleteBucketInput{})
		wantErr(t, name+" DeleteBucket(\"\")", err, storage.ErrInvalidBucket)
	}
	if n := f.requests.Load(); n != 0 {
		t.Fatalf("bucket methods with an empty name sent %d requests, want 0", n)
	}
	if _, err := d.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: "configured"}); err != nil {
		t.Fatalf("configured bucket after DeleteBucket(\"\") error = %v, want it kept", err)
	}
}
