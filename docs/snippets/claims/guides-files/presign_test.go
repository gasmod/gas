package guidesfiles_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas/storage"
	storagedir "github.com/gasmod/gas/storage/dir"
	s3svc "github.com/gasmod/gas/storage/s3"
)

// isolateAWSEnv clears the AWS environment and shared config so the SDK's
// default resolution sees only what a test sets.
func isolateAWSEnv(t *testing.T) {
	t.Helper()
	none := filepath.Join(t.TempDir(), "none")
	for _, k := range []string{
		"AWS_REGION", "AWS_DEFAULT_REGION", "AWS_PROFILE", "AWS_ACCESS_KEY_ID",
		"AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_S3",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("AWS_CONFIG_FILE", none)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", none)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
}

func presignBoth(t *testing.T, pre storage.Presigner, ttl *time.Duration) (up, down *storage.PresignURLResponse) {
	t.Helper()
	up, err := pre.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{
		TTL: ttl, PutObjectInput: storage.PutObjectInput{BaseObjectInput: obj("k")},
	})
	if err != nil {
		t.Fatalf("PresignUploadURL() error: %v", err)
	}
	down, err = pre.PresignDownloadURL(t.Context(), &storage.PresignDownloadURLInput{
		TTL: ttl, GetObjectInput: storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: obj("k")}},
	})
	if err != nil {
		t.Fatalf("PresignDownloadURL() error: %v", err)
	}
	return up, down
}

func query(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u.Query()
}

// Docs: guides/files.mdx#operations
// "`PresignUploadURL(ctx, *PresignUploadURLInput)` | `*PresignURLResponse` |
// URL, method, and the headers the client must send"
// Docs: guides/files.mdx#operations
// "Presigned URLs let the browser transfer bytes straight to S3"
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
	if resp.URL == "" || resp.Method != http.MethodPut {
		t.Fatalf("PresignUploadURL() = URL %q, Method %q, want a URL and PUT", resp.URL, resp.Method)
	}
	if resp.SignedHeader.Get("Content-Type") != "image/png" {
		t.Fatalf("SignedHeader = %v, want Content-Type image/png among the headers to send", resp.SignedHeader)
	}
	req, err := http.NewRequestWithContext(t.Context(), resp.Method, resp.URL, strings.NewReader("png bytes"))
	if err != nil {
		t.Fatalf("NewRequest() error: %v", err)
	}
	for k, vs := range resp.SignedHeader {
		if strings.EqualFold(k, "Host") {
			req.Host = vs[0]
			continue
		}
		req.Header[k] = vs
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload via presigned URL: %v", err)
	}
	msg, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("upload via presigned URL status = %d (%s), want 200", res.StatusCode, msg)
	}
	if got := readObject(t, b.p, "up.png"); got != "png bytes" {
		t.Fatalf("uploaded body = %q, want %q", got, "png bytes")
	}
}

// Docs: guides/files.mdx#operations
// "`PresignDownloadURL(ctx, *PresignDownloadURLInput)` | `*PresignURLResponse`
// | URL, method, and signed headers for a GET"
func TestPresignDownloadURLWorks(t *testing.T) {
	t.Parallel()
	b := s3Backend(t)
	mustPut(t, b.p, "down", "download me")
	pre := newS3Presigner(t, "us-east-1", b.bucket)
	resp, err := pre.PresignDownloadURL(t.Context(), &storage.PresignDownloadURLInput{
		GetObjectInput: storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: obj("down")}},
	})
	if err != nil {
		t.Fatalf("PresignDownloadURL() error: %v", err)
	}
	if resp.URL == "" || resp.Method != http.MethodGet || resp.SignedHeader == nil {
		t.Fatalf("PresignDownloadURL() = %+v, want a URL, GET and signed headers", resp)
	}
	req, _ := http.NewRequestWithContext(t.Context(), resp.Method, resp.URL, nil)
	for k, vs := range resp.SignedHeader {
		if strings.EqualFold(k, "Host") {
			req.Host = vs[0]
			continue
		}
		req.Header[k] = vs
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("download via presigned URL: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || string(body) != "download me" {
		t.Fatalf("download via presigned URL = %d %q, want 200 %q", res.StatusCode, body, "download me")
	}
}

// Docs: guides/files.mdx#operations
// "Signing is local; no network call is made."
func TestPresignIsLocal(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	presignBoth(t, f.presigner(t, "us-east-1", "bkt"), nil)
	if n := f.requests.Load(); n != 0 {
		t.Fatalf("presigning sent %d requests, want 0", n)
	}
}

// Docs: guides/files.mdx#operations
// "A nil or zero TTL signs 15 minutes"
func TestPresignDefaultTTL(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	pre := f.presigner(t, "us-east-1", "bkt")
	for name, ttl := range map[string]*time.Duration{"nil": nil, "zero": ptr(time.Duration(0))} {
		up, down := presignBoth(t, pre, ttl)
		for kind, r := range map[string]*storage.PresignURLResponse{"upload": up, "download": down} {
			if got := query(t, r.URL).Get("X-Amz-Expires"); got != "900" {
				t.Errorf("%s TTL %s: X-Amz-Expires = %q, want 900", name, kind, got)
			}
		}
	}
}

// Docs: guides/files.mdx#operations
// "a TTL that is negative, under one second, or over seven days is
// `storage.ErrInvalidTTL`."
// Docs: guides/files.mdx#errors-and-testing
// "`ErrInvalidTTL` | A presign TTL out of bounds"
func TestPresignInvalidTTL(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	pre := f.presigner(t, "us-east-1", "bkt")
	for _, ttl := range []time.Duration{-time.Second, -time.Nanosecond, 500 * time.Millisecond, time.Nanosecond, 7*24*time.Hour + time.Second} {
		_, err := pre.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{
			TTL: ptr(ttl), PutObjectInput: storage.PutObjectInput{BaseObjectInput: obj("k")},
		})
		wantErr(t, "PresignUploadURL(TTL "+ttl.String()+")", err, storage.ErrInvalidTTL)
		_, err = pre.PresignDownloadURL(t.Context(), &storage.PresignDownloadURLInput{
			TTL: ptr(ttl), GetObjectInput: storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: obj("k")}},
		})
		wantErr(t, "PresignDownloadURL(TTL "+ttl.String()+")", err, storage.ErrInvalidTTL)
	}
	// Assumption: the bounds themselves, one second and seven days, are valid.
	for _, ttl := range []time.Duration{time.Second, 7 * 24 * time.Hour} {
		presignBoth(t, pre, ptr(ttl))
	}
}

// Docs: guides/files.mdx#config
// "`storage.accessKeyID`, `storage.secretAccessKey`, `storage.sessionToken` |
// Static credentials (s3). Without `accessKeyID` the default credential chain
// applies"
func TestStaticAndDefaultCredentials(t *testing.T) {
	isolateAWSEnv(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDFROMENV")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "envsecret")

	static, err := s3svc.NewPresigner()(mockConfig(t, map[string]any{
		"region": "us-east-1", "bucket": "bkt", "accessKeyID": "AKIDSTATIC",
		"secretAccessKey": "s", "sessionToken": "TOKEN123",
	}))
	if err != nil {
		t.Fatalf("NewPresigner(static) error: %v", err)
	}
	up, _ := presignBoth(t, static, nil)
	q := query(t, up.URL)
	if !strings.HasPrefix(q.Get("X-Amz-Credential"), "AKIDSTATIC/") || q.Get("X-Amz-Security-Token") != "TOKEN123" {
		t.Fatalf("static: X-Amz-Credential %q, X-Amz-Security-Token %q, want AKIDSTATIC and TOKEN123",
			q.Get("X-Amz-Credential"), q.Get("X-Amz-Security-Token"))
	}

	chain, err := s3svc.NewPresigner()(mockConfig(t, map[string]any{"region": "us-east-1", "bucket": "bkt"}))
	if err != nil {
		t.Fatalf("NewPresigner(default chain) error: %v", err)
	}
	up, _ = presignBoth(t, chain, nil)
	if got := query(t, up.URL).Get("X-Amz-Credential"); !strings.HasPrefix(got, "AKIDFROMENV/") {
		t.Fatalf("default chain: X-Amz-Credential = %q, want the environment's AKIDFROMENV", got)
	}
}

// Docs: guides/files.mdx#config
// "`storage.region` | AWS region (s3). Empty uses the AWS SDK's default
// resolution"
func TestRegionConfig(t *testing.T) {
	isolateAWSEnv(t)
	t.Setenv("AWS_REGION", "ap-south-1")
	f := newFakeS3(t, nil)
	for region, want := range map[string]string{"eu-west-3": "eu-west-3", "": "ap-south-1"} {
		up, _ := presignBoth(t, f.presigner(t, region, "bkt"), nil)
		if got := query(t, up.URL).Get("X-Amz-Credential"); !strings.Contains(got, "/"+want+"/s3/") {
			t.Fatalf("region %q: X-Amz-Credential = %q, want scope region %s", region, got, want)
		}
	}
}

// Docs: guides/files.mdx#config
// "`storage.endpoint` | Custom endpoint for S3-compatible services (s3).
// Enables path-style addressing"
func TestEndpointPathStyle(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	up, down := presignBoth(t, f.presigner(t, "us-east-1", "mybucket"), nil)
	ep, _ := url.Parse(f.URL)
	for kind, r := range map[string]*storage.PresignURLResponse{"upload": up, "download": down} {
		u, err := url.Parse(r.URL)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if u.Host != ep.Host || u.Path != "/mybucket/k" {
			t.Fatalf("%s URL = %s, want host %s and path-style /mybucket/k", kind, r.URL, ep.Host)
		}
	}
	p := f.provider(t, "us-east-1", "mybucket")
	_, _ = p.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: "mybucket"})
	if f.requests.Load() == 0 {
		t.Fatalf("HeadBucket sent no request to the custom endpoint")
	}
}

// Docs: guides/files.mdx#config
// "`storage.bucket` | Default bucket for inputs with a nil `Bucket`. Optional;
// must be a valid bucket name when set"
func TestBucketConfig(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cases := map[string]func(v map[string]any) error{
		"s3 provider": func(v map[string]any) error {
			_, err := s3svc.NewProvider()(mockConfig(t, v))
			return err
		},
		"s3 presigner": func(v map[string]any) error {
			_, err := s3svc.NewPresigner()(mockConfig(t, v))
			return err
		},
		"dir provider": func(v map[string]any) error {
			r, err := os.OpenRoot(root)
			if err != nil {
				return err
			}
			defer func() { _ = r.Close() }()
			_, err = storagedir.NewProvider(storagedir.WithRoot(r))(mockConfig(t, v))
			return err
		},
	}
	for name, build := range cases {
		base := map[string]any{"region": "us-east-1", "accessKeyID": "a", "secretAccessKey": "s"}
		if err := build(base); err != nil {
			t.Errorf("%s without storage.bucket error = %v, want nil (optional)", name, err)
		}
		bad := map[string]any{"region": "us-east-1", "accessKeyID": "a", "secretAccessKey": "s", "bucket": "Not_A_Valid_Bucket"}
		if err := build(bad); !errors.Is(err, storage.ErrInvalidBucket) {
			t.Errorf("%s with invalid storage.bucket error = %v, want ErrInvalidBucket", name, err)
		}
	}
}

// Docs: guides/files.mdx#config
// "The providers have no start or close step: a provider is ready when its
// constructor returns, and no request is sent at construction."
func TestNoLifecycleNoRequestAtConstruction(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	p := f.provider(t, "us-east-1", "bkt")
	pre := f.presigner(t, "us-east-1", "bkt")
	if n := f.requests.Load(); n != 0 {
		t.Fatalf("construction sent %d requests, want 0", n)
	}
	d, _ := newDirProvider(t, "")
	type initer interface{ Init(context.Context) error }
	type closer interface{ Close(context.Context) error }
	for name, v := range map[string]any{"s3.Provider": p, "s3.Presigner": pre, "dir.Provider": d} {
		if _, ok := v.(initer); ok {
			t.Errorf("%s has an Init step", name)
		}
		if _, ok := v.(closer); ok {
			t.Errorf("%s has a Close step", name)
		}
		if _, ok := v.(io.Closer); ok {
			t.Errorf("%s has a Close step", name)
		}
	}
	// Ready on return: the first call works with no start step.
	if _, err := p.HeadBucket(t.Context(), &storage.HeadBucketInput{Bucket: "bkt"}); err != nil {
		t.Fatalf("HeadBucket() right after construction error: %v", err)
	}
	if _, err := d.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: "bkt"}); err != nil {
		t.Fatalf("dir CreateBucket() right after construction error: %v", err)
	}
}
