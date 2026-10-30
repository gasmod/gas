package sessionstore

import (
	"context"
	"time"
)

// sessionRow is a session as the dialect adapters read and write it, with
// metadata as JSON text.
type sessionRow struct {
	// CreatedAt is when the session was created.
	CreatedAt time.Time
	// ExpiresAt is when the session stops being valid.
	ExpiresAt time.Time
	// LastActive is when the session's expiry was last moved.
	LastActive time.Time
	// IDHash is the SHA-256 hex of the session ID, the table's key.
	IDHash string
	// Subject is the user or client the session belongs to.
	Subject string
	// Metadata is the JSON stored with the session.
	Metadata string
	// IPAddress is the client address the session was created from.
	IPAddress string
	// UserAgent is the User-Agent the session was created with.
	UserAgent string
}

// querier is what each dialect adapter implements over its sqlc queries.
// getSession returns sql.ErrNoRows, unwrapped, when no row matches; the
// Store wraps every error.
type querier interface {
	getSession(ctx context.Context, idHash string) (*sessionRow, error)
	insertSession(ctx context.Context, row *sessionRow) error
	extendSession(ctx context.Context, idHash string, expiresAt, lastActive time.Time) error
	// rotateSession moves the row of oldIDHash to row.IDHash and sets its
	// address, User-Agent, expiry and last active time to row's. It
	// returns how many rows it changed, 0 or 1.
	rotateSession(ctx context.Context, oldIDHash string, row *sessionRow) (int64, error)
	deleteSession(ctx context.Context, idHash string) error
	deleteSessionsBySubject(ctx context.Context, subject string) error
	deleteExpiredSessions(
		ctx context.Context,
		expiryCutoff, creationCutoff time.Time,
	) (int64, error)
}
