package gas

import (
	"context"
	"reflect"
)

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

// principalKey is the context key WithPrincipal stores the Principal under.
type principalKey struct{}

// WithPrincipal stores a Principal in the given context.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

// PrincipalFromContext retrieves the Principal from the context, or nil if
// no principal is present. A typed-nil Principal (a nil pointer of a type
// implementing Principal) counts as absent and also yields nil, so a caller's
// nil check cannot pass on it.
func PrincipalFromContext(ctx context.Context) Principal {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	if !ok || isNilPointer(principal) {
		return nil
	}
	return principal
}

// isNilPointer reports whether p holds a nil pointer.
func isNilPointer(p Principal) bool {
	v := reflect.ValueOf(p)
	return v.Kind() == reflect.Pointer && v.IsNil()
}
