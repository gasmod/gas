package vfs_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage/dir/internal/vfs"
)

// Open of a fresh root creates the directories of the package and the
// layout marker; Open again accepts it.
func TestOpen_Fresh(t *testing.T) {
	t.Parallel()

	_, dir := newVFS(t)

	for _, d := range []string{objectsPath, locksPath, bucketsPath, keysPath} {
		assert.DirExists(t, filepath.Join(dir, d))
	}

	b := readFile(t, filepath.Join(dir, layoutPath))
	assert.Equal(t, "2\n", b)

	openVFS(t, dir)
}

// A root that holds a bucket directory but no layout marker is an earlier
// layout, and a marker with other contents is refused too.
func TestOpen_OtherLayouts(t *testing.T) {
	t.Parallel()

	old := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(old, hash("b")), 0o750))

	other := t.TempDir()
	writeFile(t, filepath.Join(other, layoutPath), "1\n")

	for dir, want := range map[string]error{old: vfs.ErrOldLayout, other: nil} {
		root, err := os.OpenRoot(dir)
		require.NoError(t, err)

		_, err = vfs.Open(root)
		require.Error(t, err)

		if want != nil {
			require.ErrorIs(t, err, want)
		}

		requireNoPath(t, err, dir)
		require.NoError(t, root.Close())
	}
}

// Open fails when the root is closed or the marker cannot be read or
// written.
func TestOpen_Failures(t *testing.T) {
	t.Parallel()

	closed, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, closed.Close())

	_, err = vfs.Open(closed)
	require.Error(t, err)

	marker := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(marker, layoutPath), 0o750))

	root, err := os.OpenRoot(marker)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	_, err = vfs.Open(root)
	requireNoPath(t, err, marker)

	gas := t.TempDir()
	writeFile(t, filepath.Join(gas, ".gas"), "")

	groot, err := os.OpenRoot(gas)
	require.NoError(t, err)

	defer func() { require.NoError(t, groot.Close()) }()

	_, err = vfs.Open(groot)
	requireNoPath(t, err, gas)
}

// Open fails without writing the marker when its directory is read only.
func TestOpen_MarkerUnwritable(t *testing.T) {
	t.Parallel()
	skipIfModesIgnored(t)

	dir := t.TempDir()
	for _, d := range []string{objectsPath, locksPath, bucketsPath, keysPath} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, d), 0o750))
	}

	chmod(t, filepath.Join(dir, ".gas"), 0o500)

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)

	defer func() { require.NoError(t, root.Close()) }()

	_, err = vfs.Open(root)
	require.ErrorIs(t, err, os.ErrPermission)
	requireNoPath(t, err, dir)
}

// Check fails once the layout marker is gone.
func TestVFS_Check(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)

	require.NoError(t, v.Check())
	require.NoError(t, os.Remove(filepath.Join(dir, layoutPath)))

	err := v.Check()
	require.ErrorIs(t, err, os.ErrNotExist)
	requireNoPath(t, err, dir)
}

// CreateBucket makes the bucket's directories and name record, again is
// not an error, and completes a bucket whose directories were removed.
func TestCreateBucket(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	ctx := t.Context()

	require.NoError(t, v.CreateBucket(ctx, testBucket))
	require.NoError(t, v.CreateBucket(ctx, testBucket))
	require.NoError(t, v.HeadBucket(testBucket))
	require.NoError(t, os.Remove(keysDir(dir)))
	require.NoError(t, v.CreateBucket(ctx, testBucket))
	assert.DirExists(t, keysDir(dir))

	b := readFile(t, filepath.Join(dir, bucketsPath, hash(testBucket)))
	assert.Equal(t, testBucket, b)
}

// CreateBucket rewrites a name record that holds another name, keeps one
// that holds the name, and refuses one that is not a regular file.
func TestCreateBucket_NameRecord(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	record := filepath.Join(dir, bucketsPath, hash(testBucket))

	writeFile(t, record, "other")
	require.NoError(t, v.CreateBucket(t.Context(), testBucket))

	b := readFile(t, record)
	assert.Equal(t, testBucket, b)

	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(record, old, old))
	require.NoError(t, v.CreateBucket(t.Context(), testBucket))

	fi, err := os.Stat(record)
	require.NoError(t, err)
	assert.WithinDuration(t, old, fi.ModTime(), time.Second)

	other := filepath.Join(dir, bucketsPath, hash("other"))
	require.NoError(t, os.Mkdir(other, 0o750))

	err = v.CreateBucket(t.Context(), "other")
	require.Error(t, err)
	requireNoPath(t, err, dir)
}

// CreateBucket fails when a file is where the metadata directory goes,
// and HeadBucket says it is not a directory.
func TestCreateBucket_FileInPlace(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	writeFile(t, filepath.Join(dir, hash(testBucket)), "")

	err := v.CreateBucket(t.Context(), testBucket)
	requireNoPath(t, err, dir)

	err = v.HeadBucket(testBucket)
	requireNoPath(t, err, dir)

	_, err = v.OpenBucket(testBucket)
	requireNoPath(t, err, dir)
	require.NotErrorIs(t, err, vfs.ErrBucketNotFound)
}

// CreateBucket fails when its directories cannot be made, or its lifecycle
// lock cannot be taken before the context ends.
func TestCreateBucket_Failures(t *testing.T) {
	t.Parallel()
	skipIfModesIgnored(t)

	for _, sub := range []string{objectsPath, locksPath, keysPath, bucketsPath, "."} {
		t.Run(sub, func(t *testing.T) {
			t.Parallel()

			v, dir := newVFS(t)
			if sub != locksPath {
				chmod(t, filepath.Join(dir, sub), 0o500)
			} else {
				// The lifecycle lock is taken first: let it be, and fail the
				// bucket's own locks directory.
				writeFile(t, filepath.Join(dir, locksPath, hash(testBucket)), "")
			}

			err := v.CreateBucket(t.Context(), testBucket)
			requireNoPath(t, err, dir)
		})
	}

	v, dir := newVFS(t)
	release := newLock(t, filepath.Join(dir, locksPath))
	require.NoError(t, release.Lock(t.Context(), hash(testBucket)+".bucket"))

	ctx, cancel := ctxCanceled(t)
	cancel()
	require.ErrorIs(t, v.CreateBucket(ctx, testBucket), ctx.Err())
}

// HeadBucket of a missing bucket is ErrBucketNotFound; OpenBucket of one
// whose directories are gone in part is too.
func TestBucket_Missing(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)

	require.ErrorIs(t, v.HeadBucket(testBucket), vfs.ErrBucketNotFound)

	_, err := v.OpenBucket(testBucket)
	require.ErrorIs(t, err, vfs.ErrBucketNotFound)

	for _, d := range []string{objDir(dir), lockDir(dir), keysDir(dir)} {
		require.NoError(t, v.CreateBucket(t.Context(), testBucket))
		require.NoError(t, os.Remove(d))

		_, err = v.OpenBucket(testBucket)
		require.ErrorIs(t, err, vfs.ErrBucketNotFound)
		requireNoPath(t, err, dir)
	}
}

// Removed reports a bucket deleted under an open Bucket, and one created
// again since, but not one that is still there.
func TestVFS_Removed(t *testing.T) {
	t.Parallel()

	v, _ := newVFS(t)
	bk := newBucket(t, v)

	assert.False(t, v.Removed(bk))
	require.NoError(t, v.DeleteBucket(t.Context(), testBucket, func(vfs.Skipped) {}))
	assert.True(t, v.Removed(bk))
	require.NoError(t, v.CreateBucket(t.Context(), testBucket))
	assert.True(t, v.Removed(bk))
}

// ListBuckets returns the named buckets with their creation time, in UTC
// to the second, and passes over a bucket whose name record is missing or
// holds another name, reporting it; a record that is not a file fails.
func TestListBuckets(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	ctx := t.Context()

	for _, n := range []string{"a-bucket", "b-bucket", "c-bucket"} {
		require.NoError(t, v.CreateBucket(ctx, n))
	}

	require.NoError(t, os.Remove(filepath.Join(dir, bucketsPath, hash("b-bucket"))))
	writeFile(t, filepath.Join(dir, bucketsPath, hash("c-bucket")), "other")
	writeFile(t, filepath.Join(dir, "not-a-bucket"), "")

	got, skipped, err := v.ListBuckets(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "a-bucket", got[0].Name)
	assert.Equal(t, time.UTC, got[0].Created.Location())
	assert.Equal(t, got[0].Created, got[0].Created.Truncate(time.Second))
	require.Len(t, skipped, 2)

	for _, s := range skipped {
		require.ErrorIs(t, s.Err, vfs.ErrUnnamed)
	}

	require.NoError(t, os.Remove(filepath.Join(dir, bucketsPath, hash("c-bucket"))))
	require.NoError(t, os.Mkdir(filepath.Join(dir, bucketsPath, hash("c-bucket")), 0o750))

	_, _, err = v.ListBuckets(ctx)
	requireNoPath(t, err, dir)

	ended, cancel := ctxCanceled(t)
	cancel()

	_, _, err = v.ListBuckets(ended)
	require.ErrorIs(t, err, ended.Err())
}

// ListBuckets fails when a name record cannot be read.
func TestListBuckets_Unreadable(t *testing.T) {
	t.Parallel()
	skipIfModesIgnored(t)

	v, dir := newVFS(t)
	require.NoError(t, v.CreateBucket(t.Context(), testBucket))
	chmod(t, filepath.Join(dir, bucketsPath, hash(testBucket)), 0o000)

	_, _, err := v.ListBuckets(t.Context())
	require.ErrorIs(t, err, os.ErrPermission)
	requireNoPath(t, err, dir)

	chmod(t, filepath.Join(dir, bucketsPath), 0o000)

	_, _, err = v.ListBuckets(t.Context())
	requireNoPath(t, err, dir)
}

// A BodyError reads and unwraps as its reader's error; a JoinedError
// reads its errors `; ` apart and unwraps to each.
func TestErrors_Types(t *testing.T) {
	t.Parallel()

	be := &vfs.BodyError{Err: io.ErrUnexpectedEOF}
	assert.Equal(t, io.ErrUnexpectedEOF.Error(), be.Error())
	require.ErrorIs(t, be, io.ErrUnexpectedEOF)

	je := &vfs.JoinedError{Errs: []error{io.EOF, os.ErrClosed}}
	assert.Equal(t, "EOF; file already closed", je.Error())
	require.ErrorIs(t, je, io.EOF)
	require.ErrorIs(t, je, os.ErrClosed)
}

// IsMissing accepts os.ErrNotExist and refuses other errors.
func TestIsMissing(t *testing.T) {
	t.Parallel()

	_, err := os.Stat(filepath.Join(t.TempDir(), "missing"))
	assert.True(t, vfs.IsMissing(err))
	assert.False(t, vfs.IsMissing(os.ErrPermission))
	assert.False(t, vfs.IsMissing(nil))
}
