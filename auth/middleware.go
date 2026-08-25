package auth

import (
	"net/http"

	"github.com/gasmod/gas"
)

// MiddlewareOption configures the behavior of Middleware.
type MiddlewareOption func(*middlewareOptions)

type middlewareOptions struct {
	onError func(w http.ResponseWriter, r *http.Request, err error)
}

// WithOnError sets a custom error handler that is called when
// authentication fails. The handler receives the original error and is
// responsible for writing the HTTP response. When not set, the middleware
// writes a plain 401 Unauthorized response.
func WithOnError(fn func(w http.ResponseWriter, r *http.Request, err error)) MiddlewareOption {
	return func(o *middlewareOptions) {
		o.onError = fn
	}
}

// Middleware extracts and validates credentials from the request using
// the given authenticator. On success it sets the principal in the request
// context via gas.WithPrincipal and calls the next handler. On failure it
// invokes the OnError handler or writes a 401 Unauthorized response.
func Middleware(provider gas.Authenticator, opts ...MiddlewareOption) func(http.Handler) http.Handler {
	if provider == nil {
		panic("auth: nil authenticator")
	}

	o := middlewareOptions{
		onError: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		},
	}
	for _, opt := range opts {
		opt(&o)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, err := provider.Authenticate(r.Context(), r)
			if err != nil {
				o.onError(w, r, err)
				return
			}

			ctx := gas.WithPrincipal(r.Context(), principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireScheme enforces that the authenticated principal uses a specific
// authentication scheme (e.g. "jwt", "session", "apikey"). It reads the
// principal from context. If absent or the scheme doesn't match, it writes
// a 403 Forbidden response.
func RequireScheme(scheme string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := gas.PrincipalFromContext(r.Context())
			if principal == nil || principal.Scheme() != scheme {
				http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
