package db

import (
	"context"
	"database/sql"
	"time"
)

// keyRow represents an API key row in the db store.
type keyRow struct {
	CreatedAt time.Time
	LastUsed  *time.Time
	ExpiresAt *time.Time
	DeletedAt *time.Time
	ID        string
	Name      string
	KeyPrefix string
	Subject   string
	KeyHash   string
	Scopes    []string
	Metadata  []byte
}

// InsertKeyParams are the values needed to insert a new API key row.
type InsertKeyParams struct {
	CreatedAt time.Time
	ExpiresAt *time.Time
	ID        string
	Subject   string
	Name      string
	KeyHash   string
	KeyPrefix string
	Scopes    []string
	Metadata  []byte
}

// querier abstracts the sqlc-generated query methods across dialects.
// Unexported — consumers interact with Store, not this interface.
type querier interface {
	getKeyByHash(ctx context.Context, keyHash string) (*keyRow, error)
	updateLastUsed(ctx context.Context, id string, lastUsed time.Time) error
	insertKey(ctx context.Context, p InsertKeyParams) error
	softDeleteKeyByID(ctx context.Context, id string, deletedAt time.Time) error
	softDeleteKeysBySubject(ctx context.Context, subject string, deletedAt time.Time) error
	hardDeleteKeyByID(ctx context.Context, id string) error
	hardDeleteKeysBySubject(ctx context.Context, subject string) error
	listKeysBySubject(ctx context.Context, subject string) ([]keyRow, error)
	listAllKeysBySubject(ctx context.Context, subject string) ([]keyRow, error)
	withTx(tx *sql.Tx) querier
}
