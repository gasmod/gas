//go:build unix

package dir

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/gasmod/gas/storage"
)

// chmod sets the mode of path and restores restore on cleanup, so that
// TempDir removal works.
func chmod(t *testing.T, path string, mode, restore os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, restore) })
}

// TestProvider_Permissions checks that permission errors surface as errors
// wrapping os.ErrPermission and are never reported as not-found.
func TestProvider_Permissions(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("permission checks do not apply to root")
	}

	t.Run("unreadable data file", testProviderPermissionsUnreadableData)

	t.Run("unreadable metadata", testProviderPermissionsUnreadableMeta)

	t.Run("read-only metadata dir", testProviderPermissionsReadOnlyMetaDir)

	t.Run("read-only objects dir", testProviderPermissionsReadOnlyObjDir)

	t.Run("untraversable metadata dir", testProviderPermissionsUntraversableMetaDir)

	t.Run("lock file cannot be removed", testProviderPermissionsLockFileStuck)
}

// testProviderPermissionsUnreadableData checks that an unreadable data file
// is ErrPermission, not not-found.
func testProviderPermissionsUnreadableData(t *testing.T) {
	t.Parallel()
	p, dir := newTestProvider(t)
	put(t, p, "secret", []byte("x"))
	chmod(t, dataFile(t, dir, "secret"), 0o000, 0o600)

	_, err := head(p, "secret")
	wantErrIs(t, "HeadObject", err, os.ErrPermission)
	wantPresentError(t, "HeadObject", err)
	err = getErr(p, "secret")
	wantErrIs(t, "GetObject", err, os.ErrPermission)
	wantPresentError(t, "GetObject", err)
}

// testProviderPermissionsUnreadableMeta checks that unreadable metadata fails
// reads, and fails a Put before anything is written: overwriting it would
// orphan the data file it names.
func testProviderPermissionsUnreadableMeta(t *testing.T) {
	t.Parallel()
	p, dir := newTestProvider(t)
	put(t, p, "k", []byte("old"))
	oldData := dataFile(t, dir, "k")
	chmod(t, metaFile(dir, "k"), 0o000, 0o600)

	_, err := head(p, "k")
	wantErrIs(t, "HeadObject", err, os.ErrPermission)
	wantPresentError(t, "HeadObject", err)
	err = getErr(p, "k")
	wantErrIs(t, "GetObject", err, os.ErrPermission)
	_, err = del(p, "k")
	wantErrIs(t, "DeleteObject", err, os.ErrPermission)

	info, err := putWith(p, "k", nil, nil, nil)
	wantErrIs(t, "PutObject", err, os.ErrPermission)
	if info != nil {
		t.Errorf("PutObject() info = %+v, want nil", info)
	}
	if got := listDir(t, objDir(dir, testBucket)); len(got) != 1 ||
		got[0] != filepath.Base(oldData) {
		t.Errorf("objects dir = %v, want only the old data file", got)
	}
}

// testProviderPermissionsReadOnlyMetaDir checks that a metadata dir that
// cannot be written fails Put and Delete with ErrPermission, not
// ErrBucketNotFound, and Delete leaves the object whole.
func testProviderPermissionsReadOnlyMetaDir(t *testing.T) {
	t.Parallel()
	p, dir := newTestProvider(t)
	put(t, p, "existing", []byte("x"))
	chmod(t, metaDir(dir, testBucket), 0o500, 0o750)

	_, err := putWith(p, "new", nil, nil, nil)
	wantErrIs(t, "PutObject", err, os.ErrPermission)
	wantPresentError(t, "PutObject", err)
	if got := listDir(t, objDir(dir, testBucket)); len(got) != 1 {
		t.Errorf("objects dir = %v, want only existing's data file", got)
	}

	_, err = del(p, "existing")
	wantErrIs(t, "DeleteObject", err, os.ErrPermission)
	if _, data := get(t, p, "existing"); string(data) != "x" {
		t.Errorf("existing object = %q, want x", data)
	}
}

// testProviderPermissionsReadOnlyObjDir checks that an unwritable objects dir
// fails Put and keeps existing objects.
func testProviderPermissionsReadOnlyObjDir(t *testing.T) {
	t.Parallel()
	p, dir := newTestProvider(t)
	put(t, p, "existing", []byte("x"))
	chmod(t, objDir(dir, testBucket), 0o500, 0o750)

	_, err := putWith(p, "new", nil, nil, nil)
	wantErrIs(t, "PutObject", err, os.ErrPermission)
	if _, err := head(p, "new"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Head after failed put = %v, want ErrNotFound", err)
	}
	if _, data := get(t, p, "existing"); string(data) != "x" {
		t.Errorf("existing object = %q, want x", data)
	}
}

// testProviderPermissionsUntraversableMetaDir checks that a metadata dir that
// cannot be opened fails every operation with ErrPermission.
func testProviderPermissionsUntraversableMetaDir(t *testing.T) {
	t.Parallel()
	p, dir := newTestProvider(t)
	put(t, p, "k", []byte("x"))
	chmod(t, metaDir(dir, testBucket), 0o000, 0o750)

	for op, err := range allOps(context.Background(), p, base("k")) {
		wantErrIs(t, op, err, os.ErrPermission)
		wantPresentError(t, op, err)
	}
}

// testProviderPermissionsLockFileStuck checks that a lookup of a missing key
// removes the key's lock file. When that fails, Get still reports the miss
// and also the failure; Delete reports the failure instead of success.
func testProviderPermissionsLockFileStuck(t *testing.T) {
	t.Parallel()
	p, dir := newTestProvider(t)
	writeFile(t, filepath.Join(lockDir(dir, testBucket), sha("k")), nil)
	chmod(t, lockDir(dir, testBucket), 0o500, 0o750)

	err := getErr(p, "k")
	wantErrIs(t, "GetObject", err, storage.ErrKeyNotFound)
	wantErrIs(t, "GetObject", err, os.ErrPermission)

	info, err := del(p, "k")
	wantErrIs(t, "DeleteObject", err, os.ErrPermission)
	wantPresentError(t, "DeleteObject", err)
	if info != nil {
		t.Errorf("DeleteObject() info = %+v, want nil", info)
	}
}

// holdLock takes the lock of key in testBucket from outside the provider, as
// another process would, and releases it on cleanup.
func holdLock(t *testing.T, dir, key string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(lockDir(dir, testBucket), sha(key)),
		os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open lock file: %v", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("flock: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
}

// TestProvider_LockWaitHonorsContext checks that a call that waits on an
// object locked elsewhere gives up when its context ends, with the context's
// error and not a not-found error.
func TestProvider_LockWaitHonorsContext(t *testing.T) {
	t.Parallel()
	p, dir := newTestProvider(t)
	put(t, p, "k", []byte("x"))
	holdLock(t, dir, "k")

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	for op, err := range allOps(ctx, p, base("k")) {
		wantErrIs(t, op, err, context.DeadlineExceeded)
		wantPresentError(t, op, err)
	}
	// Other keys are not held up.
	put(t, p, "other", []byte("y"))
	if _, data := get(t, p, "other"); string(data) != "y" {
		t.Errorf("other = %q, want y", data)
	}
}
