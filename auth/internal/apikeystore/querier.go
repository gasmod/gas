package apikeystore

import (
	"context"
	"database/sql"
	"time"
)

// keyRow is an API key as the dialect adapters read it, with scopes split
// and metadata as JSON.
type keyRow struct {
	// CreatedAt is when the record was created.
	CreatedAt time.Time
	// LastUsed is when the key last authenticated, or nil if never.
	LastUsed *time.Time
	// ExpiresAt is when the key stops being valid, or nil if never.
	ExpiresAt *time.Time
	// DeletedAt is when the key was soft-deleted, or nil if it is not.
	DeletedAt *time.Time
	// ID identifies the record.
	ID string
	// Name is the caller-given label of the key.
	Name string
	// KeyPrefix is the leading part of the key shown to identify it.
	KeyPrefix string
	// Subject is the user or client the record belongs to.
	Subject string
	// Scopes are the permissions granted to the key.
	Scopes []string
	// Metadata is the JSON stored with the key.
	Metadata []byte
}

// querier is what each dialect adapter implements over its sqlc queries.
// getKeyByHash returns sql.ErrNoRows, unwrapped, when no row matches; the
// Store wraps every error.
//
//nolint:interfacebloat // one method per query, as the Store's methods are.
type querier interface {
	getKeyByHash(ctx context.Context, keyHash string) (*keyRow, error)
	updateLastUsed(ctx context.Context, id string, lastUsed time.Time) error
	insertKey(ctx context.Context, p *InsertKeyParams) error
	softDeleteKeyByID(ctx context.Context, id string, deletedAt time.Time) error
	softDeleteKeysBySubject(ctx context.Context, subject string, deletedAt time.Time) error
	hardDeleteKeyByID(ctx context.Context, id string) error
	hardDeleteKeysBySubject(ctx context.Context, subject string) error
	listKeysBySubject(ctx context.Context, subject string) ([]keyRow, error)
	listAllKeysBySubject(ctx context.Context, subject string) ([]keyRow, error)
	// deleteExpiredKeys removes the rows whose expiry is at or before cutoff and
	// returns how many it removed.
	deleteExpiredKeys(ctx context.Context, cutoff time.Time) (int64, error)
	// withTx returns an adapter of the same dialect whose queries run in tx.
	withTx(tx *sql.Tx) querier
}
