package auth

import (
	"context"
	"net/http"

	"github.com/gasmod/gas"
)

// Authenticator identifies the caller of an HTTP request, following the
// error contract in the package documentation.
type Authenticator interface {
	// Authenticate returns the request's principal, or nil and an error:
	// ErrNoCredentials when the request carries no credentials for it, an
	// error matching ErrUnauthenticated when they are not valid, and any
	// other error when it could not decide.
	Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error)
}

// Authorizer decides whether a principal may perform an action on a
// resource. Authentication tells who the caller is; an Authorizer decides
// what the caller may do.
type Authorizer interface {
	// Authorize returns nil to allow, an error matching ErrForbidden to
	// deny, and any other error when it could not decide.
	Authorize(ctx context.Context, principal gas.Principal, action, resource string) error
}

// PrincipalRevoker invalidates credentials, so that they no longer
// authenticate.
type PrincipalRevoker interface {
	// Revoke invalidates the credential the principal was authenticated
	// with.
	Revoke(ctx context.Context, principal gas.Principal) error
	// RevokeAll invalidates every credential of a subject.
	RevokeAll(ctx context.Context, subject string) error
	// RevokeAllByScheme invalidates every credential of a subject that has
	// the given scheme, such as all its sessions.
	RevokeAllByScheme(ctx context.Context, subject, scheme string) error
}
