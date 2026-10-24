package modulesstorage_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas/storage"
	storagedir "github.com/gasmod/gas/storage/dir"
	s3svc "github.com/gasmod/gas/storage/s3"
)

// opCalls calls every Provider method so that each fails: the bucket
// "missing-bkt" does not exist on the dir backend, and the fake S3 answers 404.
func opCalls(p storage.Provider) map[string]error {
	ctx := context.Background()
	in := storage.BaseObjectInput{Bucket: ptr("missing-bkt"), Key: "k"}
	calls := map[string]error{}
	_, calls["HeadBucket"] = p.HeadBucket(ctx, &storage.HeadBucketInput{Bucket: "missing-bkt"})
	calls["DeleteBucket"] = p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: "missing-bkt"})
	_, calls["ListBuckets"] = p.ListBuckets(ctx, &storage.ListBucketsInput{MaxBuckets: ptr(int32(0))})
	_, calls["CreateBucket"] = p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "Bad_Bucket"})
	_, calls["PutObject"] = p.PutObject(ctx, &storage.PutObjectInput{Body: strings.NewReader("x"), BaseObjectInput: in})
	_, calls["GetObject"] = p.GetObject(ctx, &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: in}})
	_, calls["HeadObject"] = p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: in})
	_, calls["DeleteObject"] = p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: in, Version: ptr("v")})
	_, calls["ListObjects"] = p.ListObjects(ctx, &storage.ListObjectsInput{Bucket: in.Bucket})
	return calls
}

// opPrefix matches the documented form "gas/storage/<backend>/<component>: <operation>: ".
var opPrefix = regexp.MustCompile(`^gas/storage/(s3|dir)/(provider|presigner): [a-z][a-z ]*: `)

// Docs: modules/storage.mdx#errors
// "Every error a method returns, except `ErrNotInitialized`, names the backend
// and the operation, in the form
// `gas/storage/<backend>/<component>: <operation>: <cause>`, for example
// `gas/storage/s3/provider: get: ...` or `gas/storage/dir/provider: put: ...`,
// and wraps one of the sentinels in `storage` when one applies."
func TestErrorMessageForm(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s3Error(w, http.StatusNotFound, "NoSuchBucket")
	})
	d, _ := newDirProvider(t, "")
	for name, p := range map[string]storage.Provider{"s3": f.provider(t, "us-east-1", ""), "dir": d} {
		for m, err := range opCalls(p) {
			if err == nil {
				t.Errorf("%s %s error = nil, want an error", name, m)
				continue
			}
			if !strings.HasPrefix(err.Error(), "gas/storage/"+name+"/provider: ") || !opPrefix.MatchString(err.Error()) {
				t.Errorf("%s %s error = %q, want gas/storage/%s/provider: <operation>: <cause>", name, m, err, name)
			}
			matched := false
			for _, s := range storageSentinels {
				if errors.Is(err, s) {
					matched = true
				}
			}
			if !matched {
				t.Errorf("%s %s error = %q wraps no storage sentinel", name, m, err)
			}
		}
	}

	// The two examples the page gives.
	_, err := f.provider(t, "us-east-1", "bkt").GetObject(t.Context(), getIn("k"))
	if err == nil || !strings.HasPrefix(err.Error(), "gas/storage/s3/provider: get: ") {
		t.Errorf("s3 GetObject error = %v, want prefix gas/storage/s3/provider: get: ", err)
	}
	_, err = d.PutObject(t.Context(), &storage.PutObjectInput{BaseObjectInput: storage.BaseObjectInput{Bucket: ptr("missing-bkt"), Key: "k"}})
	if err == nil || !strings.HasPrefix(err.Error(), "gas/storage/dir/provider: put: ") {
		t.Errorf("dir PutObject error = %v, want prefix gas/storage/dir/provider: put: ", err)
	}

	pre := f.presigner(t, "us-east-1", "bkt")
	_, err = pre.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{PutObjectInput: storage.PutObjectInput{BaseObjectInput: obj("")}})
	if err == nil || !opPrefix.MatchString(err.Error()) || !strings.HasPrefix(err.Error(), "gas/storage/s3/presigner: ") {
		t.Errorf("PresignUploadURL error = %v, want gas/storage/s3/presigner: <operation>: <cause>", err)
	}
	_, err = pre.PresignDownloadURL(t.Context(), &storage.PresignDownloadURLInput{GetObjectInput: *getIn("")})
	if err == nil || !opPrefix.MatchString(err.Error()) || !strings.HasPrefix(err.Error(), "gas/storage/s3/presigner: ") {
		t.Errorf("PresignDownloadURL error = %v, want gas/storage/s3/presigner: <operation>: <cause>", err)
	}
}

// Docs: modules/storage.mdx#errors
// "Constructor errors carry the component but no operation."
func TestConstructorErrorsNoOperation(t *testing.T) {
	t.Parallel()
	cfg := mockConfig(t, map[string]any{"region": "us-east-1", "accessKeyID": "a", "secretAccessKey": "s"})
	_, e1 := s3svc.NewProvider(nil)(cfg)
	_, e2 := s3svc.NewPresigner(nil)(cfg)
	_, e3 := storagedir.NewProvider(nil)(cfg)
	_, e4 := storagedir.NewProvider()(cfg)
	for want, err := range map[string]error{
		"gas/storage/s3/provider: storage: invalid option: option 0 is nil":  e1,
		"gas/storage/s3/presigner: storage: invalid option: option 0 is nil": e2,
		"gas/storage/dir/provider: storage: invalid option: option 0 is nil": e3,
	} {
		if err == nil || err.Error() != want {
			t.Errorf("constructor error = %v, want %q", err, want)
		}
	}
	if e4 == nil || !strings.HasPrefix(e4.Error(), "gas/storage/dir/provider: storage: ") {
		t.Errorf("dir constructor without a root error = %v, want the component then the cause", e4)
	}
}

// Docs: modules/storage.mdx#errors
// "Classify failures with `errors.Is`. Backend causes, such as an AWS SDK error,
// stay in the chain for `errors.As`."
func TestBackendCauseInChain(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request) {
		s3Error(w, http.StatusNotFound, "NoSuchKey")
	})
	_, err := f.provider(t, "us-east-1", "bkt").GetObject(t.Context(), getIn("k"))
	wantErr(t, "GetObject(404)", err, storage.ErrKeyNotFound)
	var apiErr interface{ ErrorCode() string }
	if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "NoSuchKey" {
		t.Fatalf("errors.As(err, API error) on %v = %v, want the SDK's NoSuchKey error in the chain", err, apiErr)
	}
}

// Docs: modules/storage.mdx#errors
// "The storage packages put no object key or metadata value into the messages
// they build"
// The s3 errors checked are ones raised before any request is sent, so the
// AWS SDK, whose errors may hold the URL, plays no part.
func TestErrorsHoldNoKeyOrMetadata(t *testing.T) {
	t.Parallel()
	const key, meta = "SECRETKEY1", "SECRETMETA1"
	headers := storage.ObjectHeaders{Metadata: map[string]string{"m": meta}}
	check := func(what string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s error = nil, want an error", what)
			return
		}
		if strings.Contains(err.Error(), key) || strings.Contains(err.Error(), meta) {
			t.Errorf("%s error = %q, holds the key or a metadata value", what, err)
		}
	}

	t.Run("dir", func(t *testing.T) {
		t.Parallel()
		bucket := uniqueName("leak")
		p, root := newDirProvider(t, bucket)
		ctx := t.Context()
		_, err := p.HeadObject(ctx, headIn(key))
		check("HeadObject(missing bucket)", err)
		mustCreateBucket(t, p, bucket)
		_, err = p.GetObject(ctx, getIn(key))
		check("GetObject(missing)", err)
		_, err = p.HeadObject(ctx, headIn(key))
		check("HeadObject(missing)", err)
		_, err = p.PutObject(ctx, &storage.PutObjectInput{Body: strings.NewReader("x"), ContentLength: ptr(int64(3)), ObjectHeaders: headers, BaseObjectInput: obj(key)})
		check("PutObject(mismatch)", err)
		if _, err := p.PutObject(ctx, &storage.PutObjectInput{Body: strings.NewReader("x"), ObjectHeaders: headers, BaseObjectInput: obj(key)}); err != nil {
			t.Fatal(err)
		}
		_, err = p.PutObject(ctx, &storage.PutObjectInput{Body: strings.NewReader("x"), ObjectHeaders: headers,
			WriteCondition: storage.WriteCondition{IfNoneMatch: ptr("*")}, BaseObjectInput: obj(key)})
		check("PutObject(precondition)", err)
		_, err = p.HeadObject(ctx, &storage.HeadObjectInput{Version: ptr("v"), BaseObjectInput: obj(key)})
		check("HeadObject(Version)", err)
		_, err = p.GetObject(ctx, &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{Range: ptr("bytes=9-"), BaseObjectInput: obj(key)}})
		check("GetObject(range)", err)
		if err := os.WriteFile(metaPath(root, bucket, key), []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = p.HeadObject(ctx, headIn(key))
		check("HeadObject(corrupt)", err)
		_, err = p.GetObject(ctx, getIn(key))
		check("GetObject(corrupt)", err)
	})
	t.Run("s3 before sending", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		p := f.provider(t, "us-east-1", "bkt")
		ctx := t.Context()
		_, err := p.PutObject(ctx, &storage.PutObjectInput{Body: strings.NewReader("x"), ObjectHeaders: storage.ObjectHeaders{
			Expires: ptr("not a date"), Metadata: headers.Metadata,
		}, BaseObjectInput: obj(key)})
		check("PutObject(Expires)", err)
		_, err = p.PutObject(ctx, &storage.PutObjectInput{Body: strings.NewReader("x"), ObjectHeaders: headers,
			WriteCondition: storage.WriteCondition{IfNoneMatch: ptr("etag")}, BaseObjectInput: obj(key)})
		check("PutObject(condition)", err)
		_, err = p.GetObject(ctx, &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{
			BaseObjectInput: storage.BaseObjectInput{Key: key, Bucket: ptr("Bad_Bucket")},
		}})
		check("GetObject(invalid bucket)", err)
		pre := f.presigner(t, "us-east-1", "bkt")
		_, err = pre.PresignUploadURL(ctx, &storage.PresignUploadURLInput{TTL: ptr(-time.Second), PutObjectInput: storage.PutObjectInput{
			ObjectHeaders: headers, BaseObjectInput: obj(key),
		}})
		check("PresignUploadURL(TTL)", err)
		if n := f.requests.Load(); n != 0 {
			t.Fatalf("checks sent %d requests, want 0", n)
		}
	})
}

// Docs: modules/storage.mdx#errors
// "Each sentinel is distinct: none matches another."
func TestSentinelsDistinct(t *testing.T) {
	t.Parallel()
	for i, a := range storageSentinels {
		for j, b := range storageSentinels {
			if i != j && errors.Is(a, b) {
				t.Errorf("%v matches %v", a, b)
			}
		}
	}
}

// Docs: modules/storage.mdx#errors
// "When a `PutObject` has both a missing bucket and a mismatched body, the error
// may be either `ErrBucketNotFound` or `ErrContentLengthMismatch`, depending on
// the backend and the body's size"
func TestMissingBucketAndMismatch(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		for _, body := range []string{"ab", strings.Repeat("x", 1<<16)} {
			_, err := b.p.PutObject(t.Context(), &storage.PutObjectInput{
				Body: strings.NewReader(body), ContentLength: ptr(int64(5)),
				BaseObjectInput: storage.BaseObjectInput{Bucket: ptr(uniqueName("nobucket")), Key: "k"},
			})
			if !errors.Is(err, storage.ErrBucketNotFound) && !errors.Is(err, storage.ErrContentLengthMismatch) {
				t.Errorf("%s PutObject(missing bucket, %d-byte body for length 5) error = %v, want one of the two", b.name, len(body), err)
			}
		}
	}
}

// Docs: modules/storage.mdx#errors
// "`ErrKeyNotFound` | `GetObject` of a missing key, or a `PutObject` with
// `IfMatch` of a missing key"
// Covered by TestGetObjectAndHeadObject and TestIfMatch; checked again here on
// both backends in one place.
func TestErrKeyNotFoundTable(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		_, err := b.p.GetObject(t.Context(), getIn("missing"))
		wantErr(t, b.name+" GetObject(missing)", err, storage.ErrKeyNotFound)
		_, err = b.p.PutObject(t.Context(), &storage.PutObjectInput{
			Body: strings.NewReader("x"), WriteCondition: storage.WriteCondition{IfMatch: ptr("abc")}, BaseObjectInput: obj("missing"),
		})
		wantErr(t, b.name+" PutObject(IfMatch, missing)", err, storage.ErrKeyNotFound)
	}
}

// Docs: modules/storage.mdx#errors
// "`ErrNotFound` | `HeadObject` of a missing object; on S3 also a missing
// bucket, because HEAD responses carry no error code"
func TestErrNotFoundS3MissingBucket(t *testing.T) {
	t.Parallel()
	b := s3Backend(t)
	_, err := b.p.HeadObject(t.Context(), &storage.HeadObjectInput{
		BaseObjectInput: storage.BaseObjectInput{Bucket: ptr(uniqueName("nobucket")), Key: "k"},
	})
	wantErr(t, "s3 HeadObject(missing bucket)", err, storage.ErrNotFound)
}

// Docs: modules/storage.mdx#errors
// "`ErrBucketNotFound` | An object operation, `ListObjects`, `HeadBucket` or
// `DeleteBucket` on a missing bucket (S3 `HeadObject` excepted)"
func TestBucketNotFound(t *testing.T) {
	t.Parallel()
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()
			missing := uniqueName("nobucket")
			in := storage.BaseObjectInput{Bucket: ptr(missing), Key: "k"}
			ctx := t.Context()
			_, err := b.p.PutObject(ctx, &storage.PutObjectInput{Body: strings.NewReader("x"), BaseObjectInput: in})
			wantErr(t, "PutObject", err, storage.ErrBucketNotFound)
			_, err = b.p.GetObject(ctx, &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: in}})
			wantErr(t, "GetObject", err, storage.ErrBucketNotFound)
			_, err = b.p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: in})
			wantErr(t, "DeleteObject", err, storage.ErrBucketNotFound)
			if b.s3 == nil {
				_, err = b.p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: in})
				wantErr(t, "HeadObject", err, storage.ErrBucketNotFound)
			}
			_, err = b.p.ListObjects(ctx, &storage.ListObjectsInput{Bucket: ptr(missing)})
			wantErr(t, "ListObjects", err, storage.ErrBucketNotFound)
			_, err = b.p.HeadBucket(ctx, &storage.HeadBucketInput{Bucket: missing})
			wantErr(t, "HeadBucket", err, storage.ErrBucketNotFound)
			err = b.p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: missing})
			wantErr(t, "DeleteBucket", err, storage.ErrBucketNotFound)
		})
	}
}

// Docs: modules/storage.mdx#errors
// "`ErrVersionNotFound` | S3 `GetObject` of a version that does not exist. S3
// `HeadObject` reports it as `ErrNotFound`"
func TestVersionNotFoundS3(t *testing.T) {
	t.Parallel()
	b := s3Backend(t)
	enableVersioning(t, b)
	mustPut(t, b.p, "a", "a")
	other := mustPut(t, b.p, "b", "b") // a real version ID that key "a" does not have
	if other.Version == "" {
		t.Fatalf("PutObject on a versioned bucket returned no Version")
	}
	in := storage.HeadObjectInput{Version: ptr(other.Version), BaseObjectInput: obj("a")}
	_, err := b.p.GetObject(t.Context(), &storage.GetObjectInput{HeadObjectInput: in})
	wantErr(t, "GetObject(unknown version)", err, storage.ErrVersionNotFound)
	_, err = b.p.HeadObject(t.Context(), &in)
	wantErr(t, "HeadObject(unknown version)", err, storage.ErrNotFound)
}

// Docs: modules/storage.mdx#errors
// "`ErrNotInitialized` | Every method of a provider not built by its
// constructor, such as a zero value; on s3 also a nil pointer"
// Assumption: "on s3" covers the Presigner as well as the Provider.
func TestNotInitialized(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	o := obj("k")
	for name, p := range map[string]storage.Provider{
		"s3 zero": &s3svc.Provider{}, "s3 nil": (*s3svc.Provider)(nil), "dir zero": &storagedir.Provider{},
	} {
		calls := map[string]error{}
		_, calls["CreateBucket"] = p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "bkt"})
		_, calls["HeadBucket"] = p.HeadBucket(ctx, &storage.HeadBucketInput{Bucket: "bkt"})
		calls["DeleteBucket"] = p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: "bkt"})
		_, calls["ListBuckets"] = p.ListBuckets(ctx, &storage.ListBucketsInput{})
		_, calls["HeadObject"] = p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: o})
		_, calls["PutObject"] = p.PutObject(ctx, &storage.PutObjectInput{Body: strings.NewReader("x"), BaseObjectInput: o})
		_, calls["GetObject"] = p.GetObject(ctx, getIn("k"))
		_, calls["DeleteObject"] = p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: o})
		_, calls["ListObjects"] = p.ListObjects(ctx, &storage.ListObjectsInput{})
		for m, err := range calls {
			if !errors.Is(err, storage.ErrNotInitialized) {
				t.Errorf("%s %s error = %v, want ErrNotInitialized", name, m, err)
			}
		}
	}
	for name, pre := range map[string]*s3svc.Presigner{"zero": {}, "nil": nil} {
		_, err := pre.PresignUploadURL(ctx, &storage.PresignUploadURLInput{PutObjectInput: storage.PutObjectInput{BaseObjectInput: o}})
		wantErr(t, name+" s3.Presigner PresignUploadURL", err, storage.ErrNotInitialized)
		_, err = pre.PresignDownloadURL(ctx, &storage.PresignDownloadURLInput{GetObjectInput: *getIn("k")})
		wantErr(t, name+" s3.Presigner PresignDownloadURL", err, storage.ErrNotInitialized)
	}
}
