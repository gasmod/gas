package vfs_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage/dir/internal/vfs"
)

// sweepAll sweeps v with a limit no test reaches, and returns what it did
// and reported.
func sweepAll(t *testing.T, v *vfs.VFS) (vfs.SweepResult, []vfs.Skipped) {
	t.Helper()

	report, got := collect()

	res, err := v.Sweep(t.Context(), 1000, report)
	require.NoError(t, err)

	return res, *got
}

// leftovers writes the leftovers of crashed writes of key in bucket,
// under dir, and returns their paths: a temp metadata file, a data file of
// a key with no metadata, a second data file of key, an index entry of a
// key with no metadata, and a temp name record.
func leftovers(t *testing.T, dir, key string) []string {
	t.Helper()

	paths := []string{
		metaPath(dir, testBucket, key) + testUUID,
		filepath.Join(objDir(dir), hash("gone")+"_"+testUUID),
		filepath.Join(objDir(dir), hash(key)+"_"+testUUID),
		filepath.Join(keysDir(dir), hex.EncodeToString([]byte("gone"))+".k"),
		filepath.Join(dir, bucketsPath, hash(testBucket)+testUUID),
	}

	for _, p := range paths {
		writeFile(t, p, "x")
	}

	return paths
}

// Sweep removes the leftovers of crashed writes and reports each, and
// keeps the objects and what it does not know.
func TestSweep_RemovesLeftovers(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "k", "v")

	paths := leftovers(t, dir, "k")
	foreign := filepath.Join(objDir(dir), "foreign")
	writeFile(t, foreign, "")

	res, got := sweepAll(t, v)
	assert.Equal(t, 5, res.Removed)
	assert.False(t, res.Limited)
	assert.Len(t, got, 5)

	for _, p := range paths {
		assert.NoFileExists(t, p)
	}

	for _, s := range got {
		assert.NotContains(t, s.Path, dir)
		require.Error(t, s.Err)
	}

	assert.FileExists(t, foreign)
	assert.Equal(t, "v", read(t, bk, "k"))
	assert.Equal(t, []string{"k"}, listKeys(t, bk, vfs.Bounds{}))

	res, _ = sweepAll(t, v)
	assert.Zero(t, res.Removed)
}

// Sweep leaves the leftovers of a key, or of a bucket, whose lock another
// holder has: a write may be in progress.
func TestSweep_SkipsHeldLocks(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "k", "v")

	temp := metaPath(dir, testBucket, "k") + testUUID
	extra := filepath.Join(objDir(dir), hash("k")+"_"+testUUID)
	stale := filepath.Join(keysDir(dir), hex.EncodeToString([]byte("gone"))+".k")
	record := filepath.Join(dir, bucketsPath, hash(testBucket)+testUUID)

	for _, p := range []string{temp, extra, stale, record} {
		writeFile(t, p, "x")
	}

	releaseK := holdKey(t, dir, "k")
	releaseGone := holdKey(t, dir, "gone")
	bucketLock := newLock(t, filepath.Join(dir, locksPath))
	require.NoError(t, bucketLock.Lock(t.Context(), hash(testBucket)+".bucket"))

	res, got := sweepAll(t, v)
	assert.Zero(t, res.Removed)
	assert.Empty(t, got)

	for _, p := range []string{temp, extra, stale, record} {
		assert.FileExists(t, p)
	}

	releaseK()
	releaseGone()
	require.NoError(t, bucketLock.Unlock(hash(testBucket)+".bucket"))

	res, _ = sweepAll(t, v)
	assert.Equal(t, 4, res.Removed)

	for _, p := range []string{temp, extra, stale, record} {
		assert.NoFileExists(t, p)
	}
}

// Sweep keeps the data files of a key whose metadata is corrupt, which
// DeleteBucket removes with the bucket.
func TestSweep_KeepsCorrupt(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	newBucket(t, v)

	data := filepath.Join(objDir(dir), hash("k")+"_"+testUUID)
	writeFile(t, metaPath(dir, testBucket, "k"), "{")
	writeFile(t, data, "x")

	res, _ := sweepAll(t, v)
	assert.Zero(t, res.Removed)
	assert.FileExists(t, data)
}

// Sweep looks at no more entries than its limit, then stops and says so;
// with an ended context it fails with the context's error.
func TestSweep_Limit(t *testing.T) {
	t.Parallel()

	// The leftovers are six entries: two in the buckets directory, one in
	// the metadata directory, two in the objects directory and one in the
	// key index.
	for limit := range 7 {
		v, dir := newVFS(t)
		newBucket(t, v)
		leftovers(t, dir, "k")

		res, err := v.Sweep(t.Context(), limit, func(vfs.Skipped) {})
		require.NoError(t, err)
		assert.Equal(t, limit < 6, res.Limited, limit)
	}

	v, _ := newVFS(t)
	newBucket(t, v)

	ctx, cancel := ctxCanceled(t)
	cancel()

	_, err := v.Sweep(ctx, 1000, func(vfs.Skipped) {})
	require.ErrorIs(t, err, ctx.Err())
}

// Sweep passes over a bucket whose directories are gone in part, and
// fails when a directory it reads cannot be read.
func TestSweep_Failures(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)

	v, dir := newVFS(t)
	newBucket(t, v)
	require.NoError(t, os.Remove(keysDir(dir)))

	res, _ := sweepAll(t, v)
	assert.Zero(t, res.Removed)
	require.NoError(t, v.CreateBucket(t.Context(), testBucket))

	put(t, openBucket(t, v), "k", "v")

	for _, p := range []string{
		keysDir(dir),
		objDir(dir),
		filepath.Join(dir, hash(testBucket)),
		filepath.Join(dir, bucketsPath),
	} {
		chmod(t, p, 0o000)

		_, err := v.Sweep(t.Context(), 1000, func(vfs.Skipped) {})
		require.ErrorIs(t, err, os.ErrPermission, p)
		requireNoPath(t, err, dir)
	}
}

// Sweep fails when a leftover cannot be removed.
func TestSweep_Unremovable(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)

	for _, p := range []func(dir string) string{
		func(dir string) string { return metaPath(dir, testBucket, "k") + testUUID },
		func(dir string) string { return filepath.Join(objDir(dir), hash("k")+"_"+testUUID) },
		func(dir string) string {
			return filepath.Join(keysDir(dir), hex.EncodeToString([]byte("gone"))+".k")
		},
		func(dir string) string { return filepath.Join(dir, bucketsPath, hash(testBucket)+testUUID) },
	} {
		v, dir := newVFS(t)
		newBucket(t, v)

		path := p(dir)
		writeFile(t, path, "x")
		chmod(t, filepath.Dir(path), 0o500)

		_, err := v.Sweep(t.Context(), 1000, func(vfs.Skipped) {})
		require.ErrorIs(t, err, os.ErrPermission, path)
	}
}

// onFreeLockOpened sets vfs.FreeLockOpened to run fn once, at the first
// open of the lock file of key, until t ends.
func onFreeLockOpened(t *testing.T, key string, fn func()) *atomic.Bool {
	t.Helper()

	fired := new(atomic.Bool)
	hook := func(name string) {
		if name == hash(key) && fired.CompareAndSwap(false, true) {
			fn()
		}
	}

	vfs.FreeLockOpened.Store(&hook)
	t.Cleanup(func() { vfs.FreeLockOpened.Store(nil) })

	return fired
}

// Sweep counts a key as held when the lock file it opened was released,
// and a new holder locked the file that replaced it, before Sweep locked
// the old one: it neither removes the new holder's lock file nor acts on
// the key, so a third locker still waits.
//
// Not parallel: it sets vfs.FreeLockOpened.
func TestSweep_ReleasedLockFileIsHeld(t *testing.T) {
	v, dir := newVFS(t)
	newBucket(t, v)

	stale := filepath.Join(keysDir(dir), hex.EncodeToString([]byte("gone"))+".k")
	writeFile(t, stale, "x")

	locks := lockDir(dir)
	holder, writer, third := newLock(t, locks), newLock(t, locks), newLock(t, locks)
	name := hash("gone")

	require.NoError(t, holder.Lock(t.Context(), name))

	fired := onFreeLockOpened(t, "gone", func() {
		assert.NoError(t, holder.Release(name))
		assert.NoError(t, writer.Lock(t.Context(), name))
	})

	res, _ := sweepAll(t, v)
	require.True(t, fired.Load())
	assert.Zero(t, res.Removed)
	assert.FileExists(t, stale)
	assert.FileExists(t, filepath.Join(lockDir(dir), name))

	done := lockAsync(t.Context(), third, name)
	requireBlocked(t, done)
	require.NoError(t, writer.Unlock(name))
	require.NoError(t, recv(t, done))
	require.NoError(t, third.Unlock(name))
}

// A missing lock file is not free: Sweep creates and locks it, so a
// writer that takes the key once Sweep has opened it keeps it, and Sweep
// leaves the key's index entry.
//
// Not parallel: it sets vfs.FreeLockOpened.
func TestSweep_MissingLockFileIsNotFree(t *testing.T) {
	v, dir := newVFS(t)
	newBucket(t, v)

	stale := filepath.Join(keysDir(dir), hex.EncodeToString([]byte("gone"))+".k")
	writeFile(t, stale, "x")

	name := hash("gone")
	require.NoFileExists(t, filepath.Join(lockDir(dir), name))

	writer := newLock(t, lockDir(dir))
	fired := onFreeLockOpened(t, "gone", func() {
		assert.NoError(t, writer.Lock(t.Context(), name))
	})

	res, _ := sweepAll(t, v)
	require.True(t, fired.Load())
	assert.Zero(t, res.Removed)
	assert.FileExists(t, stale)
	require.NoError(t, writer.Unlock(name))

	res, _ = sweepAll(t, v)
	assert.Equal(t, 1, res.Removed)
	assert.NoFileExists(t, stale)
	assert.NoFileExists(t, filepath.Join(lockDir(dir), name))
}

// Sweep removes the directories of the key index that hold no entry,
// such as those a delete of a long key leaves, and keeps those that do.
func TestSweep_RemovesEmptyIndexDirs(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)
	gone, kept := strings.Repeat("a", 300), strings.Repeat("b", 300)

	put(t, bk, gone, "v")
	put(t, bk, kept, "v")
	require.NoError(t, bk.DeleteObject(t.Context(), gone))

	entries := func() []string {
		des, err := os.ReadDir(keysDir(dir))
		require.NoError(t, err)

		names := make([]string, 0, len(des))
		for _, de := range des {
			names = append(names, de.Name())
		}

		return names
	}
	require.Len(t, entries(), 2)

	res, got := sweepAll(t, v)
	assert.Equal(t, 2, res.Removed)
	require.Len(t, got, 2)

	for _, s := range got {
		require.ErrorContains(t, s.Err, "empty directory of the key index")
	}

	require.Len(t, entries(), 1)
	assert.Equal(t, []string{kept}, listKeys(t, bk, vfs.Bounds{}))
}

// Writes of long keys and Sweeps that remove the emptied directories of
// the key index race without failing: a write whose directory a Sweep
// removed creates it again.
func TestSweep_RacesLongKeyWrites(t *testing.T) {
	t.Parallel()

	v, _ := newVFS(t)
	bk := newBucket(t, v)
	key := strings.Repeat("a", 300)
	deadline := time.Now().Add(time.Second)

	var wg sync.WaitGroup

	wg.Go(func() {
		for time.Now().Before(deadline) {
			_, err := bk.CreateObject(t.Context(), write(key, "v"))
			if err != nil {
				t.Errorf("unexpected error: %v", err)

				return
			}

			err = bk.DeleteObject(t.Context(), key)
			if err != nil {
				t.Errorf("unexpected error: %v", err)

				return
			}
		}
	})
	wg.Go(func() {
		for time.Now().Before(deadline) {
			_, err := v.Sweep(t.Context(), 1000, func(vfs.Skipped) {})
			if err != nil {
				t.Errorf("unexpected error: %v", err)

				return
			}
		}
	})
	wg.Wait()
}

// Sweep makes again the index entry of an object that has none, such as
// after a power loss that kept its metadata but not its entry, unless
// another holder has the key's lock.
func TestSweep_Reindexes(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)
	long := strings.Repeat("a", 300)

	put(t, bk, "k", "v")
	put(t, bk, long, "v")

	des, err := os.ReadDir(keysDir(dir))
	require.NoError(t, err)

	for _, de := range des {
		require.NoError(t, os.RemoveAll(filepath.Join(keysDir(dir), de.Name())))
	}

	release := holdKey(t, dir, "k")

	res, got := sweepAll(t, v)
	assert.Equal(t, 1, res.Reindexed)
	require.Len(t, got, 1)
	assert.Equal(t, filepath.Join(hash(testBucket), hash(long)+metaExt), got[0].Path)
	assert.Equal(t, []string{long}, listKeys(t, bk, vfs.Bounds{}))

	release()

	res, _ = sweepAll(t, v)
	assert.Equal(t, 1, res.Reindexed)
	assert.Equal(t, []string{long, "k"}, listKeys(t, bk, vfs.Bounds{}))

	res, _ = sweepAll(t, v)
	assert.Zero(t, res.Reindexed)
}

// emptyIndexDirs leaves two empty directories of the key index of bk, as
// the deletes of two long keys do, and returns their paths under dir.
func emptyIndexDirs(t *testing.T, dir string, bk *vfs.Bucket) []string {
	t.Helper()

	paths := make([]string, 0, 2)

	for _, c := range []string{"a", "b"} {
		key := strings.Repeat(c, 130)
		put(t, bk, key, "v")
		require.NoError(t, bk.DeleteObject(t.Context(), key))

		paths = append(paths, filepath.Join(
			keysDir(dir), hex.EncodeToString([]byte(key[:chunkBytes]))+".d",
		))
	}

	return paths
}

// Sweep counts each index directory against its limit, so some limit
// stops it among them, with one removed; and it leaves a directory of the
// keys directory it did not write.
func TestSweep_IndexDirsLimit(t *testing.T) {
	t.Parallel()

	for limit := 1; ; limit++ {
		require.Less(t, limit, 50, "no limit stopped the sweep among the index dirs")

		v, dir := newVFS(t)
		bk := newBucket(t, v)
		paths := emptyIndexDirs(t, dir, bk)
		foreign := filepath.Join(keysDir(dir), "foreign")
		require.NoError(t, os.Mkdir(foreign, 0o750))

		res, err := v.Sweep(t.Context(), limit, func(vfs.Skipped) {})
		require.NoError(t, err)

		if !res.Limited || res.Removed != 1 {
			continue
		}

		res, _ = sweepAll(t, v)
		assert.Equal(t, 1, res.Removed)

		for _, p := range paths {
			assert.NoDirExists(t, p)
		}

		assert.DirExists(t, foreign)

		return
	}
}

// A Sweep fails when it cannot read an index directory, remove an empty
// one, or create the index entry of an object that has none.
func TestSweep_IndexFailures(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)

	t.Run("read", func(t *testing.T) {
		t.Parallel()

		v, dir := newVFS(t)
		bk := newBucket(t, v)
		chmod(t, emptyIndexDirs(t, dir, bk)[0], 0o000)

		_, err := v.Sweep(t.Context(), 1000, func(vfs.Skipped) {})
		require.ErrorIs(t, err, os.ErrPermission)
		requireNoPath(t, err, dir)
	})

	t.Run("remove", func(t *testing.T) {
		t.Parallel()

		v, dir := newVFS(t)
		bk := newBucket(t, v)
		emptyIndexDirs(t, dir, bk)
		chmod(t, keysDir(dir), 0o500)

		_, err := v.Sweep(t.Context(), 1000, func(vfs.Skipped) {})
		require.ErrorIs(t, err, os.ErrPermission)
		requireNoPath(t, err, dir)
	})

	t.Run("reindex", func(t *testing.T) {
		t.Parallel()

		v, dir := newVFS(t)
		bk := newBucket(t, v)
		put(t, bk, "k", "v")

		entry := filepath.Join(keysDir(dir), hex.EncodeToString([]byte("k"))+".k")
		require.NoError(t, os.Remove(entry))
		chmod(t, keysDir(dir), 0o500)

		_, err := v.Sweep(t.Context(), 1000, func(vfs.Skipped) {})
		require.ErrorIs(t, err, os.ErrPermission)
		requireNoPath(t, err, dir)
	})
}

// A Sweep fails when it cannot create the lock file of a key or of a
// bucket with a leftover: a missing lock file is not free, so it takes
// the lock as a write would.
func TestSweep_LockFileUncreatable(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)

	for name, tc := range map[string]struct {
		leftover func(dir string) string
		locks    func(dir string) string
	}{
		"name record": {
			func(dir string) string { return filepath.Join(dir, bucketsPath, hash(testBucket)+testUUID) },
			func(dir string) string { return filepath.Join(dir, locksPath) },
		},
		"meta temp": {
			func(dir string) string { return metaPath(dir, testBucket, "k") + testUUID },
			lockDir,
		},
		"stale index": {
			func(dir string) string {
				return filepath.Join(keysDir(dir), hex.EncodeToString([]byte("gone"))+".k")
			},
			lockDir,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, dir := newVFS(t)
			newBucket(t, v)

			leftover := tc.leftover(dir)
			writeFile(t, leftover, "x")
			chmod(t, tc.locks(dir), 0o500)

			_, err := v.Sweep(t.Context(), 1000, func(vfs.Skipped) {})
			require.ErrorIs(t, err, os.ErrPermission)
			requireNoPath(t, err, dir)
			assert.FileExists(t, leftover)
		})
	}
}

// churnLongKeys writes and deletes keys under one long prefix in bk until
// stop closes, so the key index keeps gaining and losing a directory.
func churnLongKeys(t *testing.T, bk *vfs.Bucket, stop <-chan struct{}) {
	t.Helper()

	long := strings.Repeat("a", 130)

	for i := 0; ; i++ {
		select {
		case <-stop:
			return
		default:
		}

		key := long + string(rune('a'+i%26))

		_, err := bk.CreateObject(t.Context(), write(key, "x"))
		if err == nil {
			err = bk.DeleteObject(t.Context(), key)
		}

		if err != nil {
			t.Errorf("unexpected error: %v", err)

			return
		}
	}
}

// Listings and Sweeps that run while other Sweeps remove emptied
// directories of the key index pass over a directory that is gone, as an
// empty one, instead of failing.
func TestSweep_IndexDirGoneMidWalk(t *testing.T) {
	t.Parallel()

	v, _ := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "z", "1")

	stop := make(chan struct{})

	var wg sync.WaitGroup

	wg.Go(func() { churnLongKeys(t, bk, stop) })

	for range 2 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}

				_, err := v.Sweep(t.Context(), 1000, func(vfs.Skipped) {})
				if err != nil {
					t.Errorf("unexpected error: %v", err)

					return
				}
			}
		})
	}

	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		err := bk.ListKeys(t.Context(), vfs.Bounds{}, func(string) bool { return false },
			func(string) bool { return true }, func(vfs.Skipped) {})
		if err != nil {
			t.Errorf("unexpected error: %v", err)

			break
		}
	}

	close(stop)
	wg.Wait()
}

// Sweeps that run together, while writes and deletes empty directories of
// the key index, pass over a directory another Sweep removed since they
// read its parent, instead of failing.
func TestSweep_ConcurrentSweeps(t *testing.T) {
	t.Parallel()

	v, _ := newVFS(t)
	bk := newBucket(t, v)
	stop := make(chan struct{})

	var wg sync.WaitGroup

	wg.Go(func() { churnLongKeys(t, bk, stop) })

	for range 3 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}

				_, err := v.Sweep(t.Context(), 1000, func(vfs.Skipped) {})
				if err != nil {
					t.Errorf("unexpected error: %v", err)

					return
				}
			}
		})
	}

	time.Sleep(2 * time.Second)
	close(stop)
	wg.Wait()
}
