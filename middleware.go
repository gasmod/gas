package gas

import (
	"context"
	"net/http"
)

// namedMiddleware is a registered named middleware and its owner.
type namedMiddleware struct {
	// fn is the middleware.
	fn func(http.Handler) http.Handler
	// service owns it.
	service Service
}

// Middleware is either a named middleware, resolved from the router's
// registry whenever the routing tree is built, or an inline middleware
// function.
type Middleware struct {
	// fn is the inline function, nil for a named middleware.
	fn func(http.Handler) http.Handler
	// name is the registry name, or the display name of an inline one.
	name string
}

// MiddlewareByName returns a Middleware resolved by name from the router's
// registry, as Router.Register put it there.
func MiddlewareByName(name string) Middleware {
	return Middleware{name: name}
}

// MiddlewareFunc returns an inline Middleware. It has no name, so
// Router.Routes and the route map leave it out; MiddlewareFuncWithName
// gives it one.
func MiddlewareFunc(fn func(http.Handler) http.Handler) Middleware {
	return Middleware{fn: fn}
}

// MiddlewareFuncWithName returns an inline Middleware whose name
// Router.Routes and the route map show with the routes it applies to.
func MiddlewareFuncWithName(name string, fn func(http.Handler) http.Handler) Middleware {
	return Middleware{fn: fn, name: name}
}

// requestScopeMiddleware gives each request its own Scope and closes it
// when the request ends. The response is written by then, so a Close
// error is logged through getLogger rather than returned.
func requestScopeMiddleware(
	container *ServiceContainer,
	getLogger func() Logger,
) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			scope := container.NewScope(ctx)

			// Close runs after the response is written, when a disconnected
			// client may already have canceled ctx; scoped services still
			// get to clean up.
			defer func() {
				err := scope.Close(context.WithoutCancel(ctx))
				if err != nil {
					getLogger().Error("request scope close failed").
						Str("method", r.Method).
						Str("path", r.URL.Path).
						Err("error", err).
						Send()
				}
			}()

			next.ServeHTTP(w, r.WithContext(WithRequestScope(ctx, scope)))
		})
	}
}
