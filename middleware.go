package gas

import (
	"context"
	"net/http"
)

type namedMiddleware struct {
	fn      func(http.Handler) http.Handler
	service string
}

// Middleware represents either a named middleware (resolved from the router's
// registry at apply time) or an inline middleware function.
type Middleware struct {
	fn   func(http.Handler) http.Handler
	name string
}

// MiddlewareByName creates a Middleware that will be resolved by name from the router's
// internal registry when applied.
func MiddlewareByName(name string) Middleware {
	return Middleware{name: name}
}

// MiddlewareFunc creates a Middleware that wraps an inline handler function directly.
// The middleware will be anonymous and omitted from the route map.
// Use MiddlewareFuncWithName to give it a display name.
func MiddlewareFunc(fn func(http.Handler) http.Handler) Middleware {
	return Middleware{fn: fn}
}

// MiddlewareFuncWithName creates a named inline Middleware. The name appears
// in the route map alongside routes that this middleware applies to.
func MiddlewareFuncWithName(name string, fn func(http.Handler) http.Handler) Middleware {
	return Middleware{fn: fn, name: name}
}

func requestScopeMiddleware(container *ServiceContainer) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scope := container.NewScope()
			defer func() { _ = scope.Close() }()
			ctx := context.WithValue(r.Context(), requestScopeKey{}, scope)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
