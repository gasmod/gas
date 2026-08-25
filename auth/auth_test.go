package auth_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gasmod/gas"
	auth "github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/authtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBasePrincipal(t *testing.T) {
	t.Run("nil metadata returns non-nil metadata with nil values", func(t *testing.T) {
		p := auth.NewPrincipal("sub", "scheme", "cred", nil)

		require.NotNil(t, p.Metadata())
		assert.Nil(t, p.Metadata().Value("any"))
		assert.Nil(t, p.Metadata().Value(""))
		assert.Nil(t, p.Metadata().Value("nonexistent"))
	})

	t.Run("empty strings round-trip through accessors", func(t *testing.T) {
		p := auth.NewPrincipal("", "", "", nil)

		assert.Equal(t, "", p.Subject())
		assert.Equal(t, "", p.Scheme())
		assert.Equal(t, "", p.CredentialID())
	})

	t.Run("unicode emoji and null-byte round-trip correctly", func(t *testing.T) {
		subject := "user-\U0001F600-\x00-end"
		scheme := "\U0001F525fire\U0001F525"
		credentialID := "cred\x00with\x00nulls"

		p := auth.NewPrincipal(subject, scheme, credentialID, nil)

		assert.Equal(t, subject, p.Subject())
		assert.Equal(t, scheme, p.Scheme())
		assert.Equal(t, credentialID, p.CredentialID())
	})

	t.Run("metadata mutation after construction is visible through principal", func(t *testing.T) {
		meta := gas.BasePrincipalMetadata{"key": "original"}
		p := auth.NewPrincipal("sub", "scheme", "cred", meta)

		assert.Equal(t, "original", p.Metadata().Value("key"))

		// Mutate the original map.
		meta["key"] = "mutated"
		meta["new"] = "added"

		assert.Equal(t, "mutated", p.Metadata().Value("key"),
			"principal should see mutation because metadata is a shared reference")
		assert.Equal(t, "added", p.Metadata().Value("new"),
			"principal should see newly added keys")
	})

	t.Run("populated metadata returns correct values", func(t *testing.T) {
		meta := gas.BasePrincipalMetadata{
			"role":    "admin",
			"org_id":  42,
			"enabled": true,
		}
		p := auth.NewPrincipal("alice", "jwt", "tok-123", meta)

		assert.Equal(t, "alice", p.Subject())
		assert.Equal(t, "jwt", p.Scheme())
		assert.Equal(t, "tok-123", p.CredentialID())
		assert.Equal(t, "admin", p.Metadata().Value("role"))
		assert.Equal(t, 42, p.Metadata().Value("org_id"))
		assert.Equal(t, true, p.Metadata().Value("enabled"))
		assert.Nil(t, p.Metadata().Value("missing"))
	})
}

func TestChain(t *testing.T) {
	t.Run("empty chain returns ErrUnauthenticated", func(t *testing.T) {
		chain := auth.Chain{}

		principal, err := chain.Authenticate(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil))

		assert.Nil(t, principal)
		assert.ErrorIs(t, err, auth.ErrUnauthenticated)
	})

	t.Run("single authenticator succeeds", func(t *testing.T) {
		expected := auth.NewPrincipal("user1", "bearer", "cred1", nil)
		mock := &authtest.MockAuthenticator{
			AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
				return expected, nil
			},
		}
		chain := auth.Chain{mock}

		principal, err := chain.Authenticate(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil))

		require.NoError(t, err)
		assert.Equal(t, expected, principal)
		assert.Equal(t, 1, mock.CallCount("Authenticate"))
	})

	t.Run("single authenticator fails returns its error", func(t *testing.T) {
		specificErr := errors.New("token expired")
		mock := &authtest.MockAuthenticator{
			AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
				return nil, specificErr
			},
		}
		chain := auth.Chain{mock}

		principal, err := chain.Authenticate(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil))

		assert.Nil(t, principal)
		assert.ErrorIs(t, err, specificErr)
	})

	t.Run("first fails second succeeds third never called", func(t *testing.T) {
		expected := auth.NewPrincipal("user2", "apikey", "key-1", nil)

		first := &authtest.MockAuthenticator{
			AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
				return nil, errors.New("first failed")
			},
		}
		second := &authtest.MockAuthenticator{
			AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
				return expected, nil
			},
		}
		third := &authtest.MockAuthenticator{
			AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
				t.Fatal("third authenticator should not be called")
				return nil, nil
			},
		}
		chain := auth.Chain{first, second, third}

		principal, err := chain.Authenticate(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil))

		require.NoError(t, err)
		assert.Equal(t, expected, principal)
		assert.Equal(t, 1, first.CallCount("Authenticate"))
		assert.Equal(t, 1, second.CallCount("Authenticate"))
		assert.Equal(t, 0, third.CallCount("Authenticate"))
	})

	t.Run("all three fail returns last error", func(t *testing.T) {
		errFirst := errors.New("first error")
		errSecond := errors.New("second error")
		errThird := errors.New("third error")

		first := &authtest.MockAuthenticator{
			AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
				return nil, errFirst
			},
		}
		second := &authtest.MockAuthenticator{
			AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
				return nil, errSecond
			},
		}
		third := &authtest.MockAuthenticator{
			AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
				return nil, errThird
			},
		}
		chain := auth.Chain{first, second, third}

		principal, err := chain.Authenticate(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil))

		assert.Nil(t, principal)
		assert.ErrorIs(t, err, errThird, "should return the last error, not the first")
		assert.NotErrorIs(t, err, errFirst)
		assert.NotErrorIs(t, err, errSecond)
	})

	t.Run("first returns nil principal and nil error treated as success", func(t *testing.T) {
		first := &authtest.MockAuthenticator{
			// AuthenticateFn is nil, so it returns (nil, nil).
		}
		second := &authtest.MockAuthenticator{
			AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
				t.Fatal("second authenticator should not be called when first succeeds")
				return nil, nil
			},
		}
		chain := auth.Chain{first, second}

		principal, err := chain.Authenticate(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil))

		require.NoError(t, err)
		assert.Nil(t, principal, "nil principal with nil error is a valid success")
		assert.Equal(t, 1, first.CallCount("Authenticate"))
		assert.Equal(t, 0, second.CallCount("Authenticate"))
	})

	t.Run("chain with nil request still works if authenticators handle it", func(t *testing.T) {
		expected := auth.NewPrincipal("ctx-only", "internal", "none", nil)
		mock := &authtest.MockAuthenticator{
			AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
				return expected, nil
			},
		}
		chain := auth.Chain{mock}

		principal, err := chain.Authenticate(context.Background(), nil)

		require.NoError(t, err)
		assert.Equal(t, expected, principal)
	})

	t.Run("large chain of 100 failing authenticators returns last error", func(t *testing.T) {
		const n = 100
		authenticators := make([]gas.Authenticator, n)
		for i := range n {
			authenticators[i] = &authtest.MockAuthenticator{
				AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
					return nil, fmt.Errorf("error-%d", i)
				},
			}
		}
		chain := auth.Chain(authenticators)

		principal, err := chain.Authenticate(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil))

		assert.Nil(t, principal)
		require.Error(t, err)
		assert.Equal(t, "error-99", err.Error(), "should return the last authenticator's error")

		// Verify every authenticator was called exactly once.
		for i, a := range authenticators {
			mock := a.(*authtest.MockAuthenticator)
			assert.Equal(t, 1, mock.CallCount("Authenticate"), "authenticator %d should be called once", i)
		}
	})
}
