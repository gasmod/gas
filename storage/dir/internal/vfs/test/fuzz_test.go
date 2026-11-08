package vfs_test

import (
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir/internal/vfs"
)

// chunkBytes is how many key bytes an element of the key index spells.
const chunkBytes = 120

// plantKey writes the index entry of key in root, under the keys
// directory keys relative to it, as a write would, without syncing. It
// goes through the root: the path of a long key is longer than an
// absolute path may be.
func plantKey(t *testing.T, root *os.Root, keys, key string) {
	t.Helper()

	path := keys

	for len(key) > chunkBytes {
		path = filepath.Join(path, hex.EncodeToString([]byte(key[:chunkBytes]))+".d")
		key = key[chunkBytes:]
	}

	require.NoError(t, root.MkdirAll(path, 0o750))
	require.NoError(
		t,
		root.WriteFile(filepath.Join(path, hex.EncodeToString([]byte(key))+".k"), nil, 0o600),
	)
}

// FuzzIndexLayout checks the key index against a filter of its keys: for
// keys that keep the key rules, planted on disk in the layout of the
// package, ListKeys within a Prefix and an After yields the keys with
// that prefix after After, sorted and once each, and nothing when pass
// passes over every key.
func FuzzIndexLayout(f *testing.F) {
	long := strings.Repeat("a", 130)

	f.Add("a", "b", "", "")
	f.Add(long, long[:120], "a", "")
	f.Add(long+"/x", long+"\xff", long[:125], long[:121])
	f.Add("k", "k", "k", "j")
	f.Add(strings.Repeat("z", 1024), strings.Repeat("z", 240), "", strings.Repeat("z", 239))

	root, bk := fuzzBucket(f)

	keys, meta := filepath.Join(keysPath, hash(testBucket)), hash(testBucket)

	f.Fuzz(func(t *testing.T, k1, k2, prefix, after string) {
		var planted []string

		for _, k := range []string{k1, k2} {
			if storage.CheckKey(k) == nil && !slices.Contains(planted, k) {
				planted = append(planted, k)
			}
		}

		t.Cleanup(func() { unplant(t, root, keys, meta, planted) })

		var want []string

		for _, k := range planted {
			plantKey(t, root, keys, k)
			require.NoError(t, root.WriteFile(filepath.Join(meta, hash(k)+metaExt), nil, 0o600))

			if strings.HasPrefix(k, prefix) && k > after {
				want = append(want, k)
			}
		}

		slices.Sort(want)

		bounds := vfs.Bounds{Prefix: prefix, After: after}
		require.Equal(t, want, listKeys(t, bk, bounds))

		err := bk.ListKeys(t.Context(), bounds, func(string) bool { return true },
			func(k string) bool {
				t.Errorf("yielded a key passed over: %q", k)

				return true
			}, func(s vfs.Skipped) { t.Errorf("unexpected report: %v", s) })
		require.NoError(t, err)
	})
}

// fuzzBucket returns a root on a temp dir of f and testBucket opened in
// it, both closed when f ends.
func fuzzBucket(f *testing.F) (*os.Root, *vfs.Bucket) {
	f.Helper()

	root, err := os.OpenRoot(f.TempDir())
	require.NoError(f, err)
	f.Cleanup(func() { _ = root.Close() })

	v, err := vfs.Open(root)
	require.NoError(f, err)
	require.NoError(f, v.CreateBucket(f.Context(), testBucket))

	bk, err := v.OpenBucket(testBucket)
	require.NoError(f, err)
	f.Cleanup(func() { _ = bk.Close() })

	return root, bk
}

// unplant removes what an exec of FuzzIndexLayout planted in root: the
// entries of the keys directory keys, which the Bucket holds open and so
// stays, and the metadata files of the keys planted in the metadata
// directory meta.
func unplant(t *testing.T, root *os.Root, keys, meta string, planted []string) {
	t.Helper()

	entries, err := fs.ReadDir(root.FS(), filepath.ToSlash(keys))
	require.NoError(t, err)

	for _, e := range entries {
		require.NoError(t, root.RemoveAll(filepath.Join(keys, e.Name())))
	}

	for _, k := range planted {
		err = root.Remove(filepath.Join(meta, hash(k)+metaExt))
		if !errors.Is(err, fs.ErrNotExist) {
			require.NoError(t, err)
		}
	}
}
