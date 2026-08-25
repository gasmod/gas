package db

import (
	"context"
	"time"
)

// sessionRow represents a row returned by getSession.
type sessionRow struct {
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastActive time.Time
	ID         string
	Subject    string
	Metadata   string
	IPAddress  string
	UserAgent  string
}

// querier abstracts the sqlc-generated query methods across dialects.
// Unexported — consumers interact with Service, not this interface.
type querier interface {
	getSession(ctx context.Context, id string) (*sessionRow, error)
	insertSession(ctx context.Context, id, subject, metadata, ipAddress, userAgent string, createdAt, expiresAt, lastActive time.Time) error
	extendSession(ctx context.Context, id string, expiresAt, lastActive time.Time) error
	deleteSession(ctx context.Context, id string) error
	deleteSessionsBySubject(ctx context.Context, subject string) error
	deleteExpiredSessions(ctx context.Context, before time.Time) (int64, error)
}
