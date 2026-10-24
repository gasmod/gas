package modulesstorage_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/gasmod/gas/storage"
)

// storageSentinels is every sentinel the page's error table lists.
var storageSentinels = []error{
	storage.ErrKeyNotFound, storage.ErrNotFound, storage.ErrBucketNotFound, storage.ErrVersionNotFound,
	storage.ErrBucketExists, storage.ErrBucketNotEmpty, storage.ErrAccessDenied, storage.ErrPreconditionFailed,
	storage.ErrInvalidCondition, storage.ErrInvalidHeader, storage.ErrInvalidKey, storage.ErrInvalidBucket,
	storage.ErrInputRequired, storage.ErrInvalidTTL, storage.ErrInvalidLimit, storage.ErrInvalidContinuationToken,
	storage.ErrInvalidRange, storage.ErrNotSupported, storage.ErrContentLengthMismatch, storage.ErrInvalidOption,
	storage.ErrNotInitialized,
}

// keysOf returns the keys of objects.
func keysOf(objects []storage.ObjectSummary) []string {
	keys := make([]string, 0, len(objects))
	for _, o := range objects {
		keys = append(keys, o.Key)
	}
	return keys
}

// namesOf returns the names of buckets.
func namesOf(buckets []storage.Bucket) []string {
	names := make([]string, 0, len(buckets))
	for _, b := range buckets {
		names = append(names, b.Name)
	}
	return names
}

// sha is the hex SHA-256 the dir backend names buckets and objects by.
func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// metaPath is where the dir backend keeps the metadata of key in bucket, per
// the Layout section of the dir package documentation.
func metaPath(root, bucket, key string) string {
	return filepath.Join(root, sha(bucket), sha(key)+".gas.json")
}

// putAll writes every key with a small body, concurrently.
func putAll(t *testing.T, p storage.Provider, keys []string) {
	t.Helper()
	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)
	errs := make(chan error, len(keys))
	for _, k := range keys {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := p.PutObject(t.Context(), &storage.PutObjectInput{
				Body: strings.NewReader("x"), BaseObjectInput: obj(k),
			}); err != nil {
				errs <- fmt.Errorf("PutObject(%s): %w", k, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// getRange reads key with a Range header.
func getRange(t *testing.T, p storage.Provider, key, rng string) (string, *storage.Object, error) {
	t.Helper()
	o, err := p.GetObject(t.Context(), &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{
		Range: ptr(rng), BaseObjectInput: obj(key),
	}})
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = o.Body.Close() }()
	b, err := io.ReadAll(o.Body)
	if err != nil {
		t.Fatalf("read range %q: %v", rng, err)
	}
	return string(b), o, nil
}

func getIn(key string) *storage.GetObjectInput {
	return &storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: obj(key)}}
}

func headIn(key string) *storage.HeadObjectInput {
	return &storage.HeadObjectInput{BaseObjectInput: obj(key)}
}

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
		TTL: ttl, GetObjectInput: *getIn("k"),
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

// rawClient returns an S3 client of its own pointed at f, for the options
// that inject a client.
func (f *fakeS3) rawClient() *awss3.Client {
	return awss3.New(awss3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(f.URL),
		UsePathStyle: true,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "AKIDINJECTED", SecretAccessKey: "secret"}, nil
		}),
	})
}

// paths returns the URL paths f has received, in order.
func (f *fakeS3) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seenPaths...)
}

// enableVersioning turns on versioning for b's bucket through the raw client
// the s3 Provider exposes.
func enableVersioning(t *testing.T, b backend) {
	t.Helper()
	if _, err := b.s3.Client().PutBucketVersioning(t.Context(), &awss3.PutBucketVersioningInput{
		Bucket:                  &b.bucket,
		VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled},
	}); err != nil {
		t.Fatalf("PutBucketVersioning() error: %v", err)
	}
}

// bucketScoped is a storage.Provider whose PutObject writes into bucket.
type bucketScoped struct {
	storage.Provider
	bucket string
}

func (b *bucketScoped) PutObject(ctx context.Context, in *storage.PutObjectInput) (*storage.ObjectInfo, error) {
	in.Bucket = ptr(b.bucket)
	return b.Provider.PutObject(ctx, in)
}

// skipRecorder collects what a dir provider reports to its skip handler.
type skipRecorder struct {
	mu    sync.Mutex
	paths []string
	errs  []error
}

func (r *skipRecorder) handle(path string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, path)
	r.errs = append(r.errs, err)
}

func (r *skipRecorder) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.paths)
}

// doSigned sends a request to a presigned URL with every signed header.
func doSigned(t *testing.T, resp *storage.PresignURLResponse, body io.Reader) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), resp.Method, resp.URL, body)
	if err != nil {
		t.Fatalf("NewRequest() error: %v", err)
	}
	for k, vs := range resp.SignedHeader {
		if strings.EqualFold(k, "Host") {
			req.Host = vs[0]
			continue
		}
		if strings.EqualFold(k, "Content-Length") {
			continue // set from the body by net/http
		}
		req.Header[k] = vs
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request via presigned URL: %v", err)
	}
	b, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	return res.StatusCode, string(b)
}
