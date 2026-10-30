package auth

import (
	"errors"
	"fmt"
)

// MaxValueLength is the most bytes a subject, an API key name or a token
// purpose may hold, as described in the package documentation.
const MaxValueLength = 255

var (
	// ErrUnauthenticated indicates that a request presented credentials
	// that are not valid. ErrCredentialsExpired and ErrCredentialRevoked
	// wrap it, so it matches every way credentials can be denied.
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrNoCredentials indicates that a request carries no credentials for
	// an authenticator, such as a missing header or cookie. It does not
	// wrap ErrUnauthenticated.
	ErrNoCredentials = errors.New("no credentials")
	// ErrCredentialsExpired indicates that a request presented credentials
	// that were valid once and have expired. It wraps ErrUnauthenticated.
	ErrCredentialsExpired = fmt.Errorf("%w: credentials expired", ErrUnauthenticated)
	// ErrCredentialRevoked indicates that a request presented a credential
	// that has been revoked. It wraps ErrUnauthenticated.
	ErrCredentialRevoked = fmt.Errorf("%w: credential revoked", ErrUnauthenticated)
	// ErrForbidden indicates that an Authorizer denied a principal an
	// action on a resource.
	ErrForbidden = errors.New("forbidden")
	// ErrNilPrincipal indicates that a method that acts on a principal,
	// such as Revoke, got a nil one or a nil pointer.
	ErrNilPrincipal = errors.New("nil principal")
	// ErrSchemeMismatch indicates that a method that acts on a principal,
	// such as Revoke, got one whose scheme is not the service's.
	ErrSchemeMismatch = errors.New("principal scheme mismatch")
	// ErrEmptySubject indicates that a credential was to be issued for an
	// empty subject.
	ErrEmptySubject = errors.New("empty subject")
	// ErrValueTooLong indicates that a value, such as a subject, holds
	// more than MaxValueLength bytes.
	ErrValueTooLong = errors.New("value too long")
)

// CheckLength returns nil when value holds at most MaxValueLength bytes,
// and otherwise an error wrapping ErrValueTooLong that names field, never
// the value.
func CheckLength(field, value string) error {
	if len(value) <= MaxValueLength {
		return nil
	}

	return fmt.Errorf("%w: %s is longer than %d bytes", ErrValueTooLong, field, MaxValueLength)
}
