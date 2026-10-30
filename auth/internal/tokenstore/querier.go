package tokenstore

import (
	"context"
	"errors"
	"time"
)

// errNoToken is what consumeTokenByHash returns when there is no token to
// consume; Store.ConsumeTokenByHash turns it into ErrNotFound.
var errNoToken = errors.New("no token to consume")

// tokenRow is a consumed token as the dialect adapters return it.
type tokenRow struct {
	// ExpiresAt is when the record stops being valid.
	ExpiresAt time.Time
	// ID identifies the record.
	ID string
	// Subject is the user or client the record belongs to.
	Subject string
	// Purpose is what the token may be used for, such as a password reset.
	Purpose string
}

// tokenInsert is a new token as the dialect adapters write it.
type tokenInsert struct {
	// CreatedAt is when the record was created.
	CreatedAt time.Time
	// ExpiresAt is when the record stops being valid.
	ExpiresAt time.Time
	// ID identifies the record.
	ID string
	// Subject is the user or client the record belongs to.
	Subject string
	// TokenHash is the SHA-256 hex digest of the token.
	TokenHash string
	// Purpose is what the token may be used for, such as a password reset.
	Purpose string
}

// querier is what each dialect adapter implements over its sqlc queries.
// The Store wraps every error.
type querier interface {
	insertToken(ctx context.Context, t *tokenInsert) error
	deleteTokenByHash(ctx context.Context, tokenHash string) (int64, error)
	deleteTokensBySubjectPurpose(ctx context.Context, subject, purpose string) error
	deleteExpiredTokens(ctx context.Context, before time.Time) (int64, error)
	// consumeTokenByHash deletes a token by hash and returns its row, in
	// one atomic step. It returns errNoToken when the token does not exist
	// or another caller consumed it first.
	consumeTokenByHash(ctx context.Context, tokenHash string) (*tokenRow, error)
}
