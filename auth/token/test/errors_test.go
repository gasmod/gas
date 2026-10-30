package token_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/cryptoutil"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/token"
)

// ErrTokenInvalid wraps auth.ErrUnauthenticated, and ErrTokenExpired
// wraps auth.ErrCredentialsExpired and so auth.ErrUnauthenticated, with
// the documented messages. ErrInvalidTTL is not a denial.
func TestErrors_Sentinels(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, token.ErrTokenInvalid, auth.ErrUnauthenticated)
	require.NotErrorIs(t, token.ErrTokenInvalid, auth.ErrCredentialsExpired)
	require.EqualError(t, token.ErrTokenInvalid, "unauthenticated: invalid token")

	require.ErrorIs(t, token.ErrTokenExpired, auth.ErrCredentialsExpired)
	require.ErrorIs(t, token.ErrTokenExpired, auth.ErrUnauthenticated)
	require.NotErrorIs(t, token.ErrTokenExpired, token.ErrTokenInvalid)
	require.EqualError(
		t,
		token.ErrTokenExpired,
		"unauthenticated: credentials expired: token expired",
	)

	for _, err := range []error{
		token.ErrInvalidTTL,
		token.ErrNotInitialized,
		token.ErrNilLogger,
		token.ErrNilConfig,
		token.ErrNoConfigProvider,
		token.ErrInvalidConfig,
	} {
		require.NotErrorIs(t, err, auth.ErrUnauthenticated)
	}
}

// A database failure is an error that starts with the service's and the
// store's names and the operation, matches no denial, and includes
// neither the token nor its hash.
func TestErrors_Database(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)
	svc := newService(t, b, testConfig(), testutil.NewNopLogger())
	ctx := t.Context()

	raw, err := svc.Issue(ctx, "user-1", "reset", time.Hour)
	require.NoError(t, err)

	require.NoError(t, b.DB.DB().Close())

	_, issueErr := svc.Issue(ctx, "user-1", "reset", time.Hour)
	_, verifyErr := svc.Verify(ctx, raw, "reset")

	tests := []struct {
		err    error
		prefix string
	}{
		{issueErr, "gas/auth/token: gas/auth/token-store: insert token: "},
		{verifyErr, "gas/auth/token: gas/auth/token-store: consume token: "},
		{svc.Revoke(ctx, raw), "gas/auth/token: gas/auth/token-store: delete token by hash: "},
		{
			svc.RevokeAllByPurpose(ctx, "user-1", "reset"),
			"gas/auth/token: gas/auth/token-store: delete tokens by subject and purpose: ",
		},
	}

	for _, tt := range tests {
		require.Error(t, tt.err)
		assert.True(t, strings.HasPrefix(tt.err.Error(), tt.prefix), tt.err.Error())
		require.NotErrorIs(t, tt.err, auth.ErrUnauthenticated)
		assert.NotContains(t, tt.err.Error(), raw)
		assert.NotContains(t, tt.err.Error(), cryptoutil.SHA256Hex(raw))
	}
}
