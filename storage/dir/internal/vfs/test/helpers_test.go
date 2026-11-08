package vfs_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage/dir/internal/vfs"
)

// The layout of a root, as the package writes it.
const (
	// objectsPath holds the objects directories.
	objectsPath = ".gas/objects"
	// locksPath holds the locks directories and the lifecycle locks.
	locksPath = ".gas/locks"
	// bucketsPath holds the name records.
	bucketsPath = ".gas/buckets"
	// keysPath holds the key indexes.
	keysPath = ".gas/keys"
	// layoutPath is the layout marker.
	layoutPath = ".gas/layout"
	// metaExt ends the name of a metadata file.
	metaExt = ".gas.json"
	// testBucket is the bucket most tests use.
	testBucket = "bucket"
	// testUUID is a UUID for the names of fixtures.
	testUUID = "0b8e6f39-8a7c-4f0e-9a52-3b7f2d1c9e44"
)

// hashLike matches a name hash, which no error may hold.
var hashLike = regexp.MustCompile(`[0-9a-f]{64}`)

// hash returns the hex SHA-256 of s, the name a bucket or a key is stored
// under.
func hash(s string) string {
	sum := sha256.Sum256([]byte(s))

	return hex.EncodeToString(sum[:])
}

// metaPath returns the metadata file of key in bucket, under dir.
func metaPath(dir, bucket, key string) string {
	return filepath.Join(dir, hash(bucket), hash(key)+metaExt)
}

// objDir returns the objects directory of testBucket, under dir.
func objDir(dir string) string { return filepath.Join(dir, objectsPath, hash(testBucket)) }

// lockDir returns the locks directory of testBucket, under dir.
func lockDir(dir string) string { return filepath.Join(dir, locksPath, hash(testBucket)) }

// keysDir returns the keys directory of testBucket, under dir.
func keysDir(dir string) string { return filepath.Join(dir, keysPath, hash(testBucket)) }

// newVFS returns a VFS on a new temp dir, and the dir.
func newVFS(t *testing.T) (*vfs.VFS, string) {
	t.Helper()

	dir := t.TempDir()

	return openVFS(t, dir), dir
}

// openVFS returns a VFS on dir, whose root is closed when t ends.
func openVFS(t *testing.T, dir string) *vfs.VFS {
	t.Helper()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	v, err := vfs.Open(root)
	require.NoError(t, err)

	return v
}

// newBucket creates testBucket in v and opens it, closed when t ends.
func newBucket(t *testing.T, v *vfs.VFS) *vfs.Bucket {
	t.Helper()

	require.NoError(t, v.CreateBucket(t.Context(), testBucket))

	return openBucket(t, v)
}

// openBucket opens testBucket of v, closed when t ends.
func openBucket(t *testing.T, v *vfs.VFS) *vfs.Bucket {
	t.Helper()

	bk, err := v.OpenBucket(testBucket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bk.Close() })

	return bk
}

// write returns a Write of body under key that is never done.
func write(key, body string) *vfs.Write {
	return &vfs.Write{Key: key, Body: strings.NewReader(body), Done: func() error { return nil }}
}

// put writes body under key in bk.
func put(t *testing.T, bk *vfs.Bucket, key, body string) vfs.Written {
	t.Helper()

	out, err := bk.CreateObject(t.Context(), write(key, body))
	require.NoError(t, err)

	return out
}

// read returns the contents of the object key of bk.
func read(t *testing.T, bk *vfs.Bucket, key string) string {
	t.Helper()

	obj, err := bk.OpenObject(t.Context(), key)
	require.NoError(t, err)

	defer func() { require.NoError(t, obj.File.Close()) }()

	b, err := os.ReadFile(obj.File.Name())
	require.NoError(t, err)

	return string(b)
}

// dataFiles returns the names of the data files of key in testBucket,
// under dir.
func dataFiles(t *testing.T, dir, key string) []string {
	t.Helper()

	files, err := filepath.Glob(filepath.Join(objDir(dir), hash(key)+"_*"))
	require.NoError(t, err)

	return files
}

// requireNoPath fails t unless err holds neither dir nor a name hash.
func requireNoPath(t *testing.T, err error, dir string) {
	t.Helper()

	require.Error(t, err)
	assert.NotContains(t, err.Error(), dir)
	assert.NotRegexp(t, hashLike, err.Error())
}

// skipIfModesIgnored skips t where a file mode stops no one: as root, and
// on Windows, which does not enforce POSIX mode bits.
func skipIfModesIgnored(t *testing.T) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("Windows does not enforce POSIX file modes")
	}

	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
}

// errPrivilegeNotHeld is ERROR_PRIVILEGE_NOT_HELD, what Windows gives a
// process that may not create symlinks.
const errPrivilegeNotHeld = syscall.Errno(1314)

// symlink creates the symlink path to target. It skips t on a Windows
// account that may not create symlinks.
func symlink(t *testing.T, target, path string) {
	t.Helper()

	err := os.Symlink(target, path)
	if runtime.GOOS == "windows" && errors.Is(err, errPrivilegeNotHeld) {
		t.Skip("this Windows account may not create symlinks")
	}

	require.NoError(t, err)
}

// chmod sets the mode of path, and restores it when t ends.
func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()

	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(path, mode))
	t.Cleanup(func() { _ = os.Chmod(path, fi.Mode().Perm()) })
}

// collect returns a report function that appends to a list, and the list.
func collect() (func(vfs.Skipped), *[]vfs.Skipped) {
	var got []vfs.Skipped

	return func(s vfs.Skipped) { got = append(got, s) }, &got
}

// listKeys returns the keys of bk within bounds.
func listKeys(t *testing.T, bk *vfs.Bucket, bounds vfs.Bounds) []string {
	t.Helper()

	var keys []string

	err := bk.ListKeys(
		t.Context(),
		bounds,
		func(string) bool { return false },
		func(k string) bool {
			keys = append(keys, k)

			return true
		},
		func(s vfs.Skipped) { t.Errorf("unexpected report: %v", s) },
	)
	require.NoError(t, err)

	return keys
}

// lockAsync locks name with l in a goroutine, and returns its result.
func lockAsync(ctx context.Context, l *vfs.ObjectsLock, name string) <-chan error {
	done := make(chan error, 1)

	go func() { done <- l.Lock(ctx, name) }()

	return done
}

// recv waits for a Lock result, at most 5 seconds.
func recv(t *testing.T, ch <-chan error) error {
	t.Helper()

	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		require.FailNow(t, "Lock did not return")

		return nil
	}
}

// requireBlocked fails t if a Lock result arrives within 50ms.
func requireBlocked(t *testing.T, ch <-chan error) {
	t.Helper()

	select {
	case err := <-ch:
		require.FailNow(t, "Lock returned while the name was held", "%v", err)
	case <-time.After(50 * time.Millisecond):
	}
}

// newLock returns an ObjectsLock on its own root on dir, closed when t
// ends.
func newLock(t *testing.T, dir string) *vfs.ObjectsLock {
	t.Helper()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)

	l := vfs.NewLock(root)

	t.Cleanup(func() { _ = l.Close() })

	return l
}

// holdKey takes the lock of key in testBucket, under dir, with a lock of
// its own, as another process would, and returns the function that gives
// it up.
func holdKey(t *testing.T, dir, key string) func() {
	t.Helper()

	l := newLock(t, lockDir(dir))
	require.NoError(t, l.Lock(t.Context(), hash(key)))

	return func() { require.NoError(t, l.Unlock(hash(key))) }
}

// writeFile writes data to path, creating its directory.
func writeFile(t *testing.T, path, data string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
}

// exists reports whether path exists.
func exists(t *testing.T, path string) bool {
	t.Helper()

	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false
	}

	require.NoError(t, err)

	return true
}

// ctxCanceled returns a context of t and its cancel function.
func ctxCanceled(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()

	return context.WithCancel(t.Context())
}

// readFile returns the contents of path.
func readFile(t *testing.T, path string) string {
	t.Helper()

	b, err := os.ReadFile(path) //nolint:gosec // G304: a path of the test.
	require.NoError(t, err)

	return string(b)
}
