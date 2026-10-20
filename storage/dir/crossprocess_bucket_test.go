package dir

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gasmod/gas/storage"
)

// Environment variables that turn TestCrossProcessBucketCycler into a child
// of TestProvider_CrossProcessBucketLifecycle.
const (
	crossBucketRootEnv = "GAS_STORAGE_DIR_XPROC_BUCKET_ROOT"
	crossBucketIDEnv   = "GAS_STORAGE_DIR_XPROC_BUCKET_ID"
)

const (
	// crossBucketChildren is the number of processes sharing the root.
	crossBucketChildren = 4
	// crossBucketCycles is the number of create, write, delete cycles per
	// process.
	crossBucketCycles = 150
	// crossBucket is the bucket every child creates and deletes.
	crossBucket = "cycled-bucket"
)

// TestProvider_CrossProcessBucketLifecycle checks the ordering of
// CreateBucket and DeleteBucket across processes. It re-runs this test
// binary as crossBucketChildren processes that each create the same bucket,
// write and delete an object in it, and delete it, over and over, on one
// root. CreateBucket writes the bucket's name record before its metadata
// directory, and DeleteBucket removes them in the other order, both under
// the bucket's lifecycle lock; so however the processes interleave, the
// bucket afterwards has both or neither, and no lifecycle lock is left.
func TestProvider_CrossProcessBucketLifecycle(t *testing.T) {
	if os.Getenv(crossBucketRootEnv) != "" || os.Getenv(crossProcessRootEnv) != "" {
		t.Skip("running as a cross-process child")
	}
	if testing.Short() {
		t.Skip("spawns child processes; skipped under -short")
	}

	p, dir := newProvider(t, "")

	children := make([]*exec.Cmd, 0, crossBucketChildren)
	outputs := make([]*bytes.Buffer, 0, crossBucketChildren)
	for i := range crossBucketChildren {
		id := string(rune('A' + i))
		//nolint:gosec // re-runs this test binary with fixed arguments
		cmd := exec.CommandContext(t.Context(), os.Args[0],
			"-test.run", "^TestCrossProcessBucketCycler$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), crossBucketRootEnv+"="+dir, crossBucketIDEnv+"="+id)
		out := &bytes.Buffer{}
		cmd.Stdout, cmd.Stderr = out, out
		if err := cmd.Start(); err != nil {
			t.Fatalf("start child %s: %v", id, err)
		}
		children = append(children, cmd)
		outputs = append(outputs, out)
	}
	for i, cmd := range children {
		err := cmd.Wait()
		out := outputs[i].String()
		if err != nil {
			t.Errorf("child %d failed: %v\n%s", i, err, out)
			continue
		}
		if !strings.Contains(out, "--- PASS: TestCrossProcessBucketCycler") {
			t.Errorf("child %d did not run the cycler:\n%s", i, out)
		}
	}

	_, recordErr := os.Lstat(nameRecord(dir, crossBucket))
	_, metaErr := os.Lstat(metaDir(dir, crossBucket))
	if (recordErr == nil) != (metaErr == nil) {
		t.Errorf("name record: %v, metadata dir: %v; want both or neither", recordErr, metaErr)
	}
	out, err := p.ListBuckets(context.Background(), &storage.ListBucketsInput{})
	if err != nil || out.Skipped != 0 {
		t.Errorf("ListBuckets() = %+v, %v; want no unnamed bucket", out, err)
	}
	locks, err := filepath.Glob(filepath.Join(dir, ".gas", "locks", "*.bucket"))
	if err != nil || len(locks) != 0 {
		t.Errorf("lifecycle lock files left = %q, %v; want none", locks, err)
	}
}

// TestCrossProcessBucketCycler is the child half of
// TestProvider_CrossProcessBucketLifecycle and skips unless that test
// started it. Each cycle creates the shared bucket, writes and deletes its
// own object in it, and deletes the bucket. Another process can delete the
// bucket between any two steps, or fill it, so a write or a delete may find
// it missing, and a delete may find it not empty; anything else fails.
func TestCrossProcessBucketCycler(t *testing.T) {
	rootDir, id := os.Getenv(crossBucketRootEnv), os.Getenv(crossBucketIDEnv)
	if rootDir == "" {
		t.Skip("run only as a child of TestProvider_CrossProcessBucketLifecycle")
	}

	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("os.OpenRoot() error: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	p, err := NewProvider(WithRoot(root))(newConfig(t, ""))
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}

	ctx := context.Background()
	obj := storage.BaseObjectInput{Bucket: new(crossBucket), Key: "from-" + id}
	for i := range crossBucketCycles {
		if _, err := p.CreateBucket(
			ctx,
			&storage.CreateBucketInput{Bucket: crossBucket},
		); err != nil {
			t.Fatalf("cycle %d: CreateBucket() error: %v", i, err)
		}
		_, err := p.PutObject(ctx, &storage.PutObjectInput{
			BaseObjectInput: obj, Body: strings.NewReader(id),
		})
		if err != nil && !errors.Is(err, storage.ErrBucketNotFound) {
			t.Fatalf("cycle %d: PutObject() error: %v", i, err)
		}
		_, dErr := p.DeleteObject(ctx, &storage.DeleteObjectInput{BaseObjectInput: obj})
		if dErr != nil && !errors.Is(dErr, storage.ErrBucketNotFound) {
			t.Fatalf("cycle %d: DeleteObject() error: %v", i, dErr)
		}
		err = p.DeleteBucket(ctx, &storage.DeleteBucketInput{Bucket: crossBucket})
		if err != nil && !errors.Is(err, storage.ErrBucketNotFound) &&
			!errors.Is(err, storage.ErrBucketNotEmpty) {
			t.Fatalf("cycle %d: DeleteBucket() error: %v", i, err)
		}
		out, lErr := p.ListBuckets(ctx, &storage.ListBucketsInput{})
		if lErr != nil {
			t.Fatalf("cycle %d: ListBuckets() error: %v", i, lErr)
		}
		if out.Skipped != 0 {
			// A bucket mid-create or mid-delete is listed or not, never
			// unnamed.
			t.Fatalf("cycle %d: ListBuckets() Skipped = %d, want 0", i, out.Skipped)
		}
	}
}
