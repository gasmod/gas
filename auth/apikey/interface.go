package apikey

import (
	"context"
	"database/sql"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
)

// Provider is what a Service offers once initialized: authentication,
// revocation and key management, as described in the package
// documentation. Code that uses API keys can depend on it, and tests can
// replace it.
type Provider interface {
	// Authenticator authenticates a request by the key in its header.
	auth.Authenticator
	// PrincipalRevoker revokes the key a principal authenticated with, or
	// every key of a subject. RevokeAllByScheme is a no-op for a scheme
	// other than auth.SchemeAPIKey.
	auth.PrincipalRevoker
	// Delete removes the stored key principal authenticated with.
	Delete(ctx context.Context, principal gas.Principal) error
	// DeleteAll removes every stored key of subject.
	DeleteAll(ctx context.Context, subject string) error
	// Generate creates and stores a key, and returns it with its KeyInfo.
	Generate(
		ctx context.Context,
		subject, name string,
		scopes []string,
		opts ...GenerateOption,
	) (key string, info *KeyInfo, err error)
	// List returns the keys of subject, newest first.
	List(ctx context.Context, subject string, opts ...ListOption) ([]KeyInfo, error)
	// WithTx returns a Provider whose operations run in tx.
	WithTx(tx *sql.Tx) Provider
}
