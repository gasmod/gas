package token

import (
	"context"
	"time"
)

// Provider is what Service offers once initialized, for code that wants to
// depend on an interface, such as to swap in a fake in tests. The methods
// behave as described in the package documentation.
type Provider interface {
	// Issue stores a new token for subject and purpose that lives for ttl,
	// or for the configured default when ttl is 0, and returns it.
	Issue(ctx context.Context, subject, purpose string, ttl time.Duration) (string, error)
	// Verify consumes the token and returns its subject when it was issued
	// for purpose and has not expired.
	Verify(ctx context.Context, rawToken, purpose string) (subject string, err error)
	// Revoke deletes the token; a token that is not stored is not an
	// error.
	Revoke(ctx context.Context, rawToken string) error
	// RevokeAllByPurpose deletes every token of subject issued for purpose.
	RevokeAllByPurpose(ctx context.Context, subject, purpose string) error
}
