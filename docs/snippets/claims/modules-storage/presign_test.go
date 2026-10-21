package modulesstorage_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/gasmod/gas/storage"
	s3svc "github.com/gasmod/gas/storage/s3"
)

// Docs: modules/storage.mdx#operations
// "`PresignUploadURL(ctx, *PresignUploadURLInput)` | `*PresignURLResponse` |
// URL, method, and the headers the client must send"
// Docs: modules/storage.mdx#presigned-urls
// "Presigned URLs let the browser transfer bytes straight to S3, so large files
// never pass through your server."
func TestPresignUploadURLWorks(t *testing.T) {
	t.Parallel()
	b := s3Backend(t)
	pre := newS3Presigner(t, "us-east-1", b.bucket)
	resp, err := pre.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{PutObjectInput: storage.PutObjectInput{
		ObjectHeaders:   storage.ObjectHeaders{ContentType: ptr("image/png")},
		BaseObjectInput: obj("up.png"),
	}})
	if err != nil {
		t.Fatalf("PresignUploadURL() error: %v", err)
	}
	if resp.URL == "" || resp.Method != http.MethodPut || resp.SignedHeader.Get("Content-Type") != "image/png" {
		t.Fatalf("PresignUploadURL() = %+v, want a URL, PUT and Content-Type among the headers", resp)
	}
	if status, msg := doSigned(t, resp, strings.NewReader("png bytes")); status != http.StatusOK {
		t.Fatalf("upload via presigned URL status = %d (%s), want 200", status, msg)
	}
	if got := readObject(t, b.p, "up.png"); got != "png bytes" {
		t.Fatalf("uploaded body = %q, want %q", got, "png bytes")
	}
}

// Docs: modules/storage.mdx#operations
// "`PresignDownloadURL(ctx, *PresignDownloadURLInput)` | `*PresignURLResponse` |
// URL, method, and signed headers for a GET"
func TestPresignDownloadURLWorks(t *testing.T) {
	t.Parallel()
	b := s3Backend(t)
	mustPut(t, b.p, "down", "download me")
	pre := newS3Presigner(t, "us-east-1", b.bucket)
	resp, err := pre.PresignDownloadURL(t.Context(), &storage.PresignDownloadURLInput{GetObjectInput: *getIn("down")})
	if err != nil {
		t.Fatalf("PresignDownloadURL() error: %v", err)
	}
	if resp.URL == "" || resp.Method != http.MethodGet || resp.SignedHeader == nil {
		t.Fatalf("PresignDownloadURL() = %+v, want a URL, GET and signed headers", resp)
	}
	if status, body := doSigned(t, resp, nil); status != http.StatusOK || body != "download me" {
		t.Fatalf("download via presigned URL = %d %q, want 200 %q", status, body, "download me")
	}
}

// Docs: modules/storage.mdx#presigned-urls
// "Only the s3 backend has a `Presigner`."
func TestOnlyS3HasPresigner(t *testing.T) {
	t.Parallel()
	d, _ := newDirProvider(t, "")
	if _, ok := any(d).(storage.Presigner); ok {
		t.Fatalf("*dir.Provider implements storage.Presigner")
	}
	var _ storage.Presigner = newFakeS3(t, nil).presigner(t, "us-east-1", "bkt")
}

// Docs: modules/storage.mdx#presigned-urls
// "Signing is local: no request is sent to S3, so a presigner never reports a
// missing key or bucket. Input is validated as for `Provider`."
func TestPresignIsLocal(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	pre := f.presigner(t, "us-east-1", "bkt")
	presignBoth(t, pre, nil)
	other := storage.BaseObjectInput{Bucket: ptr("no-such-bucket"), Key: "no-such-key"}
	if _, err := pre.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{
		PutObjectInput: storage.PutObjectInput{BaseObjectInput: other},
	}); err != nil {
		t.Fatalf("PresignUploadURL(missing bucket and key) error = %v, want nil", err)
	}
	if _, err := pre.PresignDownloadURL(t.Context(), &storage.PresignDownloadURLInput{
		GetObjectInput: storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: other}},
	}); err != nil {
		t.Fatalf("PresignDownloadURL(missing bucket and key) error = %v, want nil", err)
	}
	if n := f.requests.Load(); n != 0 {
		t.Fatalf("presigning sent %d requests, want 0", n)
	}
}

// Docs: modules/storage.mdx#presigned-urls
// "A nil or zero TTL signs 15 minutes. A TTL that is negative, under one second,
// or over seven days fails with `storage.ErrInvalidTTL` and nothing is signed.
// The expiry is written in whole seconds, truncating any fraction, so 1.9
// seconds signs as one."
func TestPresignTTL(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	pre := f.presigner(t, "us-east-1", "bkt")
	for ttl, want := range map[*time.Duration]string{
		nil:                                    "900",
		ptr(time.Duration(0)):                  "900",
		ptr(1900 * time.Millisecond):           "1",
		ptr(time.Second):                       "1",
		ptr(90*time.Second + time.Millisecond): "90",
		ptr(7 * 24 * time.Hour):                "604800",
	} {
		up, down := presignBoth(t, pre, ttl)
		for kind, r := range map[string]*storage.PresignURLResponse{"upload": up, "download": down} {
			if got := query(t, r.URL).Get("X-Amz-Expires"); got != want {
				t.Errorf("TTL %v %s: X-Amz-Expires = %q, want %s", ttl, kind, got, want)
			}
		}
	}
	for _, ttl := range []time.Duration{-time.Second, -time.Nanosecond, 999 * time.Millisecond, time.Nanosecond, 7*24*time.Hour + time.Nanosecond} {
		up, err := pre.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{
			TTL: ptr(ttl), PutObjectInput: storage.PutObjectInput{BaseObjectInput: obj("k")},
		})
		wantErr(t, "PresignUploadURL(TTL "+ttl.String()+")", err, storage.ErrInvalidTTL)
		if up != nil {
			t.Errorf("PresignUploadURL(TTL %v) = %+v, want nothing signed", ttl, up)
		}
		down, err := pre.PresignDownloadURL(t.Context(), &storage.PresignDownloadURLInput{TTL: ptr(ttl), GetObjectInput: *getIn("k")})
		wantErr(t, "PresignDownloadURL(TTL "+ttl.String()+")", err, storage.ErrInvalidTTL)
		if down != nil {
			t.Errorf("PresignDownloadURL(TTL %v) = %+v, want nothing signed", ttl, down)
		}
	}
}

// Docs: modules/storage.mdx#presigned-urls
// "The TTL is passed to the SDK explicitly, so it also overrides a default
// expiry configured on a presign client given with `WithPresignClient`."
func TestPresignTTLOverridesClientDefault(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	pc := awss3.NewPresignClient(f.rawClient(), awss3.WithPresignExpires(time.Hour))
	pre, err := newPresignerWith(t, f, pc)
	if err != nil {
		t.Fatalf("NewPresigner(WithPresignClient) error: %v", err)
	}
	for ttl, want := range map[*time.Duration]string{nil: "900", ptr(2 * time.Minute): "120"} {
		up, down := presignBoth(t, pre, ttl)
		for kind, r := range map[string]*storage.PresignURLResponse{"upload": up, "download": down} {
			if got := query(t, r.URL).Get("X-Amz-Expires"); got != want {
				t.Errorf("TTL %v %s: X-Amz-Expires = %q, want %s over the client's 3600", ttl, kind, got, want)
			}
		}
	}
}

// Docs: modules/storage.mdx#presigned-urls
// "`PresignUploadURL` signs `ContentLength` and every header of `ObjectHeaders`
// that is set, and a `WriteCondition`; the client must send the returned
// `Method` and every header in `SignedHeader` unchanged. The embedded `Body` is
// ignored. `Expires` must be an IMF-fixdate, as for `PutObject`."
func TestPresignUploadSignsHeaders(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	pre := f.presigner(t, "us-east-1", "bkt")
	body := &failReader{}
	resp, err := pre.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{PutObjectInput: storage.PutObjectInput{
		Body:          body,
		ContentLength: ptr(int64(9)),
		ObjectHeaders: storage.ObjectHeaders{
			ContentType: ptr("image/png"), CacheControl: ptr("no-cache"), ContentDisposition: ptr("inline"),
			ContentEncoding: ptr("identity"), ContentLanguage: ptr("en"), Expires: ptr("Wed, 21 Oct 2026 07:28:00 GMT"),
			Metadata: map[string]string{"owner": "alice"},
		},
		WriteCondition:  storage.WriteCondition{IfNoneMatch: ptr("*")},
		BaseObjectInput: obj("signed"),
	}})
	if err != nil {
		t.Fatalf("PresignUploadURL() error: %v", err)
	}
	signed := strings.Split(query(t, resp.URL).Get("X-Amz-SignedHeaders"), ";")
	for _, h := range []string{"content-length", "content-type", "cache-control", "content-disposition",
		"content-encoding", "content-language", "expires", "x-amz-meta-owner", "if-none-match"} {
		found := false
		for _, s := range signed {
			if s == h {
				found = true
			}
		}
		if !found {
			t.Errorf("X-Amz-SignedHeaders = %v, want %s signed", signed, h)
		}
		if h != "content-length" && resp.SignedHeader.Get(h) == "" {
			t.Errorf("SignedHeader = %v, want %s returned for the client to send", resp.SignedHeader, h)
		}
	}
	if body.read {
		t.Errorf("PresignUploadURL read the embedded Body, want it ignored")
	}

	for _, v := range []string{"tomorrow", "Wed Oct 21 07:28:00 2026", "Thu, 21 Oct 2026 07:28:00 GMT"} {
		_, err := pre.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{PutObjectInput: storage.PutObjectInput{
			ObjectHeaders: storage.ObjectHeaders{Expires: ptr(v)}, BaseObjectInput: obj("bad"),
		}})
		wantErr(t, "PresignUploadURL(Expires "+v+")", err, storage.ErrInvalidHeader)
	}
}

// Docs: modules/storage.mdx#presigned-urls
// "the client must send the returned `Method` and every header in
// `SignedHeader` unchanged."
// LocalStack runs with signature validation on, so a changed signed header is
// refused.
func TestPresignUploadHeadersMustMatch(t *testing.T) {
	t.Parallel()
	b := s3Backend(t)
	pre := newS3Presigner(t, "us-east-1", b.bucket)
	resp, err := pre.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{PutObjectInput: storage.PutObjectInput{
		ObjectHeaders:   storage.ObjectHeaders{ContentType: ptr("image/png"), CacheControl: ptr("no-cache")},
		BaseObjectInput: obj("strict.png"),
	}})
	if err != nil {
		t.Fatalf("PresignUploadURL() error: %v", err)
	}
	changed := *resp
	changed.SignedHeader = resp.SignedHeader.Clone()
	changed.SignedHeader.Set("Cache-Control", "max-age=1")
	if status, _ := doSigned(t, &changed, strings.NewReader("x")); status == http.StatusOK {
		t.Fatalf("upload with a changed signed header status = 200, want refused")
	}
	if status, msg := doSigned(t, resp, strings.NewReader("x")); status != http.StatusOK {
		t.Fatalf("upload with the signed headers status = %d (%s), want 200", status, msg)
	}
}

// failReader records whether it was read.
type failReader struct {
	read bool
}

func (r *failReader) Read([]byte) (int, error) {
	r.read = true
	return 0, nil
}

func newPresignerWith(t *testing.T, f *fakeS3, pc *awss3.PresignClient) (*s3svc.Presigner, error) {
	t.Helper()
	return s3svc.NewPresigner(s3svc.WithPresignClient(pc))(mockConfig(t, f.values("us-east-1", "bkt")))
}
