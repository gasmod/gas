package s3_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas"
	storage "github.com/gasmod/gas/storage"
	s3svc "github.com/gasmod/gas/storage/s3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

const testBucket = "test-bucket"

// newTestService spins up a LocalStack container and returns an initialised
// *s3svc.Service. Container and service are cleaned up via t.Cleanup.
func newTestService(t *testing.T) *s3svc.Service {
	t.Helper()
	svc, _ := newTestServiceWithEndpoint(t)
	return svc
}

func newTestServiceWithEndpoint(t *testing.T) (*s3svc.Service, string) {
	t.Helper()

	endpoint := startLocalStack(t)
	createBucket(t, context.Background(), endpoint)

	cfg := s3svc.DefaultConfig()
	cfg.Storage.Region = "us-east-1"
	cfg.Storage.Bucket = testBucket
	cfg.Storage.AccessKeyID = "test"
	cfg.Storage.SecretAccessKey = "test"
	cfg.Storage.Endpoint = endpoint

	svc := s3svc.New(s3svc.WithConfig(cfg))(nil, gas.NewNopLogger()())
	if err := svc.Init(); err != nil {
		t.Fatalf("init service: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := svc.Close(); closeErr != nil {
			t.Logf("close service: %v", closeErr)
		}
	})

	return svc, endpoint
}

func startLocalStack(t *testing.T) string {
	t.Helper()

	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "localstack/localstack:4",
		ExposedPorts: []string{"4566/tcp"},
		Env: map[string]string{
			"SERVICES": "s3",
		},
		WaitingFor: wait.ForHTTP("/_localstack/health").
			WithPort("4566/tcp").
			WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start localstack container: %v", err)
	}
	t.Cleanup(func() {
		if termErr := container.Terminate(ctx); termErr != nil {
			t.Logf("terminate container: %v", termErr)
		}
	})

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("get container host: %v", err)
	}
	port, err := container.MappedPort(ctx, "4566")
	if err != nil {
		t.Fatalf("get mapped port: %v", err)
	}

	return fmt.Sprintf("http://%s:%s", host, port.Port())
}

func createBucket(t *testing.T, ctx context.Context, endpoint string) {
	t.Helper()

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("test", "test", ""),
		),
	)
	if err != nil {
		t.Fatalf("load AWS config for bucket creation: %v", err)
	}

	client := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		o.BaseEndpoint = new(endpoint)
		o.UsePathStyle = true
	})

	_, err = client.CreateBucket(ctx, &awss3.CreateBucketInput{
		Bucket: new(testBucket),
	})
	if err != nil {
		t.Fatalf("create test bucket: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Basic operations
// ---------------------------------------------------------------------------

func TestIntegration_UploadAndDownload(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	key := "test-file.txt"
	data := []byte("hello s3")

	if err := svc.Upload(ctx, key, bytes.NewReader(data)); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	obj, err := svc.Download(ctx, key)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	defer obj.Body.Close()

	got, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("Download = %q, want %q", got, data)
	}
}

func TestIntegration_DownloadNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.Download(ctx, "nonexistent-key")
	if !errors.Is(err, storage.ErrKeyNotFound) {
		t.Errorf("Download(nonexistent) error = %v, want %v", err, storage.ErrKeyNotFound)
	}
}

func TestIntegration_Delete(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	key := "delete-me.txt"
	if err := svc.Upload(ctx, key, bytes.NewReader([]byte("data"))); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	if err := svc.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := svc.Download(ctx, key)
	if !errors.Is(err, storage.ErrKeyNotFound) {
		t.Errorf("Download after Delete error = %v, want %v", err, storage.ErrKeyNotFound)
	}
}

func TestIntegration_PresignDownloadURL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	key := "presign-file.txt"
	if err := svc.Upload(ctx, key, bytes.NewReader([]byte("presigned"))); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	url, err := svc.PresignDownloadURL(ctx, key, 5*time.Minute)
	if err != nil {
		t.Fatalf("PresignDownloadURL: %v", err)
	}
	if url == "" {
		t.Error("PresignDownloadURL returned empty string")
	}
	if !strings.Contains(url, key) {
		t.Errorf("PresignDownloadURL = %q, expected to contain key %q", url, key)
	}
}

func TestIntegration_PresignUploadURL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	key := "presign-upload.txt"
	url, err := svc.PresignUploadURL(ctx, key, 5*time.Minute, gas.WithContentType("text/plain"))
	if err != nil {
		t.Fatalf("PresignUploadURL: %v", err)
	}
	if url == "" {
		t.Error("PresignUploadURL returned empty string")
	}
	if !strings.Contains(url, key) {
		t.Errorf("PresignUploadURL = %q, expected to contain key %q", url, key)
	}
}

func TestIntegration_Head(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	key := "head-file.txt"
	data := []byte("head me")
	if err := svc.Upload(ctx, key, bytes.NewReader(data), gas.WithContentType("text/plain")); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	info, err := svc.Head(ctx, key)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if info.Size != int64(len(data)) {
		t.Errorf("Head Size = %d, want %d", info.Size, len(data))
	}
	if info.ContentType != "text/plain" {
		t.Errorf("Head ContentType = %q, want %q", info.ContentType, "text/plain")
	}
	if info.LastModified.IsZero() {
		t.Error("Head LastModified is zero")
	}
}

func TestIntegration_HeadNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.Head(ctx, "nonexistent-key")
	if !errors.Is(err, storage.ErrKeyNotFound) {
		t.Errorf("Head(nonexistent) error = %v, want %v", err, storage.ErrKeyNotFound)
	}
}

// ---------------------------------------------------------------------------
// Adversarial: binary and weird data
// ---------------------------------------------------------------------------

func TestIntegration_BinaryData(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	value := []byte{0x00, 0x01, 0x00, 0xFF, 0x00, 0xFE}

	if err := svc.Upload(ctx, "binary-data", bytes.NewReader(value)); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	obj, err := svc.Download(ctx, "binary-data")
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	defer obj.Body.Close()

	got, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, value) {
		t.Errorf("binary round-trip failed:\n  got  %x\n  want %x", got, value)
	}
}

func TestIntegration_EmptyFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upload(ctx, "empty-file", bytes.NewReader([]byte{})); err != nil {
		t.Fatalf("Upload empty: %v", err)
	}

	obj, err := svc.Download(ctx, "empty-file")
	if err != nil {
		t.Fatalf("Download empty: %v", err)
	}
	defer obj.Body.Close()

	got, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Download = %x (len %d), want empty", got, len(got))
	}
}

func TestIntegration_LargeFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	size := 1 << 20 // 1 MB
	value := bytes.Repeat([]byte("A"), size)

	if err := svc.Upload(ctx, "large-file", bytes.NewReader(value)); err != nil {
		t.Fatalf("Upload 1MB: %v", err)
	}

	obj, err := svc.Download(ctx, "large-file")
	if err != nil {
		t.Fatalf("Download 1MB: %v", err)
	}
	defer obj.Body.Close()

	got, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != size {
		t.Fatalf("Download len = %d, want %d", len(got), size)
	}
	if !bytes.Equal(got, value) {
		t.Error("1MB round-trip data mismatch")
	}
}

func TestIntegration_KeysWithSpecialChars(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	keys := []string{
		"key with spaces",
		"path/to/file.txt",
		"path/to/deep/nested/file.txt",
		"key:with:colons",
		"日本語キー",
		"emoji-🔑-key",
	}

	for i, key := range keys {
		val := []byte(fmt.Sprintf("value-%d", i))
		if err := svc.Upload(ctx, key, bytes.NewReader(val)); err != nil {
			t.Errorf("Upload(%q): %v", key, err)
			continue
		}
		obj, err := svc.Download(ctx, key)
		if err != nil {
			t.Errorf("Download(%q): %v", key, err)
			continue
		}
		got, readErr := io.ReadAll(obj.Body)
		obj.Body.Close()
		if readErr != nil {
			t.Errorf("ReadAll(%q): %v", key, readErr)
			continue
		}
		if !bytes.Equal(got, val) {
			t.Errorf("Download(%q) = %q, want %q", key, got, val)
		}
	}
}

// ---------------------------------------------------------------------------
// Adversarial: overwrite / mutation semantics
// ---------------------------------------------------------------------------

func TestIntegration_OverwriteKey(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	key := "overwrite-key"
	if err := svc.Upload(ctx, key, bytes.NewReader([]byte("first"))); err != nil {
		t.Fatalf("Upload first: %v", err)
	}
	if err := svc.Upload(ctx, key, bytes.NewReader([]byte("second"))); err != nil {
		t.Fatalf("Upload second: %v", err)
	}

	obj, err := svc.Download(ctx, key)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	defer obj.Body.Close()

	got, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "second" {
		t.Errorf("Download = %q, want %q", got, "second")
	}
}

func TestIntegration_DeleteNonexistent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Delete(ctx, "never-existed"); err != nil {
		t.Errorf("Delete(nonexistent) returned error: %v", err)
	}
}

func TestIntegration_DeleteThenReUpload(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	key := "phoenix-file"
	if err := svc.Upload(ctx, key, bytes.NewReader([]byte("v1"))); err != nil {
		t.Fatalf("Upload v1: %v", err)
	}
	if err := svc.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Upload(ctx, key, bytes.NewReader([]byte("v2"))); err != nil {
		t.Fatalf("Upload v2: %v", err)
	}

	obj, err := svc.Download(ctx, key)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	defer obj.Body.Close()

	got, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "v2" {
		t.Errorf("Download = %q, want %q", got, "v2")
	}
}

// ---------------------------------------------------------------------------
// Adversarial: context cancellation
// ---------------------------------------------------------------------------

func TestIntegration_CancelledContext(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = svc.Upload(ctx, "k", bytes.NewReader([]byte("v")))
		_, _ = svc.Download(ctx, "k")
		_ = svc.Delete(ctx, "k")
		_, _ = svc.PresignDownloadURL(ctx, "k", time.Minute)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("operations with cancelled context hung for >5s")
	}
}

// ---------------------------------------------------------------------------
// Adversarial: concurrency
// ---------------------------------------------------------------------------

func TestIntegration_ConcurrentUploadDownload(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n*2)

	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := fmt.Sprintf("concurrent-%d", idx)
			val := []byte(fmt.Sprintf("val-%d", idx))

			if err := svc.Upload(ctx, key, bytes.NewReader(val)); err != nil {
				errs <- fmt.Errorf("Upload %s: %w", key, err)
				return
			}
			obj, err := svc.Download(ctx, key)
			if err != nil {
				errs <- fmt.Errorf("Download %s: %w", key, err)
				return
			}
			got, readErr := io.ReadAll(obj.Body)
			obj.Body.Close()
			if readErr != nil {
				errs <- fmt.Errorf("ReadAll %s: %w", key, readErr)
				return
			}
			if !bytes.Equal(got, val) {
				errs <- fmt.Errorf("Download %s = %q, want %q", key, got, val)
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}

// ---------------------------------------------------------------------------
// Closed service
// ---------------------------------------------------------------------------

func TestIntegration_ClosedService(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := svc.Upload(ctx, "any", bytes.NewReader([]byte("v"))); !errors.Is(err, storage.ErrClosed) {
		t.Errorf("Upload after Close error = %v, want %v", err, storage.ErrClosed)
	}
	if _, err := svc.Download(ctx, "any"); !errors.Is(err, storage.ErrClosed) {
		t.Errorf("Download after Close error = %v, want %v", err, storage.ErrClosed)
	}
	if err := svc.Delete(ctx, "any"); !errors.Is(err, storage.ErrClosed) {
		t.Errorf("Delete after Close error = %v, want %v", err, storage.ErrClosed)
	}
	if _, err := svc.PresignDownloadURL(ctx, "any", time.Minute); !errors.Is(err, storage.ErrClosed) {
		t.Errorf("PresignDownloadURL after Close error = %v, want %v", err, storage.ErrClosed)
	}
	if _, err := svc.PresignUploadURL(ctx, "any", time.Minute); !errors.Is(err, storage.ErrClosed) {
		t.Errorf("PresignUploadURL after Close error = %v, want %v", err, storage.ErrClosed)
	}
	if _, err := svc.Head(ctx, "any"); !errors.Is(err, storage.ErrClosed) {
		t.Errorf("Head after Close error = %v, want %v", err, storage.ErrClosed)
	}
	if err := svc.CheckReady(ctx); !errors.Is(err, storage.ErrClosed) {
		t.Errorf("CheckReady after Close error = %v, want %v", err, storage.ErrClosed)
	}
}

func TestIntegration_CheckReady(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.CheckReady(ctx); err != nil {
		t.Errorf("CheckReady on healthy service = %v, want nil", err)
	}
}

func TestIntegration_CheckReady_MissingBucket(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	endpoint := startLocalStack(t)
	ctx := context.Background()

	cfg := s3svc.DefaultConfig()
	cfg.Storage.Region = "us-east-1"
	cfg.Storage.Bucket = "nonexistent-bucket-xyz"
	cfg.Storage.AccessKeyID = "test"
	cfg.Storage.SecretAccessKey = "test"
	cfg.Storage.Endpoint = endpoint

	svc := s3svc.New(s3svc.WithConfig(cfg))(nil, gas.NewNopLogger()())
	if err := svc.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.CheckReady(ctx); err == nil {
		t.Error("CheckReady against missing bucket = nil, want error")
	}
}

func TestIntegration_CheckReady_NoDefaultBucket(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	endpoint := startLocalStack(t)
	ctx := context.Background()

	cfg := s3svc.DefaultConfig()
	cfg.Storage.Region = "us-east-1"
	cfg.Storage.Bucket = "" // per-call bucket mode
	cfg.Storage.AccessKeyID = "test"
	cfg.Storage.SecretAccessKey = "test"
	cfg.Storage.Endpoint = endpoint

	svc := s3svc.New(s3svc.WithConfig(cfg))(nil, gas.NewNopLogger()())
	if err := svc.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.CheckReady(ctx); err != nil {
		t.Errorf("CheckReady without default bucket = %v, want nil", err)
	}
}

func TestIntegration_DoubleClose(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)

	if err := svc.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("second Close panicked: %v", r)
			}
		}()
		if err := svc.Close(); err != nil {
			t.Logf("second Close returned error (acceptable): %v", err)
		}
	}()
}

// ---------------------------------------------------------------------------
// Config validation
// ---------------------------------------------------------------------------

func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := s3svc.DefaultConfig()
	if cfg.Storage.Region != "" {
		t.Errorf("Region = %q, want empty", cfg.Storage.Bucket)
	}
	if cfg.Storage.Bucket != "" {
		t.Errorf("Bucket = %q, want empty", cfg.Storage.Bucket)
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		modify  func(*s3svc.Config)
		wantErr bool
	}{
		{
			name: "valid config",
			modify: func(c *s3svc.Config) {
				c.Storage.Bucket = "my-bucket"
				c.Storage.Region = "us-east-1"
			},
			wantErr: false,
		},
		{
			name: "empty bucket is allowed (per-call gas.InBucket())",
			modify: func(c *s3svc.Config) {
				c.Storage.Region = "us-east-1"
			},
			wantErr: false,
		},
		{
			name: "empty region",
			modify: func(c *s3svc.Config) {
				c.Storage.Bucket = "my-bucket"
				c.Storage.Region = ""
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := s3svc.DefaultConfig()
			tt.modify(cfg)
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
