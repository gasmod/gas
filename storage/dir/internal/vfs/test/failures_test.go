package vfs_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage/dir/internal/vfs"
)

// The failures of this file need modes that stop the user who runs the
// tests: a directory that can be read but not searched (0o600) lists its
// entries but cannot stat them, and one that can be searched but not
// written (0o500) keeps its entries.

// A write fails when the directory of its data file or of its new index
// entry cannot be synced, and leaves no data file.
func TestCreateObject_SyncFails(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	chmod(t, objDir(dir), 0o300)

	_, err := bk.CreateObject(t.Context(), write("k", "v"))
	require.ErrorIs(t, err, os.ErrPermission)
	requireNoPath(t, err, dir)

	chmod(t, objDir(dir), 0o750)
	assert.Empty(t, dataFiles(t, dir, "k"))

	chmod(t, keysDir(dir), 0o300)

	_, err = bk.CreateObject(t.Context(), write("new", "v"))
	require.ErrorIs(t, err, os.ErrPermission)
}

// A delete whose data file, index entry and lock file cannot be removed
// reports each, though the object is deleted.
func TestDeleteObject_CleanupFails(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "k", "v")
	chmod(t, objDir(dir), 0o500)
	chmod(t, keysDir(dir), 0o500)
	chmod(t, lockDir(dir), 0o500)

	err := bk.DeleteObject(t.Context(), "k")
	je, ok := err.(*vfs.JoinedError) //nolint:errorlint // the top error is the join.
	require.True(t, ok, "%v", err)
	assert.Len(t, je.Errs, 3)
	requireNoPath(t, err, dir)
	assert.NoFileExists(t, metaPath(dir, testBucket, "k"))
}

// A read of a key with no object whose lock file cannot be removed
// reports the release with the missing object.
func TestOpenObject_ReleaseFails(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	writeFile(t, filepath.Join(lockDir(dir), hash("k")), "")
	chmod(t, lockDir(dir), 0o500)

	_, err := bk.OpenObject(t.Context(), "k")
	require.ErrorIs(t, err, vfs.ErrObjectNotFound)
	require.ErrorIs(t, err, os.ErrPermission)
}

// Stat looks again under the key's lock at an object whose data file is
// gone, and finds it deleted when the delete ended meanwhile.
func TestStat_DeletedWhileWaiting(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "k", "v")
	require.NoError(t, os.Remove(dataFiles(t, dir, "k")[0]))

	release := holdKey(t, dir, "k")
	done := make(chan error, 1)

	go func() {
		_, err := bk.Stat(t.Context(), "k")
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	require.NoError(t, os.Remove(metaPath(dir, testBucket, "k")))
	release()
	require.ErrorIs(t, <-done, vfs.ErrObjectNotFound)
}

// A waiter that gets the file lock but can then no longer look at the lock
// file's name fails, and gives the name up.
func TestLock_CurrentUnknown(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)

	dir := filepath.Join(t.TempDir(), "locks")
	require.NoError(t, os.Mkdir(dir, 0o750))

	holder, waiter := newLock(t, dir), newLock(t, dir)
	require.NoError(t, holder.Lock(t.Context(), "n"))

	done := lockAsync(t.Context(), waiter, "n")
	requireBlocked(t, done)
	chmod(t, dir, 0o600)
	require.NoError(t, holder.Unlock("n"))
	require.ErrorIs(t, recv(t, done), os.ErrPermission)
}

// DeleteBucket passes over entries that a write removed while it waited
// for their keys: a metadata file deleted and a temp file renamed away.
func TestDeleteBucket_GoneWhileWaiting(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "k", "v")

	temp := metaPath(dir, testBucket, "t") + testUUID
	writeFile(t, temp, "{}")

	releaseK, releaseT := holdKey(t, dir, "k"), holdKey(t, dir, "t")
	report, got := collect()
	done := make(chan error, 1)

	go func() { done <- v.DeleteBucket(t.Context(), testBucket, report) }()

	time.Sleep(50 * time.Millisecond)
	require.NoError(t, os.Remove(metaPath(dir, testBucket, "k")))
	require.NoError(t, os.Remove(temp))
	releaseK()
	releaseT()
	require.NoError(t, <-done)
	assert.Empty(t, filterForeign(*got))
}

// filterForeign returns the reports of got that are not leftovers of the
// objects, locks and keys directories.
func filterForeign(got []vfs.Skipped) []vfs.Skipped {
	var out []vfs.Skipped

	for _, s := range got {
		if !strings.HasPrefix(s.Path, ".gas") {
			out = append(out, s)
		}
	}

	return out
}

// DeleteBucket fails when it cannot stat a data file or a temp file of
// the metadata directory, or remove debris, and deletes nothing.
func TestDeleteBucket_StatFails(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "k", "v")
	chmod(t, objDir(dir), 0o600)

	err := v.DeleteBucket(t.Context(), testBucket, func(vfs.Skipped) {})
	require.ErrorIs(t, err, os.ErrPermission)
	requireNoPath(t, err, dir)

	v, dir = newVFS(t)
	newBucket(t, v)
	writeFile(t, metaPath(dir, testBucket, "t")+testUUID, "{}")
	chmod(t, filepath.Join(dir, hash(testBucket)), 0o600)

	err = v.DeleteBucket(t.Context(), testBucket, func(vfs.Skipped) {})
	require.ErrorIs(t, err, os.ErrPermission)

	v, dir = newVFS(t)
	newBucket(t, v)
	writeFile(t, metaPath(dir, testBucket, "c"), "{")
	chmod(t, filepath.Join(dir, hash(testBucket)), 0o500)

	err = v.DeleteBucket(t.Context(), testBucket, func(vfs.Skipped) {})
	require.ErrorIs(t, err, os.ErrPermission)
	assert.FileExists(t, metaPath(dir, testBucket, "c"))
}

// DeleteBucket reports, after the commit, a name record it cannot remove
// as an error, and lock files and data files whose locks cannot be looked
// at as leftovers.
func TestDeleteBucket_AfterCommit(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)

	v, dir := newVFS(t)
	newBucket(t, v)
	chmod(t, filepath.Join(dir, bucketsPath), 0o500)

	err := v.DeleteBucket(t.Context(), testBucket, func(vfs.Skipped) {})
	require.ErrorIs(t, err, os.ErrPermission)
	assert.True(t, strings.HasPrefix(err.Error(), "bucket deleted: "), err.Error())
	require.ErrorIs(t, v.HeadBucket(testBucket), vfs.ErrBucketNotFound)

	v, dir = newVFS(t)
	newBucket(t, v)
	require.NoError(t, os.Mkdir(filepath.Join(lockDir(dir), hash("k")), 0o750))
	writeFile(t, filepath.Join(objDir(dir), hash("k")+"_"+testUUID), "x")

	report, got := collect()
	require.NoError(t, v.DeleteBucket(t.Context(), testBucket, report))
	assert.NotEmpty(t, *got)
	assert.FileExists(t, filepath.Join(objDir(dir), hash("k")+"_"+testUUID))
}

// Removed counts a bucket as not removed when it cannot tell, and as
// removed when its open directory can no longer be looked at only where
// that means it is gone.
func TestVFS_RemovedUnknown(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	chmod(t, filepath.Join(dir, hash(testBucket)), 0o000)
	assert.False(t, v.Removed(bk))
	chmod(t, dir, 0o600)
	assert.False(t, v.Removed(bk))
}

// Open fails when a directory of the package is a file, or the root
// cannot be read to look for buckets.
func TestOpen_Unreadable(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, objectsPath), "")

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)

	_, err = vfs.Open(root)
	requireNoPath(t, err, dir)
	require.NoError(t, root.Close())

	dir = t.TempDir()

	root, err = os.OpenRoot(dir)
	require.NoError(t, err)
	chmod(t, dir, 0o300)

	_, err = vfs.Open(root)
	require.ErrorIs(t, err, os.ErrPermission)
	require.NoError(t, root.Close())
}

// CreateBucket fails when the locks directory cannot be opened, or the
// name record cannot be read or looked at; ListBuckets fails when the
// root cannot be read.
func TestBuckets_Unreadable(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)

	v, dir := newVFS(t)
	chmod(t, filepath.Join(dir, locksPath), 0o000)
	require.ErrorIs(t, v.CreateBucket(t.Context(), testBucket), os.ErrPermission)

	v, dir = newVFS(t)
	require.NoError(t, v.CreateBucket(t.Context(), testBucket))
	chmod(t, filepath.Join(dir, bucketsPath, hash(testBucket)), 0o000)
	require.ErrorIs(t, v.CreateBucket(t.Context(), testBucket), os.ErrPermission)

	v, dir = newVFS(t)
	chmod(t, filepath.Join(dir, bucketsPath), 0o600)
	require.ErrorIs(t, v.CreateBucket(t.Context(), testBucket), os.ErrPermission)

	v, dir = newVFS(t)
	chmod(t, dir, 0o300)

	_, _, err := v.ListBuckets(t.Context())
	require.ErrorIs(t, err, os.ErrPermission)
}

// Sweep fails when the root, the locks directory, a metadata file of a
// key with data files, a metadata file of a key in the index, or a
// directory of the index cannot be read.
func TestSweep_Unreadable(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)

	for name, prepare := range map[string]func(dir string){
		"root":  func(dir string) { chmod(t, dir, 0o300) },
		"locks": func(dir string) { chmod(t, filepath.Join(dir, locksPath), 0o000) },
		"meta": func(dir string) {
			writeFile(t, filepath.Join(objDir(dir), hash("k")+"_"+testUUID), "x")
			chmod(t, metaPath(dir, testBucket, "k"), 0o000)
		},
		"stat":    func(dir string) { chmod(t, filepath.Join(dir, hash(testBucket)), 0o600) },
		"objects": func(dir string) { chmod(t, objDir(dir), 0o600) },
		"index": func(dir string) {
			sub, err := filepath.Glob(filepath.Join(keysDir(dir), "*.d"))
			require.NoError(t, err)
			require.Len(t, sub, 1)
			chmod(t, sub[0], 0o000)
		},
	} {
		v, dir := newVFS(t)
		bk := newBucket(t, v)

		put(t, bk, "k", "v")
		put(t, bk, strings.Repeat("l", 130), "v")
		prepare(dir)

		_, err := v.Sweep(t.Context(), 1000, func(vfs.Skipped) {})
		require.ErrorIs(t, err, os.ErrPermission, name)
		requireNoPath(t, err, dir)
	}
}

// CreateBucket reports a lifecycle lock file it cannot remove.
func TestCreateBucket_ReleaseFails(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)

	v, dir := newVFS(t)
	require.NoError(t, v.CreateBucket(t.Context(), testBucket))
	writeFile(t, filepath.Join(dir, locksPath, hash(testBucket)+".bucket"), "")
	chmod(t, filepath.Join(dir, locksPath), 0o500)

	err := v.CreateBucket(t.Context(), testBucket)
	require.ErrorIs(t, err, os.ErrPermission)
	requireNoPath(t, err, dir)
}

// doneAfter is a context whose Done channel is closed from its n-th call
// on, and whose Err is then context.Canceled, so that a test ends a call
// at a chosen check.
type doneAfter struct {
	context.Context //nolint:containedctx // the context it ends early.

	// closed is the channel Done returns once it has ended.
	closed chan struct{}
	// open is the channel Done returns before.
	open chan struct{}
	// calls counts the calls of Done and Err.
	calls atomic.Int64
	// n is the call from which it has ended.
	n int64
}

// newDoneAfter returns a doneAfter that ends at its n-th check.
func newDoneAfter(n int64) *doneAfter {
	c := make(chan struct{})
	close(c)

	return &doneAfter{Context: context.Background(), closed: c, open: make(chan struct{}), n: n}
}

// Done returns a closed channel from the n-th check on.
func (c *doneAfter) Done() <-chan struct{} {
	if c.calls.Add(1) >= c.n {
		return c.closed
	}

	return c.open
}

// Err returns context.Canceled from the n-th check on.
func (c *doneAfter) Err() error {
	if c.calls.Add(1) >= c.n {
		return context.Canceled
	}

	return nil
}

// A waiter that gets a lock file its holder released, and finds its
// context ended, gives up with the context's error.
func TestLock_StaleThenCanceled(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	holder, waiter := newLock(t, dir), newLock(t, dir)
	require.NoError(t, holder.Lock(t.Context(), "n"))

	// The slot and the wait in the file lock call check Done once each;
	// the check after the stale file is the third.
	done := lockAsync(newDoneAfter(3), waiter, "n")
	requireBlocked(t, done)
	require.NoError(t, holder.Release("n"))
	require.ErrorIs(t, recv(t, done), context.Canceled)
}

// Sweep stops with the context's error at whichever entry it checks it.
func TestSweep_CanceledMidway(t *testing.T) {
	t.Parallel()

	for n := range int64(12) {
		v, dir := newVFS(t)
		bk := newBucket(t, v)

		put(t, bk, "k", "v")
		leftovers(t, dir, "k")

		_, err := v.Sweep(newDoneAfter(n+1), 1000, func(vfs.Skipped) {})
		if err != nil {
			require.ErrorIs(t, err, context.Canceled, n)
		}
	}
}

// DeleteBucket of an empty bucket reports, as leftovers, an objects or a
// locks directory it cannot read after the commit.
func TestDeleteBucket_LeftoversUnreadable(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)

	for _, sub := range []func(dir string) string{objDir, lockDir} {
		v, dir := newVFS(t)
		newBucket(t, v)
		chmod(t, sub(dir), 0o600)

		report, got := collect()
		require.NoError(t, v.DeleteBucket(t.Context(), testBucket, report))
		require.NotEmpty(t, *got)

		for _, s := range *got {
			requireNoPath(t, s.Err, dir)
		}
	}
}

// DeleteBucket stops with the context's error at whichever lock wait it
// finds it ended, debris included, and deletes nothing it has not
// classified.
func TestDeleteBucket_CanceledMidway(t *testing.T) {
	t.Parallel()

	for n := range int64(12) {
		v, dir := newVFS(t)
		newBucket(t, v)
		writeFile(t, metaPath(dir, testBucket, "a"), "{")
		writeFile(t, metaPath(dir, testBucket, "b")+testUUID, "{}")

		err := v.DeleteBucket(newDoneAfter(n+1), testBucket, func(vfs.Skipped) {})
		if err != nil {
			require.ErrorIs(t, err, context.Canceled, n)
		}
	}
}
