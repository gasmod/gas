package dir_test

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir"
)

// fuzzStore returns a store on a new directory of f, with bucket
// configured, initialized and closed when f ends.
func fuzzStore(f *testing.F) *dir.Provider {
	f.Helper()

	p, err := dir.New(dir.WithConfig(config(f.TempDir(), bucket)))(nil, &gas.NopLogger{})
	require.NoError(f, err)
	require.NoError(f, p.Init(context.Background()))
	f.Cleanup(func() { require.NoError(f, p.Close(context.Background())) })

	return p
}

// fuzzData is the object FuzzGetObjectRange reads.
const fuzzData = "0123456789abcdef"

// FuzzGetObjectRange reads an object with any Range. The invariants: a
// Range ParseRange refuses is storage.ErrInvalidRange, one that selects
// nothing storage.ErrRangeNotSatisfiable, each with its fixed text, and
// any other reads exactly the bytes Resolve selects, with Size their
// length, from GetObject and HeadObject alike.
func FuzzGetObjectRange(f *testing.F) {
	p := fuzzStore(f)

	_, err := p.PutObject(context.Background(), putIn("k", fuzzData))
	require.NoError(f, err)

	for _, s := range []string{
		"bytes=0-4", "bytes=5-", "bytes=-3", "bytes=8-100", " Bytes=1-1\t", "bytes=0-99999999999999999999",
		"bytes=16-", "bytes=-0", "bytes=5-1", "items=0-1", "bytes=0-1,3-4", "bytes=+1-2", "", "bytes=-",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, rng string) {
		in := getIn("k")

		in.Range = &rng

		obj, err := p.GetObject(t.Context(), in)
		_, headErr := p.HeadObject(t.Context(), &in.HeadObjectInput)

		r, parseErr := storage.ParseRange(rng)
		if parseErr != nil {
			require.EqualError(
				t,
				err,
				"gas/storage/dir: get object: invalid range: not one byte range",
			)
			require.ErrorIs(t, headErr, storage.ErrInvalidRange)

			return
		}

		off, n, resolveErr := r.Resolve(int64(len(fuzzData)))
		if resolveErr != nil {
			require.EqualError(t, err, "gas/storage/dir: get object: range not satisfiable")
			require.ErrorIs(t, headErr, storage.ErrRangeNotSatisfiable)

			return
		}

		require.NoError(t, err)
		require.NoError(t, headErr)

		b, err := io.ReadAll(obj.Body)
		require.NoError(t, err)
		require.NoError(t, obj.Body.Close())
		require.Equal(t, fuzzData[off:off+n], string(b))
		require.Equal(t, n, obj.Size)
	})
}

// FuzzListObjectsToken lists with any continuation token. The invariants:
// no panic; an empty token is refused as empty, and any other that the
// store did not issue as not one it issued, with fixed texts that never
// quote it.
func FuzzListObjectsToken(f *testing.F) {
	p := fuzzStore(f)

	for _, k := range []string{"a", "b", "c"} {
		_, err := p.PutObject(context.Background(), putIn(k, "v"))
		require.NoError(f, err)
	}

	out, err := p.ListObjects(
		context.Background(),
		&storage.ListObjectsInput{MaxKeys: new(int32(1))},
	)
	require.NoError(f, err)

	issued := out.NextContinuationToken

	for _, s := range []string{
		issued, issued[:len(issued)-1], issued + "A", "", "YQ", "AAAA", strings.Repeat("A", 4096), "@@@",
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, token string) {
		in := &storage.ListObjectsInput{ContinuationToken: &token, MaxKeys: new(int32(1))}

		page, err := p.ListObjects(t.Context(), in)

		switch token {
		case issued:
			require.NoError(t, err)
			require.Equal(t, "b", page.Objects[0].Key)
		case "":
			require.EqualError(
				t,
				err,
				"gas/storage/dir: list objects: invalid continuation token: empty",
			)
		default:
			require.EqualError(
				t,
				err,
				"gas/storage/dir: list objects: invalid continuation token: "+
					"not one this backend issued for this listing",
			)
		}
	})
}

// FuzzKeyLayout writes, lists, reads and deletes two objects under any
// keys. The invariants: a key CheckKey refuses is storage.ErrInvalidKey;
// the others read back, are listed once each in byte order, with a Prefix
// and with a Delimiter as the listing rules say, and are gone, from the
// listing too, once deleted.
func FuzzKeyLayout(f *testing.F) {
	p := fuzzStore(f)

	long := strings.Repeat("a", 120)

	for _, seed := range [][2]string{
		{"a", "a/b"},
		{long, long + "b"},
		{long[:119], long},
		{strings.Repeat("z", 1024), "z"},
		{"../x", "a\\b"},
		{"", "x"},
		{"a\x00b", "c"},
		{"é/ü", "é"},
		{"a/.", "b"},
	} {
		f.Add(seed[0], seed[1])
	}

	f.Fuzz(func(t *testing.T, k1, k2 string) {
		keys := putKeys(t, p, k1, k2)

		require.Equal(
			t,
			keys,
			nilIfEmpty(listKeys(t, p, &storage.ListObjectsInput{MaxKeys: new(int32(1))})),
		)

		if len(keys) > 0 {
			runes := []rune(keys[0])
			prefix := string(runes[:len(runes)/2])
			require.Equal(
				t,
				rollUp(keys, prefix, "/"),
				nilIfEmpty(listKeys(t, p, &storage.ListObjectsInput{
					Prefix: &prefix, Delimiter: new("/"),
				})),
			)
		}

		for _, k := range keys {
			require.Equal(t, k, read(t, p, k))
			require.NoError(t, p.DeleteObject(t.Context(), &storage.DeleteObjectInput{
				Key: k,
			}))
		}

		require.Empty(t, listKeys(t, p, &storage.ListObjectsInput{}))
	})
}

// putKeys writes an object under each key, its body the key, checks that
// a key CheckKey refuses is storage.ErrInvalidKey, and returns the keys
// written, sorted, once each.
func putKeys(t *testing.T, p *dir.Provider, keys ...string) []string {
	t.Helper()

	var out []string

	for _, k := range keys {
		_, err := p.PutObject(t.Context(), putIn(k, k))
		if storage.CheckKey(k) != nil {
			require.ErrorIs(t, err, storage.ErrInvalidKey)

			continue
		}

		require.NoError(t, err)

		out = append(out, k)
	}

	slices.Sort(out)

	return slices.Compact(out)
}

// nilIfEmpty returns s, or nil when it is empty.
func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}

	return s
}
