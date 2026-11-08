package listing_test

import (
	"bytes"
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal/listing"
)

// seal returns the token codec seals cur into for scope, failing the
// test when Seal fails.
func seal(tb testing.TB, codec *listing.Codec, scope []byte, cur listing.Cursor) string {
	tb.Helper()

	token, err := codec.Seal(scope, cur)
	require.NoError(tb, err)

	return token
}

// Seal made tokens Open refused: Open bounded tokens to 2048 bytes, and
// Seal sealed a cursor of any length, so a cursor past about 1500 bytes,
// such as S3's own token, gave a token that never opened. Seal now
// refuses a cursor longer than MaxCursorLength, and Open opens every
// token Seal makes.
func TestCodec_SealOpenSymmetric(t *testing.T) {
	t.Parallel()

	codec, err := listing.NewCodec()
	require.NoError(t, err)

	scope := listing.Scope("objects", "photos", "", "", "")

	for _, n := range []int{1, 1024, 1500, 1508, listing.MaxCursorLength} {
		cur := listing.Cursor{After: strings.Repeat("A", n), Prefix: true}

		got, oErr := codec.Open(scope, seal(t, codec, scope, cur))
		require.NoError(t, oErr, n)
		assert.Equal(t, cur, got, n)
	}

	token, err := codec.Seal(
		scope,
		listing.Cursor{After: strings.Repeat("A", listing.MaxCursorLength+1)},
	)
	require.ErrorIs(t, err, listing.ErrCursorTooLong)
	require.EqualError(t, err, "cursor longer than 1536 bytes")
	assert.Empty(t, token)

	// A token one character longer than the longest Seal makes is refused
	// before it is decoded.
	longest := seal(
		t,
		codec,
		scope,
		listing.Cursor{After: strings.Repeat("A", listing.MaxCursorLength)},
	)

	_, err = codec.Open(scope, longest+"A")
	require.ErrorIs(t, err, storage.ErrInvalidToken)
}

// names returns the names of entries, the common prefixes ending with a
// `*`.
func names(entries []listing.Entry) []string {
	out := make([]string, 0, len(entries))

	for _, e := range entries {
		if e.Prefix {
			out = append(out, e.Name+"*")

			continue
		}

		out = append(out, e.Name)
	}

	return out
}

// pages lists every page of q over keys and returns the entries in order.
func pages(t *testing.T, keys []string, q listing.Query) []string {
	t.Helper()

	var got []string

	for range len(keys) + 1 {
		entries, truncated := listing.Page(slices.Values(keys), q)
		require.LessOrEqual(t, len(entries), q.Max)

		got = append(got, names(entries)...)

		if !truncated {
			return got
		}

		last := entries[len(entries)-1]

		q.After = listing.Cursor{After: last.Name, Prefix: last.Prefix}
	}

	require.Fail(t, "the listing did not end")

	return nil
}

// A page lists the keys after StartAfter and the previous page, rolled up
// at the delimiter, each common prefix once across the pages.
func TestPage(t *testing.T) {
	t.Parallel()

	keys := []string{"a/1", "a/2", "b", "c/1/x", "c/2", "d"}

	for _, n := range []int{1, 2, 3, 100} {
		assert.Equal(t, keys, pages(t, keys, listing.Query{Max: n}))
		assert.Equal(t, []string{"a/*", "b", "c/*", "d"},
			pages(t, keys, listing.Query{Delimiter: "/", Max: n}))
		assert.Equal(t, []string{"c/1/*", "c/2"},
			pages(t, keys, listing.Query{Prefix: "c/", Delimiter: "/", Max: n}))
		assert.Equal(t, []string{"a/*", "b", "c/*", "d"},
			pages(t, keys, listing.Query{StartAfter: "a/1", Delimiter: "/", Max: n}))
		assert.Equal(
			t,
			[]string{"c/2", "d"},
			pages(t, keys, listing.Query{StartAfter: "c/1/x", Max: n}),
		)
	}

	entries, truncated := listing.Page(slices.Values(keys), listing.Query{Prefix: "z", Max: 1})
	assert.Empty(t, entries)
	assert.False(t, truncated)
}

// Page stops reading keys at the first entry past the page.
func TestPage_StopsEarly(t *testing.T) {
	t.Parallel()

	read := 0
	keys := func(yield func(string) bool) {
		for _, k := range []string{"a", "b", "c", "d"} {
			read++

			if !yield(k) {
				return
			}
		}
	}

	entries, truncated := listing.Page(keys, listing.Query{Max: 2})
	assert.Equal(t, []string{"a", "b"}, names(entries))
	assert.True(t, truncated)
	assert.Equal(t, 3, read)
}

// A token opens to the cursor it sealed, for its scope only, with the
// Codec that sealed it, and never shows the key it holds.
func TestCodec(t *testing.T) {
	t.Parallel()

	codec, err := listing.NewCodec()
	require.NoError(t, err)

	other, err := listing.NewCodec()
	require.NoError(t, err)

	scope := listing.Scope("photos", "users/", "/", "")
	cur := listing.Cursor{After: "users/alice@example.com/passport.pdf", Prefix: true}
	token := seal(t, codec, scope, cur)

	assert.NotContains(t, token, "alice")
	assert.NotContains(t, token, "users")

	got, err := codec.Open(scope, token)
	require.NoError(t, err)
	assert.Equal(t, cur, got)

	plain, err := codec.Open(scope, seal(t, codec, scope, listing.Cursor{After: "k"}))
	require.NoError(t, err)
	assert.Equal(t, listing.Cursor{After: "k"}, plain)

	assert.NotEqual(t, token, seal(t, codec, scope, cur))

	for _, c := range []struct {
		codec *listing.Codec
		token string
		scope []byte
	}{
		{other, token, scope},
		{codec, token, listing.Scope("photos", "users", "//", "")},
		{codec, token, listing.Scope("photos", "users/", "/", "", "")},
		{codec, token[:len(token)-1], scope},
		{codec, nonCanonical(t, token), scope},
		{codec, "!!!", scope},
		{codec, "", scope},
		{codec, strings.Repeat("A", 4096), scope},
		{codec, seal(t, codec, scope, listing.Cursor{}), scope},
	} {
		_, err = c.codec.Open(c.scope, c.token)
		require.EqualError(t, err,
			"invalid continuation token: not one this backend issued for this listing")
		require.ErrorIs(t, err, storage.ErrInvalidToken)
	}
}

// nonCanonical returns token with its last character changed in the bits
// base64 leaves unused, so that a lenient decoder reads the same bytes; a
// changed token must still be refused.
func nonCanonical(t *testing.T, token string) string {
	t.Helper()

	want, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)

	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

	last := token[len(token)-1]

	for i := range len(alphabet) {
		if alphabet[i] == last {
			continue
		}

		variant := token[:len(token)-1] + alphabet[i:i+1]

		got, err := base64.RawURLEncoding.DecodeString(variant)
		if err == nil && bytes.Equal(got, want) {
			return variant
		}
	}

	t.Skip("the token's length leaves no unused bits")

	return ""
}

// Open is deterministic, never panics, and refuses every token but one
// the Codec sealed, without quoting it.
func FuzzOpen(f *testing.F) {
	codec, err := listing.NewCodec()
	require.NoError(f, err)

	scope := listing.Scope("photos")

	f.Add(seal(f, codec, scope, listing.Cursor{After: "k"}))
	f.Add("AAAA")
	f.Add("secret-token-zq7")

	f.Fuzz(func(t *testing.T, token string) {
		cur, err := codec.Open(scope, token)
		again, againErr := codec.Open(scope, token)
		require.Equal(t, cur, again)
		require.Equal(t, err, againErr)

		if err != nil {
			require.EqualError(t, err,
				"invalid continuation token: not one this backend issued for this listing")

			return
		}

		require.NotEmpty(t, cur.After)
	})
}
