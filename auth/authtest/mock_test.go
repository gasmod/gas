package authtest_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gasmod/gas"
	auth "github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/authtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockAuthenticator_NilFn(t *testing.T) {
	t.Run("returns nil principal and nil error when AuthenticateFn is nil", func(t *testing.T) {
		m := &authtest.MockAuthenticator{}
		r := httptest.NewRequest(http.MethodGet, "/", nil)

		principal, err := m.Authenticate(context.Background(), r)

		assert.NoError(t, err)
		assert.Nil(t, principal)
	})
}

func TestMockAuthenticator_WithFn(t *testing.T) {
	t.Run("delegates to AuthenticateFn when set", func(t *testing.T) {
		want := auth.NewPrincipal("user-1", "test", "cred-1", nil)
		m := &authtest.MockAuthenticator{
			AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
				return want, nil
			},
		}
		r := httptest.NewRequest(http.MethodGet, "/", nil)

		got, err := m.Authenticate(context.Background(), r)

		require.NoError(t, err)
		assert.Equal(t, want, got)
	})
}

func TestMockAuthenticator_RecordsCalls(t *testing.T) {
	t.Run("records calls with correct method and args", func(t *testing.T) {
		m := &authtest.MockAuthenticator{}
		ctx := context.Background()
		r := httptest.NewRequest(http.MethodPost, "/login", nil)

		_, _ = m.Authenticate(ctx, r)

		require.Len(t, m.Calls, 1)
		assert.Equal(t, "Authenticate", m.Calls[0].Method)
		require.Len(t, m.Calls[0].Args, 2)
		assert.Equal(t, ctx, m.Calls[0].Args[0])
		assert.Equal(t, r, m.Calls[0].Args[1])
	})
}

func TestMockAuthenticator_Reset(t *testing.T) {
	t.Run("clears recorded calls", func(t *testing.T) {
		m := &authtest.MockAuthenticator{}
		r := httptest.NewRequest(http.MethodGet, "/", nil)

		_, _ = m.Authenticate(context.Background(), r)
		_, _ = m.Authenticate(context.Background(), r)
		require.Equal(t, 2, m.CallCount("Authenticate"))

		m.Reset()

		assert.Empty(t, m.Calls)
		assert.Equal(t, 0, m.CallCount("Authenticate"))
	})
}

func TestMockAuthenticator_CallCount_NonExistent(t *testing.T) {
	t.Run("returns 0 for method that was never called", func(t *testing.T) {
		m := &authtest.MockAuthenticator{}

		assert.Equal(t, 0, m.CallCount("DoesNotExist"))
	})
}

func TestMockAuthenticator_Concurrent(t *testing.T) {
	t.Run("concurrent calls cause no data race", func(t *testing.T) {
		m := &authtest.MockAuthenticator{}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		ctx := context.Background()

		var wg sync.WaitGroup
		for range 50 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = m.Authenticate(ctx, r)
			}()
		}
		wg.Wait()

		assert.Equal(t, 50, m.CallCount("Authenticate"))
	})
}

func TestMockAuthorizer_NilFn(t *testing.T) {
	t.Run("returns nil when AuthorizeFn is nil", func(t *testing.T) {
		m := &authtest.MockAuthorizer{}
		p := auth.NewPrincipal("user-1", "test", "cred-1", nil)

		err := m.Authorize(context.Background(), p, "read", "resource")

		assert.NoError(t, err)
	})
}

func TestMockAuthorizer_WithFn(t *testing.T) {
	t.Run("returns error from AuthorizeFn", func(t *testing.T) {
		wantErr := errors.New("access denied")
		m := &authtest.MockAuthorizer{
			AuthorizeFn: func(_ context.Context, _ gas.Principal, _, _ string) error {
				return wantErr
			},
		}
		p := auth.NewPrincipal("user-1", "test", "cred-1", nil)

		err := m.Authorize(context.Background(), p, "write", "secret")

		assert.ErrorIs(t, err, wantErr)
	})
}

func TestMockAuthorizer_RecordsCalls(t *testing.T) {
	t.Run("records calls with correct method and args", func(t *testing.T) {
		m := &authtest.MockAuthorizer{}
		ctx := context.Background()
		p := auth.NewPrincipal("user-1", "test", "cred-1", nil)

		_ = m.Authorize(ctx, p, "delete", "posts")

		require.Len(t, m.Calls, 1)
		assert.Equal(t, "Authorize", m.Calls[0].Method)
		require.Len(t, m.Calls[0].Args, 4)
		assert.Equal(t, ctx, m.Calls[0].Args[0])
		assert.Equal(t, p, m.Calls[0].Args[1])
		assert.Equal(t, "delete", m.Calls[0].Args[2])
		assert.Equal(t, "posts", m.Calls[0].Args[3])
	})
}

func TestMockRevoker_NilFns(t *testing.T) {
	t.Run("all methods return nil when fns are nil", func(t *testing.T) {
		m := &authtest.MockRevoker{}
		ctx := context.Background()
		p := auth.NewPrincipal("user-1", "test", "cred-1", nil)

		assert.NoError(t, m.Revoke(ctx, p))
		assert.NoError(t, m.RevokeAll(ctx, "user-1"))
		assert.NoError(t, m.RevokeAllByScheme(ctx, "user-1", "session"))
	})
}

func TestMockRevoker_WithFns(t *testing.T) {
	t.Run("delegates to all fns correctly", func(t *testing.T) {
		revokeErr := errors.New("revoke failed")
		revokeAllErr := errors.New("revoke all failed")
		revokeAllBySchemeErr := errors.New("revoke all by scheme failed")

		m := &authtest.MockRevoker{
			RevokeFn: func(_ context.Context, _ gas.Principal) error {
				return revokeErr
			},
			RevokeAllFn: func(_ context.Context, _ string) error {
				return revokeAllErr
			},
			RevokeAllBySchemeFn: func(_ context.Context, _, _ string) error {
				return revokeAllBySchemeErr
			},
		}
		ctx := context.Background()
		p := auth.NewPrincipal("user-1", "test", "cred-1", nil)

		assert.ErrorIs(t, m.Revoke(ctx, p), revokeErr)
		assert.ErrorIs(t, m.RevokeAll(ctx, "user-1"), revokeAllErr)
		assert.ErrorIs(t, m.RevokeAllByScheme(ctx, "user-1", "session"), revokeAllBySchemeErr)
	})
}

func TestMockRevoker_RecordsCalls(t *testing.T) {
	t.Run("records separate methods correctly", func(t *testing.T) {
		m := &authtest.MockRevoker{}
		ctx := context.Background()
		p := auth.NewPrincipal("user-1", "test", "cred-1", nil)

		_ = m.Revoke(ctx, p)
		_ = m.RevokeAll(ctx, "user-1")
		_ = m.RevokeAllByScheme(ctx, "user-1", "session")

		require.Len(t, m.Calls, 3)
		assert.Equal(t, "Revoke", m.Calls[0].Method)
		assert.Equal(t, "RevokeAll", m.Calls[1].Method)
		assert.Equal(t, "RevokeAllByScheme", m.Calls[2].Method)
	})
}

func TestMockRevoker_CallCount(t *testing.T) {
	t.Run("distinguishes between Revoke, RevokeAll, and RevokeAllByScheme", func(t *testing.T) {
		m := &authtest.MockRevoker{}
		ctx := context.Background()
		p := auth.NewPrincipal("user-1", "test", "cred-1", nil)

		_ = m.Revoke(ctx, p)
		_ = m.Revoke(ctx, p)
		_ = m.RevokeAll(ctx, "user-1")
		_ = m.RevokeAllByScheme(ctx, "user-1", "session")
		_ = m.RevokeAllByScheme(ctx, "user-1", "apikey")
		_ = m.RevokeAllByScheme(ctx, "user-1", "jwt")

		assert.Equal(t, 2, m.CallCount("Revoke"))
		assert.Equal(t, 1, m.CallCount("RevokeAll"))
		assert.Equal(t, 3, m.CallCount("RevokeAllByScheme"))
	})
}

func TestMockRevoker_Concurrent(t *testing.T) {
	t.Run("concurrent calls cause no data race", func(t *testing.T) {
		m := &authtest.MockRevoker{}
		ctx := context.Background()
		p := auth.NewPrincipal("user-1", "test", "cred-1", nil)

		var wg sync.WaitGroup
		for range 50 {
			wg.Add(3)
			go func() {
				defer wg.Done()
				_ = m.Revoke(ctx, p)
			}()
			go func() {
				defer wg.Done()
				_ = m.RevokeAll(ctx, "user-1")
			}()
			go func() {
				defer wg.Done()
				_ = m.RevokeAllByScheme(ctx, "user-1", "session")
			}()
		}
		wg.Wait()

		assert.Equal(t, 50, m.CallCount("Revoke"))
		assert.Equal(t, 50, m.CallCount("RevokeAll"))
		assert.Equal(t, 50, m.CallCount("RevokeAllByScheme"))
	})
}
