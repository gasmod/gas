package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"github.com/gasmod/gas/storage"
)

// specialKeys need URL encoding or are otherwise easy to mangle on the way
// to the endpoint.
var specialKeys = []string{
	"with space.txt",
	"plus+sign",
	"question?mark",
	"hash#tag",
	"percent%25literal",
	"percent%2Fslash",
	"amp&eq=;semi",
	"unicode/résumé-日本語.pdf",
	"/leading-slash",
	"double//slash",
	"trailing/",
	"tilde~star*paren()",
	"quote\"apos'",
	"tab\tkey",
	strings.Repeat("k", 1000) + "/" + strings.Repeat("é", 11),
}

// doPresigned sends the presigned request in res to the fake endpoint with
// body and the signed headers.
func doPresigned(t *testing.T, f *fakeS3, res *storage.PresignURLResponse, body []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(
		context.Background(), res.Method, res.URL, bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("NewRequest(%q) error: %v", res.URL, err)
	}
	for k, vs := range res.SignedHeader {
		if strings.EqualFold(k, "Host") {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.ContentLength = int64(len(body))
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s presigned URL error: %v", res.Method, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// TestPresigner_SpecialKeysResolve uses presigned URLs for keys that need
// escaping and checks that the request reaches the endpoint at exactly the
// object's path.
func TestPresigner_SpecialKeysResolve(t *testing.T) {
	t.Parallel()
	for _, key := range specialKeys {
		t.Run(key[:min(len(key), 30)], func(t *testing.T) {
			t.Parallel()
			f := newFakeS3(t, nil)
			p := newTestPresigner(t, f, testBucket)
			want := "/" + testBucket + "/" + key

			dl, err := p.PresignDownloadURL(context.Background(), &storage.PresignDownloadURLInput{
				GetObjectInput: storage.GetObjectInput{
					HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: base(key)},
				},
			})
			if err != nil {
				t.Fatalf("PresignDownloadURL() error: %v", err)
			}
			doPresigned(t, f, dl, nil)
			if got := f.lastRequest(t); got.Method != http.MethodGet || got.Path != want {
				t.Errorf("download request = %s %q, want GET %q", got.Method, got.Path, want)
			}

			ul, err := p.PresignUploadURL(context.Background(), &storage.PresignUploadURLInput{
				PutObjectInput: storage.PutObjectInput{
					BaseObjectInput: base(key),
					ContentType:     new("text/plain"),
				},
			})
			if err != nil {
				t.Fatalf("PresignUploadURL() error: %v", err)
			}
			doPresigned(t, f, ul, []byte("payload"))
			got := f.lastRequest(t)
			if got.Method != http.MethodPut || got.Path != want || string(got.Body) != "payload" {
				t.Errorf("upload request = %s %q body %q, want PUT %q payload",
					got.Method, got.Path, got.Body, want)
			}
		})
	}
}

// TestProvider_SpecialKeysAllOps checks that every operation, not only
// HEAD, sends special keys verbatim.
func TestProvider_SpecialKeysAllOps(t *testing.T) {
	t.Parallel()
	for _, key := range specialKeys {
		t.Run(key[:min(len(key), 30)], func(t *testing.T) {
			t.Parallel()
			f := newFakeS3(t, nil)
			p := newTestProvider(t, f)
			ctx := context.Background()
			want := "/" + testBucket + "/" + key

			if _, err := p.PutObject(ctx, &storage.PutObjectInput{
				BaseObjectInput: base(key), Body: strings.NewReader("x"),
			}); err != nil {
				t.Fatalf("PutObject() error: %v", err)
			}
			obj, err := p.GetObject(ctx, &storage.GetObjectInput{
				HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: base(key)},
			})
			if err != nil {
				t.Fatalf("GetObject() error: %v", err)
			}
			_ = obj.Body.Close()
			if _, err := p.DeleteObject(
				ctx, &storage.DeleteObjectInput{BaseObjectInput: base(key)},
			); err != nil {
				t.Fatalf("DeleteObject() error: %v", err)
			}
			for _, r := range f.requests() {
				if r.Path != want {
					t.Errorf("%s path = %q, want %q", r.Method, r.Path, want)
				}
			}
		})
	}
}

// TestProvider_UnmappedErrors checks responses that must not be reported as
// any of the not-found sentinels.
func TestProvider_UnmappedErrors(t *testing.T) {
	t.Parallel()

	t.Run("HEAD 301 wrong region", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.Header().Set("X-Amz-Bucket-Region", "eu-west-1")
			w.WriteHeader(http.StatusMovedPermanently)
		})
		err := errorMappingOps["head"](newTestProvider(t, f))
		if err == nil {
			t.Fatal("HeadObject() error = nil, want redirect error")
		}
		assertOnlySentinel(t, err, nil)
	})

	for _, op := range []string{"get", "put", "delete", "create bucket"} {
		t.Run("301 PermanentRedirect/"+op, func(t *testing.T) {
			t.Parallel()
			f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				writeS3Error(w, http.StatusMovedPermanently, "PermanentRedirect")
			})
			err := errorMappingOps[op](newTestProvider(t, f))
			if err == nil {
				t.Fatalf("%s error = nil, want redirect error", op)
			}
			assertOnlySentinel(t, err, nil)
		})
	}
}

// TestProvider_NoSuchVersion covers GetObject and HeadObject with a Version
// that does not exist. S3 answers GET with 404 NoSuchVersion, which maps to
// ErrVersionNotFound and to nothing else, and HEAD with a bodiless 404, which
// cannot name the version and stays ErrNotFound.
func TestProvider_NoSuchVersion(t *testing.T) {
	t.Parallel()

	in := storage.HeadObjectInput{
		BaseObjectInput: base("k"),
		Version:         new("does-not-exist"),
	}

	t.Run("get", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			writeS3Error(w, http.StatusNotFound, "NoSuchVersion")
		})
		p := newTestProvider(t, f)
		_, err := p.GetObject(context.Background(), &storage.GetObjectInput{HeadObjectInput: in})
		if err == nil {
			t.Fatal("GetObject() error = nil, want ErrVersionNotFound")
		}
		assertOnlySentinel(t, err, storage.ErrVersionNotFound)
		const prefix = "gas/storage/s3/provider: get: storage: version not found: "
		if !strings.HasPrefix(err.Error(), prefix) {
			t.Errorf("error = %q, want prefix %q", err, prefix)
		}
		if strings.Contains(err.Error(), "does-not-exist") {
			t.Errorf("error = %q, must not carry the caller's version id", err)
		}
		apiErr, ok := errors.AsType[smithy.APIError](err)
		if !ok || apiErr.ErrorCode() != "NoSuchVersion" {
			t.Errorf("errors.As(smithy.APIError) = %v, %v, want code NoSuchVersion", apiErr, ok)
		}
	})

	t.Run("head", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.WriteHeader(http.StatusNotFound)
		})
		p := newTestProvider(t, f)
		_, err := p.HeadObject(context.Background(), &in)
		assertOnlySentinel(t, err, storage.ErrNotFound)
	})
}

// TestProvider_DeadlineExceeded checks that a context deadline hit while the
// request is in flight surfaces as context.DeadlineExceeded.
func TestProvider_DeadlineExceeded(t *testing.T) {
	t.Parallel()
	for op, call := range errorMappingCtxOps {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			f := newFakeS3(t, func(_ http.ResponseWriter, r *http.Request, _ []byte) {
				select {
				case <-r.Context().Done():
				case <-time.After(10 * time.Second):
				}
			})
			p := newTestProvider(t, f)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			err := call(ctx, p)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("%s error = %v, want context.DeadlineExceeded", op, err)
			}
			assertOnlySentinel(t, err, nil)
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("%s returned after %v, want shortly after the deadline", op, d)
			}
		})
	}
}

var errorMappingCtxOps = map[string]func(ctx context.Context, p *Provider) error{
	"head": func(ctx context.Context, p *Provider) error {
		_, err := p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: base("k")})
		return err
	},
	"get": func(ctx context.Context, p *Provider) error {
		_, err := p.GetObject(ctx, &storage.GetObjectInput{
			HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: base("k")},
		})
		return err
	},
	"create bucket": func(ctx context.Context, p *Provider) error {
		_, err := p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "new-bucket"})
		return err
	},
	"put": func(ctx context.Context, p *Provider) error {
		_, err := p.PutObject(ctx, &storage.PutObjectInput{
			BaseObjectInput: base("k"), Body: strings.NewReader("x"),
		})
		return err
	},
	"delete": func(ctx context.Context, p *Provider) error {
		_, err := p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: base("k")})
		return err
	},
}

// TestProvider_MultipartErrorMapping checks that S3 errors returned during a
// multipart upload keep their sentinel mapping through the transfer manager.
func TestProvider_MultipartErrorMapping(t *testing.T) {
	t.Parallel()

	t.Run("NoSuchBucket on CreateMultipartUpload", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			writeS3Error(w, http.StatusNotFound, "NoSuchBucket")
		})
		p := newTestProvider(t, f)
		_, err := p.PutObject(context.Background(), &storage.PutObjectInput{
			BaseObjectInput: base("big"),
			Body:            bytes.NewReader(multipartPayload()),
		})
		assertOnlySentinel(t, err, storage.ErrBucketNotFound)
		for _, r := range f.requests() {
			if r.Method == http.MethodPut {
				t.Errorf("part uploaded after CreateMultipartUpload failed: %s", r.Path)
			}
		}
	})

	t.Run("NoSuchUpload on a part is not a not-found sentinel", func(t *testing.T) {
		t.Parallel()
		m := newMultipartServer(0)
		f := newFakeS3(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
			if r.Method == http.MethodPut && r.URL.Query().Get("partNumber") == "2" {
				writeS3Error(w, http.StatusNotFound, "NoSuchUpload")
				return
			}
			m.handle(w, r, body)
		})
		p := newTestProvider(t, f)
		_, err := p.PutObject(context.Background(), &storage.PutObjectInput{
			BaseObjectInput: base("big"),
			Body:            bytes.NewReader(multipartPayload()),
		})
		if err == nil {
			t.Fatal("PutObject() error = nil, want NoSuchUpload error")
		}
		assertOnlySentinel(t, err, nil)
		if !m.aborted.Load() {
			t.Error("AbortMultipartUpload was not sent after NoSuchUpload")
		}
	})
}

// TestProvider_MetadataHeaderEdgeCases documents how metadata that does not
// fit in an HTTP header behaves. The dir backend accepts all of these.
func TestProvider_MetadataHeaderEdgeCases(t *testing.T) {
	t.Parallel()

	invalid := map[string]map[string]string{
		"space in key":     {"bad key": "v"},
		"colon in key":     {"a:b": "v"},
		"non-ascii key":    {"日本": "v"},
		"newline in value": {"k": "line1\nline2"},
	}
	for name, meta := range invalid {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFakeS3(t, nil)
			p := newTestProvider(t, f)
			_, err := p.PutObject(context.Background(), &storage.PutObjectInput{
				BaseObjectInput: base("k"), Body: strings.NewReader("x"), Metadata: meta,
			})
			if err == nil {
				t.Errorf("PutObject(metadata %q) error = nil; header sent as %v",
					meta, f.lastRequest(t).Header)
				return
			}
			assertOnlySentinel(t, err, nil)
			if n := len(f.requests()); n != 0 {
				t.Errorf("sent %d requests with invalid metadata", n)
			}
		})
	}

	t.Run("keys differing only in case collapse to one header", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		p := newTestProvider(t, f)
		_, err := p.PutObject(context.Background(), &storage.PutObjectInput{
			BaseObjectInput: base("k"),
			Body:            strings.NewReader("x"),
			Metadata:        map[string]string{"Owner": "upper", "owner": "lower"},
		})
		if err != nil {
			t.Fatalf("PutObject() error: %v", err)
		}
		// Documented: which value survives depends on map iteration order, so
		// one of the two values is silently and nondeterministically dropped.
		got := f.lastRequest(t).Header.Values("X-Amz-Meta-Owner")
		if len(got) != 1 || (got[0] != "upper" && got[0] != "lower") {
			t.Errorf("X-Amz-Meta-Owner = %q, want exactly one of the two values", got)
		}
	})
}

// stallReader returns (0, nil) stalls times before every byte. io.Reader
// discourages but permits (0, nil); callers must keep reading.
type stallReader struct {
	r      io.Reader
	stalls int
	left   int
}

// Read returns (0, nil) stalls times, then one byte of the underlying reader.
func (s *stallReader) Read(b []byte) (int, error) {
	if s.left > 0 {
		s.left--
		return 0, nil
	}
	s.left = s.stalls
	if len(b) > 1 {
		b = b[:1]
	}
	return s.r.Read(b)
}

// dataThenErrReader returns all of data together with err on the first Read
// and io.EOF afterwards, i.e. the error is not sticky.
type dataThenErrReader struct {
	err  error
	data []byte
	done bool
}

// Read returns all of data with err on the first call and io.EOF afterwards.
func (d *dataThenErrReader) Read(b []byte) (int, error) {
	if d.done {
		return 0, io.EOF
	}
	d.done = true
	n := copy(b, d.data)
	return n, d.err
}

// TestProvider_UnusualReaders checks PutObject with readers that stall, fail
// after data, or behave oddly.
func TestProvider_UnusualReaders(t *testing.T) {
	t.Parallel()

	t.Run("reader returning (0, nil)", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		p := newTestProvider(t, f)
		body := bytes.Repeat([]byte("q"), 600)
		done := make(chan error, 1)
		go func() {
			_, err := p.PutObject(context.Background(), &storage.PutObjectInput{
				BaseObjectInput: base("k"),
				Body:            &stallReader{r: bytes.NewReader(body), stalls: 5},
			})
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("PutObject() error: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("PutObject() did not finish with a reader that returns (0, nil)")
		}
		if !bytes.Equal(f.lastRequest(t).Body, body) {
			t.Error("body mismatch")
		}
	})

	for _, n := range []int{100, 512} {
		t.Run("error returned with data "+strconv.Itoa(n), func(t *testing.T) {
			t.Parallel()
			errBoom := errors.New("boom")
			f := newFakeS3(t, nil)
			p := newTestProvider(t, f)
			_, err := p.PutObject(context.Background(), &storage.PutObjectInput{
				BaseObjectInput: base("k"),
				Body: &dataThenErrReader{
					data: bytes.Repeat([]byte("z"), n),
					err:  errBoom,
				},
			})
			if !errors.Is(err, errBoom) {
				t.Errorf("PutObject() error = %v, want %v", err, errBoom)
			}
		})
	}
}

// TestProvider_MultipartFailureGoroutines checks that failed multipart
// uploads do not leave goroutines behind. Not parallel so that the
// goroutine count is not disturbed by other tests.
func TestProvider_MultipartFailureGoroutines(t *testing.T) {
	m := newMultipartServer(2)
	f := newFakeS3(t, m.handle)
	p := newTestProvider(t, f)
	payload := multipartPayload()

	settle := func() int {
		f.srv.Client().CloseIdleConnections()
		f.srv.CloseClientConnections()
		runtime.GC()
		return runtime.NumGoroutine()
	}
	// Warm up once so lazily started SDK/transport goroutines are counted.
	_, _ = p.PutObject(context.Background(), &storage.PutObjectInput{
		BaseObjectInput: base("big"), Body: bytes.NewReader(payload),
	})
	before := settle()

	const iterations = 3
	for range iterations {
		_, err := p.PutObject(context.Background(), &storage.PutObjectInput{
			BaseObjectInput: base("big"), Body: bytes.NewReader(payload),
		})
		if err == nil {
			t.Fatal("PutObject() error = nil, want part failure")
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	after := settle()
	for after > before && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		after = settle()
	}
	if after > before {
		t.Errorf("goroutines grew from %d to %d after %d failed multipart uploads",
			before, after, iterations)
	}
}

// TestNewProvider_OptionEdgeCases checks options given a nil argument, and
// conflicting options, passed to NewProvider and NewPresigner. A nil client
// argument is ignored and the constructor falls back to a client built from
// configuration (or derived from the S3 client, for the presign client).
func TestNewProvider_OptionEdgeCases(t *testing.T) {
	isolateAWSEnv(t)

	t.Run("WithClient(nil) falls back to a client from config", func(t *testing.T) {
		p, err := NewProvider(WithClient(nil))(
			newConfig(t, map[string]any{"region": "eu-west-1"}),
		)
		if err != nil {
			t.Fatalf("NewProvider() error: %v", err)
		}
		if p.Client() == nil || p.uploader == nil {
			t.Fatal("WithClient(nil) left the provider without a client")
		}
		if got := p.Client().Options().Region; got != "eu-west-1" {
			t.Errorf("fallback client region = %q, want eu-west-1", got)
		}
	})

	t.Run("WithUploaderClient(nil) falls back to the default uploader", func(t *testing.T) {
		f := newFakeS3(t, nil)
		p, err := NewProvider(
			WithUploaderClient(transfermanager.New(f.client(t))),
			WithUploaderClient(nil),
			WithClient(f.client(t)),
		)(newConfig(t, map[string]any{"bucket": testBucket}))
		if err != nil {
			t.Fatalf("NewProvider() error: %v", err)
		}
		if p.uploader == nil {
			t.Fatal("uploader is nil")
		}
	})

	t.Run("presigner nil options fall back", func(t *testing.T) {
		f := newFakeS3(t, nil)
		c := f.client(t)
		p, err := NewPresigner(WithPresignClient(nil), WithPresignerS3Client(c))(
			newConfig(t, map[string]any{"bucket": testBucket}),
		)
		if err != nil {
			t.Fatalf("NewPresigner() error: %v", err)
		}
		if p.PresignClient() == nil || p.s3Client != c {
			t.Error(
				"WithPresignClient(nil) did not fall back to a client derived from the S3 client",
			)
		}
		p2, err := NewPresigner(WithPresignerS3Client(nil), WithPresignClient(nil))(
			newConfig(t, map[string]any{}),
		)
		if err != nil {
			t.Fatalf("NewPresigner() error: %v", err)
		}
		if p2.PresignClient() == nil {
			t.Error("nil presigner options left the presigner without a client")
		}
	})
}

// TestNewProvider_NilOption checks that a nil ProviderOption is reported as
// storage.ErrInvalidOption naming its position, instead of panicking, and
// that nothing is sent.
func TestNewProvider_NilOption(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	c := f.client(t)
	for _, tc := range []struct {
		want string
		opts []ProviderOption
	}{
		{opts: []ProviderOption{nil}, want: "option 0 is nil"},
		{opts: []ProviderOption{WithClient(c), nil}, want: "option 1 is nil"},
		{opts: []ProviderOption{WithClient(c), WithUploaderClient(nil), nil}, want: "option 2 is nil"},
	} {
		p, err := NewProvider(tc.opts...)(newConfig(t, map[string]any{}))
		if p != nil {
			t.Errorf("NewProvider() provider = %v, want nil", p)
		}
		if !errors.Is(err, storage.ErrInvalidOption) {
			t.Fatalf("NewProvider() error = %v, want ErrInvalidOption", err)
		}
		want := "gas/storage/s3/provider: storage: invalid option: " + tc.want
		if err.Error() != want {
			t.Errorf("NewProvider() error = %q, want %q", err, want)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("NewProvider sent %d requests", n)
	}
}

// TestNewPresigner_NilOption checks that a nil PresignerOption is reported
// as storage.ErrInvalidOption naming its position, instead of panicking.
func TestNewPresigner_NilOption(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	c := f.client(t)
	for _, tc := range []struct {
		want string
		opts []PresignerOption
	}{
		{opts: []PresignerOption{nil}, want: "option 0 is nil"},
		{opts: []PresignerOption{WithPresignerS3Client(c), nil}, want: "option 1 is nil"},
		{
			opts: []PresignerOption{nil, WithPresignClient(s3.NewPresignClient(c))},
			want: "option 0 is nil",
		},
	} {
		p, err := NewPresigner(tc.opts...)(newConfig(t, map[string]any{}))
		if p != nil {
			t.Errorf("NewPresigner() presigner = %v, want nil", p)
		}
		if !errors.Is(err, storage.ErrInvalidOption) {
			t.Fatalf("NewPresigner() error = %v, want ErrInvalidOption", err)
		}
		want := "gas/storage/s3/presigner: storage: invalid option: " + tc.want
		if err.Error() != want {
			t.Errorf("NewPresigner() error = %q, want %q", err, want)
		}
	}
}

// isolateAWSEnv keeps the default AWS config chain from reading the
// developer's real configuration. It uses t.Setenv, so callers cannot run
// in parallel.
func isolateAWSEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AWS_CONFIG_FILE", "/nonexistent/aws-config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/nonexistent/aws-credentials")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_ENDPOINT_URL", "")
	t.Setenv("AWS_ENDPOINT_URL_S3", "")
}

// TestNewProvider_DefaultClientError covers a failing default AWS config
// load: an explicitly selected profile that does not exist.
func TestNewProvider_DefaultClientError(t *testing.T) {
	isolateAWSEnv(t)
	cfgFile := t.TempDir() + "/config"
	if err := os.WriteFile(
		cfgFile,
		[]byte("[profile other]\nregion = eu-west-1\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", cfgFile)
	t.Setenv("AWS_PROFILE", "missing-profile")

	_, err := NewProvider()(newConfig(t, map[string]any{"bucket": testBucket}))
	if err == nil || !strings.Contains(err.Error(), "gas/storage/s3/provider: create S3 client") {
		t.Errorf("NewProvider() error = %v, want create S3 client error", err)
	}
	_, err = NewPresigner()(newConfig(t, map[string]any{"bucket": testBucket}))
	if err == nil || !strings.Contains(err.Error(), "gas/storage/s3/presigner: create S3 client") {
		t.Errorf("NewPresigner() error = %v, want create S3 client error", err)
	}
}

// TestPresigner_UploadNegativeTTL checks that PresignUploadURL rejects a
// negative TTL with storage.ErrInvalidTTL.
func TestPresigner_UploadNegativeTTL(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	p := newTestPresigner(t, f, testBucket)
	ttl := -time.Second
	_, err := p.PresignUploadURL(context.Background(), &storage.PresignUploadURLInput{
		PutObjectInput: storage.PutObjectInput{BaseObjectInput: base("k")},
		TTL:            &ttl,
	})
	if !errors.Is(err, storage.ErrInvalidTTL) ||
		!strings.HasPrefix(err.Error(), "gas/storage/s3/presigner: presign upload: ") {
		t.Errorf(
			"PresignUploadURL(negative TTL) error = %v, want presign upload ErrInvalidTTL",
			err,
		)
	}
}
