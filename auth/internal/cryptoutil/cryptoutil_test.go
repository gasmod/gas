package cryptoutil_test

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth/internal/cryptoutil"
)

func TestSHA256Hex(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{in: "abc", want: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, cryptoutil.SHA256Hex(tt.in), "SHA256Hex(%q)", tt.in)
	}
}

func TestRandomString_EncodesNBytes(t *testing.T) {
	for _, n := range []int{0, 1, 16, 32, 33} {
		s, err := cryptoutil.RandomString(n)
		require.NoError(t, err)

		b, err := base64.RawURLEncoding.DecodeString(s)
		require.NoError(t, err, "RandomString(%d) = %q is not unpadded URL-safe base64", n, s)
		assert.Len(t, b, n)
	}
}

// Credential IDs, API keys, and tokens all come from RandomString, so a
// source that stopped being random would make them guessable and collide.
func TestRandomString_Unique(t *testing.T) {
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
