package vfs_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage/dir/internal/vfs"
)

// indexKeys are keys around the 120-byte elements of the key index: of
// 119, 120, 121, 240, 241 and 1024 bytes sharing prefixes, a key and a
// longer one with the same first 120 bytes, and bytes below and above the
// ones they share: a space, the lowest byte a key may hold, and the
// highest code point, whose UTF-8 ends the first element.
func indexKeys() []string {
	a := func(n int) string { return strings.Repeat("a", n) }

	return []string{
		a(1), a(119), a(120), a(121), a(240), a(241), a(1024),
		a(119) + "b", a(120) + " ", a(120) + "b", a(116) + "\U0010FFFF",
		a(240) + "0", "b", "a/b", "a/c/d", a(130) + "/x",
	}
}

// ListKeys yields every key in byte order, across the elements of the
// index.
func TestListKeys_Order(t *testing.T) {
	t.Parallel()

	v, _ := newVFS(t)
	bk := newBucket(t, v)
	keys := indexKeys()

	for _, k := range keys {
		put(t, bk, k, "v")
	}

	assert.Equal(t, slices.Sorted(slices.Values(keys)), listKeys(t, bk, vfs.Bounds{}))
}

// ListKeys yields the keys that start with Prefix and come after After,
// as a filter of every key would.
func TestListKeys_Bounds(t *testing.T) {
	t.Parallel()

	v, _ := newVFS(t)
	bk := newBucket(t, v)
	keys := indexKeys()

	for _, k := range keys {
		put(t, bk, k, "v")
	}

	sorted := slices.Sorted(slices.Values(keys))
	a := strings.Repeat("a", 120)

	for _, b := range []vfs.Bounds{
		{Prefix: "a"},
		{Prefix: "b"},
		{Prefix: "c"},
		{Prefix: a},
		{Prefix: a + "a"},
		{Prefix: strings.Repeat("a", 125)},
		{Prefix: "a/"},
		{After: "a/b"},
		{After: a},
		{After: a + "a"},
		{After: strings.Repeat("a", 500)},
		{After: "a"},
		{After: "zz"},
		{Prefix: "a", After: a + "b"},
		{Prefix: a, After: "b"},
		{Prefix: "a", After: "0"},
		{After: strings.Repeat("a", 119) + "c"},
	} {
		var want []string

		for _, k := range sorted {
			if strings.HasPrefix(k, b.Prefix) && k > b.After {
				want = append(want, k)
			}
		}

		assert.Equal(t, want, listKeys(t, bk, b), "%+v", b)
	}
}

// ListKeys stops when yield returns false, and passes over the keys pass
// tells it to.
func TestListKeys_StopAndPass(t *testing.T) {
	t.Parallel()

	v, _ := newVFS(t)
	bk := newBucket(t, v)

	for _, k := range []string{"a", "b", "c", "d"} {
		put(t, bk, k, "v")
	}

	var got []string

	err := bk.ListKeys(t.Context(), vfs.Bounds{}, func(k string) bool { return k == "b" },
		func(k string) bool {
			got = append(got, k)

			return len(got) < 2
		}, func(vfs.Skipped) {})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "c"}, got)
}

// ListKeys passes over an entry whose object is gone, and reports an entry
// of the index the package did not write.
func TestListKeys_StaleAndForeign(t *testing.T) {
	t.Parallel()

	v, dir := newVFS(t)
	bk := newBucket(t, v)
	keys := keysDir(dir)

	put(t, bk, "k", "v")
	writeFile(t, filepath.Join(keys, hex.EncodeToString([]byte("stale"))+".k"), "")
	writeFile(t, filepath.Join(keys, "abc.k"), "")
	writeFile(t, filepath.Join(keys, "zz.k"), "")
	writeFile(t, filepath.Join(keys, "616263.d", "x"), "")
	writeFile(t, filepath.Join(keys, ".k"), "")

	report, got := collect()

	var listed []string

	err := bk.ListKeys(t.Context(), vfs.Bounds{}, func(string) bool { return false },
		func(k string) bool {
			listed = append(listed, k)

			return true
		}, report)
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, listed)
	require.Len(t, *got, 4)

	for _, s := range *got {
		require.ErrorIs(t, s.Err, vfs.ErrForeign)
		assert.True(t, strings.HasPrefix(s.Path, filepath.Join(keysPath, hash(testBucket))), s.Path)
	}
}

// ListKeys fails with the context's error when it has ended, and when a
// directory of the index or the metadata directory cannot be read.
func TestListKeys_Failures(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)

	v, dir := newVFS(t)
	bk := newBucket(t, v)

	put(t, bk, strings.Repeat("a", 200), "v")

	ctx, cancel := ctxCanceled(t)
	cancel()

	err := bk.ListKeys(ctx, vfs.Bounds{}, func(string) bool { return false },
		func(string) bool { return true }, func(vfs.Skipped) {})
	require.ErrorIs(t, err, ctx.Err())

	chmod(t, filepath.Join(dir, hash(testBucket)), 0o000)

	err = bk.ListKeys(t.Context(), vfs.Bounds{}, func(string) bool { return false },
		func(string) bool { return true }, func(vfs.Skipped) {})
	require.ErrorIs(t, err, os.ErrPermission)
	requireNoPath(t, err, dir)

	sub, err := filepath.Glob(filepath.Join(keysDir(dir), "*.d"))
	require.NoError(t, err)
	require.Len(t, sub, 1)
	chmod(t, sub[0], 0o000)

	err = bk.ListKeys(t.Context(), vfs.Bounds{}, func(string) bool { return false },
		func(string) bool { return true }, func(vfs.Skipped) {})
	require.ErrorIs(t, err, os.ErrPermission)
	requireNoPath(t, err, dir)
}

// An index entry whose key breaks the key rules is passed over, and
// reported as ErrCorrupt.
func TestListKeys_CorruptEntry(t *testing.T) {
	t.Parallel()

	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })

	v, err := vfs.Open(root)
	require.NoError(t, err)
	require.NoError(t, v.CreateBucket(t.Context(), testBucket))

	bk := openBucket(t, v)
	put(t, bk, "k", "v")

	bad := "a\x01b"
	plantKey(t, root, filepath.Join(keysPath, hash(testBucket)), bad)
	require.NoError(
		t,
		root.WriteFile(filepath.Join(hash(testBucket), hash(bad)+metaExt), nil, 0o600),
	)

	var keys []string

	report, got := collect()

	err = bk.ListKeys(t.Context(), vfs.Bounds{}, func(string) bool { return false },
		func(k string) bool {
			keys = append(keys, k)

			return true
		}, report)
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, keys)
	require.Len(t, *got, 1)
	require.ErrorIs(t, (*got)[0].Err, vfs.ErrCorrupt)
}
