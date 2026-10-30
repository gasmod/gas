package test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"uuid"

	"github.com/gasmod/gas/storage/dir/internal/vfs"
)

// The on-disk layout of a VFS root, as the package writes it.
const (
	objectsPath   = ".gas/objects"
	locksPath     = ".gas/locks"
	bucketsPath   = ".gas/buckets"
	metaFileExt   = ".gas.json"
	bucketLockExt = ".bucket"
	fileHeadSize  = 512
	hashLen       = 64
)

// hash returns the hex SHA-256 of s, the name a bucket or key is stored
// under.
func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// bucketDirs returns the metadata, objects and locks directories of the
// bucket name, relative to the VFS root.
func bucketDirs(name string) (meta, obj, locks string) {
	h := hash(name)
	return h, filepath.Join(objectsPath, h), filepath.Join(locksPath, h)
}

// bucketNameFile returns the name record of the bucket name, relative to the
// VFS root.
func bucketNameFile(name string) string {
	return filepath.Join(bucketsPath, hash(name))
}

// bucketLockName returns the name of the lifecycle lock of the bucket name,
// relative to the shared locks directory.
func bucketLockName(name string) string {
	return hash(name) + bucketLockExt
}

// newDataFileName returns a fresh data file name for the object whose key
// hashes to name: the hash, "_", and a new UUID.
func newDataFileName(name string) string {
	return name + "_" + uuid.New().String()
}

// newTestVFS returns a VFS on a fresh temp dir and the dir's path.
func newTestVFS(t *testing.T) (*vfs.VFS, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	v, err := vfs.NewVFS(root)
	if err != nil {
		t.Fatalf("NewVFS: %v", err)
	}
	return v, dir
}

// newTestBucket creates bucket "b" and opens it.
func newTestBucket(t *testing.T, v *vfs.VFS) *vfs.Bucket {
	t.Helper()
	if _, err := v.CreateBucket(t.Context(), "b"); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	return newOpenBucket(t, v)
}

// newOpenBucket opens the existing bucket "b" and closes it on cleanup.
func newOpenBucket(t *testing.T, v *vfs.VFS) *vfs.Bucket {
	t.Helper()
	b, err := v.OpenBucket("b")
	if err != nil {
		t.Fatalf("OpenBucket: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// writeMeta writes content as the metadata file of key in bucket "b".
func writeMeta(t *testing.T, dir, key, content string) {
	t.Helper()
	m, _, _ := bucketPaths(dir)
	if err := os.WriteFile(
		filepath.Join(m, hash(key)+metaFileExt),
		[]byte(content),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// bucketPaths returns the absolute meta, object and lock dirs of bucket "b".
func bucketPaths(dir string) (meta, obj, locks string) {
	m, o, l := bucketDirs("b")
	return filepath.Join(dir, m), filepath.Join(dir, o), filepath.Join(dir, l)
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

// putObject writes key with body and contentType (sniffed when empty),
// failing the test on error.
func putObject(t *testing.T, b *vfs.Bucket, key, body, contentType string) {
	t.Helper()
	in := &vfs.CreateObjectInput{Key: key, Body: bytes.NewReader([]byte(body))}
	if contentType != "" {
		in.ContentType = &contentType
	}
	if _, err := b.CreateObject(context.Background(), in); err != nil {
		t.Fatalf("CreateObject(%q): %v", key, err)
	}
}

// dataFile returns the data file name key's committed metadata points to.
func dataFile(t *testing.T, b *vfs.Bucket, key string) string {
	t.Helper()
	obj, err := b.OpenObject(context.Background(), &vfs.OpenObjectInput{Key: key})
	if err != nil {
		t.Fatalf("OpenObject(%q): %v", key, err)
	}
	if err := obj.File.Close(); err != nil {
		t.Fatalf("Close(%q): %v", key, err)
	}
	return obj.DataFile
}

// getObject reads key in full, failing the test on error.
func getObject(t *testing.T, b *vfs.Bucket, key string) (string, *vfs.Object) {
	t.Helper()
	obj, err := b.OpenObject(context.Background(), &vfs.OpenObjectInput{Key: key})
	if err != nil {
		t.Fatalf("OpenObject(%q): %v", key, err)
	}
	defer func() { _ = obj.File.Close() }()
	body, err := io.ReadAll(obj.File)
	if err != nil {
		t.Fatalf("ReadAll(%q): %v", key, err)
	}
	return string(body), obj
}

// noPanic turns a panic in fn into a test failure, so one regression does not
// take down the whole test binary.
func noPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic: %v", r)
		}
	}()
	fn()
}

// skipIfNoPermissions skips tests that rely on unix permission bits being
// enforced.
func skipIfNoPermissions(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits do not apply on windows")
	}
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
}

// readOnlyDir makes dir read-only and restores its mode on cleanup so
// TempDir removal works.
func readOnlyDir(t *testing.T, dir string) {
	t.Helper()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat(%s): %v", dir, err)
	}
	//nolint:gosec // a directory needs the search bit; this one is read-only
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("Chmod(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, fi.Mode().Perm()) })
}

// slotCount returns the number of in-process slots l holds.
//func slotCount(l *vfs.ObjectsLock) int {
//	l.mu.Lock()
//	defer l.mu.Unlock()
//	return len(l.slots)
//}

// errReader returns data together with err on the first read, then io.EOF.
type errReader struct {
	err  error
	data []byte
	done bool
}

// Read returns r.data with r.err on the first call, then io.EOF.
func (r *errReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	n := copy(p, r.data)
	return n, r.err
}
