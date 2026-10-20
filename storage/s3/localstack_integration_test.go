package s3_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/storage"
	s3svc "github.com/gasmod/gas/storage/s3"
)

// localstackImage is the LocalStack release the suite runs against, pinned by
// digest so a retagged image cannot change the behavior under test.
// Pinned: newer LocalStack images need an auth token; 4.14.0 is the last Community build.
const localstackImage = "localstack/localstack:4.14.0@sha256:3ebc37595918b8accb852f8048fef2aff047d465167edd655528065b07bc364a"

// The LocalStack container is shared by every test in the package. It is
// started on first use and reaped by testcontainers when the test binary
// exits.
var (
	lsOnce     sync.Once
	lsEndpoint string
	lsErr      error
	lsSeq      atomic.Int64
)

// localstackEndpoint skips under -short, starts the shared LocalStack
// container on first use, and returns the endpoint it serves S3 on. Signature
// validation is switched on explicitly so the presigning tests are meaningful.
func localstackEndpoint(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping LocalStack integration test in short mode")
	}
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
					WithStartupTimeout(90 * time.Second),
			},
			Started: true,
		})
		if err != nil {
			lsErr = fmt.Errorf("start localstack: %w", err)
			return
		}
		host, err := ctr.Host(ctx)
		if err != nil {
			lsErr = fmt.Errorf("localstack host: %w", err)
			return
		}
		port, err := ctr.MappedPort(ctx, "4566/tcp")
		if err != nil {
			lsErr = fmt.Errorf("localstack mapped port: %w", err)
			return
		}
		lsEndpoint = fmt.Sprintf("http://%s:%s", host, port.Port())
	})
	if lsErr != nil {
		t.Fatal(lsErr)
	}
	return lsEndpoint
}

// uniqueBucket returns a bucket name no other test in this run uses, so the
// tests can share one container and run in parallel.
func uniqueBucket(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, lsSeq.Add(1))
}

// lsConfig returns a gas.ConfigProvider that points the s3 backend at
// LocalStack in region, with bucket as the default bucket and accessKeyID as
// the credentials (LocalStack treats a 12-digit key as the account ID).
func lsConfig(t *testing.T, region, bucket, accessKeyID string) gas.ConfigProvider {
	t.Helper()
	mock, err := configtest.NewMockConfigWithValues(map[string]any{"storage": map[string]any{
		"region":          region,
		"bucket":          bucket,
		"accessKeyID":     accessKeyID,
		"secretAccessKey": "test",
		"endpoint":        localstackEndpoint(t),
	}})
	if err != nil {
		t.Fatalf("NewMockConfigWithValues() error: %v", err)
	}
	return mock
}

// lsProvider builds an s3.Provider for LocalStack in region from
// configuration only, with bucket as its default bucket.
func lsProvider(t *testing.T, region, bucket string) *s3svc.Provider {
	t.Helper()
	p, err := s3svc.NewProvider()(lsConfig(t, region, bucket, "test"))
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}
	return p
}

// lsProviderWithBucket returns a us-east-1 provider whose default bucket
// exists.
func lsProviderWithBucket(t *testing.T, prefix string) (*s3svc.Provider, string) {
	t.Helper()
	bucket := uniqueBucket(prefix)
	p := lsProvider(t, "us-east-1", bucket)
	if _, err := p.CreateBucket(
		t.Context(),
		&storage.CreateBucketInput{Bucket: bucket},
	); err != nil {
		t.Fatalf("CreateBucket(%s) error: %v", bucket, err)
	}
	return p, bucket
}

// lsObj returns a BaseObjectInput for key in bucket; an empty bucket leaves
// Bucket nil so the configured default applies.
func lsObj(bucket, key string) storage.BaseObjectInput {
	if bucket == "" {
		return storage.BaseObjectInput{Key: key}
	}
	return storage.BaseObjectInput{Bucket: &bucket, Key: key}
}

// lsPut uploads data to key in bucket and fails the test on error.
func lsPut(t *testing.T, p storage.Provider, bucket, key string, data []byte) *storage.ObjectInfo {
	t.Helper()
	info, err := p.PutObject(t.Context(), &storage.PutObjectInput{
		Body:            bytes.NewReader(data),
		BaseObjectInput: lsObj(bucket, key),
	})
	if err != nil {
		t.Fatalf("PutObject(%s) error: %v", key, err)
	}
	return info
}

// lsGet downloads key from bucket, at version when it is not nil, and
// returns its contents.
func lsGet(t *testing.T, p storage.Provider, bucket, key string, version *string) ([]byte, error) {
	t.Helper()
	o, err := p.GetObject(
		t.Context(),
		&storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{
			Version:         version,
			BaseObjectInput: lsObj(bucket, key),
		}},
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = o.Body.Close() }()
	return io.ReadAll(o.Body)
}

// assertIs fails unless err matches want and none of notWant.
func assertIs(t *testing.T, what string, err, want error, notWant ...error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("%s error = %v, want %v", what, err, want)
		return
	}
	for _, n := range notWant {
		if errors.Is(err, n) {
			t.Errorf("%s error = %v, must not match %v", what, err, n)
		}
	}
}

// bucketRegion returns the region S3 reports for bucket.
func bucketRegion(t *testing.T, p *s3svc.Provider, bucket string) string {
	t.Helper()
	out, err := p.Client().
		GetBucketLocation(t.Context(), &awss3.GetBucketLocationInput{Bucket: &bucket})
	if err != nil {
		t.Fatalf("GetBucketLocation(%s) error: %v", bucket, err)
	}
	return string(out.LocationConstraint)
}

// TestLocalStack_CreateBucket checks that CreateBucket creates a bucket in
// the client's region and that a bucket the caller already owns is success
// in and outside us-east-1.
func TestLocalStack_CreateBucket(t *testing.T) {
	t.Parallel()
	localstackEndpoint(t)

	t.Run("new bucket in us-east-1", func(t *testing.T) {
		t.Parallel()
		bucket := uniqueBucket("ls-new")
		p := lsProvider(t, "us-east-1", bucket)
		info, err := p.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: bucket})
		if err != nil {
			t.Fatalf("CreateBucket() error: %v", err)
		}
		if info.Name != bucket {
			t.Errorf("CreateBucket() Name = %q, want %q", info.Name, bucket)
		}
		// GetBucketLocation reports us-east-1 as an empty constraint.
		if got := bucketRegion(t, p, bucket); got != "" {
			t.Errorf("bucket region = %q, want us-east-1 (empty)", got)
		}
	})

	t.Run("new bucket outside us-east-1", func(t *testing.T) {
		t.Parallel()
		bucket := uniqueBucket("ls-eu")
		p := lsProvider(t, "eu-central-1", bucket)
		if _, err := p.CreateBucket(
			t.Context(),
			&storage.CreateBucketInput{Bucket: bucket},
		); err != nil {
			t.Fatalf("CreateBucket() in eu-central-1 error: %v", err)
		}
		if got := bucketRegion(t, p, bucket); got != "eu-central-1" {
			t.Errorf("bucket region = %q, want eu-central-1", got)
		}
	})

	t.Run("new bucket in aws-global", func(t *testing.T) {
		t.Parallel()
		bucket := uniqueBucket("ls-global")
		p := lsProvider(t, "aws-global", bucket)
		if _, err := p.CreateBucket(
			t.Context(),
			&storage.CreateBucketInput{Bucket: bucket},
		); err != nil {
			t.Fatalf("CreateBucket() in aws-global error: %v", err)
		}
		if got := bucketRegion(t, p, bucket); got != "" {
			t.Errorf("bucket region = %q, want us-east-1 (empty)", got)
		}
	})

	for _, region := range []string{"us-east-1", "eu-central-1"} {
		t.Run("owned bucket is success in "+region, func(t *testing.T) {
			t.Parallel()
			bucket := uniqueBucket("ls-owned")
			p := lsProvider(t, region, bucket)
			if _, err := p.CreateBucket(
				t.Context(),
				&storage.CreateBucketInput{Bucket: bucket},
			); err != nil {
				t.Fatalf("first CreateBucket() error: %v", err)
			}
			lsPut(t, p, bucket, "kept", []byte("kept"))
			info, err := p.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: bucket})
			if err != nil {
				t.Fatalf("CreateBucket() of an owned bucket error = %v, want nil", err)
			}
			if info == nil {
				t.Fatal("CreateBucket() of an owned bucket returned nil BucketInfo")
			}
			if got, err := lsGet(t, p, bucket, "kept", nil); err != nil || string(got) != "kept" {
				t.Errorf("object after CreateBucket of its bucket = %q, %v; want kept", got, err)
			}
		})
	}
}

// lsProviderAs is lsProvider with the credentials of account, a 12-digit
// account ID LocalStack keeps separate buckets for.
func lsProviderAs(t *testing.T, region, bucket, account string) *s3svc.Provider {
	t.Helper()
	p, err := s3svc.NewProvider()(lsConfig(t, region, bucket, account))
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}
	return p
}

// TestLocalStack_CreateBucketOtherAccount checks that a bucket name another
// account owns is ErrBucketExists, in and outside us-east-1.
func TestLocalStack_CreateBucketOtherAccount(t *testing.T) {
	t.Parallel()
	localstackEndpoint(t)

	for _, region := range []string{"us-east-1", "eu-central-1"} {
		t.Run(region, func(t *testing.T) {
			t.Parallel()
			bucket := uniqueBucket("ls-acct")
			owner := lsProviderAs(t, region, bucket, "000000000001")
			other := lsProviderAs(t, region, bucket, "000000000002")
			if _, err := owner.CreateBucket(
				t.Context(),
				&storage.CreateBucketInput{Bucket: bucket},
			); err != nil {
				t.Fatalf("CreateBucket() by the owner error: %v", err)
			}
			info, err := other.CreateBucket(t.Context(), &storage.CreateBucketInput{Bucket: bucket})
			assertIs(t, "CreateBucket() of another account's bucket", err, storage.ErrBucketExists)
			if info != nil {
				t.Errorf("CreateBucket() of another account's bucket = %+v, want nil", info)
			}
		})
	}
}

// TestLocalStack_PutHeadGet checks that an object's body, content type, and
// metadata round-trip.
func TestLocalStack_PutHeadGet(t *testing.T) {
	t.Parallel()
	p, bucket := lsProviderWithBucket(t, "ls-rt")

	data := []byte("hello localstack")
	ct := "text/plain"
	if _, err := p.PutObject(t.Context(), &storage.PutObjectInput{
		Body: bytes.NewReader(data),
		ObjectHeaders: storage.ObjectHeaders{
			ContentType: &ct,
			Metadata:    map[string]string{"owner": "gas"},
		},
		BaseObjectInput: lsObj(bucket, "dir/a.txt"),
	}); err != nil {
		t.Fatalf("PutObject() error: %v", err)
	}

	h, err := p.HeadObject(
		t.Context(),
		&storage.HeadObjectInput{BaseObjectInput: lsObj(bucket, "dir/a.txt")},
	)
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	if h.Size != int64(len(data)) || h.ContentType != ct || h.Metadata["owner"] != "gas" ||
		h.LastModified.IsZero() {
		t.Errorf(
			"HeadObject() = %+v, want size %d, content type %s, owner metadata, and a modification time",
			h,
			len(data),
			ct,
		)
	}

	got, err := lsGet(t, p, bucket, "dir/a.txt", nil)
	if err != nil || !bytes.Equal(got, data) {
		t.Errorf("GetObject() = %q, %v; want %q", got, err, data)
	}
}

// TestLocalStack_PutNilBody checks that a nil body stores an empty object.
func TestLocalStack_PutNilBody(t *testing.T) {
	t.Parallel()
	p, bucket := lsProviderWithBucket(t, "ls-empty")

	if _, err := p.PutObject(
		t.Context(),
		&storage.PutObjectInput{BaseObjectInput: lsObj(bucket, "empty")},
	); err != nil {
		t.Fatalf("PutObject(nil body) error: %v", err)
	}
	h, err := p.HeadObject(
		t.Context(),
		&storage.HeadObjectInput{BaseObjectInput: lsObj(bucket, "empty")},
	)
	if err != nil || h.Size != 0 {
		t.Errorf("HeadObject() of the empty object = %+v, %v; want size 0", h, err)
	}
}

// TestLocalStack_GetRange checks that a ranged read returns only the range
// and reports its size.
func TestLocalStack_GetRange(t *testing.T) {
	t.Parallel()
	p, bucket := lsProviderWithBucket(t, "ls-range")
	lsPut(t, p, bucket, "range", []byte("0123456789"))

	r := "bytes=2-4"
	o, err := p.GetObject(
		t.Context(),
		&storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{
			Range: &r, BaseObjectInput: lsObj(bucket, "range"),
		}},
	)
	if err != nil {
		t.Fatalf("GetObject(range) error: %v", err)
	}
	b, err := io.ReadAll(o.Body)
	_ = o.Body.Close()
	if err != nil {
		t.Fatalf("read range body: %v", err)
	}
	if string(b) != "234" || o.Size != 3 {
		t.Errorf("GetObject(%s) body = %q, size = %d; want 234, 3", r, b, o.Size)
	}
}

// TestLocalStack_SpecialKeys checks that keys with spaces, separators,
// colons, and non-ASCII characters round-trip.
func TestLocalStack_SpecialKeys(t *testing.T) {
	t.Parallel()
	p, bucket := lsProviderWithBucket(t, "ls-keys")

	keys := []string{
		"key with spaces",
		"path/to/deep/nested/file.txt",
		"key:with:colons",
		"query?and&amp=chars",
		"percent%20encoded",
		"日本語キー",
		"emoji-🔑-key",
	}
	for i, key := range keys {
		val := []byte(fmt.Sprintf("value-%d", i))
		lsPut(t, p, bucket, key, val)
		got, err := lsGet(t, p, bucket, key, nil)
		if err != nil || !bytes.Equal(got, val) {
			t.Errorf("GetObject(%q) = %q, %v; want %q", key, got, err, val)
		}
	}
}

// TestLocalStack_OverwriteAndReupload checks that a put replaces the object
// and that a deleted key can be written again.
func TestLocalStack_OverwriteAndReupload(t *testing.T) {
	t.Parallel()
	p, bucket := lsProviderWithBucket(t, "ls-overwrite")

	lsPut(t, p, bucket, "k", []byte("first"))
	lsPut(t, p, bucket, "k", []byte("second"))
	if got, err := lsGet(t, p, bucket, "k", nil); err != nil || string(got) != "second" {
		t.Fatalf("GetObject() after overwrite = %q, %v; want second", got, err)
	}

	if _, err := p.DeleteObject(
		t.Context(),
		&storage.DeleteObjectInput{BaseObjectInput: lsObj(bucket, "k")},
	); err != nil {
		t.Fatalf("DeleteObject() error: %v", err)
	}
	lsPut(t, p, bucket, "k", []byte("third"))
	if got, err := lsGet(t, p, bucket, "k", nil); err != nil || string(got) != "third" {
		t.Errorf("GetObject() after delete and re-upload = %q, %v; want third", got, err)
	}
}

// TestLocalStack_CanceledContext checks that every Provider operation with a
// canceled context fails with context.Canceled instead of reaching S3 or
// hanging.
func TestLocalStack_CanceledContext(t *testing.T) {
	t.Parallel()
	p, bucket := lsProviderWithBucket(t, "ls-cancel")
	lsPut(t, p, bucket, "k", []byte("v"))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	in := lsObj(bucket, "k")

	_, err := p.CreateBucket(ctx, &storage.CreateBucketInput{Bucket: uniqueBucket("ls-cancel-new")})
	assertIs(t, "CreateBucket(canceled)", err, context.Canceled)
	_, err = p.PutObject(
		ctx,
		&storage.PutObjectInput{Body: strings.NewReader("x"), BaseObjectInput: in},
	)
	assertIs(t, "PutObject(canceled)", err, context.Canceled)
	_, err = p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: in})
	assertIs(t, "HeadObject(canceled)", err, context.Canceled)
	_, err = p.GetObject(
		ctx,
		&storage.GetObjectInput{HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: in}},
	)
	assertIs(t, "GetObject(canceled)", err, context.Canceled)
	_, err = p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: in})
	assertIs(t, "DeleteObject(canceled)", err, context.Canceled)

	// Nothing was changed by the canceled calls.
	if got, err := lsGet(t, p, bucket, "k", nil); err != nil || string(got) != "v" {
		t.Errorf("GetObject() after canceled calls = %q, %v; want v", got, err)
	}
}

// TestLocalStack_Range checks ranged GetObject and HeadObject against S3 on
// the cases where S3 and RFC 9110 single-range semantics agree, which are
// the cases the dir backend pins too. LocalStack also honors forms RFC 9110
// rejects, such as a unit other than bytes, spaces around "=" or "-", and a
// "+" sign; those are backend-specific and not asserted.
func TestLocalStack_Range(t *testing.T) {
	t.Parallel()
	p, bucket := lsProviderWithBucket(t, "ls-ranges")
	lsPut(t, p, bucket, "ten", []byte("0123456789"))
	lsPut(t, p, bucket, "empty", nil)

	const whole = "0123456789"
	cases := []struct {
		key, header, want string
		invalid           bool
	}{
		{key: "ten", header: "bytes=2-4", want: "234"},
		{key: "ten", header: "bytes=7-", want: "789"},
		{key: "ten", header: "bytes=-3", want: "789"},
		{key: "ten", header: "bytes=0-0", want: "0"},
		{key: "ten", header: "bytes=9-9", want: "9"},
		{key: "ten", header: "bytes=02-04", want: "234"},
		{key: "ten", header: "bytes=5-100", want: "56789"},
		{key: "ten", header: "bytes=0-18446744073709551616", want: whole},
		{key: "ten", header: "bytes=-20", want: whole},
		{key: "ten", header: "BYTES=2-4", want: "234"},
		{key: "ten", header: " bytes=2-4", want: "234"},
		{key: "ten", header: "bytes=2-4 ", want: "234"},
		{key: "ten", header: "bytes=10-", invalid: true},
		{key: "ten", header: "bytes=10-20", invalid: true},
		{key: "ten", header: "bytes=18446744073709551616-", invalid: true},
		{key: "ten", header: "bytes=-0", invalid: true},
		{key: "ten", header: "garbage", want: whole},
		{key: "ten", header: "bytes=", want: whole},
		{key: "ten", header: "bytes=-", want: whole},
		{key: "ten", header: "bytes=a-b", want: whole},
		{key: "ten", header: "bytes=4-2", want: whole},
		{key: "ten", header: "bytes=0-1,3-4", want: whole},
		{key: "empty", header: "bytes=-3", want: ""},
		{key: "empty", header: "bytes=0-0", invalid: true},
		{key: "empty", header: "bytes=2-4", invalid: true},
		{key: "empty", header: "bytes=-0", invalid: true},
		{key: "empty", header: "garbage", want: ""},
		{key: "empty", header: "bytes=0-1,3-4", want: ""},
	}
	for _, c := range cases {
		in := storage.HeadObjectInput{BaseObjectInput: lsObj(bucket, c.key), Range: new(c.header)}
		h, herr := p.HeadObject(t.Context(), &in)
		o, gerr := p.GetObject(t.Context(), &storage.GetObjectInput{HeadObjectInput: in})
		if c.invalid {
			assertIs(
				t,
				fmt.Sprintf("HeadObject(%s, %q)", c.key, c.header),
				herr,
				storage.ErrInvalidRange,
			)
			assertIs(
				t,
				fmt.Sprintf("GetObject(%s, %q)", c.key, c.header),
				gerr,
				storage.ErrInvalidRange,
			)
			continue
		}
		if herr != nil || h.Size != int64(len(c.want)) {
			t.Errorf(
				"HeadObject(%s, %q) = %+v, %v; want size %d",
				c.key,
				c.header,
				h,
				herr,
				len(c.want),
			)
		}
		if gerr != nil {
			t.Errorf("GetObject(%s, %q) error: %v", c.key, c.header, gerr)
			continue
		}
		b, err := io.ReadAll(o.Body)
		_ = o.Body.Close()
		if err != nil || string(b) != c.want || o.Size != int64(len(c.want)) {
			t.Errorf(
				"GetObject(%s, %q) = %q (size %d), %v; want %q",
				c.key,
				c.header,
				b,
				o.Size,
				err,
				c.want,
			)
		}
	}
}

// TestLocalStack_MissingKey checks the not-found errors for a deleted key and
// that deleting a missing key succeeds.
func TestLocalStack_MissingKey(t *testing.T) {
	t.Parallel()
	p, bucket := lsProviderWithBucket(t, "ls-gone")
	lsPut(t, p, bucket, "gone", []byte("x"))

	if _, err := p.DeleteObject(
		t.Context(),
		&storage.DeleteObjectInput{BaseObjectInput: lsObj(bucket, "gone")},
	); err != nil {
		t.Fatalf("DeleteObject() error: %v", err)
	}
	_, err := lsGet(t, p, bucket, "gone", nil)
	assertIs(t, "GetObject(missing key)", err, storage.ErrKeyNotFound,
		storage.ErrBucketNotFound, storage.ErrVersionNotFound)
	_, err = p.HeadObject(
		t.Context(),
		&storage.HeadObjectInput{BaseObjectInput: lsObj(bucket, "gone")},
	)
	assertIs(t, "HeadObject(missing key)", err, storage.ErrNotFound, storage.ErrBucketNotFound)

	if _, err := p.DeleteObject(t.Context(), &storage.DeleteObjectInput{
		BaseObjectInput: lsObj(bucket, "never-existed"),
	}); err != nil {
		t.Errorf("DeleteObject(missing key) error = %v, want nil", err)
	}
}

// TestLocalStack_MissingBucket checks the error each operation returns for a
// bucket that does not exist. HEAD responses carry no error code, so
// HeadObject can only say ErrNotFound.
func TestLocalStack_MissingBucket(t *testing.T) {
	t.Parallel()
	p, _ := lsProviderWithBucket(t, "ls-mb")
	nb := uniqueBucket("ls-no-such-bucket")

	_, err := lsGet(t, p, nb, "k", nil)
	assertIs(t, "GetObject(missing bucket)", err, storage.ErrBucketNotFound, storage.ErrKeyNotFound)
	_, err = p.PutObject(
		t.Context(),
		&storage.PutObjectInput{Body: strings.NewReader("x"), BaseObjectInput: lsObj(nb, "k")},
	)
	assertIs(t, "PutObject(missing bucket)", err, storage.ErrBucketNotFound)
	_, err = p.DeleteObject(
		t.Context(),
		&storage.DeleteObjectInput{BaseObjectInput: lsObj(nb, "k")},
	)
	assertIs(t, "DeleteObject(missing bucket)", err, storage.ErrBucketNotFound)
	_, err = p.HeadObject(t.Context(), &storage.HeadObjectInput{BaseObjectInput: lsObj(nb, "k")})
	assertIs(t, "HeadObject(missing bucket)", err, storage.ErrNotFound)
}

// TestLocalStack_Versioning checks reads, heads, and deletes by version on a
// versioned bucket.
func TestLocalStack_Versioning(t *testing.T) {
	t.Parallel()
	p, vb := lsProviderWithBucket(t, "ls-versioned")
	if _, err := p.Client().PutBucketVersioning(t.Context(), &awss3.PutBucketVersioningInput{
		Bucket: &vb,
		VersioningConfiguration: &types.VersioningConfiguration{
			Status: types.BucketVersioningStatusEnabled,
		},
	}); err != nil {
		t.Fatalf("PutBucketVersioning() error: %v", err)
	}

	v1 := lsPut(t, p, vb, "doc", []byte("v1"))
	v2 := lsPut(t, p, vb, "doc", []byte("v2"))
	other := lsPut(t, p, vb, "other", []byte("o"))
	if v1.Version == "" || v2.Version == "" || v1.Version == v2.Version {
		t.Fatalf(
			"PutObject() versions = %q, %q; want two distinct versions",
			v1.Version,
			v2.Version,
		)
	}

	t.Run("read and head an old version", func(t *testing.T) {
		if b, err := lsGet(t, p, vb, "doc", &v1.Version); err != nil || string(b) != "v1" {
			t.Errorf("GetObject(v1) = %q, %v; want v1", b, err)
		}
		h, err := p.HeadObject(
			t.Context(),
			&storage.HeadObjectInput{Version: &v1.Version, BaseObjectInput: lsObj(vb, "doc")},
		)
		if err != nil || h.Version != v1.Version {
			t.Errorf("HeadObject(v1) = %+v, %v; want version %s", h, err, v1.Version)
		}
	})

	t.Run("version of another key", func(t *testing.T) {
		_, err := lsGet(t, p, vb, "doc", &other.Version)
		assertIs(t, "GetObject(another key's version)", err, storage.ErrVersionNotFound,
			storage.ErrKeyNotFound, storage.ErrNotFound)
		// HEAD responses carry no error code, so a missing version is
		// ErrNotFound, not ErrVersionNotFound.
		_, err = p.HeadObject(
			t.Context(),
			&storage.HeadObjectInput{Version: &other.Version, BaseObjectInput: lsObj(vb, "doc")},
		)
		assertIs(t, "HeadObject(another key's version)", err, storage.ErrNotFound)
	})

	t.Run("malformed version fails", func(t *testing.T) {
		// AWS may answer a malformed version ID with a 400 that maps to no
		// sentinel, so only the failure is asserted.
		bogus := "nonexistent-version"
		if _, err := lsGet(t, p, vb, "doc", &bogus); err == nil {
			t.Error("GetObject(malformed version) error = nil, want an error")
		}
	})

	t.Run("delete marker and version delete", func(t *testing.T) {
		del, err := p.DeleteObject(
			t.Context(),
			&storage.DeleteObjectInput{BaseObjectInput: lsObj(vb, "doc")},
		)
		if err != nil || del.Version == "" {
			t.Fatalf("DeleteObject() = %+v, %v; want a delete-marker version", del, err)
		}
		_, err = lsGet(t, p, vb, "doc", nil)
		assertIs(t, "GetObject(after delete marker)", err, storage.ErrKeyNotFound)
		b, err := lsGet(t, p, vb, "doc", &v2.Version)
		if err != nil || string(b) != "v2" {
			t.Errorf("GetObject(v2) after the delete marker = %q, %v; want v2", b, err)
		}

		_, err = p.DeleteObject(t.Context(), &storage.DeleteObjectInput{
			Version: &v1.Version, BaseObjectInput: lsObj(vb, "doc"),
		})
		if err != nil {
			t.Fatalf("DeleteObject(v1) error: %v", err)
		}
		_, err = lsGet(t, p, vb, "doc", &v1.Version)
		assertIs(
			t,
			"GetObject(deleted version)",
			err,
			storage.ErrVersionNotFound,
			storage.ErrKeyNotFound,
		)
	})
}

// TestLocalStack_DefaultBucket checks that an input with a nil Bucket uses
// the configured bucket.
func TestLocalStack_DefaultBucket(t *testing.T) {
	t.Parallel()
	p, bucket := lsProviderWithBucket(t, "ls-default")
	lsPut(t, p, "", "via-default", []byte("dflt"))

	if b, err := lsGet(t, p, bucket, "via-default", nil); err != nil || string(b) != "dflt" {
		t.Errorf("GetObject() with the bucket named = %q, %v; want dflt", b, err)
	}
	if b, err := lsGet(t, p, "", "via-default", nil); err != nil || string(b) != "dflt" {
		t.Errorf("GetObject() with the default bucket = %q, %v; want dflt", b, err)
	}
}

// TestLocalStack_MultipartUnseekable checks that a large body of unknown
// length is uploaded in parts and reads back intact.
func TestLocalStack_MultipartUnseekable(t *testing.T) {
	t.Parallel()
	p, bucket := lsProviderWithBucket(t, "ls-big")

	const size = 40 << 20
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	go func() {
		_, err := pw.Write(data)
		_ = pw.CloseWithError(err)
	}()
	if _, err := p.PutObject(
		t.Context(),
		&storage.PutObjectInput{Body: pr, BaseObjectInput: lsObj(bucket, "big")},
	); err != nil {
		t.Fatalf("PutObject(40 MiB pipe) error: %v", err)
	}

	raw, err := p.Client().
		HeadObject(t.Context(), &awss3.HeadObjectInput{Bucket: &bucket, Key: new("big")})
	if err != nil {
		t.Fatalf("HeadObject() error: %v", err)
	}
	if raw.ETag == nil || !strings.Contains(*raw.ETag, "-") {
		t.Errorf("ETag = %v, want a part-count suffix (a multipart upload)", raw.ETag)
	}
	got, err := lsGet(t, p, bucket, "big", nil)
	if err != nil {
		t.Fatalf("GetObject() error: %v", err)
	}
	if sha256.Sum256(got) != sha256.Sum256(data) {
		t.Errorf(
			"GetObject() returned %d bytes that differ from the %d uploaded",
			len(got),
			len(data),
		)
	}
}

// TestLocalStack_ContentLength checks that a set ContentLength is enforced
// against S3 on both upload paths: a body that is shorter or longer fails
// with ErrContentLengthMismatch, the object already under the key is
// unchanged, and no multipart upload is left in progress. A matching length
// uploads on both paths.
func TestLocalStack_ContentLength(t *testing.T) {
	t.Parallel()
	p, bucket := lsProviderWithBucket(t, "ls-clen")

	const big = 20 << 20 // above the transfer manager's multipart threshold
	payload := make([]byte, big)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	pipe := func(data []byte) io.Reader {
		pr, pw := io.Pipe()
		go func() {
			_, err := pw.Write(data)
			_ = pw.CloseWithError(err)
		}()
		return pr
	}

	for _, tc := range []struct {
		body   func() io.Reader
		name   string
		length int64
	}{
		{name: "single part short", body: func() io.Reader { return strings.NewReader("abc") }, length: 5},
		{name: "single part long", body: func() io.Reader { return strings.NewReader("abcdefg") }, length: 5},
		{name: "multipart short", body: func() io.Reader { return pipe(payload) }, length: big + 1000},
		{name: "multipart long", body: func() io.Reader { return pipe(payload) }, length: big - 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := "clen/" + strings.ReplaceAll(tc.name, " ", "-")
			lsPut(t, p, bucket, key, []byte("previous"))
			_, err := p.PutObject(t.Context(), &storage.PutObjectInput{
				Body:            tc.body(),
				ContentLength:   new(tc.length),
				BaseObjectInput: lsObj(bucket, key),
			})
			assertIs(t, "PutObject()", err, storage.ErrContentLengthMismatch)
			if got, getErr := lsGet(
				t,
				p,
				bucket,
				key,
				nil,
			); getErr != nil ||
				string(got) != "previous" {
				t.Errorf("object after a rejected put = %q, %v; want previous", got, getErr)
			}
			ups, err := p.Client().
				ListMultipartUploads(t.Context(), &awss3.ListMultipartUploadsInput{Bucket: &bucket})
			if err != nil {
				t.Fatalf("ListMultipartUploads() error: %v", err)
			}
			if len(ups.Uploads) != 0 {
				t.Errorf("multipart uploads in progress = %d, want 0", len(ups.Uploads))
			}
		})
	}

	t.Run("matching length on both paths", func(t *testing.T) {
		for _, data := range [][]byte{[]byte("hello"), payload} {
			if _, err := p.PutObject(t.Context(), &storage.PutObjectInput{
				Body:            pipe(data),
				ContentLength:   new(int64(len(data))),
				BaseObjectInput: lsObj(bucket, "clen/exact"),
			}); err != nil {
				t.Fatalf("PutObject(%d bytes) error: %v", len(data), err)
			}
			got, err := lsGet(t, p, bucket, "clen/exact", nil)
			if err != nil || !bytes.Equal(got, data) {
				t.Errorf(
					"GetObject() = %d bytes, %v; want the %d uploaded",
					len(got),
					err,
					len(data),
				)
			}
		}
	})
}

// TestLocalStack_Concurrent checks concurrent puts and gets through one
// provider.
func TestLocalStack_Concurrent(t *testing.T) {
	t.Parallel()
	p, bucket := lsProviderWithBucket(t, "ls-conc")

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			k := fmt.Sprintf("conc/%d", i)
			d := []byte(k)
			if _, err := p.PutObject(t.Context(), &storage.PutObjectInput{
				Body: bytes.NewReader(d), BaseObjectInput: lsObj(bucket, k),
			}); err != nil {
				t.Errorf("PutObject(%s) error: %v", k, err)
				return
			}
			if b, err := lsGet(t, p, bucket, k, nil); err != nil || !bytes.Equal(b, d) {
				t.Errorf("GetObject(%s) = %q, %v; want %q", k, b, err, d)
			}
		})
	}
	wg.Wait()
}

// lsPresigner builds an s3.Presigner for LocalStack in us-east-1 with bucket
// as the default bucket.
func lsPresigner(t *testing.T, bucket string) *s3svc.Presigner {
	t.Helper()
	ps, err := s3svc.NewPresigner()(lsConfig(t, "us-east-1", bucket, "test"))
	if err != nil {
		t.Fatalf("NewPresigner() error: %v", err)
	}
	return ps
}

// doPresigned sends the request r describes with body, returning the status
// and response body.
func doPresigned(t *testing.T, r *storage.PresignURLResponse, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), r.Method, r.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, vs := range r.SignedHeader {
		if strings.EqualFold(k, "Host") {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.ContentLength = int64(len(body))
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s presigned URL: %v", r.Method, err)
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read presigned response: %v", err)
	}
	return res.StatusCode, b
}

// presignDownload presigns a download of key from the default bucket.
func presignDownload(
	t *testing.T,
	ps *s3svc.Presigner,
	key string,
	ttl *time.Duration,
) *storage.PresignURLResponse {
	t.Helper()
	dl, err := ps.PresignDownloadURL(t.Context(), &storage.PresignDownloadURLInput{
		TTL: ttl,
		GetObjectInput: storage.GetObjectInput{
			HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: lsObj("", key)},
		},
	})
	if err != nil {
		t.Fatalf("PresignDownloadURL(%s) error: %v", key, err)
	}
	return dl
}

// TestLocalStack_Presign checks that presigned URLs work against a server
// that validates signatures, and that a tampered, mismatched, or expired
// request is rejected.
func TestLocalStack_Presign(t *testing.T) {
	t.Parallel()
	p, bucket := lsProviderWithBucket(t, "ls-presign")
	ps := lsPresigner(t, bucket)
	lsPut(t, p, bucket, "fixed", []byte("fixed"))

	t.Run("upload then download", func(t *testing.T) {
		t.Parallel()
		body := []byte("presigned payload")
		ct := "application/octet-stream"
		cl := int64(len(body))
		up, err := ps.PresignUploadURL(
			t.Context(),
			&storage.PresignUploadURLInput{PutObjectInput: storage.PutObjectInput{
				ObjectHeaders: storage.ObjectHeaders{
					ContentType: &ct, Metadata: map[string]string{"src": "presign"},
				},
				ContentLength:   &cl,
				BaseObjectInput: lsObj("", "presigned/obj"),
			}},
		)
		if err != nil {
			t.Fatalf("PresignUploadURL() error: %v", err)
		}
		if status, rb := doPresigned(t, up, body); status != http.StatusOK {
			t.Fatalf("presigned PUT status = %d, body %s; want 200", status, rb)
		}
		h, err := p.HeadObject(
			t.Context(),
			&storage.HeadObjectInput{BaseObjectInput: lsObj(bucket, "presigned/obj")},
		)
		if err != nil || h.ContentType != ct || h.Metadata["src"] != "presign" {
			t.Errorf(
				"HeadObject() after the presigned PUT = %+v, %v; want content type %s and src metadata",
				h,
				err,
				ct,
			)
		}
		status, rb := doPresigned(t, presignDownload(t, ps, "presigned/obj", nil), nil)
		if status != http.StatusOK || !bytes.Equal(rb, body) {
			t.Errorf("presigned GET = %d, %q; want 200, %q", status, rb, body)
		}
	})

	t.Run("tampered signature is rejected", func(t *testing.T) {
		t.Parallel()
		dl := presignDownload(t, ps, "fixed", nil)
		u, err := url.Parse(dl.URL)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("X-Amz-Signature", strings.Repeat("0", len(q.Get("X-Amz-Signature"))))
		u.RawQuery = q.Encode()
		tampered := *dl
		tampered.URL = u.String()
		if status, _ := doPresigned(t, &tampered, nil); status != http.StatusForbidden {
			t.Errorf("tampered presigned GET status = %d, want 403", status)
		}
	})

	t.Run("mismatched content type is rejected", func(t *testing.T) {
		t.Parallel()
		ct := "text/plain"
		up, err := ps.PresignUploadURL(
			t.Context(),
			&storage.PresignUploadURLInput{PutObjectInput: storage.PutObjectInput{
				ObjectHeaders:   storage.ObjectHeaders{ContentType: &ct},
				BaseObjectInput: lsObj("", "presigned/ct"),
			}},
		)
		if err != nil {
			t.Fatalf("PresignUploadURL() error: %v", err)
		}
		bad := *up
		bad.SignedHeader = up.SignedHeader.Clone()
		bad.SignedHeader.Set("Content-Type", "image/png")
		if status, _ := doPresigned(t, &bad, []byte("x")); status != http.StatusForbidden {
			t.Errorf("presigned PUT with another Content-Type status = %d, want 403", status)
		}
	})

	t.Run("expired URL is rejected", func(t *testing.T) {
		t.Parallel()
		ttl := time.Second
		dl := presignDownload(t, ps, "fixed", &ttl)
		u, err := url.Parse(dl.URL)
		if err != nil {
			t.Fatal(err)
		}
		if got := u.Query().Get("X-Amz-Expires"); got != "1" {
			t.Fatalf("X-Amz-Expires = %q, want 1", got)
		}
		time.Sleep(2500 * time.Millisecond)
		if status, _ := doPresigned(t, dl, nil); status != http.StatusForbidden {
			t.Errorf("expired presigned GET status = %d, want 403", status)
		}
	})

	t.Run("seven-day upload URL works", func(t *testing.T) {
		t.Parallel()
		up, err := ps.PresignUploadURL(t.Context(), &storage.PresignUploadURLInput{
			TTL:            new(7 * 24 * time.Hour),
			PutObjectInput: storage.PutObjectInput{BaseObjectInput: lsObj("", "ttl-7d")},
		})
		if err != nil {
			t.Fatalf("PresignUploadURL(7d) error: %v", err)
		}
		if status, rb := doPresigned(t, up, []byte("week")); status != http.StatusOK {
			t.Errorf("7-day presigned PUT status = %d, body %s; want 200", status, rb)
		}
	})
}
