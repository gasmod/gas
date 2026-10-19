package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/storage"
)

var lastModified = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// objectHeaders writes the response headers of an object of size bytes.
func objectHeaders(w http.ResponseWriter, size int) {
	w.Header().Set("Content-Length", strconv.Itoa(size))
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Last-Modified", lastModified.Format(http.TimeFormat))
	w.Header().Set("ETag", `"etag"`)
	w.Header().Set("X-Amz-Version-Id", "ver-123")
	w.Header().Set("X-Amz-Meta-Owner", "ahmed")
	w.Header().Set("X-Amz-Meta-Trace-Id", "t-1")
}

// TestNewProvider covers NewProvider with injected clients, invalid
// configuration, and config failures.
func TestNewProvider(t *testing.T) {
	t.Parallel()

	t.Run("with client", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		c := f.client(t)
		p, err := NewProvider(WithClient(c))(newConfig(t, map[string]any{"bucket": testBucket}))
		if err != nil {
			t.Fatalf("NewProvider() error: %v", err)
		}
		if p.Client() != c {
			t.Error("Client() is not the injected client")
		}
		if p.uploader == nil {
			t.Error("default uploader not created")
		}
		if p.config.Storage.Bucket != testBucket {
			t.Errorf("bucket = %q, want %q", p.config.Storage.Bucket, testBucket)
		}
	})

	t.Run("empty bucket allowed", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		if _, err := NewProvider(
			WithClient(f.client(t)),
		)(
			newConfig(t, map[string]any{}),
		); err != nil {
			t.Fatalf("NewProvider() error: %v", err)
		}
	})

	t.Run("invalid bucket", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		for _, bk := range []string{"Bad", "a_b", "ab", "1.2.3.4", "bucket-"} {
			p, err := NewProvider(
				WithClient(f.client(t)),
			)(
				newConfig(t, map[string]any{"bucket": bk}),
			)
			if !errors.Is(err, storage.ErrInvalidBucket) {
				t.Errorf("NewProvider(bucket=%q) error = %v, want ErrInvalidBucket", bk, err)
			}
			if p != nil {
				t.Errorf("NewProvider(bucket=%q) returned provider", bk)
			}
		}
	})

	t.Run("missing config provider", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		_, err := NewProvider(WithClient(f.client(t)))(nil)
		if err == nil || !strings.Contains(err.Error(), "config provider is nil") {
			t.Errorf("NewProvider() error = %v, want config provider is nil error", err)
		}
	})

	t.Run("bind failure", func(t *testing.T) {
		t.Parallel()
		bindErr := errors.New("bind failed")
		_, err := NewProvider()(&configtest.MockConfig{
			BindFn: func(any, ...config.BindOption) error { return bindErr },
		})
		if !errors.Is(err, bindErr) {
			t.Errorf("NewProvider() error = %v, want %v", err, bindErr)
		}
	})

	t.Run("custom uploader is used for puts", func(t *testing.T) {
		t.Parallel()
		var mainHits, upHits atomic.Int32
		main := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			mainHits.Add(1)
			w.WriteHeader(http.StatusOK)
		})
		up := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			upHits.Add(1)
			w.WriteHeader(http.StatusOK)
		})
		p, err := NewProvider(
			WithClient(main.client(t)),
			WithUploaderClient(transfermanager.New(up.client(t))),
		)(newConfig(t, map[string]any{"bucket": testBucket}))
		if err != nil {
			t.Fatalf("NewProvider() error: %v", err)
		}
		if _, err := p.PutObject(context.Background(), &storage.PutObjectInput{
			BaseObjectInput: base("k"),
			Body:            strings.NewReader("x"),
		}); err != nil {
			t.Fatalf("PutObject() error: %v", err)
		}
		if upHits.Load() != 1 || mainHits.Load() != 0 {
			t.Errorf("hits main=%d uploader=%d, want 0 and 1", mainHits.Load(), upHits.Load())
		}
	})
}

// TestNewProvider_FromConfig builds the client from configuration. It uses
// t.Setenv to isolate the AWS shared config, so it cannot run in parallel.
func TestNewProvider_FromConfig(t *testing.T) {
	t.Setenv("AWS_CONFIG_FILE", "/nonexistent/aws-config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/nonexistent/aws-credentials")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_ENDPOINT_URL", "")
	t.Setenv("AWS_ENDPOINT_URL_S3", "")

	t.Run(
		"endpoint, region and static credentials",
		testNewProviderFromConfigEndpointRegionAndStaticCredentials,
	)

	t.Run(
		"no endpoint keeps virtual-host style",
		testNewProviderFromConfigNoEndpointKeepsVirtualHostStyle,
	)

	t.Run("presigner from config", testNewProviderFromConfigPresignerFromConfig)
}

// testNewProviderFromConfigEndpointRegionAndStaticCredentials checks a client
// built from endpoint, region, and static credentials.
func testNewProviderFromConfigEndpointRegionAndStaticCredentials(t *testing.T) {
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		objectHeaders(w, 0)
	})
	p, err := NewProvider()(newConfig(t, map[string]any{
		"bucket":          testBucket,
		"region":          "eu-central-1",
		"accessKeyID":     "AKIDCFG",
		"secretAccessKey": "secret",
		"sessionToken":    "session-tok",
		"endpoint":        f.srv.URL,
	}))
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}
	opts := p.Client().Options()
	if opts.Region != "eu-central-1" {
		t.Errorf("Region = %q, want eu-central-1", opts.Region)
	}
	if !opts.UsePathStyle {
		t.Error("UsePathStyle = false, want true when Endpoint is set")
	}
	if aws.ToString(opts.BaseEndpoint) != f.srv.URL {
		t.Errorf("BaseEndpoint = %q, want %q", aws.ToString(opts.BaseEndpoint), f.srv.URL)
	}

	if _, err := p.HeadObject(
		context.Background(),
		&storage.HeadObjectInput{BaseObjectInput: base("k")},
	); err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	req := f.lastRequest(t)
	if req.Path != "/"+testBucket+"/k" {
		t.Errorf("path = %q, want path-style /%s/k", req.Path, testBucket)
	}
	auth := req.Header.Get("Authorization")
	if !strings.Contains(auth, "Credential=AKIDCFG/") ||
		!strings.Contains(auth, "/eu-central-1/s3/") {
		t.Errorf("Authorization = %q, want static key and configured region", auth)
	}
	if req.Header.Get("X-Amz-Security-Token") != "session-tok" {
		t.Errorf(
			"X-Amz-Security-Token = %q, want session-tok",
			req.Header.Get("X-Amz-Security-Token"),
		)
	}
}

// testNewProviderFromConfigNoEndpointKeepsVirtualHostStyle checks that
// addressing stays virtual-host style without an endpoint.
func testNewProviderFromConfigNoEndpointKeepsVirtualHostStyle(t *testing.T) {
	p, err := NewProvider()(newConfig(t, map[string]any{"region": "us-west-2"}))
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}
	opts := p.Client().Options()
	if opts.UsePathStyle {
		t.Error("UsePathStyle = true, want false without Endpoint")
	}
	if opts.BaseEndpoint != nil {
		t.Errorf("BaseEndpoint = %q, want nil", *opts.BaseEndpoint)
	}
}

// testNewProviderFromConfigPresignerFromConfig checks a Presigner built from
// configuration.
func testNewProviderFromConfigPresignerFromConfig(t *testing.T) {
	pr, err := NewPresigner()(newConfig(t, map[string]any{
		"region":          "ap-south-1",
		"accessKeyID":     "AKIDPRE",
		"secretAccessKey": "secret",
		"endpoint":        "http://localhost:9000",
	}))
	if err != nil {
		t.Fatalf("NewPresigner() error: %v", err)
	}
	if pr.PresignClient() == nil {
		t.Fatal("PresignClient() = nil")
	}
	res, err := pr.PresignDownloadURL(context.Background(), &storage.PresignDownloadURLInput{
		GetObjectInput: storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{
			BaseObjectInput: inBucket("my-bucket"),
		}},
	})
	if err != nil {
		t.Fatalf("PresignDownloadURL() error: %v", err)
	}
	if !strings.HasPrefix(res.URL, "http://localhost:9000/my-bucket/k?") {
		t.Errorf("URL = %q, want path-style on configured endpoint", res.URL)
	}
	if !strings.Contains(res.URL, "AKIDPRE%2F") ||
		!strings.Contains(res.URL, "%2Fap-south-1%2Fs3%2F") {
		t.Errorf("URL = %q, want static key and region in credential scope", res.URL)
	}
}

// TestProvider_ZeroValue checks that a Provider not built by NewProvider,
// including a nil pointer, returns ErrNotInitialized.
func TestProvider_ZeroValue(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	ctx := context.Background()

	providers := map[string]*Provider{
		"nil pointer":   nil,
		"zero":          {},
		"client only":   {client: f.client(t), config: &Config{}},
		"uploader only": {uploader: transfermanager.New(f.client(t)), config: &Config{}},
		"no config":     {client: f.client(t), uploader: transfermanager.New(f.client(t))},
	}
	for name, p := range providers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := inBucket(testBucket)
			_, err := p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: testBucket})
			if !errors.Is(err, storage.ErrNotInitialized) {
				t.Errorf("CreateBucket() error = %v, want ErrNotInitialized", err)
			}
			if name == "nil pointer" && p.Client() != nil {
				t.Error("Client() on a nil *Provider should be nil")
			}
			_, err = p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: in})
			if !errors.Is(err, storage.ErrNotInitialized) {
				t.Errorf("HeadObject() error = %v, want ErrNotInitialized", err)
			}
			_, err = p.PutObject(ctx, &storage.PutObjectInput{BaseObjectInput: in})
			if !errors.Is(err, storage.ErrNotInitialized) {
				t.Errorf("PutObject() error = %v, want ErrNotInitialized", err)
			}
			_, err = p.GetObject(
				ctx,
				&storage.GetObjectInput{
					HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: in},
				},
			)
			if !errors.Is(err, storage.ErrNotInitialized) {
				t.Errorf("GetObject() error = %v, want ErrNotInitialized", err)
			}
			_, err = p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: in})
			if !errors.Is(err, storage.ErrNotInitialized) {
				t.Errorf("DeleteObject() error = %v, want ErrNotInitialized", err)
			}
			_, err = p.CreateBucket(ctx, nil)
			if !errors.Is(err, storage.ErrNotInitialized) {
				t.Errorf("CreateBucket(nil) error = %v, want ErrNotInitialized", err)
			}
			_, err = p.HeadObject(ctx, nil)
			if !errors.Is(err, storage.ErrNotInitialized) {
				t.Errorf(
					"HeadObject(nil) error = %v, want ErrNotInitialized (readiness checked first)",
					err,
				)
			}
		})
	}
	if len(f.requests()) != 0 {
		t.Errorf("uninitialized provider sent %d requests", len(f.requests()))
	}
}

// TestProvider_NilInput checks that nil inputs return ErrInputRequired with
// the operation prefix.
func TestProvider_NilInput(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	p := newTestProvider(t, f)
	ctx := context.Background()

	calls := map[string]func() error{
		"create bucket": func() error { _, err := p.CreateBucket(ctx, nil); return err },
		"head":          func() error { _, err := p.HeadObject(ctx, nil); return err },
		"put":           func() error { _, err := p.PutObject(ctx, nil); return err },
		"get":           func() error { _, err := p.GetObject(ctx, nil); return err },
		"delete": func() error {
			_, err := p.DeleteObject(ctx, nil)
			return err
		},
		"list":         func() error { _, err := p.ListObjects(ctx, nil); return err },
		"list buckets": func() error { _, err := p.ListBuckets(ctx, nil); return err },
	}
	for op, call := range calls {
		err := call()
		if !errors.Is(err, storage.ErrInputRequired) {
			t.Errorf("%s(nil) error = %v, want ErrInputRequired", op, err)
		}
		if err != nil && !strings.HasPrefix(err.Error(), "gas/storage/s3/provider: "+op+": ") {
			t.Errorf("%s(nil) error = %q, want gas/storage/s3/provider prefix", op, err)
		}
	}
	if len(f.requests()) != 0 {
		t.Errorf("nil input sent %d requests", len(f.requests()))
	}
}

// TestProvider_ValidationErrors checks key and bucket validation across every
// operation.
func TestProvider_ValidationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   storage.BaseObjectInput
		want error
		cfg  string
		name string
	}{
		{
			name: "zero-value key",
			in:   storage.BaseObjectInput{},
			cfg:  testBucket,
			want: storage.ErrInvalidKey,
		},
		{name: "empty key", in: base(""), cfg: testBucket, want: storage.ErrInvalidKey},
		{name: "dot key", in: base("."), cfg: testBucket, want: storage.ErrInvalidKey},
		{name: "a/..", in: base("a/.."), cfg: testBucket, want: storage.ErrInvalidKey},
		{
			name: "too long key",
			in:   base(strings.Repeat("k", 1025)),
			cfg:  testBucket,
			want: storage.ErrInvalidKey,
		},
		{name: "invalid utf8 key", in: base("\xff"), cfg: testBucket, want: storage.ErrInvalidKey},
		{
			name: "pointer to empty bucket",
			in:   inBucket(""),
			cfg:  testBucket,
			want: storage.ErrInvalidBucket,
		},
		{
			name: "invalid bucket",
			in:   inBucket("Bad_Bucket"),
			cfg:  testBucket,
			want: storage.ErrInvalidBucket,
		},
		{name: "no bucket anywhere", in: base("k"), cfg: "", want: storage.ErrInvalidBucket},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeS3(t, nil)
			p, err := NewProvider(
				WithClient(f.client(t)),
			)(
				newConfig(t, map[string]any{"bucket": tt.cfg}),
			)
			if err != nil {
				t.Fatal(err)
			}
			pr := newTestPresigner(t, f, tt.cfg)
			ctx := context.Background()

			_, err = p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: tt.in})
			if !errors.Is(err, tt.want) {
				t.Errorf("HeadObject() error = %v, want %v", err, tt.want)
			}
			_, err = p.PutObject(ctx, &storage.PutObjectInput{BaseObjectInput: tt.in})
			if !errors.Is(err, tt.want) {
				t.Errorf("PutObject() error = %v, want %v", err, tt.want)
			}
			_, err = p.GetObject(
				ctx,
				&storage.GetObjectInput{
					HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: tt.in},
				},
			)
			if !errors.Is(err, tt.want) {
				t.Errorf("GetObject() error = %v, want %v", err, tt.want)
			}
			_, err = p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: tt.in})
			if !errors.Is(err, tt.want) {
				t.Errorf("DeleteObject() error = %v, want %v", err, tt.want)
			}
			_, err = pr.PresignUploadURL(
				ctx,
				&storage.PresignUploadURLInput{
					PutObjectInput: storage.PutObjectInput{BaseObjectInput: tt.in},
				},
			)
			if !errors.Is(err, tt.want) {
				t.Errorf("PresignUploadURL() error = %v, want %v", err, tt.want)
			}
			_, err = pr.PresignDownloadURL(ctx, &storage.PresignDownloadURLInput{
				GetObjectInput: storage.GetObjectInput{
					HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: tt.in},
				},
			})
			if !errors.Is(err, tt.want) {
				t.Errorf("PresignDownloadURL() error = %v, want %v", err, tt.want)
			}
			if n := len(f.requests()); n != 0 {
				t.Errorf("invalid input sent %d requests", n)
			}
		})
	}
}

// TestProvider_HeadObject covers HeadObject.
func TestProvider_HeadObject(t *testing.T) {
	t.Parallel()

	t.Run(
		"forwards all fields and maps response",
		testProviderHeadObjectForwardsAllFieldsAndMapsResponse,
	)

	t.Run(
		"minimal input sends no optional params",
		testProviderHeadObjectMinimalInputSendsNoOptionalParams,
	)

	t.Run("explicit bucket overrides config", testProviderHeadObjectExplicitBucketOverridesConfig)
}

// testProviderHeadObjectForwardsAllFieldsAndMapsResponse checks the request
// parameters and response mapping of HeadObject.
func testProviderHeadObjectForwardsAllFieldsAndMapsResponse(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		objectHeaders(w, 42)
	})
	p := newTestProvider(t, f)
	expires := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

	info, err := p.HeadObject(context.Background(), &storage.HeadObjectInput{
		BaseObjectInput:            base("dir/file.txt"),
		PartNumber:                 new(int32(3)),
		Range:                      new("bytes=0-9"),
		ResponseCacheControl:       new("no-cache"),
		ResponseContentDisposition: new(`attachment; filename="x.txt"`),
		ResponseContentEncoding:    new("gzip"),
		ResponseContentLanguage:    new("en-US"),
		ResponseContentType:        new("application/x-override"),
		ResponseExpires:            &expires,
		Version:                    new("v-42"),
	})
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}

	req := f.lastRequest(t)
	if req.Method != http.MethodHead {
		t.Errorf("method = %s, want HEAD", req.Method)
	}
	if req.Path != "/"+testBucket+"/dir/file.txt" {
		t.Errorf("path = %q", req.Path)
	}
	wantQuery := map[string]string{
		"partNumber":                   "3",
		"versionId":                    "v-42",
		"response-cache-control":       "no-cache",
		"response-content-disposition": `attachment; filename="x.txt"`,
		"response-content-encoding":    "gzip",
		"response-content-language":    "en-US",
		"response-content-type":        "application/x-override",
		"response-expires":             expires.Format(http.TimeFormat),
	}
	for k, v := range wantQuery {
		if got := req.Query[k]; len(got) != 1 || got[0] != v {
			t.Errorf("query %s = %v, want %q", k, got, v)
		}
	}
	if req.Header.Get("Range") != "bytes=0-9" {
		t.Errorf("Range header = %q", req.Header.Get("Range"))
	}

	if info.Size != 42 {
		t.Errorf("Size = %d, want 42", info.Size)
	}
	if info.ContentType != "text/plain" {
		t.Errorf("ContentType = %q", info.ContentType)
	}
	if !info.LastModified.Equal(lastModified) {
		t.Errorf("LastModified = %v, want %v", info.LastModified, lastModified)
	}
	if info.Version != "ver-123" {
		t.Errorf("Version = %q", info.Version)
	}
	want := map[string]string{"owner": "ahmed", "trace-id": "t-1"}
	if !maps.Equal(info.Metadata, want) {
		t.Errorf("Metadata = %v, want %v", info.Metadata, want)
	}
}

// testProviderHeadObjectMinimalInputSendsNoOptionalParams checks that a
// key-only input sends no optional parameters.
func testProviderHeadObjectMinimalInputSendsNoOptionalParams(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusOK)
	})
	p := newTestProvider(t, f)
	info, err := p.HeadObject(
		context.Background(),
		&storage.HeadObjectInput{BaseObjectInput: base("k")},
	)
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	req := f.lastRequest(t)
	for _, q := range []string{"partNumber", "versionId", "response-content-type"} {
		if _, ok := req.Query[q]; ok {
			t.Errorf("unexpected query param %s", q)
		}
	}
	if req.Header.Get("Range") != "" {
		t.Errorf("unexpected Range header %q", req.Header.Get("Range"))
	}
	// absent response headers map to zero values
	if info.Size != 0 || info.ContentType != "" || info.Version != "" ||
		!info.LastModified.IsZero() {
		t.Errorf("info = %+v, want zero values", info)
	}
}

// testProviderHeadObjectExplicitBucketOverridesConfig checks that an explicit
// bucket overrides the configured one.
func testProviderHeadObjectExplicitBucketOverridesConfig(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	p := newTestProvider(t, f)
	if _, err := p.HeadObject(context.Background(), &storage.HeadObjectInput{
		BaseObjectInput: inBucket("other-bucket"),
	}); err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	if got := f.lastRequest(t).Path; got != "/other-bucket/k" {
		t.Errorf("path = %q, want /other-bucket/k", got)
	}
}

// TestProvider_Keys checks that unusual keys reach the endpoint unchanged.
func TestProvider_Keys(t *testing.T) {
	t.Parallel()

	keys := []string{
		"a",
		"a/b/c",
		"with space.txt",
		"unicode/résumé-日本語.pdf",
		"trailing/",
		"plus+sign",
		"percent%2Fencoded",
		"question?mark",
		"hash#tag",
		"amp&eq=",
		"..",
		"../escape",
		"/abs",
		"a//b",
		strings.Repeat("k", 1024),
	}
	for _, key := range keys {
		t.Run(key[:min(len(key), 40)], func(t *testing.T) {
			t.Parallel()
			f := newFakeS3(t, nil)
			p := newTestProvider(t, f)
			if _, err := p.HeadObject(
				context.Background(),
				&storage.HeadObjectInput{BaseObjectInput: base(key)},
			); err != nil {
				t.Fatalf("HeadObject(%q) error: %v", key, err)
			}
			want := "/" + testBucket + "/" + key
			if got := f.lastRequest(t).Path; got != want {
				t.Errorf("path = %q, want %q (key must be sent verbatim)", got, want)
			}
		})
	}
}

// TestProvider_GetObject covers GetObject.
func TestProvider_GetObject(t *testing.T) {
	t.Parallel()

	t.Run(
		"forwards all fields and maps response",
		testProviderGetObjectForwardsAllFieldsAndMapsResponse,
	)

	t.Run("empty body", testProviderGetObjectEmptyBody)
}

// testProviderGetObjectForwardsAllFieldsAndMapsResponse checks the request
// parameters and response mapping of GetObject.
func testProviderGetObjectForwardsAllFieldsAndMapsResponse(t *testing.T) {
	t.Parallel()
	body := "hello, object"
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		objectHeaders(w, len(body))
		_, _ = io.WriteString(w, body)
	})
	p := newTestProvider(t, f)
	expires := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)

	obj, err := p.GetObject(
		context.Background(),
		&storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{
			BaseObjectInput:            base("k.txt"),
			PartNumber:                 new(int32(1)),
			Range:                      new("bytes=5-"),
			ResponseCacheControl:       new("max-age=60"),
			ResponseContentDisposition: new("inline"),
			ResponseContentEncoding:    new("identity"),
			ResponseContentLanguage:    new("ar-EG"),
			ResponseContentType:        new("text/x-get"),
			ResponseExpires:            &expires,
			Version:                    new("v-get"),
		}},
	)
	if err != nil {
		t.Fatalf("GetObject() error: %v", err)
	}
	data, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatalf("ReadAll() error: %v", err)
	}
	if err := obj.Body.Close(); err != nil {
		t.Errorf("Close() error: %v", err)
	}
	if string(data) != body {
		t.Errorf("body = %q, want %q", data, body)
	}

	req := f.lastRequest(t)
	if req.Method != http.MethodGet || req.Path != "/"+testBucket+"/k.txt" {
		t.Errorf("request = %s %s", req.Method, req.Path)
	}
	wantQuery := map[string]string{
		"partNumber":                   "1",
		"versionId":                    "v-get",
		"response-cache-control":       "max-age=60",
		"response-content-disposition": "inline",
		"response-content-encoding":    "identity",
		"response-content-language":    "ar-EG",
		"response-content-type":        "text/x-get",
		"response-expires":             expires.Format(http.TimeFormat),
	}
	for k, v := range wantQuery {
		if got := req.Query[k]; len(got) != 1 || got[0] != v {
			t.Errorf("query %s = %v, want %q", k, got, v)
		}
	}
	if req.Header.Get("Range") != "bytes=5-" {
		t.Errorf("Range = %q", req.Header.Get("Range"))
	}

	if obj.Size != int64(len(body)) || obj.ContentType != "text/plain" ||
		obj.Version != "ver-123" {
		t.Errorf("object info = %+v", obj.ObjectInfo)
	}
	if !obj.LastModified.Equal(lastModified) {
		t.Errorf("LastModified = %v", obj.LastModified)
	}
	if obj.Metadata["owner"] != "ahmed" {
		t.Errorf("Metadata = %v", obj.Metadata)
	}
}

// testProviderGetObjectEmptyBody checks GetObject of an empty object.
func testProviderGetObjectEmptyBody(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
	})
	p := newTestProvider(t, f)
	obj, err := p.GetObject(context.Background(), &storage.GetObjectInput{
		HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: base("k")},
	})
	if err != nil {
		t.Fatalf("GetObject() error: %v", err)
	}
	defer func() { _ = obj.Body.Close() }()
	data, _ := io.ReadAll(obj.Body)
	if len(data) != 0 || obj.Size != 0 {
		t.Errorf("size=%d len=%d, want 0", obj.Size, len(data))
	}
}

// TestProvider_PutObject covers PutObject.
func TestProvider_PutObject(t *testing.T) {
	t.Parallel()

	t.Run("forwards all fields", testProviderPutObjectForwardsAllFields)

	t.Run("nil body sends empty object", testProviderPutObjectNilBodySendsEmptyObject)

	t.Run("unseekable small body", testProviderPutObjectUnseekableSmallBody)

	t.Run("reader shapes", testProviderPutObjectReaderShapes)

	t.Run(
		"reader error is returned and nothing is sent",
		testProviderPutObjectReaderErrorIsReturnedAndNothingIsSent,
	)
}

// testProviderPutObjectForwardsAllFields checks that every input field
// reaches the request.
func testProviderPutObjectForwardsAllFields(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("ETag", `"e"`)
		w.Header().Set("X-Amz-Version-Id", "put-ver")
		w.WriteHeader(http.StatusOK)
	})
	p := newTestProvider(t, f)
	expires := time.Date(2032, 1, 1, 0, 0, 0, 0, time.UTC)
	body := "payload body"

	info, err := p.PutObject(context.Background(), &storage.PutObjectInput{
		BaseObjectInput: base("up/load.txt"),
		Body:            strings.NewReader(body),
		ContentLength:   new(int64(len(body))),
		ContentType:     new("text/x-put"),
		Expires:         &expires,
		Metadata:        map[string]string{"owner": "ahmed", "Mixed-Case": "v"},
	})
	if err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}
	if info.Version != "put-ver" {
		t.Errorf("Version = %q, want put-ver", info.Version)
	}
	if info.Size != 0 || info.ContentType != "" || info.Metadata != nil {
		t.Errorf("PutObject returns only Version, got %+v", info)
	}

	req := f.lastRequest(t)
	if req.Method != http.MethodPut || req.Path != "/"+testBucket+"/up/load.txt" {
		t.Errorf("request = %s %s", req.Method, req.Path)
	}
	if string(req.Body) != body {
		t.Errorf("body = %q, want %q", req.Body, body)
	}
	if req.Header.Get("Content-Type") != "text/x-put" {
		t.Errorf("Content-Type = %q", req.Header.Get("Content-Type"))
	}
	if req.Header.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length = %q", req.Header.Get("Content-Length"))
	}
	if req.Header.Get("Expires") != expires.Format(http.TimeFormat) {
		t.Errorf(
			"Expires = %q, want %q",
			req.Header.Get("Expires"),
			expires.Format(http.TimeFormat),
		)
	}
	if req.Header.Get("X-Amz-Meta-Owner") != "ahmed" ||
		req.Header.Get("X-Amz-Meta-Mixed-Case") != "v" {
		t.Errorf("metadata headers = %v", req.Header)
	}
}

// testProviderPutObjectNilBodySendsEmptyObject checks that a nil Body uploads
// an empty object.
func testProviderPutObjectNilBodySendsEmptyObject(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	p := newTestProvider(t, f)
	if _, err := p.PutObject(
		context.Background(),
		&storage.PutObjectInput{BaseObjectInput: base("empty")},
	); err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}
	req := f.lastRequest(t)
	if req.Method != http.MethodPut || len(req.Body) != 0 {
		t.Errorf(
			"request = %s with %d body bytes, want PUT with empty body",
			req.Method,
			len(req.Body),
		)
	}
	if cl := req.Header.Get("Content-Length"); cl != "0" && cl != "" {
		t.Errorf("Content-Length = %q, want 0", cl)
	}
}

// testProviderPutObjectUnseekableSmallBody checks an unseekable body below
// the multipart threshold.
func testProviderPutObjectUnseekableSmallBody(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	p := newTestProvider(t, f)
	pr, pw := io.Pipe()
	payload := bytes.Repeat([]byte("abc"), 1000)
	go func() {
		_, _ = pw.Write(payload)
		_ = pw.Close()
	}()
	if _, err := p.PutObject(context.Background(), &storage.PutObjectInput{
		BaseObjectInput: base("pipe"),
		Body:            pr,
	}); err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}
	req := f.lastRequest(t)
	if !bytes.Equal(req.Body, payload) {
		t.Errorf("body length = %d, want %d", len(req.Body), len(payload))
	}
	if req.Header.Get("Content-Length") != strconv.Itoa(len(payload)) {
		t.Errorf("Content-Length = %q, want %d", req.Header.Get("Content-Length"), len(payload))
	}
}

// testProviderPutObjectReaderShapes checks that differently shaped readers
// upload the same bytes.
func testProviderPutObjectReaderShapes(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte("xyz"), 700)
	readers := map[string]func() io.Reader{
		"one byte": func() io.Reader { return iotest.OneByteReader(bytes.NewReader(payload)) },
		"data+EOF": func() io.Reader { return iotest.DataErrReader(bytes.NewReader(payload)) },
		"half":     func() io.Reader { return iotest.HalfReader(bytes.NewReader(payload)) },
	}
	for name, mk := range readers {
		f := newFakeS3(t, nil)
		p := newTestProvider(t, f)
		if _, err := p.PutObject(context.Background(), &storage.PutObjectInput{
			BaseObjectInput: base("k"),
			Body:            mk(),
		}); err != nil {
			t.Fatalf("%s: PutObject() error: %v", name, err)
		}
		if !bytes.Equal(f.lastRequest(t).Body, payload) {
			t.Errorf("%s: body mismatch", name)
		}
	}
}

// testProviderPutObjectReaderErrorIsReturnedAndNothingIsSent checks that a
// body read error is returned without the key and nothing is sent.
func testProviderPutObjectReaderErrorIsReturnedAndNothingIsSent(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	p := newTestProvider(t, f)
	errBoom := errors.New("boom")
	const key = "private/user-supplied-key"
	_, err := p.PutObject(context.Background(), &storage.PutObjectInput{
		BaseObjectInput: base(key),
		Body: io.MultiReader(
			strings.NewReader("partial"),
			iotest.ErrReader(errBoom),
		),
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("PutObject() error = %v, want %v", err, errBoom)
	}
	if strings.Contains(err.Error(), key) {
		t.Errorf("error = %q, must not contain the object key", err)
	}
	if !strings.HasPrefix(err.Error(), "gas/storage/s3/provider: put: ") {
		t.Errorf("error = %q, want gas/storage/s3/provider: put: prefix", err)
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("sent %d requests for a body that failed to read", n)
	}
}

// multipartServer scripts the CreateMultipartUpload / UploadPart /
// CompleteMultipartUpload / AbortMultipartUpload exchange.
type multipartServer struct {
	parts     map[int][]byte
	mu        chan struct{}
	failPart  int
	failAbort bool
	completed atomic.Bool
	aborted   atomic.Bool
	single    atomic.Bool
}

// newMultipartServer returns a multipartServer that fails the given part
// number, or none when failPart is 0.
func newMultipartServer(failPart int) *multipartServer {
	m := &multipartServer{parts: map[int][]byte{}, failPart: failPart, mu: make(chan struct{}, 1)}
	m.mu <- struct{}{}
	return m
}

// handle answers one request of the multipart upload exchange.
func (m *multipartServer) handle(w http.ResponseWriter, r *http.Request, body []byte) {
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodPost && q.Has("uploads"):
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<InitiateMultipartUploadResult><Bucket>%s</Bucket><Key>big</Key><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>`, testBucket)
	case r.Method == http.MethodPut && q.Get("uploadId") == "upload-1":
		n, _ := strconv.Atoi(q.Get("partNumber"))
		if n == m.failPart {
			writeS3Error(w, http.StatusInternalServerError, "InternalError")
			return
		}
		<-m.mu
		m.parts[n] = body
		m.mu <- struct{}{}
		w.Header().Set("ETag", fmt.Sprintf(`"part-%d"`, n))
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost && q.Get("uploadId") == "upload-1":
		m.completed.Store(true)
		w.Header().Set("Content-Type", "application/xml")
		w.Header().Set("X-Amz-Version-Id", "mpu-ver")
		_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<CompleteMultipartUploadResult><Bucket>%s</Bucket><Key>big</Key><ETag>"final"</ETag></CompleteMultipartUploadResult>`, testBucket)
	case r.Method == http.MethodDelete && q.Get("uploadId") == "upload-1":
		if m.failAbort {
			writeS3Error(w, http.StatusInternalServerError, "InternalError")
			return
		}
		m.aborted.Store(true)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut:
		m.single.Store(true)
		w.WriteHeader(http.StatusOK)
	default:
		writeS3Error(w, http.StatusBadRequest, "UnexpectedRequest")
	}
}

// pipeBody returns a reader that streams payload through an io.Pipe, so its
// length is unknown.
func pipeBody(payload []byte) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		const chunk = 64 << 10
		for i := 0; i < len(payload); i += chunk {
			if _, err := pw.Write(payload[i:min(i+chunk, len(payload))]); err != nil {
				return
			}
		}
		_ = pw.Close()
	}()
	return pr
}

// multipartPayload is 17MB, which exceeds the transfer manager's 16MB
// multipart threshold and is split into 8MB parts: 8 + 8 + 1.
var multipartPayload = sync.OnceValue(func() []byte {
	payload := make([]byte, 17<<20)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	return payload
})

// TestProvider_PutObjectMultipart covers PutObject bodies large enough for a
// multipart upload.
func TestProvider_PutObjectMultipart(t *testing.T) {
	t.Parallel()

	t.Run("unknown-length stream", testProviderPutObjectMultipartUnknownLengthStream)

	t.Run("part failure aborts upload", testProviderPutObjectMultipartPartFailureAbortsUpload)
}

// TestProvider_PutObjectContentLength checks that a set ContentLength is
// enforced: a shorter or longer body fails with ErrContentLengthMismatch.
// A small body is buffered before any request, so nothing is sent; a
// multipart upload is aborted and never completed.
func TestProvider_PutObjectContentLength(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		body     string
		length   int64
		mismatch bool
	}{
		{name: "exact", body: "hello", length: 5},
		{name: "zero", body: "", length: 0},
		{name: "short", body: "hel", length: 5, mismatch: true},
		{name: "long", body: "hello world", length: 5, mismatch: true},
		{name: "negative", body: "hello", length: -1, mismatch: true},
	} {
		t.Run("single part "+tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeS3(t, nil)
			_, err := newTestProvider(t, f).PutObject(context.Background(), &storage.PutObjectInput{
				BaseObjectInput: base("k"),
				Body:            strings.NewReader(tc.body),
				ContentLength:   new(tc.length),
			})
			if !tc.mismatch {
				if err != nil {
					t.Fatalf("PutObject() error: %v", err)
				}
				if got := string(f.lastRequest(t).Body); got != tc.body {
					t.Errorf("sent body %q, want %q", got, tc.body)
				}
				return
			}
			if !errors.Is(err, storage.ErrContentLengthMismatch) {
				t.Fatalf("PutObject() error = %v, want ErrContentLengthMismatch", err)
			}
			if n := len(f.requests()); n != 0 {
				t.Errorf("PutObject() sent %d requests, want none", n)
			}
		})
	}

	for _, tc := range []struct {
		name  string
		delta int64
	}{{"short", 1000}, {"long", -1000}} {
		t.Run("multipart "+tc.name, func(t *testing.T) {
			t.Parallel()
			payload := multipartPayload()
			m := newMultipartServer(0)
			f := newFakeS3(t, m.handle)
			_, err := newTestProvider(t, f).PutObject(context.Background(), &storage.PutObjectInput{
				BaseObjectInput: base("big"),
				Body:            pipeBody(payload),
				ContentLength:   new(int64(len(payload)) + tc.delta),
			})
			if !errors.Is(err, storage.ErrContentLengthMismatch) {
				t.Fatalf("PutObject() error = %v, want ErrContentLengthMismatch", err)
			}
			if m.completed.Load() || !m.aborted.Load() || m.single.Load() {
				t.Errorf("completed=%v aborted=%v single=%v, want an aborted multipart upload",
					m.completed.Load(), m.aborted.Load(), m.single.Load())
			}
		})
	}

	// Guards transfermanager's multiUploader.seterr (v0.4.16), which keeps
	// the first error: when the abort itself fails, the mismatch survives
	// with its chain and the abort's error is dropped. The upload stays
	// incomplete on the server.
	t.Run("multipart short with a failing abort", func(t *testing.T) {
		t.Parallel()
		payload := multipartPayload()
		m := newMultipartServer(0)
		m.failAbort = true
		f := newFakeS3(t, m.handle)
		_, err := newTestProvider(t, f).PutObject(context.Background(), &storage.PutObjectInput{
			BaseObjectInput: base("big"),
			Body:            pipeBody(payload),
			ContentLength:   new(int64(len(payload)) + 1000),
		})
		if !errors.Is(err, storage.ErrContentLengthMismatch) {
			t.Fatalf("PutObject() error = %v, want ErrContentLengthMismatch", err)
		}
		if m.completed.Load() {
			t.Error("multipart upload completed, want it left incomplete")
		}
	})
}

// testProviderPutObjectMultipartUnknownLengthStream checks a multipart upload
// of a stream of unknown length.
func testProviderPutObjectMultipartUnknownLengthStream(t *testing.T) {
	t.Parallel()
	payload := multipartPayload()
	m := newMultipartServer(0)
	f := newFakeS3(t, m.handle)
	p := newTestProvider(t, f)

	info, err := p.PutObject(context.Background(), &storage.PutObjectInput{
		BaseObjectInput: base("big"),
		Body:            pipeBody(payload),
		ContentType:     new("application/x-big"),
		Metadata:        map[string]string{"owner": "ahmed"},
	})
	if err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}
	if !m.completed.Load() || m.single.Load() {
		t.Fatalf(
			"completed=%v single=%v, want multipart upload",
			m.completed.Load(),
			m.single.Load(),
		)
	}
	if info.Version != "mpu-ver" {
		t.Errorf("Version = %q, want mpu-ver", info.Version)
	}
	var got []byte
	for i := 1; i <= len(m.parts); i++ {
		got = append(got, m.parts[i]...)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf(
			"reassembled %d bytes in %d parts, want %d bytes",
			len(got),
			len(m.parts),
			len(payload),
		)
	}
	for _, req := range f.requests() {
		if req.Method == http.MethodPost && req.Query["uploads"] != nil {
			if req.Header.Get("Content-Type") != "application/x-big" ||
				req.Header.Get("X-Amz-Meta-Owner") != "ahmed" {
				t.Errorf("CreateMultipartUpload headers = %v", req.Header)
			}
		}
	}
}

// testProviderPutObjectMultipartPartFailureAbortsUpload checks that a failed
// part aborts the multipart upload.
func testProviderPutObjectMultipartPartFailureAbortsUpload(t *testing.T) {
	t.Parallel()
	payload := multipartPayload()
	m := newMultipartServer(2)
	f := newFakeS3(t, m.handle)
	p := newTestProvider(t, f)

	_, err := p.PutObject(context.Background(), &storage.PutObjectInput{
		BaseObjectInput: base("big"),
		Body:            pipeBody(payload),
	})
	if err == nil {
		t.Fatal("PutObject() error = nil, want error")
	}
	if apiErr, ok := errors.AsType[smithy.APIError](
		err,
	); !ok ||
		apiErr.ErrorCode() != "InternalError" {
		t.Errorf("error = %v, want InternalError API error in chain", err)
	}
	if !m.aborted.Load() {
		t.Error("AbortMultipartUpload was not sent after part failure")
	}
	if m.completed.Load() {
		t.Error("CompleteMultipartUpload sent despite part failure")
	}
}

// TestProvider_DeleteObject covers DeleteObject.
func TestProvider_DeleteObject(t *testing.T) {
	t.Parallel()

	t.Run("forwards version and maps response", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.Header().Set("X-Amz-Version-Id", "deleted-ver")
			w.Header().Set("X-Amz-Delete-Marker", "true")
			w.WriteHeader(http.StatusNoContent)
		})
		p := newTestProvider(t, f)
		info, err := p.DeleteObject(context.Background(), &storage.DeleteObjectInput{
			BaseObjectInput: base("del/me"),
			Version:         new("v-del"),
		})
		if err != nil {
			t.Fatalf("DeleteObject() error: %v", err)
		}
		if info.Version != "deleted-ver" {
			t.Errorf("Version = %q", info.Version)
		}
		req := f.lastRequest(t)
		if req.Method != http.MethodDelete || req.Path != "/"+testBucket+"/del/me" {
			t.Errorf("request = %s %s", req.Method, req.Path)
		}
		if got := req.Query["versionId"]; len(got) != 1 || got[0] != "v-del" {
			t.Errorf("versionId = %v", got)
		}
	})

	t.Run("no version", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.WriteHeader(http.StatusNoContent)
		})
		p := newTestProvider(t, f)
		info, err := p.DeleteObject(
			context.Background(),
			&storage.DeleteObjectInput{BaseObjectInput: base("k")},
		)
		if err != nil {
			t.Fatalf("DeleteObject() error: %v", err)
		}
		if info.Version != "" {
			t.Errorf("Version = %q, want empty", info.Version)
		}
		if _, ok := f.lastRequest(t).Query["versionId"]; ok {
			t.Error("versionId sent without Version input")
		}
	})
}

var errorMappingOps = map[string]func(p *Provider) error{
	"create bucket": func(p *Provider) error {
		_, err := p.CreateBucket(
			context.Background(),
			&storage.CreateBucketInput{Bucket: "new-bucket"},
		)
		return err
	},
	"head": func(p *Provider) error {
		_, err := p.HeadObject(
			context.Background(),
			&storage.HeadObjectInput{BaseObjectInput: base("k")},
		)
		return err
	},
	"get": func(p *Provider) error {
		_, err := p.GetObject(context.Background(), &storage.GetObjectInput{
			HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: base("k")},
		})
		return err
	},
	"put": func(p *Provider) error {
		_, err := p.PutObject(context.Background(), &storage.PutObjectInput{
			BaseObjectInput: base("k"), Body: strings.NewReader("x"),
		})
		return err
	},
	"delete": func(p *Provider) error {
		_, err := p.DeleteObject(
			context.Background(),
			&storage.DeleteObjectInput{BaseObjectInput: base("k")},
		)
		return err
	},
}

// errorMappingCase is a scripted S3 error response and the sentinel it must
// map to.
type errorMappingCase struct {
	want    error
	name    string
	code    string
	ops     []string
	status  int
	bodyErr bool
}

// checkErrorMapping runs op against a fake that answers with tt and checks
// the prefix, message, sentinel, and API error.
func checkErrorMapping(t *testing.T, tt errorMappingCase, op string) {
	t.Helper()
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		if tt.bodyErr {
			writeS3Error(w, tt.status, tt.code)
			return
		}
		w.WriteHeader(tt.status)
	})
	p := newTestProvider(t, f)
	err := errorMappingOps[op](p)
	if err == nil {
		t.Fatal("error = nil, want error")
	}
	if !strings.HasPrefix(err.Error(), "gas/storage/s3/provider: "+op) {
		t.Errorf("error = %q, want gas/storage/s3/provider: %s prefix", err, op)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("error = %q, want a single-line message", err)
	}
	assertOnlySentinel(t, err, tt.want)
	if _, ok := errors.AsType[smithy.APIError](err); !ok {
		t.Errorf("original API error not reachable via errors.As: %v", err)
	}
	if tt.code != "" {
		apiErr, _ := errors.AsType[smithy.APIError](err)
		if apiErr != nil && apiErr.ErrorCode() != tt.code {
			t.Errorf("ErrorCode() = %q, want %q", apiErr.ErrorCode(), tt.code)
		}
	}
}

// TestProvider_ErrorMapping checks how each operation maps S3 error responses
// to storage sentinels.
func TestProvider_ErrorMapping(t *testing.T) {
	t.Parallel()

	tests := []errorMappingCase{
		{
			name:    "NoSuchKey",
			status:  404,
			code:    "NoSuchKey",
			bodyErr: true,
			want:    storage.ErrKeyNotFound,
			ops:     []string{"get", "put", "delete"},
		},
		{
			name:    "NoSuchBucket",
			status:  404,
			code:    "NoSuchBucket",
			bodyErr: true,
			want:    storage.ErrBucketNotFound,
			ops:     []string{"get", "put", "delete"},
		},
		{
			name:    "NoSuchVersion",
			status:  404,
			code:    "NoSuchVersion",
			bodyErr: true,
			want:    storage.ErrVersionNotFound,
			ops:     []string{"get"},
		},
		{name: "bare 404 on HEAD", status: 404, want: storage.ErrNotFound, ops: []string{"head"}},
		{
			name:    "InvalidRange",
			status:  416,
			code:    "InvalidRange",
			bodyErr: true,
			want:    storage.ErrInvalidRange,
			ops:     []string{"get"},
		},
		{
			name:   "bare 416 on HEAD",
			status: 416,
			want:   storage.ErrInvalidRange,
			ops:    []string{"head"},
		},
		{
			name:    "BucketAlreadyExists",
			status:  409,
			code:    "BucketAlreadyExists",
			bodyErr: true,
			want:    storage.ErrBucketExists,
			ops:     []string{"create bucket"},
		},
		{
			name: "AccessDenied passes through", status: 403, code: "AccessDenied", bodyErr: true,
			ops: []string{"get", "put", "delete", "create bucket"},
		},
		{name: "bare 403 on HEAD passes through", status: 403, ops: []string{"head"}},
		{
			name: "InternalError passes through", status: 500, code: "InternalError", bodyErr: true,
			ops: []string{"get", "put", "delete", "create bucket"},
		},
	}
	for _, tt := range tests {
		for _, op := range tt.ops {
			t.Run(tt.name+"/"+op, func(t *testing.T) {
				t.Parallel()
				checkErrorMapping(t, tt, op)
			})
		}
	}

	t.Run("head on missing bucket is ErrNotFound", func(t *testing.T) {
		t.Parallel()
		// S3 answers HEAD with a bodiless 404 for both missing key and
		// missing bucket, so the two cannot be told apart.
		f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.WriteHeader(http.StatusNotFound)
		})
		p := newTestProvider(t, f)
		err := errorMappingOps["head"](p)
		if !errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrBucketNotFound) {
			t.Errorf("error = %v, want ErrNotFound only", err)
		}
	})

	t.Run("typed NoSuchKey from GetObject", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			writeS3Error(w, http.StatusNotFound, "NoSuchKey")
		})
		p := newTestProvider(t, f)
		err := errorMappingOps["get"](p)
		if _, ok := errors.AsType[*types.NoSuchKey](err); !ok {
			t.Errorf("error = %v, want *types.NoSuchKey in chain", err)
		}
	})
}

// TestWrapS3Error checks wrapS3Error on typed, generic, wrapped, and unmapped
// errors.
func TestWrapS3Error(t *testing.T) {
	t.Parallel()

	plain := errors.New("plain")
	tests := []struct {
		err  error
		want error
		name string
	}{
		{name: "typed NoSuchKey", err: &types.NoSuchKey{}, want: storage.ErrKeyNotFound},
		{name: "typed NoSuchBucket", err: &types.NoSuchBucket{}, want: storage.ErrBucketNotFound},
		{name: "typed NotFound", err: &types.NotFound{}, want: storage.ErrNotFound},
		{name: "typed BucketAlreadyOwnedByYou is unmapped", err: &types.BucketAlreadyOwnedByYou{}},
		{
			name: "typed BucketAlreadyExists",
			err:  &types.BucketAlreadyExists{},
			want: storage.ErrBucketExists,
		},
		{
			name: "generic code BucketAlreadyOwnedByYou is unmapped",
			err:  &smithy.GenericAPIError{Code: "BucketAlreadyOwnedByYou"},
		},
		{
			name: "generic code BucketAlreadyExists",
			err:  &smithy.GenericAPIError{Code: "BucketAlreadyExists"},
			want: storage.ErrBucketExists,
		},
		{
			name: "generic code InvalidRange",
			err:  &smithy.GenericAPIError{Code: "InvalidRange"},
			want: storage.ErrInvalidRange,
		},
		{
			// The SDK names a bodiless HEAD 416 after its status text.
			name: "generic code RequestedRangeNotSatisfiable",
			err:  &smithy.GenericAPIError{Code: "RequestedRangeNotSatisfiable"},
			want: storage.ErrInvalidRange,
		},
		{
			name: "generic code NoSuchKey",
			err:  &smithy.GenericAPIError{Code: "NoSuchKey"},
			want: storage.ErrKeyNotFound,
		},
		{
			name: "generic code NoSuchBucket",
			err:  &smithy.GenericAPIError{Code: "NoSuchBucket"},
			want: storage.ErrBucketNotFound,
		},
		{
			name: "generic code NoSuchVersion",
			err:  &smithy.GenericAPIError{Code: "NoSuchVersion"},
			want: storage.ErrVersionNotFound,
		},
		{
			name: "operation error wrapping NoSuchVersion",
			err: &smithy.OperationError{
				ServiceID:     "S3",
				OperationName: "GetObject",
				Err:           &smithy.GenericAPIError{Code: "NoSuchVersion"},
			},
			want: storage.ErrVersionNotFound,
		},
		{name: "case-sensitive NoSuchVersion", err: &smithy.GenericAPIError{Code: "nosuchversion"}},
		{
			name: "generic code NotFound",
			err:  &smithy.GenericAPIError{Code: "NotFound"},
			want: storage.ErrNotFound,
		},
		{
			name: "wrapped typed",
			err:  fmt.Errorf("op: %w", &types.NoSuchKey{}),
			want: storage.ErrKeyNotFound,
		},
		{name: "operation error wrapping generic", err: &smithy.OperationError{
			ServiceID:     "S3",
			OperationName: "GetObject",
			Err:           &smithy.GenericAPIError{Code: "NoSuchBucket"},
		}, want: storage.ErrBucketNotFound},
		{name: "case-sensitive code", err: &smithy.GenericAPIError{Code: "nosuchkey"}},
		{name: "other code", err: &smithy.GenericAPIError{Code: "AccessDenied"}},
		{name: "plain error", err: plain},
		{name: "context canceled", err: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := wrapS3Error(tt.err)
			if !errors.Is(got, tt.err) {
				t.Errorf("wrapS3Error() lost the original error: %v", got)
			}
			if strings.Contains(got.Error(), "\n") {
				t.Errorf("wrapS3Error() = %q, want a single-line message", got)
			}
			assertOnlySentinel(t, got, tt.want)
		})
	}

	if wrapS3Error(nil) != nil {
		t.Error("wrapS3Error(nil) != nil")
	}
}

// TestValueOf checks valueOf with nil and non-nil pointers.
func TestValueOf(t *testing.T) {
	t.Parallel()
	if valueOf[int64](nil) != 0 || valueOf(new(int64(7))) != 7 {
		t.Error("valueOf[int64] mismatch")
	}
	if valueOf[string](nil) != "" || valueOf(new("s")) != "s" {
		t.Error("valueOf[string] mismatch")
	}
	if !valueOf[time.Time](nil).IsZero() {
		t.Error("valueOf[time.Time](nil) not zero")
	}
}

// TestProvider_ContextCanceled checks that a canceled context fails every
// operation without sending a request.
func TestProvider_ContextCanceled(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	p := newTestProvider(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: "new-bucket"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("CreateBucket() error = %v, want context.Canceled", err)
	}
	_, err = p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: base("k")})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("HeadObject() error = %v, want context.Canceled", err)
	}
	_, err = p.GetObject(
		ctx,
		&storage.GetObjectInput{
			HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: base("k")},
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("GetObject() error = %v, want context.Canceled", err)
	}
	_, err = p.PutObject(
		ctx,
		&storage.PutObjectInput{BaseObjectInput: base("k"), Body: strings.NewReader("x")},
	)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("PutObject() error = %v, want context.Canceled", err)
	}
	_, err = p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: base("k")})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("DeleteObject() error = %v, want context.Canceled", err)
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("canceled context still sent %d requests", n)
	}
}

// TestProvider_DIWiring checks that the constructors resolve through the DI
// container.
func TestProvider_DIWiring(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	mock, err := configtest.NewMockConfigWithValues(
		map[string]any{"storage": map[string]any{"bucket": testBucket}},
	)
	if err != nil {
		t.Fatal(err)
	}
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](mock),
		gas.WithSingletonService[storage.Provider](NewProvider(WithClient(f.client(t)))),
		gas.WithSingletonService[storage.Presigner](
			NewPresigner(WithPresignerS3Client(f.client(t))),
		),
	)
	if err := w.InitServices(context.Background()); err != nil {
		t.Fatalf("InitServices() error: %v", err)
	}
	if _, err := w.ServiceContainer().Resolve[storage.Provider](); err != nil {
		t.Errorf("Resolve[storage.Provider]() error: %v", err)
	}
	if _, err := w.ServiceContainer().Resolve[storage.Presigner](); err != nil {
		t.Errorf("Resolve[storage.Presigner]() error: %v", err)
	}
}

// TestProvider_DIMissingConfigProvider checks that an application that
// registers no gas.ConfigProvider gets the container's resolution error at
// startup, for both constructors.
func TestProvider_DIMissingConfigProvider(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)

	const want = "no registration for gas.ConfigProvider"

	w := gas.NewWorker(
		gas.WithSingletonService[storage.Provider](NewProvider(WithClient(f.client(t)))),
	)
	err := w.InitServices(context.Background())
	if !errors.Is(err, gas.ErrNotRegistered) || !strings.Contains(err.Error(), want) {
		t.Errorf("InitServices() with NewProvider error = %v, want %q", err, want)
	}

	w = gas.NewWorker(
		gas.WithSingletonService[storage.Presigner](
			NewPresigner(WithPresignerS3Client(f.client(t))),
		),
	)
	err = w.InitServices(context.Background())
	if !errors.Is(err, gas.ErrNotRegistered) || !strings.Contains(err.Error(), want) {
		t.Errorf("InitServices() with NewPresigner error = %v, want %q", err, want)
	}
}

// TestProvider_CreateBucket covers CreateBucket against the fake endpoint:
// the request it sends, how the response maps to storage.BucketInfo, input
// validation, and how an existing bucket is reported.
func TestProvider_CreateBucket(t *testing.T) {
	t.Parallel()

	t.Run("sends PUT on the bucket and maps the response", testProviderCreateBucketMapsResponse)
	t.Run("response without ARN or Location", testProviderCreateBucketEmptyResponse)
	t.Run("ignores the configured bucket", testProviderCreateBucketIgnoresConfiguredBucket)
	t.Run("invalid bucket names send nothing", testProviderCreateBucketInvalidNames)
	t.Run("BucketAlreadyExists is ErrBucketExists", testProviderCreateBucketExists)
	t.Run("typed exists error stays reachable", testProviderCreateBucketTypedExists)
	t.Run("BucketAlreadyOwnedByYou is success", testProviderCreateBucketOwned)
	t.Run("us-east-1 sends no location constraint", testProviderCreateBucketUSEast1)
	t.Run("aws-global sends no location constraint", testProviderCreateBucketAWSGlobal)
	t.Run("other regions send their location constraint", testProviderCreateBucketOtherRegion)
}

// TestProvider_CreateBucketResolvedRegion checks that with no configured
// region, CreateBucket sends the region the SDK resolved (here from
// AWS_REGION) as the location constraint. It sets the environment, so it does
// not run in parallel.
func TestProvider_CreateBucketResolvedRegion(t *testing.T) {
	t.Setenv("AWS_REGION", "eu-west-1")
	f := newFakeS3(t, nil)
	p, err := NewProvider()(newConfig(t, map[string]any{
		"accessKeyID":     testAKID,
		"secretAccessKey": testSecret,
		"endpoint":        f.srv.URL,
	}))
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}
	if _, err := p.CreateBucket(
		context.Background(),
		&storage.CreateBucketInput{Bucket: "new-bucket"},
	); err != nil {
		t.Fatalf("CreateBucket() error: %v", err)
	}
	assertLocationConstraint(t, f.lastRequest(t).Body, "eu-west-1")
}

// createBucketInRegion calls CreateBucket on a provider whose client is in
// region and backed by f.
func createBucketInRegion(
	t *testing.T,
	f *fakeS3,
	region, bucket string,
) (*storage.BucketInfo, error) {
	t.Helper()
	p, err := NewProvider(WithClient(f.clientInRegion(t, region)))(newConfig(t, map[string]any{}))
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}
	return p.CreateBucket(context.Background(), &storage.CreateBucketInput{Bucket: bucket})
}

// assertLocationConstraint checks that a CreateBucket request body names
// want as its location constraint, or carries no configuration when want is
// empty.
func assertLocationConstraint(t *testing.T, body []byte, want string) {
	t.Helper()
	if want == "" {
		if len(bytes.TrimSpace(body)) != 0 {
			t.Errorf("request body = %q, want none (no location constraint)", body)
		}
		return
	}
	tag := "<LocationConstraint>" + want + "</LocationConstraint>"
	if !bytes.Contains(body, []byte(tag)) {
		t.Errorf("request body = %q, want it to contain %s", body, tag)
	}
}

// testProviderCreateBucketUSEast1 checks that a us-east-1 client sends no
// CreateBucketConfiguration, which S3 rejects there.
func testProviderCreateBucketUSEast1(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	if _, err := createBucketInRegion(t, f, "us-east-1", "new-bucket"); err != nil {
		t.Fatalf("CreateBucket() error: %v", err)
	}
	assertLocationConstraint(t, f.lastRequest(t).Body, "")
}

// testProviderCreateBucketAWSGlobal checks that the aws-global pseudo-region,
// which the SDK maps to the global (us-east-1) endpoint, sends no location
// constraint: S3 rejects "aws-global" as one with InvalidLocationConstraint.
func testProviderCreateBucketAWSGlobal(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	if _, err := createBucketInRegion(t, f, "aws-global", "new-bucket"); err != nil {
		t.Fatalf("CreateBucket() error: %v", err)
	}
	assertLocationConstraint(t, f.lastRequest(t).Body, "")
}

// testProviderCreateBucketOtherRegion checks that a client outside us-east-1
// sends its region as the location constraint, without which S3 answers 400
// IllegalLocationConstraintException.
func testProviderCreateBucketOtherRegion(t *testing.T) {
	t.Parallel()
	for _, region := range []string{"eu-central-1", "ap-southeast-2", "us-west-2"} {
		f := newFakeS3(t, nil)
		if _, err := createBucketInRegion(t, f, region, "new-bucket"); err != nil {
			t.Fatalf("CreateBucket() in %s error: %v", region, err)
		}
		assertLocationConstraint(t, f.lastRequest(t).Body, region)
	}
}

// testProviderCreateBucketOwned checks that BucketAlreadyOwnedByYou, S3's
// answer outside us-east-1 for a bucket the caller already owns, is reported
// as success, with an empty BucketInfo because that answer carries no ARN or
// Location.
func testProviderCreateBucketOwned(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeS3Error(w, http.StatusConflict, "BucketAlreadyOwnedByYou")
	})
	info, err := createBucket(t, f, testBucket)
	if err != nil {
		t.Fatalf("CreateBucket() of an owned bucket error = %v, want nil", err)
	}
	if info == nil || info.ID != "" || info.Location != "" {
		t.Errorf("CreateBucket() of an owned bucket = %+v, want empty BucketInfo", info)
	}
}

// createBucket calls CreateBucket on a provider backed by f.
func createBucket(t *testing.T, f *fakeS3, bucket string) (*storage.BucketInfo, error) {
	t.Helper()
	return newTestProvider(t, f).CreateBucket(
		context.Background(),
		&storage.CreateBucketInput{Bucket: bucket},
	)
}

// testProviderCreateBucketMapsResponse checks the CreateBucket request and
// the mapping of the ARN and Location.
func testProviderCreateBucketMapsResponse(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Location", "/new-bucket")
		w.Header().Set("X-Amz-Bucket-Arn", "arn:aws:s3:::new-bucket")
		w.WriteHeader(http.StatusOK)
	})
	info, err := createBucket(t, f, "new-bucket")
	if err != nil {
		t.Fatalf("CreateBucket() error: %v", err)
	}
	if info.ID != "arn:aws:s3:::new-bucket" || info.Location != "/new-bucket" {
		t.Errorf("CreateBucket() = %+v, want ARN and Location from the response", info)
	}
	req := f.lastRequest(t)
	if req.Method != http.MethodPut || req.Path != "/new-bucket" {
		t.Errorf("request = %s %s, want PUT /new-bucket", req.Method, req.Path)
	}
}

// testProviderCreateBucketEmptyResponse checks CreateBucket when S3 returns
// no ARN or Location.
func testProviderCreateBucketEmptyResponse(t *testing.T) {
	t.Parallel()
	info, err := createBucket(t, newFakeS3(t, nil), "new-bucket")
	if err != nil {
		t.Fatalf("CreateBucket() error: %v", err)
	}
	if info == nil || info.ID != "" || info.Location != "" {
		t.Errorf("CreateBucket() = %+v, want empty BucketInfo", info)
	}
}

// testProviderCreateBucketIgnoresConfiguredBucket checks that CreateBucket
// creates the named bucket, not the configured one.
func testProviderCreateBucketIgnoresConfiguredBucket(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	if _, err := createBucket(t, f, "other-bucket"); err != nil {
		t.Fatalf("CreateBucket() error: %v", err)
	}
	if got := f.lastRequest(t).Path; got != "/other-bucket" {
		t.Errorf("path = %q, want /other-bucket", got)
	}
}

// testProviderCreateBucketInvalidNames checks that invalid names return
// ErrInvalidBucket without a request.
func testProviderCreateBucketInvalidNames(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	for _, bk := range []string{"", "ab", "Bad_Bucket", "1.2.3.4", "bucket-", "xn--b"} {
		info, err := createBucket(t, f, bk)
		if !errors.Is(err, storage.ErrInvalidBucket) {
			t.Errorf("CreateBucket(%q) error = %v, want ErrInvalidBucket", bk, err)
			continue
		}
		if info != nil {
			t.Errorf("CreateBucket(%q) = %+v, want nil", bk, info)
		}
		if !strings.HasPrefix(err.Error(), "gas/storage/s3/provider: create bucket: ") {
			t.Errorf("CreateBucket(%q) error = %q, want create bucket prefix", bk, err)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("invalid bucket sent %d requests", n)
	}
}

// testProviderCreateBucketExists checks that BucketAlreadyExists, a name
// another account owns, returns ErrBucketExists with the API error in the
// chain.
func testProviderCreateBucketExists(t *testing.T) {
	t.Parallel()
	const code = "BucketAlreadyExists"
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeS3Error(w, http.StatusConflict, code)
	})
	info, err := createBucket(t, f, testBucket)
	if err == nil {
		t.Fatal("CreateBucket() error = nil, want ErrBucketExists")
	}
	if info != nil {
		t.Errorf("CreateBucket() = %+v, want nil", info)
	}
	assertOnlySentinel(t, err, storage.ErrBucketExists)
	if apiErr, ok := errors.AsType[smithy.APIError](err); !ok || apiErr.ErrorCode() != code {
		t.Errorf("error = %v, want API error %s reachable via errors.As", err, code)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("error = %q, want a single-line message", err)
	}
}

// testProviderCreateBucketTypedExists checks that the typed SDK error stays
// reachable with errors.As.
func testProviderCreateBucketTypedExists(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeS3Error(w, http.StatusConflict, "BucketAlreadyExists")
	})
	_, err := createBucket(t, f, testBucket)
	if _, ok := errors.AsType[*types.BucketAlreadyExists](err); !ok {
		t.Errorf("error = %v, want *types.BucketAlreadyExists in chain", err)
	}
}
