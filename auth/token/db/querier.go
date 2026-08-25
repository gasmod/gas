package db

import (
	"context"
	"time"
)

// tokenRow represents a row returned by getTokenByHash.
type tokenRow struct {
	ExpiresAt time.Time
	ID        string
	Subject   string
	Purpose   string
}

// querier abstracts the sqlc-generated query methods across dialects.
// Unexported — consumers interact with Store, not this interface.
type querier interface {
	insertToken(ctx context.Context, id, subject, tokenHash, purpose string, createdAt, expiresAt time.Time) error
	deleteTokenByHash(ctx context.Context, tokenHash string) (int64, error)
	deleteTokensBySubjectPurpose(ctx context.Context, subject, purpose string) error
	deleteExpiredTokens(ctx context.Context, before time.Time) (int64, error)
	// consumeTokenByHash atomically fetches and deletes a token by hash.
	// Returns (nil, nil) if the token does not exist or was consumed
	// concurrently by another caller.
	consumeTokenByHash(ctx context.Context, tokenHash string) (*tokenRow, error)
}
