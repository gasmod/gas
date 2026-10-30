package cryptoutil_test

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth/internal/cryptoutil"
)

// SHA256Hex returns the SHA-256 digest as 64 lowercase hex characters.
func TestSHA256Hex(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"":    "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"abc": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
	} {
		assert.Equal(t, want, cryptoutil.SHA256Hex(in), "SHA256Hex(%q)", in)
	}
}

// RandomString encodes n bytes as unpadded URL-safe base64, and n of 0
// gives the empty string.
func TestRandomString_EncodesNBytes(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, 1, 16, 32, 33} {
		s, err := cryptoutil.RandomString(n)
		require.NoError(t, err)

		b, err := base64.RawURLEncoding.DecodeString(s)
		require.NoError(t, err, "RandomString(%d) = %q is not unpadded URL-safe base64", n, s)
		assert.Len(t, b, n)
	}
}

// Session IDs, API keys and tokens all come from RandomString, so a source
// that stopped being random would make them guessable and collide.
func TestRandomString_Unique(t *testing.T) {
	t.Parallel()

	const count = 1000

	seen := make(map[string]struct{}, count)

	for range count {
		s, err := cryptoutil.RandomString(32)
		require.NoError(t, err)

		_, dup := seen[s]
		require.False(t, dup, "RandomString returned %q twice", s)

		seen[s] = struct{}{}
	}
}

// A negative length is an error, not a panic.
func TestRandomString_Negative(t *testing.T) {
	t.Parallel()

	s, err := cryptoutil.RandomString(-1)
	require.ErrorIs(t, err, cryptoutil.ErrInvalidLength)
	require.EqualError(t, err, "invalid random string length: must not be negative")
	assert.Empty(t, s)
}

// Equal reports whether two strings are the same, including strings of
// different lengths and the empty string.
func TestEqual(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{a: "", b: "", want: true},
		{a: "abc", b: "abc", want: true},
		{a: "abc", b: "abd", want: false},
		{a: "abc", b: "ab", want: false},
		{a: "", b: "a", want: false},
	} {
		assert.Equal(t, tc.want, cryptoutil.Equal(tc.a, tc.b), "Equal(%q, %q)", tc.a, tc.b)
	}
}
