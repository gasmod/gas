package auth

import (
	"github.com/gasmod/gas"
)

// Scheme constants name the authentication method a principal came from,
// as gas.Principal.Scheme returns it.
const (
	// SchemeJWT is the scheme of principals authenticated by a JWT.
	SchemeJWT = "jwt"
	// SchemeSession is the scheme of principals authenticated by a
	// server-side session.
	SchemeSession = "session"
	// SchemeAPIKey is the scheme of principals authenticated by an API
	// key.
	SchemeAPIKey = "apikey"
)

// BasePrincipal is a gas.Principal holding fixed values, as described in
// the package documentation.
type BasePrincipal struct {
	// metadata is returned by Metadata; it is never nil.
	metadata gas.PrincipalMetadata
	// subject is returned by Subject.
	subject string
	// scheme is returned by Scheme.
	scheme string
	// credentialID is returned by CredentialID.
	credentialID string
}

var _ gas.Principal = (*BasePrincipal)(nil)

// NewPrincipal returns a BasePrincipal with the given values. A nil meta
// becomes an empty gas.BasePrincipalMetadata, so Metadata().Value never
// dereferences nil. A non-nil meta is kept as given, not copied.
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

// Subject returns the stable identifier of the user or client.
func (p *BasePrincipal) Subject() string { return p.subject }

// Scheme returns the authentication method, such as SchemeJWT.
func (p *BasePrincipal) Scheme() string { return p.scheme }

// CredentialID returns the identifier of the credential used, such as a
// session's or an API key's ID.
func (p *BasePrincipal) CredentialID() string { return p.credentialID }

// Metadata returns the principal's metadata. It is never nil.
func (p *BasePrincipal) Metadata() gas.PrincipalMetadata { return p.metadata }
