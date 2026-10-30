package auth_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/authtest"
)

// errCache stands for a second infrastructure error.
var errCache = errors.New("cache is down")

// authn runs a chain on a plain GET request.
func authn(t *testing.T, c auth.Chain) (gas.Principal, error) {
	t.Helper()

	return c.Authenticate(
		t.Context(),
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil),
	)
}

// An empty or nil chain has seen no credentials.
func TestChain_Empty(t *testing.T) {
	t.Parallel()

	for _, c := range []auth.Chain{nil, {}} {
		p, err := authn(t, c)
		require.ErrorIs(t, err, auth.ErrNoCredentials)
		assert.Nil(t, p)
	}
}

// The first member that succeeds wins, and the members after it are not
// called.
func TestChain_FirstSuccessWins(t *testing.T) {
	t.Parallel()

	first := returning(nil, auth.ErrNoCredentials)
	second := succeeding("bob")
	third := succeeding("carol")

	p, err := authn(t, auth.Chain{first, second, third})
	require.NoError(t, err)
	assert.Equal(t, "bob", p.Subject())
	assert.Equal(t, 1, first.CallCount("Authenticate"))
	assert.Equal(t, 1, second.CallCount("Authenticate"))
	assert.Equal(t, 0, third.CallCount("Authenticate"))
}

// A later success wins over an earlier denial or infrastructure error.
func TestChain_SuccessAfterFailures(t *testing.T) {
	t.Parallel()

	p, err := authn(t, auth.Chain{
		returning(nil, auth.ErrCredentialsExpired),
		returning(nil, errDB),
		succeeding("dave"),
	})
	require.NoError(t, err)
	assert.Equal(t, "dave", p.Subject())
}

// A chain where every member finds no credentials returns
// ErrNoCredentials, and calls every member.
func TestChain_AllNoCredentials(t *testing.T) {
	t.Parallel()

	members := make(auth.Chain, 5)
	for i := range members {
		members[i] = returning(nil, fmt.Errorf("member %d: %w", i, auth.ErrNoCredentials))
	}

	p, err := authn(t, members)
	require.ErrorIs(t, err, auth.ErrNoCredentials)
	require.EqualError(t, err, "no credentials")
	assert.Nil(t, p)

	for i, m := range members {
		mock, ok := m.(*authtest.MockAuthenticator)
		require.True(t, ok)
		assert.Equal(t, 1, mock.CallCount("Authenticate"), "member %d", i)
	}
}

// Without an infrastructure error, the chain returns the error of the
// first member that denied credentials, unwrapped, so an expired token
// stays expired when a later member finds nothing or denies too.
func TestChain_FirstDenialWins(t *testing.T) {
	t.Parallel()

	expired := fmt.Errorf("jwt: %w", auth.ErrCredentialsExpired)

	_, err := authn(t, auth.Chain{
		returning(nil, auth.ErrNoCredentials),
		returning(nil, expired),
		returning(nil, auth.ErrUnauthenticated),
		returning(nil, auth.ErrNoCredentials),
	})
	require.ErrorIs(t, err, auth.ErrCredentialsExpired)
	require.EqualError(t, err, "jwt: unauthenticated: credentials expired")

	_, err = authn(t, auth.Chain{
		returning(nil, auth.ErrCredentialRevoked),
		returning(nil, auth.ErrUnauthenticated),
	})
	require.ErrorIs(t, err, auth.ErrCredentialRevoked)
}

// Infrastructure errors win over denials, are all kept, in member order,
// and are not hidden by a member that finds no credentials.
func TestChain_InfrastructureErrorsJoined(t *testing.T) {
	t.Parallel()

	_, err := authn(t, auth.Chain{
		returning(nil, auth.ErrCredentialsExpired),
		returning(nil, errDB),
		returning(nil, auth.ErrNoCredentials),
		returning(nil, errCache),
	})
	require.ErrorIs(t, err, errDB)
	require.ErrorIs(t, err, errCache)
	require.NotErrorIs(t, err, auth.ErrUnauthenticated)
	require.EqualError(t, err, "database is down\ncache is down")

	_, err = authn(t, auth.Chain{returning(nil, errDB)})
	require.EqualError(t, err, "database is down")
}

// ErrForbidden from an authenticator is not a denial of credentials, so the
// chain treats it as an infrastructure error.
func TestChain_ForbiddenIsInfrastructure(t *testing.T) {
	t.Parallel()

	_, err := authn(t, auth.Chain{
		returning(nil, auth.ErrUnauthenticated),
		returning(nil, auth.ErrForbidden),
	})
	require.ErrorIs(t, err, auth.ErrForbidden)
	require.NotErrorIs(t, err, auth.ErrUnauthenticated)
}

// A member that returns no principal and no error, typed nil included,
// denies the request: the chain moves on, and if nothing else succeeds
// returns ErrUnauthenticated.
func TestChain_NilPrincipalIsDenial(t *testing.T) {
	t.Parallel()

	for name, p := range map[string]gas.Principal{
		"untyped nil": nil,
		"typed nil":   (*auth.BasePrincipal)(nil),
	} {
		got, err := authn(t, auth.Chain{returning(p, nil), succeeding("erin")})
		require.NoError(t, err, name)
		assert.Equal(t, "erin", got.Subject(), name)

		got, err = authn(t, auth.Chain{returning(nil, auth.ErrNoCredentials), returning(p, nil)})
		require.ErrorIs(t, err, auth.ErrUnauthenticated, name)
		require.EqualError(t, err, "unauthenticated", name)
		assert.Nil(t, got, name)
	}
}

// The chain passes its context and request to every member it calls.
func TestChain_PassesContextAndRequest(t *testing.T) {
	t.Parallel()

	type key struct{}

	ctx := context.WithValue(t.Context(), key{}, "v")
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/x", nil)
	m := returning(nil, auth.ErrNoCredentials)

	_, _ = auth.Chain{m, m}.Authenticate(ctx, r)

	calls := m.Calls()
	require.Len(t, calls, 2)

	for _, c := range calls {
		assert.Equal(t, ctx, c.Args[0])
		assert.Same(t, r, c.Args[1])
	}
}

// A nil member panics when the chain reaches it.
func TestChain_NilMemberPanics(t *testing.T) {
	t.Parallel()

	assert.Panics(t, func() { _, _ = authn(t, auth.Chain{nil}) })
}
