package session

import (
	"time"

	"github.com/gasmod/gas"
)

// Session is a session that Create or Rotate issued, as described in the
// package documentation. Its ID is the secret the client sends back.
type Session struct {
	// CreatedAt is when the session was created. The session expires at
	// CreatedAt plus the MaxLifetime setting at the latest.
	CreatedAt time.Time
	// ExpiresAt is when the session expires unless it is used and
	// extended.
	ExpiresAt time.Time
	// LastActive is when the session was last used, as last recorded:
	// when it was created or rotated, when Authenticate extended it, and,
	// with ActivityInterval set, when Authenticate found the last record
	// at least that old. See the package documentation.
	LastActive time.Time
	// Metadata holds the values stored with the session, which its
	// principal carries.
	Metadata gas.BasePrincipalMetadata
	// ID is the session ID, the secret the client sends back in the
	// cookie or, with AcceptBearer, in a Bearer header. The service
	// stores only its hash.
	ID string
	// Subject is the user or client the session belongs to.
	Subject string
	// IPAddress is the remote address of the request that issued the
	// session.
	IPAddress string
	// UserAgent is the User-Agent of the request that issued the session.
	UserAgent string
}

// Info describes a live session, as List returns it. It holds the
// hash of the session ID, never the ID, so it is safe to show to the
// session's user and to send back to revoke it with RevokeByID.
type Info struct {
	// CreatedAt is when the session was created.
	CreatedAt time.Time
	// ExpiresAt is when the session expires unless it is used and
	// extended.
	ExpiresAt time.Time
	// LastActive is when the session was last used, as last recorded, as
	// Session.LastActive describes.
	LastActive time.Time
	// Metadata holds the values stored with the session. It is never nil.
	Metadata gas.BasePrincipalMetadata
	// ID identifies the session: the SHA-256 hex of its session ID, which
	// is also the CredentialID of the principal Authenticate returns for
	// it. It is not the session ID, and Rotate changes it.
	ID string
	// Subject is the user or client the session belongs to.
	Subject string
	// IPAddress is the remote address of the request that issued or last
	// rotated the session.
	IPAddress string
	// UserAgent is the User-Agent of the request that issued or last
	// rotated the session.
	UserAgent string
}
