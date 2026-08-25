package jwt

import (
	"context"
	"net/http"
	"time"

	"github.com/gasmod/gas"
)

// Provider defines an interface for JWT authentication and signing operations.
type Provider interface {
	// Authenticate reads a Bearer token from the Authorization header, verifies
	// it, and returns a principal with scheme "jwt".
	Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error)
	// Sign creates a signed JWT with the given subject and custom claims using
	// the default expiry.
	Sign(subject string, claims map[string]any) (string, error)
	// SignWithExpiry creates a signed JWT with the given subject, custom claims,
	// and explicit expiry duration.
	SignWithExpiry(subject string, claims map[string]any, expiry time.Duration) (string, error)
	// Verify parses and validates a JWT string, returning the extracted claims.
	Verify(tokenString string) (*TokenClaims, error)
}
