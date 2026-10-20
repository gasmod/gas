package dir

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/storage"
)

const testBucket = "test-bucket"

// openRoot opens an os.Root on a fresh temp dir and closes it on cleanup.
func openRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("os.OpenRoot() error: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root, dir
}

// newConfig returns a gas.ConfigProvider seeded with the given storage bucket.
func newConfig(t *testing.T, bucket string) gas.ConfigProvider {
	t.Helper()

	mock, err := configtest.NewMockConfigWithValues(map[string]any{
		"storage": map[string]any{"bucket": bucket},
	})
	if err != nil {
		t.Fatalf("NewMockConfigWithValues() error: %v", err)
	}
	return mock
}

// newProvider builds a Provider through NewProvider over a fresh root with
// cfgBucket as the configured bucket, and creates no bucket. It returns the
// provider and the absolute path of the root directory.
func newProvider(t *testing.T, cfgBucket string) (*Provider, string) {
	t.Helper()
	root, dir := openRoot(t)
	p, err := NewProvider(WithRoot(root))(newConfig(t, cfgBucket))
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}
	return p, dir
}

// newTestProvider is newProvider with testBucket configured and created.
func newTestProvider(t *testing.T) (*Provider, string) {
	t.Helper()
	p, dir := newProvider(t, testBucket)
	createBucket(t, p, testBucket)
	return p, dir
}

// createBucket creates bucket through the provider.
func createBucket(t *testing.T, p *Provider, bucket string) {
	t.Helper()
	_, err := p.CreateBucket(context.Background(), &storage.CreateBucketInput{Bucket: bucket})
	if err != nil {
		t.Fatalf("CreateBucket(%q) error: %v", bucket, err)
	}
}

// base returns an input for key in the configured bucket.
func base(key string) storage.BaseObjectInput {
	return storage.BaseObjectInput{Key: key}
}

// inBucket returns an input for key "k" in the given bucket.
func inBucket(bucket string) storage.BaseObjectInput {
	return storage.BaseObjectInput{Bucket: new(bucket), Key: "k"}
}

// randomBytes returns n random bytes.
func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// putWith puts key with the given body, content type and metadata.
func putWith(p *Provider, key string, body io.Reader, ct *string, meta map[string]string) (
	*storage.ObjectInfo, error,
) {
	return p.PutObject(context.Background(), &storage.PutObjectInput{
		BaseObjectInput: base(key),
		Body:            body,
		ObjectHeaders:   storage.ObjectHeaders{ContentType: ct, Metadata: meta},
	})
}

// put puts key with body and fails the test on error.
func put(t *testing.T, p *Provider, key string, body []byte) *storage.ObjectInfo {
	t.Helper()
	info, err := putWith(p, key, bytes.NewReader(body), nil, nil)
	if err != nil {
		t.Fatalf("PutObject(%q) error: %v", key, err)
	}
	return info
}

// get reads key in full and fails the test on error.
func get(t *testing.T, p *Provider, key string) (*storage.Object, []byte) {
	t.Helper()
	obj, err := p.GetObject(context.Background(), &storage.GetObjectInput{
		HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: base(key)},
	})
	if err != nil {
		t.Fatalf("GetObject(%q) error: %v", key, err)
	}
	data, err := io.ReadAll(obj.Body)
	if cErr := obj.Body.Close(); cErr != nil {
		t.Errorf("Body.Close() error: %v", cErr)
	}
	if err != nil {
		t.Fatalf("ReadAll(%q) error: %v", key, err)
	}
	return obj, data
}

// head heads key in the configured bucket.
func head(p *Provider, key string) (*storage.ObjectInfo, error) {
	return p.HeadObject(context.Background(), &storage.HeadObjectInput{BaseObjectInput: base(key)})
}

// getIn gets in, closes any body, and returns the error.
func getIn(p *Provider, in storage.BaseObjectInput) error {
	obj, err := p.GetObject(context.Background(), &storage.GetObjectInput{
		HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: in},
	})
	if obj != nil && obj.Body != nil {
		_ = obj.Body.Close()
	}
	return err
}

// getErr gets key in the configured bucket and returns the error.
func getErr(p *Provider, key string) error {
	return getIn(p, base(key))
}

// del deletes key in the configured bucket.
func del(p *Provider, key string) (*storage.ObjectInfo, error) {
	return p.DeleteObject(
		context.Background(),
		&storage.DeleteObjectInput{BaseObjectInput: base(key)},
	)
}

// allOps runs Head, Put, Get and Delete of in and returns their errors by
// operation name.
func allOps(ctx context.Context, p *Provider, in storage.BaseObjectInput) map[string]error {
	errs := map[string]error{}
	_, errs["head"] = p.HeadObject(ctx, &storage.HeadObjectInput{BaseObjectInput: in})
	_, errs["put"] = p.PutObject(ctx, &storage.PutObjectInput{
		BaseObjectInput: in,
		Body:            strings.NewReader("x"),
	})
	obj, err := p.GetObject(ctx, &storage.GetObjectInput{
		HeadObjectInput: storage.HeadObjectInput{BaseObjectInput: in},
	})
	if obj != nil {
		_ = obj.Body.Close()
	}
	errs["get"] = err
	_, errs["delete"] = p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: in})
	return errs
}

// sha returns the hex SHA-256 of s, the on-disk name of a bucket or key.
func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// metaDir is the absolute metadata directory of bucket under dir.
func metaDir(dir, bucket string) string {
	return filepath.Join(dir, sha(bucket))
}

// objDir is the absolute objects directory of bucket under dir.
func objDir(dir, bucket string) string {
	return filepath.Join(dir, ".gas", "objects", sha(bucket))
}

// lockDir is the absolute locks directory of bucket under dir.
func lockDir(dir, bucket string) string {
	return filepath.Join(dir, ".gas", "locks", sha(bucket))
}

// metaFile is the absolute metadata file of key in testBucket under dir.
func metaFile(dir, key string) string {
	return filepath.Join(metaDir(dir, testBucket), sha(key)+".gas.json")
}

// dataFile is the absolute path of the one data file of key in testBucket.
func dataFile(t *testing.T, dir, key string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(objDir(dir, testBucket), sha(key)+"_*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("data files of %q = %v, want exactly one", key, matches)
	}
	return matches[0]
}

// listDir returns the sorted names in path.
func listDir(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", path, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// assertEmptyBucket fails if the metadata or objects directory of testBucket
// holds anything, temp files included.
func assertEmptyBucket(t *testing.T, dir string) {
	t.Helper()
	for _, d := range []string{metaDir(dir, testBucket), objDir(dir, testBucket)} {
		if got := listDir(t, d); len(got) != 0 {
			t.Errorf("%s = %v, want empty", d, got)
		}
	}
}

// assertNoTempFiles fails if a metadata temp file is left anywhere under dir.
// A committed metadata file ends in the extension; a temp file has a UUID
// after it.
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(d.Name(), ".gas.json") && !strings.HasSuffix(d.Name(), ".gas.json") {
			t.Errorf("temp file left behind: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}

// writeFile writes data to path, creating parent directories.
func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	//nolint:gosec // G703: test paths are built under t.TempDir()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// wantErrIs fails the test unless errors.Is(err, target).
func wantErrIs(t *testing.T, op string, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s error = %v, want errors.Is(_, %v)", op, err, target)
	}
}

// notFoundSentinels are the storage errors a caller treats as "absent".
var notFoundSentinels = []error{
	storage.ErrNotFound,
	storage.ErrKeyNotFound,
	storage.ErrBucketNotFound,
	storage.ErrVersionNotFound,
}

// wantPresentError fails the test unless err is an error that matches none
// of the not-found sentinels: corruption and I/O failures must never read as
// a missing object or bucket.
func wantPresentError(t *testing.T, op string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s error = nil, want an error", op)
	}
	for _, s := range notFoundSentinels {
		if errors.Is(err, s) {
			t.Fatalf("%s error = %v, must not match %v", op, err, s)
		}
	}
}
