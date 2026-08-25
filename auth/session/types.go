package session

import (
	"time"

	"github.com/gasmod/gas"
)

// Session represents a user session with associated metadata, identifiers, and timestamps.
type Session struct {
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastActive time.Time
	Metadata   gas.BasePrincipalMetadata
	ID         string
	Subject    string
	IPAddress  string
	UserAgent  string
}
