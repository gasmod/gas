package vfs_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage/dir/internal/vfs"
)

// A metadata file far larger than any the package writes is ErrCorrupt,
// read no further than the bound on what one holds.
//
// Not parallel: it measures what the process allocates.
func TestOpenObject_HugeMetadata(t *testing.T) {
	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, "k", "v")
	require.NoError(t, os.Truncate(metaPath(dir, testBucket, "k"), 256<<20))

	var before, after runtime.MemStats

	runtime.ReadMemStats(&before)

	_, err := bk.OpenObject(t.Context(), "k")

	runtime.ReadMemStats(&after)

	require.ErrorIs(t, err, vfs.ErrCorrupt)
	requireNoPath(t, err, dir)
	assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(16<<20))
}

// A metadata file or a data file that is a symlink, even to a copy of
// the right file inside the root, is not followed: the object is
// ErrCorrupt.
func TestOpenObject_Symlinks(t *testing.T) {
	t.Parallel()

	for name, target := range map[string]func(dir string) string{
		"meta": func(dir string) string { return metaPath(dir, testBucket, "k") },
		"data": func(dir string) string { return dataFiles(t, dir, "k")[0] },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, dir := newVFS(t)
			bk := newBucket(t, v)

			put(t, bk, "k", "v")

			path := target(dir)
			cp := path + ".copy"
			require.NoError(t, os.Rename(path, cp))
			symlink(t, filepath.Base(cp), path)

			_, err := bk.OpenObject(t.Context(), "k")
			require.ErrorIs(t, err, vfs.ErrCorrupt)
			requireNoPath(t, err, dir)
		})
	}
}

// A name record far larger than a bucket name, or one that holds a name
// with the bucket's hash that breaks the bucket rules, is ErrCorrupt: the
// bucket is left out, and every other bucket is listed.
func TestListBuckets_CorruptNameRecord(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)

	require.NoError(t, v.CreateBucket(t.Context(), testBucket))
	require.NoError(t, v.CreateBucket(t.Context(), "big-bucket"))
	require.NoError(t, os.Truncate(filepath.Join(dir, bucketsPath, hash("big-bucket")), 1<<20))

	bad := "Bad_Name"
	require.NoError(t, os.Mkdir(filepath.Join(dir, hash(bad)), 0o750))
	writeFile(t, filepath.Join(dir, bucketsPath, hash(bad)), bad)

	got, skipped, err := v.ListBuckets(t.Context())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, testBucket, got[0].Name)
	require.Len(t, skipped, 2)

	for _, s := range skipped {
		require.ErrorIs(t, s.Err, vfs.ErrCorrupt)
		assert.NotContains(t, s.Err.Error(), bad)
		assert.False(t, strings.HasPrefix(s.Path, dir))
	}
}
