package vfs_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage/dir/internal/vfs"
)

// The tests of this file fail file operations through vfs.Fault, to reach
// the failure paths a real file system does not take on demand. They set
// a global hook, so none is parallel; each rule matches paths under its
// own temp dir.

// errFault is the error the tests inject.
var errFault = errors.New("injected fault")

// faultRule fails an operation: the nth one (from 1) of kind op whose path
// match accepts, or every one when nth is 0. It fails with err, after it
// runs do when set.
type faultRule struct {
	match func(path string) bool
	do    func()
	err   error
	op    vfs.Op
	nth   int32
	seen  atomic.Int32
	fired atomic.Int32
}

// at matches path itself.
func at(path string) func(string) bool {
	return func(p string) bool { return filepath.Clean(p) == filepath.Clean(path) }
}

// under matches the paths below dir.
func under(dir string) func(string) bool {
	return func(p string) bool {
		return strings.HasPrefix(filepath.Clean(p), filepath.Clean(dir)+string(filepath.Separator))
	}
}

// inject sets vfs.Fault to apply rules, the first that fails an operation
// winning, until t ends or the returned function is called.
func inject(t *testing.T, rules ...*faultRule) func() {
	t.Helper()

	hook := func(op vfs.Op, path string) error {
		for _, r := range rules {
			if r.op != op || !r.match(path) {
				continue
			}

			if n := r.seen.Add(1); r.nth != 0 && n != r.nth {
				continue
			}

			r.fired.Add(1)

			if r.do != nil {
				r.do()
			}

			return r.err
		}

		return nil
	}

	vfs.Fault.Store(&hook)

	stop := func() { vfs.Fault.Store(nil) }
	t.Cleanup(stop)

	return stop
}

// fail returns a rule that fails the nth operation op at a path match
// accepts with errFault.
func fail(op vfs.Op, match func(string) bool, nth int32) *faultRule {
	return &faultRule{op: op, match: match, nth: nth, err: errFault}
}

// requireFired fails t unless each rule failed exactly one operation.
func requireFired(t *testing.T, rules ...*faultRule) {
	t.Helper()

	for _, r := range rules {
		require.Equal(
			t,
			int32(1),
			r.fired.Load(),
			"rule %s failed %d operations",
			r.op,
			r.fired.Load(),
		)
	}
}

// requireFault fails t unless err wraps errFault, names no path and the
// rules each failed one operation.
func requireFault(t *testing.T, err error, dir string, rules ...*faultRule) {
	t.Helper()

	require.ErrorIs(t, err, errFault)
	requireNoPath(t, err, dir)
	requireFired(t, rules...)
}

// skipOnWindows skips t on Windows, which syncs no directory and takes no
// symlink without privileges.
func skipOnWindows(t *testing.T, why string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip(why)
	}
}

// entries returns the names in dir.
func entries(t *testing.T, dir string) []string {
	t.Helper()

	des, err := os.ReadDir(dir)
	require.NoError(t, err)

	names := make([]string, 0, len(des))
	for _, de := range des {
		names = append(names, de.Name())
	}

	return names
}

// metaDir returns the metadata directory of testBucket, under dir.
func metaDir(dir string) string { return filepath.Join(dir, hash(testBucket)) }

// recordPath returns the name record of testBucket, under dir.
func recordPath(dir string) string { return filepath.Join(dir, bucketsPath, hash(testBucket)) }

// indexEntries returns the regular files of the key index of testBucket,
// under dir.
func indexEntries(t *testing.T, dir string) []string {
	t.Helper()

	var files []string

	err := filepath.WalkDir(keysDir(dir), func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			files = append(files, p)
		}

		return err
	})
	require.NoError(t, err)

	return files
}

// staleIndexEntry writes key to bk and removes its metadata and data
// files, leaving its index entry, as a crash between the steps of a
// delete does.
func staleIndexEntry(t *testing.T, bk *vfs.Bucket, dir, key string) {
	t.Helper()

	put(t, bk, key, "v")

	require.NoError(t, os.Remove(metaPath(dir, testBucket, key)))

	for _, f := range dataFiles(t, dir, key) {
		require.NoError(t, os.Remove(f))
	}
}

// A failed write, sync, close or rename of a bucket's name record fails
// CreateBucket, leaves no temp file, and creates no bucket.
func TestFault_CreateBucket_RecordFails(t *testing.T) {
	for _, op := range []vfs.Op{vfs.OpWrite, vfs.OpSync, vfs.OpClose, vfs.OpRename} {
		t.Run(string(op), func(t *testing.T) {
			v, dir := newVFS(t)
			rule := fail(op, under(filepath.Join(dir, bucketsPath)), 1)
			stop := inject(t, rule)

			err := v.CreateBucket(t.Context(), testBucket)
			requireFault(t, err, dir, rule)
			stop()

			assert.Empty(t, entries(t, filepath.Join(dir, bucketsPath)))
			require.ErrorIs(t, v.HeadBucket(testBucket), vfs.ErrBucketNotFound)
		})
	}
}

// A failed sync of the buckets directory fails CreateBucket.
func TestFault_CreateBucket_DirSyncFails(t *testing.T) {
	skipOnWindows(t, "Windows syncs no directory")

	v, dir := newVFS(t)
	rule := fail(vfs.OpSync, at(filepath.Join(dir, bucketsPath)), 1)
	inject(t, rule)

	err := v.CreateBucket(t.Context(), testBucket)
	requireFault(t, err, dir, rule)
}

// A name record longer than a read of it takes holds something else, so
// CreateBucket replaces it.
func TestCreateBucket_ReplacesOversizedRecord(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	writeFile(t, recordPath(dir), strings.Repeat("x", 64<<10+1))

	require.NoError(t, v.CreateBucket(t.Context(), testBucket))
	assert.Equal(t, testBucket, readFile(t, recordPath(dir)))
}

// A failed stat or rename of the layout marker fails Open; a failed rename
// leaves no temp file.
func TestFault_Open_LayoutFails(t *testing.T) {
	for _, op := range []vfs.Op{vfs.OpLstat, vfs.OpRename} {
		t.Run(string(op), func(t *testing.T) {
			dir := t.TempDir()
			layout := filepath.Join(dir, layoutPath)

			rule := fail(
				op,
				func(p string) bool { return strings.HasPrefix(filepath.Clean(p), layout) },
				1,
			)
			stop := inject(t, rule)

			root, err := os.OpenRoot(dir)
			require.NoError(t, err)
			t.Cleanup(func() { _ = root.Close() })

			_, err = vfs.Open(root)
			requireFault(t, err, dir, rule)
			stop()

			matches, err := filepath.Glob(layout + "*")
			require.NoError(t, err)
			assert.Empty(t, matches)
		})
	}
}

// A layout marker that is a symlink, though its target holds the layout,
// is not a regular file, and Open refuses it.
func TestOpen_LayoutMarkerIsSymlink(t *testing.T) {
	skipOnWindows(t, "Windows takes no symlink without privileges")
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".gas", "target"), "2\n")
	require.NoError(t, os.Symlink("target", filepath.Join(dir, layoutPath)))

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	_, err = vfs.Open(root)
	require.ErrorContains(t, err, "layout marker: not a regular file")
}

// A file in place of a bucket's objects directory, where the open reports
// a missing path as Windows does, is not a directory.
func TestFault_OpenBucket_DirIsFile(t *testing.T) {
	v, dir := newVFS(t)
	require.NoError(t, v.CreateBucket(t.Context(), testBucket))
	require.NoError(t, os.RemoveAll(objDir(dir)))
	writeFile(t, objDir(dir), "x")

	rule := &faultRule{op: vfs.OpOpenRoot, match: at(objDir(dir)), nth: 1, err: os.ErrNotExist}
	inject(t, rule)

	_, err := v.OpenBucket(testBucket)
	require.ErrorContains(t, err, "open objects dir: not a directory")
	requireFired(t, rule)
}

// A failed close of a bucket's directory fails Bucket.Close.
func TestFault_BucketClose(t *testing.T) {
	v, dir := newVFS(t)
	require.NoError(t, v.CreateBucket(t.Context(), testBucket))

	bk, err := v.OpenBucket(testBucket)
	require.NoError(t, err)

	rule := fail(vfs.OpClose, at(objDir(dir)), 1)
	inject(t, rule)

	requireFault(t, bk.Close(), dir, rule)
}

// A failed write or sync of a data file, or sync of the objects
// directory, fails CreateObject and leaves no data file.
func TestFault_CreateObject_DataFileFails(t *testing.T) {
	cases := map[string]func(dir string) *faultRule{
		"write":    func(dir string) *faultRule { return fail(vfs.OpWrite, under(objDir(dir)), 1) },
		"sync":     func(dir string) *faultRule { return fail(vfs.OpSync, under(objDir(dir)), 1) },
		"dir sync": func(dir string) *faultRule { return fail(vfs.OpSync, at(objDir(dir)), 1) },
	}

	for name, rule := range cases {
		t.Run(name, func(t *testing.T) {
			if name == "dir sync" {
				skipOnWindows(t, "Windows syncs no directory")
			}

			v, dir := newVFS(t)
			bk := newBucket(t, v)
			r := rule(dir)
			stop := inject(t, r)

			_, err := bk.CreateObject(t.Context(), write("k", "v"))
			requireFault(t, err, dir, r)
			stop()

			assert.Empty(t, dataFiles(t, dir, "k"))

			_, err = bk.Stat(t.Context(), "k")
			require.ErrorIs(t, err, vfs.ErrObjectNotFound)
		})
	}
}

// A failed close of a new index entry fails CreateObject, which removes
// the entry it created.
func TestFault_CreateObject_IndexEntryCloseFails(t *testing.T) {
	v, dir := newVFS(t)
	bk := newBucket(t, v)

	rule := fail(vfs.OpClose, under(keysDir(dir)), 1)
	stop := inject(t, rule)

	_, err := bk.CreateObject(t.Context(), write("k", "v"))
	requireFault(t, err, dir, rule)
	stop()

	assert.Empty(t, indexEntries(t, dir))
	assert.Empty(t, listKeys(t, bk, vfs.Bounds{}))
}

// A failed stat, read or close of a metadata file fails OpenObject.
func TestFault_OpenObject_MetaFails(t *testing.T) {
	for _, op := range []vfs.Op{vfs.OpStat, vfs.OpLstat, vfs.OpRead, vfs.OpClose} {
		t.Run(string(op), func(t *testing.T) {
			v, dir := newVFS(t)
			bk := newBucket(t, v)
			put(t, bk, "k", "v")

			rule := fail(op, at(metaPath(dir, testBucket, "k")), 1)
			stop := inject(t, rule)

			obj, err := bk.OpenObject(t.Context(), "k")
			assert.Nil(t, obj)
			requireFault(t, err, dir, rule)
			stop()

			assert.Equal(t, "v", read(t, bk, "k"))
		})
	}
}

// A failed unlock after OpenObject read the object fails it, and closes
// the data file; one after it failed to is joined with its error. The key
// is not left locked.
func TestFault_OpenObject_UnlockFails(t *testing.T) {
	for _, dataGone := range []bool{false, true} {
		name := "object read"
		if dataGone {
			name = "data file gone"
		}

		t.Run(name, func(t *testing.T) {
			v, dir := newVFS(t)
			bk := newBucket(t, v)
			put(t, bk, "k", "v")

			if dataGone {
				for _, f := range dataFiles(t, dir, "k") {
					require.NoError(t, os.Remove(f))
				}
			}

			rule := fail(vfs.OpClose, under(lockDir(dir)), 1)
			stop := inject(t, rule)

			obj, err := bk.OpenObject(t.Context(), "k")
			assert.Nil(t, obj)
			requireFault(t, err, dir, rule)

			if dataGone {
				require.ErrorIs(t, err, vfs.ErrCorrupt)
			}

			stop()

			_, err = bk.Stat(t.Context(), "k")
			if dataGone {
				require.ErrorIs(t, err, vfs.ErrCorrupt)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// A failed unlock after DeleteObject found corrupt metadata is joined with
// ErrCorrupt.
func TestFault_DeleteObject_UnlockFails(t *testing.T) {
	v, dir := newVFS(t)
	bk := newBucket(t, v)
	put(t, bk, "k", "v")
	writeFile(t, metaPath(dir, testBucket, "k"), "{")

	rule := fail(vfs.OpClose, under(lockDir(dir)), 1)
	inject(t, rule)

	err := bk.DeleteObject(t.Context(), "k")
	require.ErrorIs(t, err, vfs.ErrCorrupt)
	requireFault(t, err, dir, rule)
}

// A failed file lock, blocking or not, fails Lock and gives the name back.
func TestFault_Lock_FlockFails(t *testing.T) {
	for _, blocking := range []bool{false, true} {
		name := "try"
		if blocking {
			name = "blocking"
		}

		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			l := newLock(t, dir)

			nth := int32(1)

			if blocking {
				// The holder makes the try fail without the fault, so
				// the second flock is the blocking one.
				holder := newLock(t, dir)
				require.NoError(t, holder.Lock(t.Context(), "n"))
				t.Cleanup(func() { _ = holder.Unlock("n") })

				nth = 2
			}

			rule := fail(vfs.OpFlock, under(dir), nth)
			stop := inject(t, rule)

			err := l.Lock(t.Context(), "n")
			requireFault(t, err, dir, rule)
			stop()

			if !blocking {
				require.NoError(t, l.Lock(t.Context(), "n"))
			}
		})
	}
}

// A failed stat of the lock file just locked fails Lock.
func TestFault_Lock_StatFails(t *testing.T) {
	dir := t.TempDir()
	l := newLock(t, dir)

	rule := fail(vfs.OpStat, at(filepath.Join(dir, "n")), 1)
	stop := inject(t, rule)

	requireFault(t, l.Lock(t.Context(), "n"), dir, rule)
	stop()

	require.NoError(t, l.Lock(t.Context(), "n"))
}

// A lock file released while Lock waited is given up and Lock tries again;
// a failure to close it fails Lock.
func TestFault_Lock_StaleCloseFails(t *testing.T) {
	dir := t.TempDir()
	l := newLock(t, dir)
	file := filepath.Join(dir, "n")

	stale := &faultRule{op: vfs.OpLstat, match: at(file), nth: 1, err: os.ErrNotExist}
	closeFails := fail(vfs.OpClose, at(file), 1)
	inject(t, stale, closeFails)

	err := l.Lock(t.Context(), "n")
	require.ErrorContains(t, err, "unlock stale lock file")
	requireFault(t, err, dir, stale, closeFails)
}

// A Lock that finds its lock file released, and the lock closed, fails
// with os.ErrClosed rather than trying again.
func TestFault_Lock_ClosedWhileStale(t *testing.T) {
	dir := t.TempDir()
	l := newLock(t, dir)

	stale := &faultRule{
		op:    vfs.OpLstat,
		match: at(filepath.Join(dir, "n")),
		nth:   1,
		err:   os.ErrNotExist,
		do:    func() { _ = l.Close() },
	}
	inject(t, stale)

	require.ErrorIs(t, l.Lock(t.Context(), "n"), os.ErrClosed)
	requireFired(t, stale)
}

// Sweep leaves a temp file of a name record, or of metadata, that its
// write renamed away since Sweep read the directory.
func TestFault_Sweep_TempRenamedAway(t *testing.T) {
	cases := map[string]func(dir string) string{
		"name record": func(dir string) string { return recordPath(dir) + testUUID },
		"metadata":    func(dir string) string { return metaPath(dir, testBucket, "k") + testUUID },
	}

	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			v, dir := newVFS(t)
			newBucket(t, v)

			tmp := path(dir)
			writeFile(t, tmp, "x")

			rule := &faultRule{op: vfs.OpLstat, match: at(tmp), nth: 1, err: os.ErrNotExist}
			inject(t, rule)

			res, err := v.Sweep(t.Context(), 100, func(vfs.Skipped) {})
			require.NoError(t, err)
			requireFired(t, rule)
			assert.Zero(t, res.Removed)
			assert.True(t, exists(t, tmp))
		})
	}
}

// Sweep passes over a file in a metadata directory that it did not write.
func TestSweep_ForeignMetaEntry(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	newBucket(t, v)

	foreign := filepath.Join(metaDir(dir), "notes.txt")
	writeFile(t, foreign, "x")

	res, err := v.Sweep(t.Context(), 100, func(vfs.Skipped) {})
	require.NoError(t, err)
	assert.Zero(t, res.Removed)
	assert.True(t, exists(t, foreign))
}

// A failed read of metadata under the key's lock fails Sweep: as it makes
// a missing index entry again, and as it removes orphan data files.
func TestFault_Sweep_MetaReadFails(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"reindex": func(t *testing.T, dir string) {
			t.Helper()

			for _, f := range indexEntries(t, dir) {
				require.NoError(t, os.Remove(f))
			}
		},
		"orphans": func(t *testing.T, dir string) {
			t.Helper()

			writeFile(t, filepath.Join(objDir(dir), hash("k")+"_"+testUUID), "x")
		},
	}

	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			v, dir := newVFS(t)
			bk := newBucket(t, v)
			put(t, bk, "k", "v")
			setup(t, dir)

			// The first read is the one without the lock.
			rule := fail(vfs.OpRead, at(metaPath(dir, testBucket, "k")), 2)
			inject(t, rule)

			_, err := v.Sweep(t.Context(), 100, func(vfs.Skipped) {})
			requireFault(t, err, dir, rule)
		})
	}
}

// Sweep makes no index entry again for an object deleted between its read
// of the metadata and its read under the key's lock.
func TestFault_Sweep_DeletedBeforeReindex(t *testing.T) {
	v, dir := newVFS(t)
	bk := newBucket(t, v)
	put(t, bk, "k", "v")

	for _, f := range indexEntries(t, dir) {
		require.NoError(t, os.Remove(f))
	}

	rule := &faultRule{
		op:    vfs.OpStat,
		match: at(metaPath(dir, testBucket, "k")),
		nth:   2,
		err:   os.ErrNotExist,
	}
	inject(t, rule)

	res, err := v.Sweep(t.Context(), 100, func(vfs.Skipped) {})
	require.NoError(t, err)
	requireFired(t, rule)
	assert.Zero(t, res.Reindexed)
	assert.Empty(t, indexEntries(t, dir))
}

// A bucket whose metadata directory no longer stats is removed; one whose
// stat fails otherwise is not.
func TestFault_Removed_StatFails(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"missing": {err: os.ErrNotExist, want: true},
		"failed":  {err: errFault, want: false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			v, dir := newVFS(t)
			bk := newBucket(t, v)

			rule := &faultRule{op: vfs.OpStat, match: at(metaDir(dir)), nth: 1, err: tc.err}
			inject(t, rule)

			assert.Equal(t, tc.want, v.Removed(bk))
			requireFired(t, rule)
		})
	}
}

// A failed stat of the metadata of an indexed key fails Sweep.
func TestFault_Sweep_IndexStatFails(t *testing.T) {
	v, dir := newVFS(t)
	bk := newBucket(t, v)
	staleIndexEntry(t, bk, dir, "k")

	rule := fail(vfs.OpLstat, at(metaPath(dir, testBucket, "k")), 1)
	inject(t, rule)

	_, err := v.Sweep(t.Context(), 100, func(vfs.Skipped) {})
	requireFault(t, err, dir, rule)
}

// Sweep keeps an index entry whose metadata it cannot rule out under the
// key's lock, such as when a write made the object again meanwhile.
func TestFault_Sweep_StaleEntryNotRemoved(t *testing.T) {
	v, dir := newVFS(t)
	bk := newBucket(t, v)
	staleIndexEntry(t, bk, dir, "k")

	rule := fail(vfs.OpLstat, at(metaPath(dir, testBucket, "k")), 2)
	inject(t, rule)

	res, err := v.Sweep(t.Context(), 100, func(vfs.Skipped) {})
	require.NoError(t, err)
	requireFired(t, rule)
	assert.Zero(t, res.Removed)
	assert.Len(t, indexEntries(t, dir), 1)
}

// A failed read of a directory of the key index fails Sweep, at the top
// and below it.
func TestFault_Sweep_IndexDirReadFails(t *testing.T) {
	long := strings.Repeat("a", 121)

	cases := map[string]func(dir string) string{
		"top":   keysDir,
		"below": func(dir string) string { return filepath.Dir(indexEntries(t, dir)[0]) },
	}

	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			v, dir := newVFS(t)
			bk := newBucket(t, v)
			put(t, bk, long, "v")

			// The first read is the listing's.
			rule := fail(vfs.OpReadDir, at(path(dir)), 2)
			inject(t, rule)

			_, err := v.Sweep(t.Context(), 100, func(vfs.Skipped) {})
			requireFault(t, err, dir, rule)
		})
	}
}

// A bucket's metadata directory that reads as missing fails Sweep while
// the bucket still opens, and not once it no longer does; an error opening
// it again is joined.
func TestFault_Sweep_BucketReadsAsMissing(t *testing.T) {
	cases := map[string]struct {
		reopen  error
		wantErr bool
	}{
		"still there":   {wantErr: true},
		"gone":          {reopen: os.ErrNotExist},
		"reopen failed": {reopen: errFault, wantErr: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			v, dir := newVFS(t)
			newBucket(t, v)

			missing := &faultRule{
				op:    vfs.OpReadDir,
				match: at(metaDir(dir)),
				nth:   1,
				err:   os.ErrNotExist,
			}
			rules := []*faultRule{missing}

			if tc.reopen != nil {
				// The first open is the sweep's own.
				rules = append(
					rules,
					&faultRule{op: vfs.OpOpenRoot, match: at(metaDir(dir)), nth: 2, err: tc.reopen},
				)
			}

			inject(t, rules...)

			_, err := v.Sweep(t.Context(), 100, func(vfs.Skipped) {})
			requireFired(t, rules...)

			if !tc.wantErr {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, os.ErrNotExist)
			requireNoPath(t, err, dir)

			if errors.Is(tc.reopen, errFault) {
				require.ErrorIs(t, err, errFault)
			}
		})
	}
}

// A failed close of a bucket's directory fails DeleteBucket before it
// removes anything.
func TestFault_DeleteBucket_CloseFails(t *testing.T) {
	v, dir := newVFS(t)
	require.NoError(t, v.CreateBucket(t.Context(), testBucket))

	rule := fail(vfs.OpClose, at(objDir(dir)), 1)
	stop := inject(t, rule)

	requireFault(t, v.DeleteBucket(t.Context(), testBucket, func(vfs.Skipped) {}), dir, rule)
	stop()

	require.NoError(t, v.HeadBucket(testBucket))
}

// A failed stat of a temp metadata file fails DeleteBucket: as it
// classifies the file, and as it checks it again before removing it.
func TestFault_DeleteBucket_TempStatFails(t *testing.T) {
	for _, nth := range []int32{1, 2} {
		t.Run([]string{"", "classify", "recheck"}[nth], func(t *testing.T) {
			v, dir := newVFS(t)
			require.NoError(t, v.CreateBucket(t.Context(), testBucket))

			tmp := metaPath(dir, testBucket, "k") + testUUID
			writeFile(t, tmp, "x")

			rule := fail(vfs.OpLstat, at(tmp), nth)
			inject(t, rule)

			err := v.DeleteBucket(t.Context(), testBucket, func(vfs.Skipped) {})
			requireFault(t, err, dir, rule)
		})
	}
}

// A failure to open or close the locks directory, or to open the objects
// directory, of a deleted bucket is reported, and the bucket is deleted.
func TestFault_DeleteBucket_LeftoversFail(t *testing.T) {
	cases := map[string]func(dir string) *faultRule{
		// The first open or close of each is OpenBucket's or Close's.
		"open locks":   func(dir string) *faultRule { return fail(vfs.OpOpenRoot, at(lockDir(dir)), 2) },
		"close locks":  func(dir string) *faultRule { return fail(vfs.OpClose, at(lockDir(dir)), 2) },
		"open objects": func(dir string) *faultRule { return fail(vfs.OpOpenRoot, at(objDir(dir)), 2) },
	}

	for name, rule := range cases {
		t.Run(name, func(t *testing.T) {
			v, dir := newVFS(t)
			require.NoError(t, v.CreateBucket(t.Context(), testBucket))

			r := rule(dir)
			inject(t, r)

			report, got := collect()
			require.NoError(t, v.DeleteBucket(t.Context(), testBucket, report))
			requireFired(t, r)

			require.Len(t, *got, 1)
			require.ErrorIs(t, (*got)[0].Err, errFault)
			requireNoPath(t, (*got)[0].Err, dir)
			require.ErrorIs(t, v.HeadBucket(testBucket), vfs.ErrBucketNotFound)
		})
	}
}

// A bucket deleted while ListBuckets reads it, its name record gone before
// the stat or the read of it, is left out with no Skipped.
func TestFault_ListBuckets_DeletedWhileListed(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string) []*faultRule{
		"before stat": func(t *testing.T, dir string) []*faultRule {
			t.Helper()

			require.NoError(t, os.Remove(recordPath(dir)))

			return nil
		},
		"before read": func(_ *testing.T, dir string) []*faultRule {
			return []*faultRule{
				{op: vfs.OpStat, match: at(recordPath(dir)), nth: 1, err: os.ErrNotExist},
			}
		},
	}

	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			v, dir := newVFS(t)
			require.NoError(t, v.CreateBucket(t.Context(), testBucket))

			rules := append(setup(t, dir),
				&faultRule{op: vfs.OpLstat, match: at(metaDir(dir)), nth: 1, err: os.ErrNotExist})
			inject(t, rules...)

			got, skipped, err := v.ListBuckets(t.Context())
			require.NoError(t, err)
			requireFired(t, rules...)
			assert.Empty(t, got)
			assert.Empty(t, skipped)
		})
	}
}
