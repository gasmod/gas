package gas

import (
	"context"
	"net/http"
)

// Authenticator extracts a Principal from an HTTP request. Implementations
// may use JWTs, server-side sessions, API keys, or any other credential scheme.
type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request) (Principal, error)
}

// Authorizer decides whether a Principal is allowed to perform an action on a
// resource. It is a separate concern from authentication — an Authenticator
// identifies who the caller is, while an Authorizer enforces what they can do.
type Authorizer interface {
	Authorize(ctx context.Context, principal Principal, action, resource string) error
}

// PrincipalRevoker invalidates credentials associated with a Principal.
// Use Revoke to invalidate a single credential, RevokeAll to invalidate every
// credential for a subject, or RevokeAllByScheme to invalidate all credentials
// of a specific scheme (e.g. all sessions, all API keys) for a subject.
type PrincipalRevoker interface {
	Revoke(ctx context.Context, principal Principal) error
	RevokeAll(ctx context.Context, subject string) error
	RevokeAllByScheme(ctx context.Context, subject, scheme string) error
}

// Principal represents an authenticated identity. Subject is the stable user
// identifier, Scheme identifies the authentication method (e.g. "jwt",
// "session", "apikey"), and CredentialID is the specific credential instance
// (session ID, JWT jti, API key ID).
type Principal interface {
	Subject() string
	Scheme() string
	CredentialID() string
	Metadata() PrincipalMetadata
}

// PrincipalMetadata provides read-only access to arbitrary key-value metadata
// attached to a Principal at construction time.
type PrincipalMetadata interface {
	Value(key string) any
}

// BasePrincipalMetadata is a map-based implementation of PrincipalMetadata.
type BasePrincipalMetadata map[string]any

// Value returns the metadata value for the given key, or nil if not present.
func (m BasePrincipalMetadata) Value(key string) any {
	return m[key]
}

// MetadataValue is a type-safe helper that retrieves a typed value from
// PrincipalMetadata. It returns the value and true if the key exists and the
// type assertion succeeds, or the zero value and false otherwise.
func MetadataValue[T any](m PrincipalMetadata, key string) (T, bool) {
	if v, ok := m.Value(key).(T); ok {
		return v, true
	}
	var v T
	return v, false
}

type principalKey struct{}

// WithPrincipal stores a Principal in the given context.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

// PrincipalFromContext retrieves the Principal from the context, or nil if
// no principal is present.
func PrincipalFromContext(ctx context.Context) Principal {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	if !ok {
		return nil
	}
	return principal
}

// MustPrincipalFromContext retrieves the Principal from the context and panics
// if no principal is present.
func MustPrincipalFromContext(ctx context.Context) Principal {
	principal := PrincipalFromContext(ctx)
	if principal == nil {
		panic("gas: no principal in context")
	}
	return principal
}
