package auth

import (
	"errors"
	"net/http"

	"github.com/gasmod/gas"
)

// RequireScheme returns middleware that lets a request through only when
// Middleware has authenticated it with the given scheme, as described in
// the package documentation: 401 without a principal, 403 for another
// scheme.
func RequireScheme(scheme string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := gas.PrincipalFromContext(r.Context())
			if !ok {
				writeError(w, r, gas.Unauthorized("unauthorized"))

				return
			}

			if principal.Scheme() != scheme {
				writeError(w, r, gas.Forbidden("forbidden"))

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireAuthorization returns middleware that lets a request through only
// when a allows its principal the action on the resource, as described in
// the package documentation: 401 without a principal, 403 when a denies,
// 500 when a fails.
//
// RequireAuthorization panics when a is nil or a nil pointer, since that
// is a wiring mistake best caught at startup.
func RequireAuthorization(a Authorizer, action, resource string) func(http.Handler) http.Handler {
	if isNilPointer(a) {
		panic("gas: auth.RequireAuthorization: nil Authorizer")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := gas.PrincipalFromContext(r.Context())
			if !ok {
				writeError(w, r, gas.Unauthorized("unauthorized"))

				return
			}

			err := a.Authorize(r.Context(), principal, action, resource)

			switch {
			case err == nil:
				next.ServeHTTP(w, r)
			case errors.Is(err, ErrForbidden):
				writeError(w, r, gas.Forbidden("forbidden"))
			default:
				logFailure(r, "authorization failed", err)
				writeError(w, r, gas.Internal("internal server error"))
			}
		})
	}
}
