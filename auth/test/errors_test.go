package auth_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/testutil"
)

// Expired and revoked credentials are denials, so they match
// ErrUnauthenticated; ErrNoCredentials and ErrForbidden are their own.
func TestErrors_Wrapping(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, auth.ErrCredentialsExpired, auth.ErrUnauthenticated)
	require.ErrorIs(t, auth.ErrCredentialRevoked, auth.ErrUnauthenticated)
	require.NotErrorIs(t, auth.ErrNoCredentials, auth.ErrUnauthenticated)
	require.NotErrorIs(t, auth.ErrForbidden, auth.ErrUnauthenticated)
	require.NotErrorIs(t, auth.ErrCredentialsExpired, auth.ErrCredentialRevoked)
	require.NotErrorIs(t, auth.ErrUnauthenticated, auth.ErrCredentialsExpired)
}

// The messages are the ones the package documentation lists, in its
// order, and it lists every one.
func TestErrors_Messages(t *testing.T) {
	t.Parallel()

	got := make([]string, 0, 10)

	for _, err := range []error{
		auth.ErrNoCredentials,
		auth.ErrUnauthenticated,
		auth.ErrCredentialsExpired,
		auth.ErrCredentialRevoked,
		auth.ErrForbidden,
		auth.ErrEmptySubject,
		auth.ErrValueTooLong,
		auth.ErrSchemeMismatch,
		auth.ErrNilPrincipal,
		auth.CheckLength("subject", strings.Repeat("s", auth.MaxValueLength+1)),
	} {
		got = append(got, err.Error())
	}

	assert.Equal(t, testutil.DocBlock(t, "../doc.go", "Their messages"), got)
}

// The sentinels of the shared issuing and revoking rules are not denials
// of credentials, so Middleware would treat them as infrastructure errors.
func TestErrors_SharedRulesAreNotDenials(t *testing.T) {
	t.Parallel()

	for _, err := range []error{
		auth.ErrEmptySubject, auth.ErrValueTooLong, auth.ErrNilPrincipal, auth.ErrSchemeMismatch,
	} {
		require.NotErrorIs(t, err, auth.ErrUnauthenticated)
		require.NotErrorIs(t, err, auth.ErrNoCredentials)
	}
}

// CheckLength accepts up to MaxValueLength (255) bytes, counted in bytes,
// not characters.
func TestCheckLength_Accepts(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 255, auth.MaxValueLength)

	for _, v := range []string{"", "alice", strings.Repeat("a", 255), strings.Repeat("é", 127)} {
		require.NoError(t, auth.CheckLength("subject", v))
	}
}

// A longer value is ErrValueTooLong, with an error that names the field
// and never contains the value.
func TestCheckLength_TooLong(t *testing.T) {
	t.Parallel()

	for _, v := range []string{strings.Repeat("s", 256), strings.Repeat("é", 128)} {
		err := auth.CheckLength("subject", v)
		require.ErrorIs(t, err, auth.ErrValueTooLong)
		require.EqualError(t, err, "value too long: subject is longer than 255 bytes")
		assert.NotContains(t, err.Error(), v[:16])
	}

	err := auth.CheckLength("name", strings.Repeat("n", 300))
	require.EqualError(t, err, "value too long: name is longer than 255 bytes")
}
