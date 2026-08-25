package token

import (
	"context"
	"time"
)

// Provider defines the contract for managing and validating single-use tokens.
type Provider interface {
	// Issue generates a random token, stores its hash with purpose, subject, and
	// expiry, and returns the raw token. If ttl is 0, DefaultTTL is used.
	Issue(ctx context.Context, subject, purpose string, ttl time.Duration) (string, error)
	// Verify hashes the raw token, looks it up by hash, unconditionally deletes it,
	// then checks purpose match and expiry. Returns the subject on success.
	//
	// The token is deleted before validation so that a single presentation always
	// consumes it — regardless of whether the caller supplied the wrong purpose or
	// the token has expired. This keeps a simple invariant: once the raw secret has
	// been presented, it cannot be reused.
	Verify(ctx context.Context, rawToken, purpose string) (subject string, err error)
	// Revoke deletes a specific token by its raw value.
	Revoke(ctx context.Context, rawToken string) error
	// RevokeAllByPurpose deletes all tokens for a subject with the given purpose.
	RevokeAllByPurpose(ctx context.Context, subject, purpose string) error
}
