package gas

import (
	"context"
	"net/http"
)

// requestScopeKey is the context key of a request's Scope.
type requestScopeKey struct{}

// RequestScope returns the request's Scope, which an App gives every
// request. It panics when r has none, such as a request not served by an
// App's router.
func RequestScope(r *http.Request) *Scope {
	s, ok := r.Context().Value(requestScopeKey{}).(*Scope)
	if !ok {
		panic("gas: no request scope; serve the request through an App's router")
	}

	return s
}

// WithRequestScope returns a copy of ctx that carries scope as the
// request scope, such as for a test that serves a handler without an App.
func WithRequestScope(ctx context.Context, scope *Scope) context.Context {
	return context.WithValue(ctx, requestScopeKey{}, scope)
}
