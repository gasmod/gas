package s3

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/storage"
)

// parsePresigned parses the URL of a presigned response, failing the test on
// error.
func parsePresigned(t *testing.T, res *storage.PresignURLResponse) *url.URL {
	t.Helper()
	if res == nil {
		t.Fatal("presign response is nil")
	}
	u, err := url.Parse(res.URL)
	if err != nil {
		t.Fatalf("url.Parse(%q) error: %v", res.URL, err)
	}
	return u
}

// signedHeaderList returns the X-Amz-SignedHeaders of a presigned URL.
func signedHeaderList(u *url.URL) []string {
	return strings.Split(u.Query().Get("X-Amz-SignedHeaders"), ";")
}

// hasSignedHeader reports whether name is among the signed headers of a
// presigned URL.
func hasSignedHeader(u *url.URL, name string) bool {
	return headerHasToken(u.Query().Get("X-Amz-SignedHeaders"), name)
}

// TestNewPresigner covers NewPresigner with injected clients, invalid
// configuration, and config failures.
func TestNewPresigner(t *testing.T) {
	t.Parallel()

	t.Run("with s3 client", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		p, err := NewPresigner(
			WithPresignerS3Client(f.client(t)),
		)(
			newConfig(t, map[string]any{"bucket": testBucket}),
		)
		if err != nil {
			t.Fatalf("NewPresigner() error: %v", err)
		}
		if p.PresignClient() == nil {
			t.Error("PresignClient() = nil, want client derived from s3 client")
		}
	})

	t.Run("with presign client only", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		pc := s3.NewPresignClient(f.client(t))
		p, err := NewPresigner(WithPresignClient(pc))(newConfig(t, map[string]any{}))
		if err != nil {
			t.Fatalf("NewPresigner() error: %v", err)
		}
		if p.PresignClient() != pc {
			t.Error("PresignClient() is not the injected client")
		}
		if p.s3Client != nil {
			t.Error("s3Client should not be created when a presign client is injected")
		}
	})

	t.Run("presign client wins over s3 client", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		pc := s3.NewPresignClient(f.client(t))
		p, err := NewPresigner(
			WithPresignerS3Client(f.client(t)),
			WithPresignClient(pc),
		)(
			newConfig(t, map[string]any{}),
		)
		if err != nil {
			t.Fatalf("NewPresigner() error: %v", err)
		}
		if p.PresignClient() != pc {
			t.Error("PresignClient() is not the injected presign client")
		}
	})

	t.Run("invalid bucket", func(t *testing.T) {
		t.Parallel()
		f := newFakeS3(t, nil)
		p, err := NewPresigner(
			WithPresignerS3Client(f.client(t)),
		)(
			newConfig(t, map[string]any{"bucket": "Not_Valid"}),
		)
		if !errors.Is(err, storage.ErrInvalidBucket) {
			t.Errorf("NewPresigner() error = %v, want ErrInvalidBucket", err)
		}
		if p != nil {
			t.Error("NewPresigner() returned presigner")
		}
		if err != nil && !strings.HasPrefix(err.Error(), "gas/storage/s3/presigner: ") {
			t.Errorf("error = %q, want gas/storage/s3/presigner prefix", err)
		}
	})

	t.Run("missing config provider", func(t *testing.T) {
		t.Parallel()
		_, err := NewPresigner()(nil)
		if err == nil || !strings.Contains(err.Error(), "config provider is nil") {
			t.Errorf("NewPresigner() error = %v, want config provider is nil error", err)
		}
	})

	t.Run("bind failure", func(t *testing.T) {
		t.Parallel()
		bindErr := errors.New("bind failed")
		_, err := NewPresigner()(&configtest.MockConfig{
			BindFn: func(any, ...config.BindOption) error { return bindErr },
		})
		if !errors.Is(err, bindErr) {
			t.Errorf("NewPresigner() error = %v, want %v", err, bindErr)
		}
	})
}

// TestPresigner_ZeroValue checks that a Presigner not built by NewPresigner
// returns ErrNotInitialized.
func TestPresigner_ZeroValue(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	ctx := context.Background()

	presigners := map[string]*Presigner{
		"nil pointer":    nil,
		"zero":           {},
		"s3 client only": {s3Client: f.client(t), config: &Config{}},
		"no config":      {presignClient: s3.NewPresignClient(f.client(t))},
	}
	for name, p := range presigners {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := p.PresignUploadURL(ctx, &storage.PresignUploadURLInput{
				PutObjectInput: storage.PutObjectInput{BaseObjectInput: inBucket(testBucket)},
			})
			if !errors.Is(err, storage.ErrNotInitialized) {
				t.Errorf("PresignUploadURL() error = %v, want ErrNotInitialized", err)
			}
			_, err = p.PresignDownloadURL(ctx, &storage.PresignDownloadURLInput{
				GetObjectInput: storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{
					BaseObjectInput: inBucket(testBucket),
				}},
			})
			if !errors.Is(err, storage.ErrNotInitialized) {
				t.Errorf("PresignDownloadURL() error = %v, want ErrNotInitialized", err)
			}
			_, err = p.PresignUploadURL(ctx, nil)
			if !errors.Is(err, storage.ErrNotInitialized) {
				t.Errorf("PresignUploadURL(nil) error = %v, want ErrNotInitialized", err)
			}
			if name != "no config" && p.PresignClient() != nil {
				t.Error("PresignClient() should be nil")
			}
		})
	}
}

// TestPresigner_NilInput checks that nil inputs return ErrInputRequired with
// the operation prefix.
func TestPresigner_NilInput(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	p := newTestPresigner(t, f, testBucket)

	_, err := p.PresignUploadURL(context.Background(), nil)
	if !errors.Is(err, storage.ErrInputRequired) {
		t.Errorf("PresignUploadURL(nil) error = %v, want ErrInputRequired", err)
	}
	if err != nil && !strings.HasPrefix(err.Error(), "gas/storage/s3/presigner: presign upload: ") {
		t.Errorf("error = %q", err)
	}
	_, err = p.PresignDownloadURL(context.Background(), nil)
	if !errors.Is(err, storage.ErrInputRequired) {
		t.Errorf("PresignDownloadURL(nil) error = %v, want ErrInputRequired", err)
	}
	if err != nil &&
		!strings.HasPrefix(err.Error(), "gas/storage/s3/presigner: presign download: ") {
		t.Errorf("error = %q", err)
	}
}

// TestPresigner_PresignUploadURL covers PresignUploadURL.
func TestPresigner_PresignUploadURL(t *testing.T) {
	t.Parallel()

	t.Run("forwards all fields", testPresignerPresignUploadURLForwardsAllFields)

	t.Run("default TTL", testPresignerPresignUploadURLDefaultTTL)

	t.Run("explicit bucket", testPresignerPresignUploadURLExplicitBucket)

	t.Run("body is ignored", testPresignerPresignUploadURLBodyIsIgnored)
}

// testPresignerPresignUploadURLForwardsAllFields checks that every input
// field is signed into the URL.
func testPresignerPresignUploadURLForwardsAllFields(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	p := newTestPresigner(t, f, testBucket)
	expires := time.Date(2033, 2, 3, 4, 5, 6, 0, time.UTC)
	ttl := 90 * time.Second

	res, err := p.PresignUploadURL(context.Background(), &storage.PresignUploadURLInput{
		PutObjectInput: storage.PutObjectInput{
			BaseObjectInput: base("up/file name.png"),
			ContentLength:   new(int64(1234)),
			ContentType:     new("image/png"),
			Expires:         &expires,
			Metadata:        map[string]string{"owner": "ahmed"},
		},
		TTL: &ttl,
	})
	if err != nil {
		t.Fatalf("PresignUploadURL() error: %v", err)
	}
	if res.Method != http.MethodPut {
		t.Errorf("Method = %q, want PUT", res.Method)
	}
	u := parsePresigned(t, res)
	srvURL, _ := url.Parse(f.srv.URL)
	if u.Host != srvURL.Host {
		t.Errorf("host = %q, want %q", u.Host, srvURL.Host)
	}
	if u.Path != "/"+testBucket+"/up/file name.png" {
		t.Errorf("path = %q", u.Path)
	}
	q := u.Query()
	if q.Get("X-Amz-Expires") != "90" {
		t.Errorf("X-Amz-Expires = %q, want 90", q.Get("X-Amz-Expires"))
	}
	if q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" || q.Get("X-Amz-Signature") == "" {
		t.Errorf("missing SigV4 params: %v", q)
	}
	if !strings.HasPrefix(q.Get("X-Amz-Credential"), testAKID+"/") {
		t.Errorf("X-Amz-Credential = %q", q.Get("X-Amz-Credential"))
	}

	for _, h := range []string{"content-type", "content-length", "expires", "x-amz-meta-owner", "host"} {
		if !hasSignedHeader(u, h) {
			t.Errorf("header %q not signed; signed = %v", h, signedHeaderList(u))
		}
	}
	if res.SignedHeader.Get("Content-Type") != "image/png" {
		t.Errorf("SignedHeader Content-Type = %q", res.SignedHeader.Get("Content-Type"))
	}
	if res.SignedHeader.Get("Content-Length") != "1234" {
		t.Errorf("SignedHeader Content-Length = %q", res.SignedHeader.Get("Content-Length"))
	}
	if res.SignedHeader.Get("X-Amz-Meta-Owner") != "ahmed" {
		t.Errorf("SignedHeader X-Amz-Meta-Owner = %q", res.SignedHeader.Get("X-Amz-Meta-Owner"))
	}
	if res.SignedHeader.Get("Expires") != expires.Format(http.TimeFormat) {
		t.Errorf("SignedHeader Expires = %q", res.SignedHeader.Get("Expires"))
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("presigning sent %d requests", n)
	}
}

// testPresignerPresignUploadURLDefaultTTL checks the expiry used when TTL is
// nil.
func testPresignerPresignUploadURLDefaultTTL(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	p := newTestPresigner(t, f, testBucket)
	res, err := p.PresignUploadURL(context.Background(), &storage.PresignUploadURLInput{
		PutObjectInput: storage.PutObjectInput{BaseObjectInput: base("k")},
	})
	if err != nil {
		t.Fatalf("PresignUploadURL() error: %v", err)
	}
	u := parsePresigned(t, res)
	if got := u.Query().Get("X-Amz-Expires"); got != "900" {
		t.Errorf("X-Amz-Expires = %q, want default 900", got)
	}
	if hasSignedHeader(u, "content-type") {
		t.Error("content-type signed without ContentType input")
	}
}

// testPresignerPresignUploadURLExplicitBucket checks that an explicit bucket
// overrides the configured one.
func testPresignerPresignUploadURLExplicitBucket(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	p := newTestPresigner(t, f, "")
	res, err := p.PresignUploadURL(context.Background(), &storage.PresignUploadURLInput{
		PutObjectInput: storage.PutObjectInput{BaseObjectInput: inBucket("other-bucket")},
	})
	if err != nil {
		t.Fatalf("PresignUploadURL() error: %v", err)
	}
	if u := parsePresigned(t, res); u.Path != "/other-bucket/k" {
		t.Errorf("path = %q", u.Path)
	}
}

// testPresignerPresignUploadURLBodyIsIgnored checks that the input Body is
// not read.
func testPresignerPresignUploadURLBodyIsIgnored(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	p := newTestPresigner(t, f, testBucket)
	_, err := p.PresignUploadURL(context.Background(), &storage.PresignUploadURLInput{
		PutObjectInput: storage.PutObjectInput{
			BaseObjectInput: base("k"),
			Body:            strings.NewReader("ignored"),
		},
	})
	if err != nil {
		t.Fatalf("PresignUploadURL() error: %v", err)
	}
}

// TestPresigner_PresignDownloadURL covers PresignDownloadURL.
func TestPresigner_PresignDownloadURL(t *testing.T) {
	t.Parallel()

	t.Run("forwards all fields", testPresignerPresignDownloadURLForwardsAllFields)

	t.Run("minimal", testPresignerPresignDownloadURLMinimal)

	t.Run(
		"presigned URL is usable against the endpoint",
		testPresignerPresignDownloadURLPresignedURLIsUsableAgainstTheEndpoint,
	)
}

// testPresignerPresignDownloadURLForwardsAllFields checks that every input
// field reaches the URL.
func testPresignerPresignDownloadURLForwardsAllFields(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	p := newTestPresigner(t, f, testBucket)
	expires := time.Date(2034, 4, 5, 6, 7, 8, 0, time.UTC)
	ttl := 2 * time.Hour

	res, err := p.PresignDownloadURL(context.Background(), &storage.PresignDownloadURLInput{
		GetObjectInput: storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{
			BaseObjectInput:            base("down/load.pdf"),
			PartNumber:                 new(int32(4)),
			Range:                      new("bytes=100-199"),
			ResponseCacheControl:       new("private"),
			ResponseContentDisposition: new(`attachment; filename="r.pdf"`),
			ResponseContentEncoding:    new("br"),
			ResponseContentLanguage:    new("fr"),
			ResponseContentType:        new("application/pdf"),
			ResponseExpires:            &expires,
			Version:                    new("v-dl"),
		}},
		TTL: &ttl,
	})
	if err != nil {
		t.Fatalf("PresignDownloadURL() error: %v", err)
	}
	if res.Method != http.MethodGet {
		t.Errorf("Method = %q, want GET", res.Method)
	}
	u := parsePresigned(t, res)
	if u.Path != "/"+testBucket+"/down/load.pdf" {
		t.Errorf("path = %q", u.Path)
	}
	q := u.Query()
	want := map[string]string{
		"X-Amz-Expires":                "7200",
		"partNumber":                   "4",
		"versionId":                    "v-dl",
		"response-cache-control":       "private",
		"response-content-disposition": `attachment; filename="r.pdf"`,
		"response-content-encoding":    "br",
		"response-content-language":    "fr",
		"response-content-type":        "application/pdf",
		"response-expires":             expires.Format(http.TimeFormat),
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("query %s = %q, want %q", k, q.Get(k), v)
		}
	}
	if !hasSignedHeader(u, "range") {
		t.Errorf("range header not signed; signed = %v", signedHeaderList(u))
	}
	if res.SignedHeader.Get("Range") != "bytes=100-199" {
		t.Errorf("SignedHeader Range = %q", res.SignedHeader.Get("Range"))
	}
}

// testPresignerPresignDownloadURLMinimal checks that a key-only input adds no
// optional parameters.
func testPresignerPresignDownloadURLMinimal(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	p := newTestPresigner(t, f, testBucket)
	res, err := p.PresignDownloadURL(context.Background(), &storage.PresignDownloadURLInput{
		GetObjectInput: storage.GetObjectInput{
			HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: base("k")},
		},
	})
	if err != nil {
		t.Fatalf("PresignDownloadURL() error: %v", err)
	}
	u := parsePresigned(t, res)
	q := u.Query()
	if q.Get("X-Amz-Expires") != "900" {
		t.Errorf("X-Amz-Expires = %q, want 900", q.Get("X-Amz-Expires"))
	}
	for _, k := range []string{"versionId", "partNumber", "response-content-type"} {
		if q.Has(k) {
			t.Errorf("unexpected query %s", k)
		}
	}
	if hasSignedHeader(u, "range") {
		t.Error("range signed without Range input")
	}
}

// testPresignerPresignDownloadURLPresignedURLIsUsableAgainstTheEndpoint
// fetches a presigned URL from the fake endpoint.
func testPresignerPresignDownloadURLPresignedURLIsUsableAgainstTheEndpoint(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		_, _ = w.Write([]byte("ok"))
	})
	p := newTestPresigner(t, f, testBucket)
	res, err := p.PresignDownloadURL(context.Background(), &storage.PresignDownloadURLInput{
		GetObjectInput: storage.GetObjectInput{
			HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: base("k")},
		},
	})
	if err != nil {
		t.Fatalf("PresignDownloadURL() error: %v", err)
	}
	req, err := http.NewRequestWithContext(
		context.Background(),
		res.Method,
		res.URL,
		http.NoBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET presigned URL error: %v", err)
	}
	_ = resp.Body.Close()
	got := f.lastRequest(t)
	if got.Method != http.MethodGet || got.Path != "/"+testBucket+"/k" ||
		got.Query["X-Amz-Signature"] == nil {
		t.Errorf("request = %s %s %v", got.Method, got.Path, got.Query)
	}
}

// presignOps presigns key "k" with ttl through each Presigner method, keyed
// by the op prefix the method's errors carry.
var presignOps = map[string]func(p *Presigner, ttl *time.Duration) (*storage.PresignURLResponse, error){
	"presign upload": func(p *Presigner, ttl *time.Duration) (*storage.PresignURLResponse, error) {
		return p.PresignUploadURL(context.Background(), &storage.PresignUploadURLInput{
			PutObjectInput: storage.PutObjectInput{BaseObjectInput: base("k")},
			TTL:            ttl,
		})
	},
	"presign download": func(p *Presigner, ttl *time.Duration) (*storage.PresignURLResponse, error) {
		return p.PresignDownloadURL(context.Background(), &storage.PresignDownloadURLInput{
			GetObjectInput: storage.GetObjectInput{
				HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: base("k")},
			},
			TTL: ttl,
		})
	},
}

// TestPresigner_TTL checks the X-Amz-Expires each Presigner method signs for
// accepted TTLs: nil and zero select the 15-minute default, and the SDK
// truncates fractional seconds toward zero.
func TestPresigner_TTL(t *testing.T) {
	t.Parallel()

	const week = 7 * 24 * time.Hour
	tests := []struct {
		ttl  *time.Duration
		name string
		want string
	}{
		{name: "nil is the default", ttl: nil, want: "900"},
		{name: "zero is the default", ttl: new(time.Duration(0)), want: "900"},
		{name: "one second", ttl: new(time.Second), want: "1"},
		{name: "fraction truncates", ttl: new(1500 * time.Millisecond), want: "1"},
		{name: "just under two seconds truncates", ttl: new(2*time.Second - 1), want: "1"},
		{name: "fifteen minutes", ttl: new(15 * time.Minute), want: "900"},
		{name: "one hour", ttl: new(time.Hour), want: "3600"},
		{name: "just under seven days truncates", ttl: new(week - 1), want: "604799"},
		{name: "seven days", ttl: new(week), want: "604800"},
	}
	for op, call := range presignOps {
		for _, tt := range tests {
			t.Run(op+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				f := newFakeS3(t, nil)
				res, err := call(newTestPresigner(t, f, testBucket), tt.ttl)
				if err != nil {
					t.Fatalf("%s error: %v", op, err)
				}
				if got := parsePresigned(t, res).Query().Get("X-Amz-Expires"); got != tt.want {
					t.Errorf("X-Amz-Expires = %q, want %q", got, tt.want)
				}
				if n := len(f.requests()); n != 0 {
					t.Errorf("presigning sent %d requests", n)
				}
			})
		}
	}
}

// TestPresigner_InvalidTTL checks that each Presigner method rejects a TTL
// that is negative, under one second, or over seven days with
// storage.ErrInvalidTTL, and signs nothing.
func TestPresigner_InvalidTTL(t *testing.T) {
	t.Parallel()

	const week = 7 * 24 * time.Hour
	tests := []struct {
		name string
		ttl  time.Duration
	}{
		{name: "minus one second", ttl: -time.Second},
		{name: "minus one nanosecond", ttl: -1},
		{name: "most negative", ttl: math.MinInt64},
		{name: "one nanosecond", ttl: 1},
		{name: "one millisecond", ttl: time.Millisecond},
		{name: "just under one second", ttl: time.Second - 1},
		{name: "just over seven days", ttl: week + 1},
		{name: "seven days and half a second", ttl: week + 500*time.Millisecond},
		{name: "eight days", ttl: 8 * 24 * time.Hour},
		{name: "largest", ttl: math.MaxInt64},
	}
	for op, call := range presignOps {
		for _, tt := range tests {
			t.Run(op+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				f := newFakeS3(t, nil)
				ttl := tt.ttl
				res, err := call(newTestPresigner(t, f, testBucket), &ttl)
				if res != nil {
					t.Errorf("%s response = %+v, want nil", op, res)
				}
				if !errors.Is(err, storage.ErrInvalidTTL) {
					t.Fatalf("%s error = %v, want ErrInvalidTTL", op, err)
				}
				want := "gas/storage/s3/presigner: " + op + ": storage: invalid TTL"
				if !strings.HasPrefix(err.Error(), want) {
					t.Errorf("error = %q, want prefix %q", err, want)
				}
				for _, s := range []error{storage.ErrInvalidKey, storage.ErrInvalidBucket} {
					if errors.Is(err, s) {
						t.Errorf("error = %v, must not match %v", err, s)
					}
				}
				if n := len(f.requests()); n != 0 {
					t.Errorf("presigning sent %d requests", n)
				}
			})
		}
	}
}

// TestPresigner_DefaultTTLIsExplicit checks that a nil TTL signs the
// presigner's own 15-minute default even when the presign client given
// with WithPresignClient carries a different default expiry.
func TestPresigner_DefaultTTLIsExplicit(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	pc := s3.NewPresignClient(f.client(t), s3.WithPresignExpires(time.Hour))
	p, err := NewPresigner(WithPresignClient(pc))(
		newConfig(t, map[string]any{"bucket": testBucket}),
	)
	if err != nil {
		t.Fatalf("NewPresigner() error: %v", err)
	}
	for op, call := range presignOps {
		res, err := call(p, nil)
		if err != nil {
			t.Fatalf("%s error: %v", op, err)
		}
		if got := parsePresigned(t, res).Query().Get("X-Amz-Expires"); got != "900" {
			t.Errorf("%s X-Amz-Expires = %q, want 900", op, got)
		}
	}
}

// TestPresigner_ErrorPrefix checks the presigner error prefix on a validation
// error.
func TestPresigner_ErrorPrefix(t *testing.T) {
	t.Parallel()
	f := newFakeS3(t, nil)
	p := newTestPresigner(t, f, "")
	_, err := p.PresignUploadURL(context.Background(), &storage.PresignUploadURLInput{
		PutObjectInput: storage.PutObjectInput{BaseObjectInput: base("k")},
	})
	if !errors.Is(err, storage.ErrInvalidBucket) {
		t.Fatalf("error = %v, want ErrInvalidBucket", err)
	}
	if !strings.HasPrefix(err.Error(), "gas/storage/s3/presigner: presign upload: ") {
		t.Errorf("error = %q", err)
	}
}

// TestPresigner_SigningError checks that an error from the SDK while signing,
// here from a credentials provider that fails, is returned wrapped with the
// operation prefix and no URL.
func TestPresigner_SigningError(t *testing.T) {
	t.Parallel()
	credErr := errors.New("no credentials")
	f := newFakeS3(t, nil)
	pc := s3.NewPresignClient(f.client(t), s3.WithPresignClientFromClientOptions(
		func(o *s3.Options) {
			o.Credentials = aws.CredentialsProviderFunc(
				func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{}, credErr
				},
			)
		},
	))
	p, err := NewPresigner(WithPresignClient(pc))(
		newConfig(t, map[string]any{"bucket": testBucket}),
	)
	if err != nil {
		t.Fatalf("NewPresigner() error: %v", err)
	}
	for op, call := range presignOps {
		res, err := call(p, nil)
		if !errors.Is(err, credErr) {
			t.Errorf("%s error = %v, want %v", op, err, credErr)
		}
		if res != nil {
			t.Errorf("%s returned %+v, want nil", op, res)
		}
		if err != nil && !strings.HasPrefix(err.Error(), "gas/storage/s3/presigner: "+op+": ") {
			t.Errorf("%s error = %q, want the %q prefix", op, err, op)
		}
	}
}
