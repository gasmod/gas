package gas

import "net/http"

type namedMiddleware struct {
	fn     func(http.Handler) http.Handler
	module string
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
func MiddlewareFunc(fn func(http.Handler) http.Handler) Middleware {
	return Middleware{fn: fn}
}
