package s3

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/storage"
)

const (
	testBucket = "test-bucket"
	testRegion = "us-east-1"
	testAKID   = "AKIDTEST"
	testSecret = "SECRETTEST"
)

// recordedRequest is a snapshot of an HTTP request received by fakeS3.
type recordedRequest struct {
	Header http.Header
	Query  map[string][]string
	Method string
	Path   string
	Body   []byte
}

// fakeS3 is an httptest-backed S3 endpoint whose responses are scripted per
// test. All received requests are recorded.
type fakeS3 struct {
	srv     *httptest.Server
	handler func(w http.ResponseWriter, r *http.Request, body []byte)
	reqs    []recordedRequest
	mu      sync.Mutex
}

// newFakeS3 starts a fake S3 endpoint that records every request and answers
// with handler, or 200 OK when handler is nil.
func newFakeS3(
	t *testing.T,
	handler func(w http.ResponseWriter, r *http.Request, body []byte),
) *fakeS3 {
	t.Helper()
	f := &fakeS3{handler: handler}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.Query(),
			Header: r.Header.Clone(),
			Body:   body,
		})
		f.mu.Unlock()
		if f.handler == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		f.handler(w, r, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// requests returns a copy of the requests received so far.
func (f *fakeS3) requests() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.reqs...)
}

// lastRequest returns the most recent request, failing the test when there is
// none.
func (f *fakeS3) lastRequest(t *testing.T) recordedRequest {
	t.Helper()
	reqs := f.requests()
	if len(reqs) == 0 {
		t.Fatal("no request received by fake S3")
	}
	return reqs[len(reqs)-1]
}

// clientInRegion is client with the client's region set to region.
func (f *fakeS3) clientInRegion(t *testing.T, region string) *s3.Client {
	t.Helper()
	c := f.client(t)
	return s3.New(c.Options(), func(o *s3.Options) { o.Region = region })
}

// client returns an S3 client pointed at the fake server with path-style
// addressing, static credentials, no retries, and request checksums only
// when required so that request bodies are sent verbatim.
func (f *fakeS3) client(t *testing.T) *s3.Client {
	t.Helper()
	return s3.New(s3.Options{
		Region:       testRegion,
		BaseEndpoint: aws.String(f.srv.URL),
		UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(
			testAKID,
			testSecret,
			"",
		),
		HTTPClient:                 f.srv.Client(),
		Retryer:                    aws.NopRetryer{},
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

// newConfig returns a gas.ConfigProvider seeded with the given storage
// settings.
func newConfig(t *testing.T, storageCfg map[string]any) gas.ConfigProvider {
	t.Helper()
	mock, err := configtest.NewMockConfigWithValues(map[string]any{"storage": storageCfg})
	if err != nil {
		t.Fatalf("NewMockConfigWithValues() error: %v", err)
	}
	return mock
}

// newTestProvider builds a Provider on the fake endpoint with testBucket
// configured.
func newTestProvider(t *testing.T, f *fakeS3) *Provider {
	t.Helper()
	p, err := NewProvider(
		WithClient(f.client(t)),
	)(
		newConfig(t, map[string]any{"bucket": testBucket}),
	)
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}
	return p
}

// newTestPresigner builds a Presigner on the fake endpoint with bucket
// configured.
func newTestPresigner(t *testing.T, f *fakeS3, bucket string) *Presigner {
	t.Helper()
	p, err := NewPresigner(
		WithPresignerS3Client(f.client(t)),
	)(
		newConfig(t, map[string]any{"bucket": bucket}),
	)
	if err != nil {
		t.Fatalf("NewPresigner() error: %v", err)
	}
	return p
}

// base returns an input for key in the configured bucket.
func base(key string) storage.BaseObjectInput {
	return storage.BaseObjectInput{Key: key}
}

// inBucket returns an input for key "k" in the given bucket.
func inBucket(bucket string) storage.BaseObjectInput {
	return storage.BaseObjectInput{Bucket: new(bucket), Key: "k"}
}

// writeS3Error writes an S3 REST-XML error document.
func writeS3Error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(
		w,
		`<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s message</Message><RequestId>req-1</RequestId><HostId>host-1</HostId></Error>`,
		code,
		code,
	)
}

// assertOnlySentinel checks that err matches want (nil for none) and none of
// the other backend sentinels (the not-found ones, ErrVersionNotFound,
// ErrBucketExists, and ErrInvalidRange).
func assertOnlySentinel(t *testing.T, err, want error) {
	t.Helper()
	for _, s := range []error{
		storage.ErrKeyNotFound,
		storage.ErrBucketNotFound,
		storage.ErrNotFound,
		storage.ErrVersionNotFound,
		storage.ErrBucketExists,
		storage.ErrInvalidRange,
	} {
		wantIs := errors.Is(s, want)
		if got := errors.Is(err, s); got != wantIs {
			t.Errorf("errors.Is(%v, %v) = %v, want %v", err, s, got, wantIs)
		}
	}
}

// headerHasToken reports whether the semicolon-separated header value h
// contains token, ignoring case.
func headerHasToken(h, token string) bool {
	for part := range strings.SplitSeq(h, ";") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}
