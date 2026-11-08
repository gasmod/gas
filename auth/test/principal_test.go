package auth_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
)

// NewPrincipal keeps its values exactly, empty and non-ASCII ones
// included.
func TestNewPrincipal_Values(t *testing.T) {
	t.Parallel()

	for _, v := range [][3]string{
		{"alice", auth.SchemeJWT, "tok-123"},
		{"", "", ""},
		{"user-\U0001F600-\x00-end", "\U0001F525fire", "cred\x00with\x00nulls"},
	} {
		p := auth.NewPrincipal(v[0], v[1], v[2], nil)
		assert.Equal(t, v[0], p.Subject())
		assert.Equal(t, v[1], p.Scheme())
		assert.Equal(t, v[2], p.CredentialID())
	}
}

// A nil metadata becomes an empty one, so Value never dereferences nil.
func TestNewPrincipal_NilMetadata(t *testing.T) {
	t.Parallel()

	p := auth.NewPrincipal("sub", auth.SchemeSession, "cred", nil)

	require.NotNil(t, p.Metadata())
	assert.Equal(t, gas.BasePrincipalMetadata{}, p.Metadata())
	assert.Nil(t, p.Metadata().Value("any"))
}

// A non-nil metadata is kept, not copied, so later changes to the map show
// through the principal.
func TestNewPrincipal_MetadataIsShared(t *testing.T) {
	t.Parallel()

	meta := gas.BasePrincipalMetadata{"role": "admin", "org_id": 42}
	p := auth.NewPrincipal("alice", auth.SchemeAPIKey, "key-1", meta)

	assert.Equal(t, "admin", p.Metadata().Value("role"))
	assert.Equal(t, 42, p.Metadata().Value("org_id"))
	assert.Nil(t, p.Metadata().Value("missing"))

	meta["role"] = "user"

	assert.Equal(t, "user", p.Metadata().Value("role"))
}

// The scheme constants are the strings the module's authenticators use.
func TestSchemes(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "jwt", auth.SchemeJWT)
	assert.Equal(t, "session", auth.SchemeSession)
	assert.Equal(t, "apikey", auth.SchemeAPIKey)
}
