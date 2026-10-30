package session

import (
	"time"

	"github.com/gasmod/gas"
)

// Session is a session that Create or Rotate issued, as described in the
// package documentation. Its ID is the secret the cookie carries.
type Session struct {
	// CreatedAt is when the session was created. The session expires at
	// CreatedAt plus the MaxLifetime setting at the latest.
	CreatedAt time.Time
	// ExpiresAt is when the session expires unless it is used and
	// extended.
	ExpiresAt time.Time
	// LastActive is when the session's expiry was last set.
	LastActive time.Time
	// Metadata holds the values stored with the session, which its
	// principal carries.
	Metadata gas.BasePrincipalMetadata
	// ID is the session ID, the secret the cookie carries. It is stored
	// nowhere else.
	ID string
	// Subject is the user or client the session belongs to.
	Subject string
	// IPAddress is the remote address of the request that issued the
	// session.
	IPAddress string
	// UserAgent is the User-Agent of the request that issued the session.
	UserAgent string
}
