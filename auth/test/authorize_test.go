package auth_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/authtest"
	"github.com/gasmod/gas/auth/internal/testutil"
)

// RequireScheme lets a principal of the scheme through, compared exactly.
func TestRequireScheme_Match(t *testing.T) {
	t.Parallel()

	for _, scheme := range []string{auth.SchemeJWT, ""} {
		p := auth.NewPrincipal("alice", scheme, "c", nil)
		rec := serve(auth.RequireScheme(scheme)(okHandler), withPrincipal(p))

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "alice", rec.Body.String())
	}
}

// Without a principal the request is unauthenticated: 401, not 403. A
// typed nil principal counts as none.
func TestRequireScheme_NoPrincipal401(t *testing.T) {
	t.Parallel()

	for _, r := range []*http.Request{nil, withPrincipal((*auth.BasePrincipal)(nil))} {
		rec := serve(auth.RequireScheme(auth.SchemeJWT)(okHandler), r)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Equal(t, "Unauthorized\n", rec.Body.String())
	}
}

// A principal of another scheme, case included, gets a 403 that names
// neither scheme.
func TestRequireScheme_OtherScheme403(t *testing.T) {
	t.Parallel()

	for _, scheme := range []string{auth.SchemeSession, "JWT"} {
		p := auth.NewPrincipal("alice", scheme, "c", nil)
		rec := serve(auth.RequireScheme(auth.SchemeJWT)(okHandler), withPrincipal(p))

		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Equal(t, "Forbidden\n", rec.Body.String())
	}
}

// allowing is an Authorizer that returns err.
func allowing(err error) *authtest.MockAuthorizer {
	return &authtest.MockAuthorizer{
		AuthorizeFn: func(context.Context, gas.Principal, string, string) error { return err },
	}
}

// RequireAuthorization asks the authorizer about the principal, the action
// and the resource, and lets the request through when it allows.
func TestRequireAuthorization_Allow(t *testing.T) {
	t.Parallel()

	a := allowing(nil)
	p := auth.NewPrincipal("alice", auth.SchemeJWT, "c", nil)

	rec := serve(auth.RequireAuthorization(a, "read", "reports")(okHandler), withPrincipal(p))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "alice", rec.Body.String())

	calls := a.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "Authorize", calls[0].Method)
	assert.Equal(t, p, calls[0].Args[1])
	assert.Equal(t, "read", calls[0].Args[2])
	assert.Equal(t, "reports", calls[0].Args[3])
}

// A denial, wrapped or not, is a 403.
func TestRequireAuthorization_Deny403(t *testing.T) {
	t.Parallel()

	p := auth.NewPrincipal("alice", auth.SchemeJWT, "c", nil)

	for _, err := range []error{auth.ErrForbidden, fmt.Errorf("policy: %w", auth.ErrForbidden)} {
		rec := serve(
			auth.RequireAuthorization(allowing(err), "write", "reports")(okHandler),
			withPrincipal(p),
		)

		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Equal(t, "Forbidden\n", rec.Body.String())
	}
}

// Any other error is a 500, logged with the request's logger, whose body
// says nothing about the cause.
func TestRequireAuthorization_Error500(t *testing.T) {
	t.Parallel()

	var logger testutil.RecordingLogger

	p := auth.NewPrincipal("alice", auth.SchemeJWT, "c", nil)
	r := withLogger(withPrincipal(p), &logger)

	rec := serve(auth.RequireAuthorization(allowing(errDB), "read", "reports")(okHandler), r)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "Internal Server Error\n", rec.Body.String())

	entries := logger.Entries()
	require.Len(t, entries, 1)
	assert.Equal(t, "error", entries[0].Level)
	assert.Equal(t, "authorization failed", entries[0].Msg)
	assert.Equal(t, errDB, entries[0].Fields["error"])

	rec = serve(
		auth.RequireAuthorization(allowing(auth.ErrUnauthenticated), "read", "reports")(okHandler),
		withPrincipal(p),
	)
	assert.Equal(t, http.StatusInternalServerError, rec.Code, "only ErrForbidden denies")
}

// Once the request's context has ended, such as when the client went
// away, an authorizer error still gets the 500, but is not logged.
func TestRequireAuthorization_EndedContextNotLogged(t *testing.T) {
	t.Parallel()

	var logger testutil.RecordingLogger

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := auth.NewPrincipal("alice", auth.SchemeJWT, "c", nil)
	r := withLogger(newGet().WithContext(gas.WithPrincipal(ctx, p)), &logger)

	rec := serve(auth.RequireAuthorization(allowing(errDB), "read", "reports")(okHandler), r)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "Internal Server Error\n", rec.Body.String())
	assert.Empty(t, logger.Entries())
}

// Without a principal the request is a 401 and the authorizer is not asked.
func TestRequireAuthorization_NoPrincipal401(t *testing.T) {
	t.Parallel()

	a := allowing(nil)
	rec := serve(auth.RequireAuthorization(a, "read", "reports")(okHandler), nil)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, 0, a.CallCount("Authorize"))
}

// The unset MockAuthorizer denies, so a test that forgets to set it fails
// closed.
func TestRequireAuthorization_UnsetMockDenies(t *testing.T) {
	t.Parallel()

	p := auth.NewPrincipal("alice", auth.SchemeJWT, "c", nil)
	rec := serve(
		auth.RequireAuthorization(&authtest.MockAuthorizer{}, "read", "x")(okHandler),
		withPrincipal(p),
	)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// A nil Authorizer, or a nil pointer, panics at construction.
func TestRequireAuthorization_NilPanics(t *testing.T) {
	t.Parallel()

	for _, a := range []auth.Authorizer{nil, (*authtest.MockAuthorizer)(nil)} {
		assert.PanicsWithValue(t, "gas: auth.RequireAuthorization: nil Authorizer", func() {
			auth.RequireAuthorization(a, "read", "x")
		})
	}
}
