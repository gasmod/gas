package dir_test

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir"
)

// longKeys returns keys around the 120-byte elements of the key index:
// keys of 119, 120, 121, 240 and 241 bytes and of 1024, a key and a longer
// one with the same first 120 bytes, and keys that differ in the last
// byte of an element and in the first byte of the next.
func longKeys() []string {
	a := strings.Repeat("a", 120)

	return []string{
		strings.Repeat("a", 119),
		a,
		a + "/",
		a + "b",
		a + "a",
		a + strings.Repeat("c", 120),
		a + strings.Repeat("c", 121),
		strings.Repeat("a", 119) + "b",
		strings.Repeat("z", 1024),
		"b",
		"a",
		"a/b",
		"a/b/c",
	}
}

// rollUp returns the entries a listing of sorted with prefix and delim
// returns, in order, the common prefixes ending with `*`.
func rollUp(sorted []string, prefix, delim string) []string {
	var out []string

	for _, k := range sorted {
		if !strings.HasPrefix(k, prefix) {
			continue
		}

		if i := strings.Index(k[len(prefix):], delim); i >= 0 {
			cp := k[:len(prefix)+i+len(delim)] + "*"
			if len(out) == 0 || out[len(out)-1] != cp {
				out = append(out, cp)
			}

			continue
		}

		out = append(out, k)
	}

	return out
}

// ListObjects lists keys of any length in byte order, page by page, with
// the same entries whatever the page size, and rolls them up at a
// delimiter and limits them to a prefix across the elements of the key
// index.
func TestListObjects_LongKeys(t *testing.T) {
	t.Parallel()

	p, _ := open(t)
	keys := longKeys()

	for _, k := range keys {
		put(t, p, k, "v")
	}

	want := slices.Sorted(slices.Values(keys))

	for _, n := range []int32{1, 2, 3, 1000} {
		assert.Equal(t, want, listKeys(t, p, &storage.ListObjectsInput{MaxKeys: &n}), n)
	}

	prefix := strings.Repeat("a", 120)

	assert.Equal(
		t,
		[]string{prefix, prefix + "/", prefix + "a", prefix + "b", prefix + "c*"},
		listKeys(t, p, &storage.ListObjectsInput{Prefix: &prefix, Delimiter: new("c")}),
	)

	for _, delim := range []string{"/", "a", "c", "zz"} {
		for _, n := range []int32{1, 1000} {
			assert.Equal(
				t,
				rollUp(want, "", delim),
				listKeys(t, p, &storage.ListObjectsInput{Delimiter: &delim, MaxKeys: &n}),
				delim,
			)
		}
	}

	assert.Equal(
		t,
		[]string{strings.Repeat("a", 119) + "b", "b", strings.Repeat("z", 1024)},
		listKeys(
			t,
			p,
			&storage.ListObjectsInput{StartAfter: new(prefix + strings.Repeat("c", 121))},
		),
	)
}

// A key whose object is gone, such as one a crash left in the key index,
// is left out silently, and does not hide the common prefix of the keys
// after it.
func TestListObjects_StaleIndexEntry(t *testing.T) {
	t.Parallel()

	var got skips

	path := t.TempDir()
	p := openAt(t, path, dir.WithSkipHandler(got.handler()))

	put(t, p, "a/2", "v")
	put(t, p, "b", "v")
	require.NoError(t, os.WriteFile(
		filepath.Join(keysDir(path), hex.EncodeToString([]byte("a/1"))+".k"), nil, 0o600))
	require.NoError(t, os.WriteFile(
		filepath.Join(keysDir(path), hex.EncodeToString([]byte("ab"))+".k"), nil, 0o600))

	assert.Equal(t, []string{"a/2", "b"}, listKeys(t, p, &storage.ListObjectsInput{}))
	assert.Equal(
		t,
		[]string{"a/*", "b"},
		listKeys(t, p, &storage.ListObjectsInput{Delimiter: new("/")}),
	)
	assert.Empty(t, got.all())
}

// A file in the key index that the store did not write is left out of a
// listing and passed to the skip handler.
func TestListObjects_ForeignIndexFile(t *testing.T) {
	t.Parallel()

	var got skips

	path := t.TempDir()
	p := openAt(t, path, dir.WithSkipHandler(got.handler()))

	put(t, p, "k", "v")
	require.NoError(t, os.WriteFile(filepath.Join(keysDir(path), "notes.txt"), nil, 0o600))

	assert.Equal(t, []string{"k"}, listKeys(t, p, &storage.ListObjectsInput{}))

	all := got.all()
	require.Len(t, all, 1)
	assert.Equal(t, filepath.Join(".gas", "keys", sha(bucket), "notes.txt"), all[0].path)
}

// A listing's errors of a bucket deleted under it, and of a bucket whose
// keys directory cannot be read, hold no path.
func TestListObjects_Errors(t *testing.T) {
	t.Parallel()

	p, path := open(t)
	put(t, p, "k", "v")
	require.NoError(t, os.RemoveAll(keysDir(path)))
	require.NoError(t, os.WriteFile(keysDir(path), nil, 0o600))

	_, err := p.ListObjects(t.Context(), &storage.ListObjectsInput{})
	require.ErrorIs(t, err, storage.ErrBackend)
	requireClean(t, err, path, bucket)
}
