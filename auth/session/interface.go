package session

import (
	"context"
	"net/http"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
)

// Provider is what Service offers, for code that wants to depend on an
// interface, such as to swap in a mock in its tests.
type Provider interface {
	auth.Authenticator
	auth.PrincipalRevoker

	// Create issues a new session for subject, as Service.Create does.
	Create(
		ctx context.Context,
		subject string,
		meta gas.BasePrincipalMetadata,
		r *http.Request,
	) (*Session, error)
	// Rotate replaces the request's session with a new one, as
	// Service.Rotate does.
	Rotate(ctx context.Context, r *http.Request) (*Session, error)
	// SetCookie writes the session's cookie, as Service.SetCookie does.
	SetCookie(w http.ResponseWriter, sess *Session) error
	// ClearCookie writes a cookie that removes the session cookie, as
	// Service.ClearCookie does.
	ClearCookie(w http.ResponseWriter) error
	// List returns the live sessions of subject, newest first, as
	// Service.List does.
	List(ctx context.Context, subject string) ([]Info, error)
	// RevokeByID deletes the session of subject that an Info.ID
	// names, as Service.RevokeByID does.
	RevokeByID(ctx context.Context, subject, id string) error
	// RevokeOthers deletes every session of the principal's subject but
	// the principal's own, as Service.RevokeOthers does.
	RevokeOthers(ctx context.Context, principal gas.Principal) error
}
