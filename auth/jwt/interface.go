package jwt

import (
	"time"

	"github.com/gasmod/gas/auth"
)

// Provider is what Service provides: authenticating requests, signing
// tokens and verifying them.
type Provider interface {
	// Authenticator authenticates a request by its bearer token.
	auth.Authenticator
	// Sign returns a token for subject with claims, valid for the
	// configured expiry.
	Sign(subject string, claims map[string]any) (string, error)
	// SignWithExpiry returns a token for subject with claims, valid for
	// expiry.
	SignWithExpiry(subject string, claims map[string]any, expiry time.Duration) (string, error)
	// Verify checks a token and returns its claims.
	Verify(token string) (*TokenClaims, error)
}
