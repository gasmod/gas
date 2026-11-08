package authtest_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/authtest"
)

// errBoom is an error a mock's function returns.
var errBoom = errors.New("boom")

// The unset mocks fail closed: the authenticator finds no credentials and
// the authorizer denies. The unset revoker succeeds, as revoking nothing
// does.
func TestMocks_UnsetDefaults(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	p := auth.NewPrincipal("alice", auth.SchemeJWT, "c", nil)

	got, err := (&authtest.MockAuthenticator{}).Authenticate(
		ctx,
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil),
	)
	require.ErrorIs(t, err, auth.ErrNoCredentials)
	assert.Nil(t, got)

	require.ErrorIs(
		t,
		(&authtest.MockAuthorizer{}).Authorize(ctx, p, "read", "x"),
		auth.ErrForbidden,
	)

	r := &authtest.MockRevoker{}
	require.NoError(t, r.Revoke(ctx, p))
	require.NoError(t, r.RevokeAll(ctx, "alice"))
	require.NoError(t, r.RevokeAllByScheme(ctx, "alice", auth.SchemeJWT))
}

// Each mock returns what its function returns, and passes it the
// arguments it got.
func TestMocks_DelegateToFields(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	want := auth.NewPrincipal("alice", auth.SchemeJWT, "c", nil)

	a := &authtest.MockAuthenticator{
		AuthenticateFn: func(c context.Context, r *http.Request) (gas.Principal, error) {
			assert.Equal(t, ctx, c)
			assert.Same(t, req, r)

			return want, nil
		},
	}
	got, err := a.Authenticate(ctx, req)
	require.NoError(t, err)
	assert.Same(t, want, got)

	z := &authtest.MockAuthorizer{
		AuthorizeFn: func(_ context.Context, p gas.Principal, action, resource string) error {
			assert.Same(t, want, p)
			assert.Equal(t, "read", action)
			assert.Equal(t, "reports", resource)

			return nil
		},
	}
	require.NoError(t, z.Authorize(ctx, want, "read", "reports"))

	r := &authtest.MockRevoker{
		RevokeFn: func(context.Context, gas.Principal) error { return errBoom },
		RevokeAllFn: func(_ context.Context, s string) error {
			return fmt.Errorf("all %s: %w", s, errBoom)
		},
		RevokeAllBySchemeFn: func(_ context.Context, s, sch string) error {
			return fmt.Errorf("%s %s: %w", s, sch, errBoom)
		},
	}
	require.ErrorIs(t, r.Revoke(ctx, want), errBoom)
	require.EqualError(t, r.RevokeAll(ctx, "alice"), "all alice: boom")
	require.EqualError(t, r.RevokeAllByScheme(ctx, "alice", "jwt"), "alice jwt: boom")
}

// Each call is recorded with its method name and arguments, the context
// included, in order; Calls returns a copy, CallCount counts one method,
// and Reset forgets them.
func TestMocks_Calls(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	p := auth.NewPrincipal("alice", auth.SchemeJWT, "c", nil)

	r := &authtest.MockRevoker{}

	_ = r.Revoke(ctx, p)
	_ = r.RevokeAll(ctx, "alice")
	_ = r.RevokeAllByScheme(ctx, "alice", "jwt")
	_ = r.RevokeAll(ctx, "bob")

	calls := r.Calls()
	assert.Equal(t, []authtest.Call{
		{Method: "Revoke", Args: []any{ctx, p}},
		{Method: "RevokeAll", Args: []any{ctx, "alice"}},
		{Method: "RevokeAllByScheme", Args: []any{ctx, "alice", "jwt"}},
		{Method: "RevokeAll", Args: []any{ctx, "bob"}},
	}, calls)
	assert.Equal(t, 2, r.CallCount("RevokeAll"))
	assert.Equal(t, 0, r.CallCount("Missing"))

	calls[0].Method = "changed"
	assert.Equal(t, "Revoke", r.Calls()[0].Method, "Calls returns a copy")

	r.Reset()
	assert.Empty(t, r.Calls())
	assert.Equal(t, 0, r.CallCount("RevokeAll"))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	a := &authtest.MockAuthenticator{}

	_, _ = a.Authenticate(ctx, req)
	assert.Equal(t, []authtest.Call{{Method: "Authenticate", Args: []any{ctx, req}}}, a.Calls())
	assert.Equal(t, 1, a.CallCount("Authenticate"))
	a.Reset()
	assert.Empty(t, a.Calls())

	z := &authtest.MockAuthorizer{}

	_ = z.Authorize(ctx, p, "read", "x")
	assert.Equal(
		t,
		[]authtest.Call{{Method: "Authorize", Args: []any{ctx, p, "read", "x"}}},
		z.Calls(),
	)
	assert.Equal(t, 1, z.CallCount("Authorize"))
	z.Reset()
	assert.Empty(t, z.Calls())
}

// The mocks may be called, and their calls read, from several goroutines
// at once.
func TestMocks_Concurrent(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	p := auth.NewPrincipal("alice", auth.SchemeJWT, "c", nil)

	var (
		a  authtest.MockAuthenticator
		z  authtest.MockAuthorizer
		r  authtest.MockRevoker
		wg sync.WaitGroup
	)

	for range 8 {
		wg.Go(func() {
			for range 50 {
				_, _ = a.Authenticate(ctx, req)
				_ = z.Authorize(ctx, p, "read", "x")
				_ = r.RevokeAll(ctx, "alice")
				_ = a.Calls()
				_ = z.CallCount("Authorize")
				_ = r.Calls()
			}
		})
	}

	wg.Wait()
	assert.Equal(t, 400, a.CallCount("Authenticate"))
	assert.Equal(t, 400, z.CallCount("Authorize"))
	assert.Equal(t, 400, r.CallCount("RevokeAll"))
}

// The example in the package documentation's Usage section: a mock with
// its function set authenticates every request Middleware serves.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	m := &authtest.MockAuthenticator{
		AuthenticateFn: func(context.Context, *http.Request) (gas.Principal, error) {
			return auth.NewPrincipal("user-1", auth.SchemeJWT, "token-1", nil), nil
		},
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := gas.PrincipalFromContext(r.Context())
		if !assert.True(t, ok) {
			return
		}

		_, _ = w.Write([]byte(p.Subject()))
	})
	handler := auth.Middleware(m)(next)

	for range 3 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(
			rec,
			httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil),
		)
		assert.Equal(t, "user-1", rec.Body.String())
	}

	assert.Equal(t, 3, m.CallCount("Authenticate"))
}
