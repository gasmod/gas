package session

import (
	"context"
	"net/http"

	"github.com/gasmod/gas"
)

// Provider is an interface for managing session-based authentication and authorization.
type Provider interface {
	// Authenticate reads a session ID from the configured cookie, looks it up
	// in the database, validates expiry, and optionally extends the TTL.
	Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error)
	// Revoke deletes a session by the principal's credential ID.
	Revoke(ctx context.Context, principal gas.Principal) error
	// RevokeAll deletes all sessions for the given subject.
	RevokeAll(ctx context.Context, subject string) error
	// RevokeAllByScheme delegates to RevokeAll if the scheme is "session",
	// otherwise it is a no-op.
	RevokeAllByScheme(ctx context.Context, subject, scheme string) error
	// Create generates a cryptographically random session ID, stores the session
	// in the database, and returns it. The caller is responsible for calling
	// SetCookie afterward. The *http.Request is used to capture IP and user agent.
	Create(ctx context.Context, subject string, meta gas.BasePrincipalMetadata, r *http.Request) (*Session, error)
	// SetCookie writes the session cookie to the response.
	SetCookie(w http.ResponseWriter, session *Session)
	// ClearCookie writes an expired cookie to the response, effectively removing it.
	ClearCookie(w http.ResponseWriter)
}
