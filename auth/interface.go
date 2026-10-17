package auth

import (
	"context"
	"net/http"

	"github.com/gasmod/gas"
)

// Authenticator extracts a gas.Principal from an HTTP request. Implementations
// may use JWTs, server-side sessions, API keys, or any other credential scheme.
// Middleware runs an Authenticator and stores the principal it returns on the
// request context, where handlers read it with gas.Context.Principal.
//
// An implementation returns a non-nil principal on success and an error,
// such as ErrUnauthenticated, otherwise. Middleware and Chain treat a nil
// principal with a nil error as ErrUnauthenticated, never as a success.
type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error)
}

// Authorizer decides whether a gas.Principal is allowed to perform an action
// on a resource. It is a separate concern from authentication: an
// Authenticator identifies who the caller is, while an Authorizer enforces
// what they can do.
type Authorizer interface {
	Authorize(ctx context.Context, principal gas.Principal, action, resource string) error
}

// PrincipalRevoker invalidates credentials associated with a gas.Principal.
// Use Revoke to invalidate a single credential, RevokeAll to invalidate every
// credential for a subject, or RevokeAllByScheme to invalidate all credentials
// of a specific scheme (e.g. all sessions, all API keys) for a subject.
type PrincipalRevoker interface {
	Revoke(ctx context.Context, principal gas.Principal) error
	RevokeAll(ctx context.Context, subject string) error
	RevokeAllByScheme(ctx context.Context, subject, scheme string) error
}
