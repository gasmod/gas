package test

import (
	"context"
	"fmt"

	"github.com/gasmod/gas"
)

// appliedMigration is a row in the __gas_migrations tracking table.
type appliedMigration struct {
	Version        string
	Service        string
	Description    string
	MigrateVersion string
	ModuleVersion  string
	Dirty          bool
}

// getAppliedMigrations reads every tracking row, ordered by version.
//
//goland:noinspection SqlResolve,SqlNoDataSourceInspection
func getAppliedMigrations(ctx context.Context, db gas.DatabaseProvider) ([]appliedMigration, error) {
	return queryTracking(ctx, db, "")
}

// getDirtyMigrations reads the tracking rows marked dirty, ordered by version.
//
//goland:noinspection SqlResolve,SqlNoDataSourceInspection
func getDirtyMigrations(ctx context.Context, db gas.DatabaseProvider) ([]appliedMigration, error) {
	return queryTracking(ctx, db, "WHERE dirty = TRUE")
}

//goland:noinspection SqlResolve,SqlNoDataSourceInspection
func queryTracking(ctx context.Context, db gas.DatabaseProvider, where string) ([]appliedMigration, error) {
	rows, err := db.Query(ctx, `
		SELECT version, service, description, migrate_version, module_version, dirty
		FROM __gas_migrations `+where+`
		ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("query tracking rows: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []appliedMigration
	for rows.Next() {
		var m appliedMigration
		if err := rows.Scan(
			&m.Version,
			&m.Service,
			&m.Description,
			&m.MigrateVersion,
			&m.ModuleVersion,
			&m.Dirty,
		); err != nil {
			return nil, fmt.Errorf("scan tracking row: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tracking rows: %w", err)
	}
	return out, nil
}

// markDirty writes a dirty tracking row for version, as a failed migration
// leaves it.
//
//goland:noinspection SqlResolve,SqlNoDataSourceInspection
func markDirty(ctx context.Context, db gas.DatabaseProvider, version, service, description string) error {
	if _, err := db.Exec(ctx, `
		INSERT INTO __gas_migrations (version, service, description, dirty)
		VALUES (?, ?, ?, TRUE)
		ON CONFLICT (version) DO UPDATE SET dirty = TRUE`,
		version, service, description,
	); err != nil {
		return fmt.Errorf("mark %s dirty: %w", version, err)
	}
	return nil
}
