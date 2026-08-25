package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gasmod/gas"
	auth "github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/authtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMiddleware(t *testing.T) {
	t.Run("auth succeeds sets principal in context and calls next handler", func(t *testing.T) {
		principal := auth.NewPrincipal("user-123", "jwt", "cred-abc", nil)

		mock := &authtest.MockAuthenticator{
			AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
				return principal, nil
			},
		}

		var called int32
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&called, 1)
			p := gas.PrincipalFromContext(r.Context())
			require.NotNil(t, p)
			assert.Equal(t, "user-123", p.Subject())
			assert.Equal(t, "jwt", p.Scheme())
			w.WriteHeader(http.StatusOK)
		})

		handler := auth.Middleware(mock)(next)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, int32(1), atomic.LoadInt32(&called))
		assert.Equal(t, 1, mock.CallCount("Authenticate"))
	})

	t.Run("auth fails returns 401 and does not call next handler", func(t *testing.T) {
		mock := &authtest.MockAuthenticator{
			AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
				return nil, auth.ErrUnauthenticated
			},
		}

		var called int32
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&called, 1)
		})

		handler := auth.Middleware(mock)(next)
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Equal(t, int32(0), atomic.LoadInt32(&called))
	})

	t.Run("auth returns ErrCredentialsExpired still returns 401 not 403", func(t *testing.T) {
		mock := &authtest.MockAuthenticator{
			AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
				return nil, auth.ErrCredentialsExpired
			},
		}

		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("next handler should not be called")
		})

		handler := auth.Middleware(mock)(next)
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("401 response body is exactly Unauthorized with newline and no internal details", func(t *testing.T) {
		mock := &authtest.MockAuthenticator{
			AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
				return nil, auth.ErrUnauthenticated
			},
		}

		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("next handler should not be called")
		})

		handler := auth.Middleware(mock)(next)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, "Unauthorized\n", rec.Body.String())
	})

	t.Run("auth returns nil principal and nil error calls next with nil principal in context", func(t *testing.T) {
		mock := &authtest.MockAuthenticator{
			// AuthenticateFn is nil, so returns (nil, nil)
		}

		var called int32
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&called, 1)
			p := gas.PrincipalFromContext(r.Context())
			assert.Nil(t, p)
			w.WriteHeader(http.StatusOK)
		})

		handler := auth.Middleware(mock)(next)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, int32(1), atomic.LoadInt32(&called))
	})

	t.Run("WithOnError receives the authentication error and controls the response", func(t *testing.T) {
		mock := &authtest.MockAuthenticator{
			AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
				return nil, auth.ErrCredentialsExpired
			},
		}

		var receivedErr error
		onError := func(w http.ResponseWriter, _ *http.Request, err error) {
			receivedErr = err
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"credentials expired"}`))
		}

		var called int32
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&called, 1)
		})

		handler := auth.Middleware(mock, auth.WithOnError(onError))(next)
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Equal(t, int32(0), atomic.LoadInt32(&called))
		assert.ErrorIs(t, receivedErr, auth.ErrCredentialsExpired)
		assert.Equal(t, "application/json", rec.Result().Header.Get("Content-Type"))
		assert.Equal(t, `{"error":"credentials expired"}`, rec.Body.String())
	})

	t.Run("401 response Content-Type is text/plain charset=utf-8", func(t *testing.T) {
		mock := &authtest.MockAuthenticator{
			AuthenticateFn: func(_ context.Context, _ *http.Request) (gas.Principal, error) {
				return nil, auth.ErrUnauthenticated
			},
		}

		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("next handler should not be called")
		})

		handler := auth.Middleware(mock)(next)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, "text/plain; charset=utf-8", rec.Result().Header.Get("Content-Type"))
	})
}

func TestRequireScheme(t *testing.T) {
	t.Run("no principal in context returns 403", func(t *testing.T) {
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("next handler should not be called")
		})

		handler := auth.RequireScheme("jwt")(next)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("principal present with matching scheme calls next handler", func(t *testing.T) {
		principal := auth.NewPrincipal("user-123", "jwt", "cred-abc", nil)

		var called int32
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&called, 1)
			w.WriteHeader(http.StatusOK)
		})

		handler := auth.RequireScheme("jwt")(next)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		ctx := gas.WithPrincipal(req.Context(), principal)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, int32(1), atomic.LoadInt32(&called))
	})

	t.Run("principal present with non-matching scheme returns 403", func(t *testing.T) {
		principal := auth.NewPrincipal("user-123", "apikey", "key-1", nil)

		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("next handler should not be called")
		})

		handler := auth.RequireScheme("jwt")(next)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		ctx := gas.WithPrincipal(req.Context(), principal)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("scheme comparison is case-sensitive JWT does not match jwt", func(t *testing.T) {
		principal := auth.NewPrincipal("user-123", "jwt", "cred-abc", nil)

		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("next handler should not be called")
		})

		handler := auth.RequireScheme("JWT")(next)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		ctx := gas.WithPrincipal(req.Context(), principal)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("empty scheme string with principal having empty scheme passes", func(t *testing.T) {
		principal := auth.NewPrincipal("user-123", "", "cred-abc", nil)

		var called int32
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&called, 1)
			w.WriteHeader(http.StatusOK)
		})

		handler := auth.RequireScheme("")(next)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		ctx := gas.WithPrincipal(req.Context(), principal)
		req = req.WithContext(ctx)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, int32(1), atomic.LoadInt32(&called))
	})

	t.Run("403 response body is exactly Forbidden with newline", func(t *testing.T) {
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("next handler should not be called")
		})

		handler := auth.RequireScheme("jwt")(next)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, "Forbidden\n", rec.Body.String())
	})
}
