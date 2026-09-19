package gas

import (
	"context"
	"net/http"
)

type requestScopeKey struct{}

// RequestScope returns the per-request Scope from the request context.
// Panics if called outside the scope middleware (i.e. before InitServices
// installs it, or on a non-App-managed handler).
func RequestScope(r *http.Request) *Scope {
	s, ok := r.Context().Value(requestScopeKey{}).(*Scope)
	if !ok {
		panic("gas: no request scope in context — is the request served by an App-managed router?")
	}
	return s
}

// WithRequestScope adds a Scope instance to the context using a custom key for managing scoped service lifetimes.
// Useful for testing and managing scoped service lifetimes within request contexts.
func WithRequestScope(ctx context.Context, scope *Scope) context.Context {
	return context.WithValue(ctx, requestScopeKey{}, scope)
}
