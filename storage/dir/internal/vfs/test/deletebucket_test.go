package vfs_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage/dir/internal/vfs"
)

// DeleteBucket of an empty bucket removes its directories and name
// record; of a missing one it is ErrBucketNotFound.
func TestDeleteBucket_Empty(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, strings.Repeat("k", 300), "v")
	require.NoError(t, bk.DeleteObject(t.Context(), strings.Repeat("k", 300)))

	report, got := collect()
	require.NoError(t, v.DeleteBucket(t.Context(), testBucket, report))
	assert.Empty(t, *got)

	for _, p := range []string{
		filepath.Join(dir, hash(testBucket)),
		objDir(dir),
		lockDir(dir),
		keysDir(dir),
		filepath.Join(dir, bucketsPath, hash(testBucket)),
	} {
		assert.False(t, exists(t, p), p)
	}

	require.ErrorIs(t, v.DeleteBucket(t.Context(), testBucket, report), vfs.ErrBucketNotFound)
}

// DeleteBucket of a bucket that holds an object is ErrBucketNotEmpty and
// removes nothing, debris included.
func TestDeleteBucket_NotEmpty(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)
	debris := metaPath(dir, testBucket, "x") + testUUID

	writeFile(t, debris, "{}")
	put(t, bk, "k", "v")

	err := v.DeleteBucket(t.Context(), testBucket, func(vfs.Skipped) {})
	require.ErrorIs(t, err, vfs.ErrBucketNotEmpty)
	assert.FileExists(t, debris)
	assert.Equal(t, "v", read(t, bk, "k"))
}

// DeleteBucket removes what no read can use with the bucket, and reports
// each piece: corrupt metadata with its data file, metadata whose data
// file is gone, and temp metadata files.
func TestDeleteBucket_Debris(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "gone", "v")
	require.NoError(t, os.Remove(dataFiles(t, dir, "gone")[0]))
	writeFile(t, metaPath(dir, testBucket, "corrupt"), "{")
	writeFile(t, filepath.Join(objDir(dir), hash("corrupt")+"_"+testUUID), "x")
	writeFile(t, metaPath(dir, testBucket, "temp")+testUUID, "{}")

	report, got := collect()
	require.NoError(t, v.DeleteBucket(t.Context(), testBucket, report))
	assert.False(t, exists(t, filepath.Join(dir, hash(testBucket))))
	require.Len(t, *got, 3)

	for _, s := range *got {
		require.Error(t, s.Err)
		assert.True(t, strings.HasPrefix(s.Path, hash(testBucket)), s.Path)
	}
}

// A file in the metadata directory that the package did not write stops
// DeleteBucket, which leaves it; so does one in the objects directory,
// which is left and reported.
func TestDeleteBucket_ForeignFile(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	newBucket(t, v)

	foreign := filepath.Join(dir, hash(testBucket), "notes.txt")
	writeFile(t, foreign, "x")

	err := v.DeleteBucket(t.Context(), testBucket, func(vfs.Skipped) {})
	require.ErrorIs(t, err, vfs.ErrForeign)
	requireNoPath(t, err, dir)
	assert.FileExists(t, foreign)

	require.NoError(t, os.Remove(foreign))

	other := filepath.Join(objDir(dir), "notes.txt")
	writeFile(t, other, "x")

	report, got := collect()
	require.NoError(t, v.DeleteBucket(t.Context(), testBucket, report))
	assert.FileExists(t, other)
	require.NotEmpty(t, *got)
	require.ErrorIs(t, (*got)[0].Err, vfs.ErrForeign)
}

// DeleteBucket waits for a write in progress of a key the bucket holds;
// once the write commits, the bucket is not empty and nothing is reported.
func TestDeleteBucket_InFlightWrite(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	newBucket(t, v)

	release := holdKey(t, dir, "k")
	data := hash("k") + "_" + testUUID
	writeFile(t, filepath.Join(objDir(dir), data), "body")

	meta, err := json.Marshal(
		map[string]string{"key": "k", "etag": md5Hex("body"), "dataFile": data},
	)
	require.NoError(t, err)

	tmp := metaPath(dir, testBucket, "k") + "0f1e2d3c-4b5a-4968-8778-695a4b3c2d1e"
	writeFile(t, tmp, string(meta))

	report, got := collect()
	done := make(chan error, 1)

	go func() { done <- v.DeleteBucket(t.Context(), testBucket, report) }()

	select {
	case err = <-done:
		require.FailNow(t, "DeleteBucket returned while a writer held the key", "%v", err)
	case <-time.After(200 * time.Millisecond):
	}

	require.NoError(t, os.Rename(tmp, metaPath(dir, testBucket, "k")))
	release()
	require.ErrorIs(t, <-done, vfs.ErrBucketNotEmpty)
	assert.Empty(t, *got)
	assert.Equal(t, "body", read(t, openBucket(t, v), "k"))
}

// DeleteBucket leaves a lock file another holder has locked, and the data
// file of a write of a new key in progress, and reports both; the bucket
// is deleted.
func TestDeleteBucket_KeepsWriteInProgress(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	newBucket(t, v)

	release := holdKey(t, dir, "k")
	defer release()

	data := filepath.Join(objDir(dir), hash("k")+"_"+testUUID)
	writeFile(t, data, "partial")

	report, got := collect()
	require.NoError(t, v.DeleteBucket(t.Context(), testBucket, report))
	assert.FileExists(t, data)
	assert.FileExists(t, filepath.Join(lockDir(dir), hash("k")))
	// The data file and the lock file, and their directories.
	assert.Len(t, *got, 4)
	require.ErrorIs(t, v.HeadBucket(testBucket), vfs.ErrBucketNotFound)
}

// A bucket created again after a delete gets a new name record, and so a
// new creation time.
func TestDeleteBucket_ThenCreate(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	require.NoError(t, v.CreateBucket(t.Context(), testBucket))

	record := filepath.Join(dir, bucketsPath, hash(testBucket))
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(record, old, old))
	require.NoError(t, v.DeleteBucket(t.Context(), testBucket, func(vfs.Skipped) {}))
	require.NoError(t, v.CreateBucket(t.Context(), testBucket))

	got, _, err := v.ListBuckets(t.Context())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.True(t, got[0].Created.After(old.Add(time.Minute)))
}

// DeleteBucket waits for a CreateBucket of the same name, and gives up
// when its context ends.
func TestDeleteBucket_LifecycleLock(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	require.NoError(t, v.CreateBucket(t.Context(), testBucket))

	l := newLock(t, filepath.Join(dir, locksPath))
	require.NoError(t, l.Lock(t.Context(), hash(testBucket)+".bucket"))

	ctx, cancel := ctxCanceled(t)
	done := make(chan error, 1)

	go func() { done <- v.DeleteBucket(ctx, testBucket, func(vfs.Skipped) {}) }()

	time.Sleep(50 * time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, ctx.Err())
	require.NoError(t, l.Unlock(hash(testBucket)+".bucket"))
	require.NoError(t, v.HeadBucket(testBucket))
}

// DeleteBucket fails when the metadata directory cannot be read or
// removed, or an entry cannot be told; and reports what it cannot clean
// up after the commit.
func TestDeleteBucket_Failures(t *testing.T) {
	t.Parallel()
	skipIfModesIgnored(t)

	v, dir := newVFS(t)
	newBucket(t, v)
	chmod(t, filepath.Join(dir, hash(testBucket)), 0o000)

	err := v.DeleteBucket(t.Context(), testBucket, func(vfs.Skipped) {})
	require.ErrorIs(t, err, os.ErrPermission)
	requireNoPath(t, err, dir)

	v, dir = newVFS(t)

	bk := newBucket(t, v)

	put(t, bk, "k", "v")
	chmod(t, objDir(dir), 0o000)

	err = v.DeleteBucket(t.Context(), testBucket, func(vfs.Skipped) {})
	require.ErrorIs(t, err, os.ErrPermission)

	v, dir = newVFS(t)
	newBucket(t, v)
	chmod(t, dir, 0o500)

	err = v.DeleteBucket(t.Context(), testBucket, func(vfs.Skipped) {})
	require.ErrorIs(t, err, os.ErrPermission)
	requireNoPath(t, err, dir)

	v, dir = newVFS(t)
	newBucket(t, v)
	writeFile(t, filepath.Join(keysDir(dir), "x.d", "y"), "")
	chmod(t, filepath.Join(keysDir(dir), "x.d"), 0o500)

	report, got := collect()
	require.NoError(t, v.DeleteBucket(t.Context(), testBucket, report))
	assert.NotEmpty(t, *got)

	for _, s := range *got {
		requireNoPath(t, s.Err, dir)
	}
}

// DeleteBucket fails when the metadata of an entry cannot be read, and
// removes nothing.
func TestDeleteBucket_UnreadableEntry(t *testing.T) {
	t.Parallel()
	skipIfModesIgnored(t)

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "k", "v")
	chmod(t, metaPath(dir, testBucket, "k"), 0o000)

	err := v.DeleteBucket(t.Context(), testBucket, func(vfs.Skipped) {})
	require.ErrorIs(t, err, os.ErrPermission)
	assert.FileExists(t, metaPath(dir, testBucket, "k"))
}
