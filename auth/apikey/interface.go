package apikey

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/gasmod/gas"
)

// Provider defines an interface for managing API keys and their associated operations, such as authentication and revocation.
type Provider interface {
	// Authenticate reads the API key from the configured header, hashes it,
	// looks it up in the database, validates expiry, updates last_used, and
	// returns a principal with scheme "apikey".
	Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error)
	// Revoke soft-deletes an API key by the principal's credential ID. The
	// row stays in the database with deleted_at set and is excluded from
	// authentication and listing.
	Revoke(ctx context.Context, principal gas.Principal) error
	// RevokeAll soft-deletes all API keys for the given subject.
	RevokeAll(ctx context.Context, subject string) error
	// RevokeAllByScheme delegates to RevokeAll if the scheme is "apikey",
	// otherwise it is a no-op.
	RevokeAllByScheme(ctx context.Context, subject, scheme string) error
	// Delete permanently removes an API key row by the principal's credential
	// ID. Prefer Revoke for normal flows.
	Delete(ctx context.Context, principal gas.Principal) error
	// DeleteAll permanently removes all API key rows for the given subject.
	// Prefer RevokeAll for normal flows.
	DeleteAll(ctx context.Context, subject string) error
	// Generate creates a new API key for the given subject, stores its hash in
	// the database, and returns the full key exactly once along with the stored
	// KeyInfo record. The plaintext key is only available from this call; only
	// its hash is persisted.
	// Optional GenerateOption values (WithMetadata, WithTTL, WithExpiresAt) customize the key.
	Generate(ctx context.Context, subject, name string, scopes []string, opts ...GenerateOption) (key string, info *KeyInfo, err error)
	// List returns non-sensitive information about API keys for a subject. By
	// default only active (non-revoked) keys are returned; pass
	// WithIncludeRevoked to also include soft-deleted keys.
	List(ctx context.Context, subject string, opts ...ListOption) ([]KeyInfo, error)
	// WithTx returns a Provider whose operations execute against the given
	// transaction, allowing API key operations to be composed atomically with
	// caller-owned writes. The caller owns tx lifecycle (commit/rollback); the
	// returned Provider is scoped to the lifetime of tx and must not be cached.
	WithTx(tx *sql.Tx) Provider
}
