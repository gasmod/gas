package guidesfiles_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/storage"
	storagedir "github.com/gasmod/gas/storage/dir"
	s3svc "github.com/gasmod/gas/storage/s3"
)

// localstackImage is the LocalStack release these tests run against, the same
// digest-pinned image the storage module's own integration tests use.
const localstackImage = "localstack/localstack:4.14.0@sha256:3ebc37595918b8accb852f8048fef2aff047d465167edd655528065b07bc364a"

// The LocalStack container is shared by every test in the package. It is
// started on first use and terminated by TestMain once the tests finish.
var (
	lsOnce      sync.Once
	lsEndpoint  string
	lsErr       error
	lsTerminate func()
	nameSeq     atomic.Int64
)

func TestMain(m *testing.M) {
	code := m.Run()
	if lsTerminate != nil {
		lsTerminate()
	}
	os.Exit(code)
}

// localstackURL starts the shared LocalStack container on first use and
// returns the endpoint it serves S3 on. Signature validation is on, so
// presigned URLs are checked for real.
func localstackURL(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("LocalStack is a Linux container image")
	}
	requireDocker(t)
	lsOnce.Do(func() {
		ctx := context.Background()
		ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        localstackImage,
				ExposedPorts: []string{"4566/tcp"},
				Env: map[string]string{
					"SERVICES":                     "s3",
					"S3_SKIP_SIGNATURE_VALIDATION": "0",
				},
				WaitingFor: wait.ForHTTP("/_localstack/health").
					WithPort("4566/tcp").
					WithStartupTimeout(120 * time.Second),
			},
			Started: true,
		})
		if err != nil {
			lsErr = fmt.Errorf("start localstack: %w", err)
			return
		}
		lsTerminate = func() { _ = ctr.Terminate(context.Background()) }
		host, err := ctr.Host(ctx)
		if err != nil {
			lsErr = fmt.Errorf("localstack host: %w", err)
			return
		}
		port, err := ctr.MappedPort(ctx, "4566/tcp")
		if err != nil {
			lsErr = fmt.Errorf("localstack port: %w", err)
			return
		}
		lsEndpoint = fmt.Sprintf("http://%s:%s", host, port.Port())
	})
	if lsErr != nil {
		t.Fatal(lsErr)
	}
	return lsEndpoint
}

// uniqueName returns a valid bucket name no other test in this run uses.
func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano()%1_000_000, nameSeq.Add(1))
}

// mockConfig returns a gas.ConfigProvider holding values under "storage".
func mockConfig(t *testing.T, values map[string]any) gas.ConfigProvider {
	t.Helper()
	cfg, err := configtest.NewMockConfigWithValues(map[string]any{"storage": values})
	if err != nil {
		t.Fatalf("NewMockConfigWithValues() error: %v", err)
	}
	return cfg
}

// s3Values returns the storage config for LocalStack in region with bucket as
// the default bucket.
func s3Values(t *testing.T, region, bucket string) map[string]any {
	t.Helper()
	v := map[string]any{
		"accessKeyID":     "test",
		"secretAccessKey": "test",
		"endpoint":        localstackURL(t),
	}
	if region != "" {
		v["region"] = region
	}
	if bucket != "" {
		v["bucket"] = bucket
	}
	return v
}

func newS3Provider(t *testing.T, region, bucket string) *s3svc.Provider {
	t.Helper()
	p, err := s3svc.NewProvider()(mockConfig(t, s3Values(t, region, bucket)))
	if err != nil {
		t.Fatalf("s3.NewProvider() error: %v", err)
	}
	return p
}

func newS3Presigner(t *testing.T, region, bucket string) *s3svc.Presigner {
	t.Helper()
	p, err := s3svc.NewPresigner()(mockConfig(t, s3Values(t, region, bucket)))
	if err != nil {
		t.Fatalf("s3.NewPresigner() error: %v", err)
	}
	return p
}

// newDirProvider returns a dir provider over a fresh root, with bucket as the
// default bucket, plus the root's path on disk.
func newDirProvider(t *testing.T, bucket string, opts ...storagedir.Option) (*storagedir.Provider, string) {
	t.Helper()
	path := t.TempDir()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatalf("OpenRoot() error: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	values := map[string]any{}
	if bucket != "" {
		values["bucket"] = bucket
	}
	p, err := storagedir.NewProvider(append([]storagedir.Option{storagedir.WithRoot(root)}, opts...)...)(
		mockConfig(t, values),
	)
	if err != nil {
		t.Fatalf("dir.NewProvider() error: %v", err)
	}
	return p, path
}

// backend is one storage.Provider under test, with a default bucket that
// exists.
type backend struct {
	name   string
	p      storage.Provider
	bucket string
	s3     *s3svc.Provider // nil for the dir backend
}

// backends returns both backends the page documents, each with a fresh,
// existing default bucket.
func backends(t *testing.T) []backend {
	t.Helper()
	return []backend{s3Backend(t), dirBackend(t)}
}

func s3Backend(t *testing.T) backend {
	t.Helper()
	bucket := uniqueName("files")
	p := newS3Provider(t, "us-east-1", bucket)
	mustCreateBucket(t, p, bucket)
	return backend{name: "s3", p: p, bucket: bucket, s3: p}
}

func dirBackend(t *testing.T) backend {
	t.Helper()
	bucket := uniqueName("files")
	p, _ := newDirProvider(t, bucket)
	mustCreateBucket(t, p, bucket)
	return backend{name: "dir", p: p, bucket: bucket}
}

func mustCreateBucket(t *testing.T, p storage.Provider, bucket string) {
	t.Helper()
	if _, err := p.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: bucket}); err != nil {
		t.Fatalf("CreateBucket(%s) error: %v", bucket, err)
	}
}

func obj(key string) storage.BaseObjectInput { return storage.BaseObjectInput{Key: key} }

func ptr[T any](v T) *T { return &v }

func mustPut(t *testing.T, p storage.Provider, key, body string) *storage.ObjectInfo {
	t.Helper()
	info, err := p.PutObject(t.Context(), &storage.PutObjectInput{
		Body:            strings.NewReader(body),
		BaseObjectInput: obj(key),
	})
	if err != nil {
		t.Fatalf("PutObject(%s) error: %v", key, err)
	}
	return info
}

// readObject returns the body of key, failing the test on any error.
func readObject(t *testing.T, p storage.Provider, key string) string {
	t.Helper()
	o, err := p.GetObject(t.Context(), &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: obj(key)}})
	if err != nil {
		t.Fatalf("GetObject(%s) error: %v", key, err)
	}
	defer func() { _ = o.Body.Close() }()
	b, err := io.ReadAll(o.Body)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return string(b)
}

func wantErr(t *testing.T, what string, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: error = %v, want errors.Is %v", what, err, target)
	}
}

// fakeS3 is an HTTP server standing in for S3. It counts requests and
// answers each with handle, which defaults to an empty 200.
type fakeS3 struct {
	*httptest.Server
	requests atomic.Int64
	mu       sync.Mutex
	bodies   []string
}

func newFakeS3(t *testing.T, handle http.HandlerFunc) *fakeS3 {
	t.Helper()
	f := &fakeS3{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, string(b))
		f.mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(b))
		if handle != nil {
			handle(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(f.Close)
	return f
}

// fakeValues returns storage config pointing the s3 backend at f.
func (f *fakeS3) values(region, bucket string) map[string]any {
	v := map[string]any{
		"accessKeyID":     "AKIDFAKE",
		"secretAccessKey": "secret",
		"endpoint":        f.URL,
	}
	if region != "" {
		v["region"] = region
	}
	if bucket != "" {
		v["bucket"] = bucket
	}
	return v
}

func (f *fakeS3) provider(t *testing.T, region, bucket string) *s3svc.Provider {
	t.Helper()
	p, err := s3svc.NewProvider()(mockConfig(t, f.values(region, bucket)))
	if err != nil {
		t.Fatalf("s3.NewProvider() error: %v", err)
	}
	return p
}

func (f *fakeS3) presigner(t *testing.T, region, bucket string) *s3svc.Presigner {
	t.Helper()
	p, err := s3svc.NewPresigner()(mockConfig(t, f.values(region, bucket)))
	if err != nil {
		t.Fatalf("s3.NewPresigner() error: %v", err)
	}
	return p
}

// s3Error writes an S3 XML error response.
func s3Error(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, code)
}
