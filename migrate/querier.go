package migrate

import (
	"context"
	"database/sql"
)

// querier abstracts the sqlc-generated query methods across dialects.
// Unexported — consumers interact with Service, not this interface.
type querier interface {
	getAppliedMigrations(ctx context.Context) ([]appliedMigration, error)
	getDirtyMigrations(ctx context.Context) ([]appliedMigration, error)
	// markMigrationApplied records a migration as applied within tx, so the
	// tracking row commits atomically with the migration's own DDL.
	markMigrationApplied(ctx context.Context, tx *sql.Tx, version, service, description, migrateVersion, moduleVersion string) error
	markMigrationDirty(ctx context.Context, version, service, description, migrateVersion, moduleVersion string) error
	removeMigration(ctx context.Context, version string) error
}
