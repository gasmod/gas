// Package auth provides authentication, authorization, and credential
// management for the Gas ecosystem.
package auth

import (
	"context"
	"errors"
	"net/http"

	"github.com/gasmod/gas"
)

// Scheme constants identify the authentication method used by a principal.
const (
	// SchemeJWT is the scheme for JWT-authenticated principals.
	SchemeJWT = "jwt"
	// SchemeSession is the scheme for session-authenticated principals.
	SchemeSession = "session"
	// SchemeAPIKey is the scheme for API-key-authenticated principals.
	SchemeAPIKey = "apikey"
)

// Sentinel errors returned by authenticators and middleware.
var (
	// ErrUnauthenticated indicates that no valid credentials were provided.
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrForbidden indicates that the principal lacks permission for the action.
	ErrForbidden = errors.New("forbidden")
	// ErrCredentialsExpired indicates that the presented credentials have expired.
	ErrCredentialsExpired = errors.New("credentials expired")
	// ErrCredentialRevoked indicates that the presented credentials have been revoked.
	ErrCredentialRevoked = errors.New("credential revoked")
)

// BasePrincipal is a concrete implementation of gas.Principal.
type BasePrincipal struct {
	metadata     gas.PrincipalMetadata
	subject      string
	scheme       string
	credentialID string
}

var _ gas.Principal = (*BasePrincipal)(nil)

// NewPrincipal creates a new BasePrincipal. If meta is nil, an empty
// gas.BasePrincipalMetadata is used to avoid nil dereferences on
// Metadata().Value().
func NewPrincipal(subject, scheme, credentialID string, meta gas.PrincipalMetadata) *BasePrincipal {
	if meta == nil {
		meta = gas.BasePrincipalMetadata{}
	}
	return &BasePrincipal{
		subject:      subject,
		scheme:       scheme,
		credentialID: credentialID,
		metadata:     meta,
	}
}

// Subject returns the stable user identifier.
func (p *BasePrincipal) Subject() string { return p.subject }

// Scheme returns the authentication method (e.g. "jwt", "session", "apikey").
func (p *BasePrincipal) Scheme() string { return p.scheme }

// CredentialID returns the identifier for the specific credential used.
func (p *BasePrincipal) CredentialID() string { return p.credentialID }

// Metadata returns the principal's metadata.
func (p *BasePrincipal) Metadata() gas.PrincipalMetadata { return p.metadata }

// Chain is a composite authenticator that tries each authenticator in
// order. It satisfies gas.Authenticator.
type Chain []gas.Authenticator

var _ gas.Authenticator = Chain(nil)

// Authenticate tries each authenticator in order. It returns the first
// successful principal. If all fail, it returns the last error. If the
// chain is empty, it returns ErrUnauthenticated.
func (c Chain) Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error) {
	if len(c) == 0 {
		return nil, ErrUnauthenticated
	}

	var lastErr error
	for _, a := range c {
		principal, err := a.Authenticate(ctx, r)
		if err == nil {
			return principal, nil
		}
		lastErr = err
	}

	return nil, lastErr
}
