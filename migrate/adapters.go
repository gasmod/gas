package migrate

import (
	"context"
	"database/sql"
	"time"

	mydb "github.com/gasmod/gas/migrate/db/mysql"
	pgdb "github.com/gasmod/gas/migrate/db/postgres"
	litedb "github.com/gasmod/gas/migrate/db/sqlite"
)

type postgresAdapter struct {
	q *pgdb.Queries
}

func newPostgresAdapter(q *pgdb.Queries) *postgresAdapter {
	return &postgresAdapter{q: q}
}

func (a *postgresAdapter) getAppliedMigrations(ctx context.Context) ([]appliedMigration, error) {
	rows, err := a.q.GetAppliedMigrations(ctx)
	if err != nil {
		//nolint:wrapcheck // wrapped by the caller
		return nil, err
	}
	out := make([]appliedMigration, len(rows))
	for i, r := range rows {
		out[i] = appliedMigration{
			Version:        r.Version,
			Service:        r.Service,
			Description:    r.Description,
			MigrateVersion: r.MigrateVersion,
			ModuleVersion:  r.ModuleVersion,
			AppliedAt:      r.AppliedAt,
			Dirty:          r.Dirty,
		}
	}
	return out, nil
}

func (a *postgresAdapter) getDirtyMigrations(ctx context.Context) ([]appliedMigration, error) {
	rows, err := a.q.GetDirtyMigrations(ctx)
	if err != nil {
		//nolint:wrapcheck // wrapped by the caller
		return nil, err
	}
	out := make([]appliedMigration, len(rows))
	for i, r := range rows {
		out[i] = appliedMigration{
			Version:        r.Version,
			Service:        r.Service,
			Description:    r.Description,
			MigrateVersion: r.MigrateVersion,
			ModuleVersion:  r.ModuleVersion,
			AppliedAt:      r.AppliedAt,
			Dirty:          r.Dirty,
		}
	}
	return out, nil
}

func (a *postgresAdapter) markMigrationApplied(ctx context.Context, tx *sql.Tx, version, service, description, migrateVersion, moduleVersion string) error {
	//nolint:wrapcheck // wrapped by the caller
	return a.q.WithTx(tx).MarkMigrationApplied(ctx, pgdb.MarkMigrationAppliedParams{
		Version:        version,
		Service:        service,
		Description:    description,
		MigrateVersion: migrateVersion,
		ModuleVersion:  moduleVersion,
	})
}

func (a *postgresAdapter) markMigrationDirty(ctx context.Context, version, service, description, migrateVersion, moduleVersion string) error {
	//nolint:wrapcheck // wrapped by the caller
	return a.q.MarkMigrationDirty(ctx, pgdb.MarkMigrationDirtyParams{
		Version:        version,
		Service:        service,
		Description:    description,
		MigrateVersion: migrateVersion,
		ModuleVersion:  moduleVersion,
	})
}

func (a *postgresAdapter) removeMigration(ctx context.Context, version string) error {
	//nolint:wrapcheck // wrapped by the caller
	return a.q.RemoveMigration(ctx, version)
}

type mysqlAdapter struct {
	q *mydb.Queries
}

func newMySQLAdapter(q *mydb.Queries) *mysqlAdapter {
	return &mysqlAdapter{q: q}
}

func (a *mysqlAdapter) getAppliedMigrations(ctx context.Context) ([]appliedMigration, error) {
	rows, err := a.q.GetAppliedMigrations(ctx)
	if err != nil {
		//nolint:wrapcheck // wrapped by the caller
		return nil, err
	}
	out := make([]appliedMigration, len(rows))
	for i, r := range rows {
		out[i] = appliedMigration{
			Version:        r.Version,
			Service:        r.Service,
			Description:    r.Description,
			MigrateVersion: r.MigrateVersion,
			ModuleVersion:  r.ModuleVersion,
			AppliedAt:      r.AppliedAt,
			Dirty:          r.Dirty,
		}
	}
	return out, nil
}

func (a *mysqlAdapter) getDirtyMigrations(ctx context.Context) ([]appliedMigration, error) {
	rows, err := a.q.GetDirtyMigrations(ctx)
	if err != nil {
		//nolint:wrapcheck // wrapped by the caller
		return nil, err
	}
	out := make([]appliedMigration, len(rows))
	for i, r := range rows {
		out[i] = appliedMigration{
			Version:        r.Version,
			Service:        r.Service,
			Description:    r.Description,
			MigrateVersion: r.MigrateVersion,
			ModuleVersion:  r.ModuleVersion,
			AppliedAt:      r.AppliedAt,
			Dirty:          r.Dirty,
		}
	}
	return out, nil
}

func (a *mysqlAdapter) markMigrationApplied(ctx context.Context, tx *sql.Tx, version, service, description, migrateVersion, moduleVersion string) error {
	//nolint:wrapcheck // wrapped by the caller
	return a.q.WithTx(tx).MarkMigrationApplied(ctx, mydb.MarkMigrationAppliedParams{
		Version:        version,
		Service:        service,
		Description:    description,
		MigrateVersion: migrateVersion,
		ModuleVersion:  moduleVersion,
	})
}

func (a *mysqlAdapter) markMigrationDirty(ctx context.Context, version, service, description, migrateVersion, moduleVersion string) error {
	//nolint:wrapcheck // wrapped by the caller
	return a.q.MarkMigrationDirty(ctx, mydb.MarkMigrationDirtyParams{
		Version:        version,
		Service:        service,
		Description:    description,
		MigrateVersion: migrateVersion,
		ModuleVersion:  moduleVersion,
	})
}

func (a *mysqlAdapter) removeMigration(ctx context.Context, version string) error {
	//nolint:wrapcheck // wrapped by the caller
	return a.q.RemoveMigration(ctx, version)
}

type sqliteAdapter struct {
	q *litedb.Queries
}

func newSQLiteAdapter(q *litedb.Queries) *sqliteAdapter {
	return &sqliteAdapter{q: q}
}

func (a *sqliteAdapter) getAppliedMigrations(ctx context.Context) ([]appliedMigration, error) {
	rows, err := a.q.GetAppliedMigrations(ctx)
	if err != nil {
		//nolint:wrapcheck // wrapped by the caller
		return nil, err
	}
	out := make([]appliedMigration, len(rows))
	for i, r := range rows {
		out[i] = appliedMigration{
			Version:        r.Version,
			Service:        r.Service,
			Description:    r.Description,
			MigrateVersion: r.MigrateVersion,
			ModuleVersion:  r.ModuleVersion,
			AppliedAt:      parseSQLiteTime(r.AppliedAt),
			Dirty:          r.Dirty > 0,
		}
	}
	return out, nil
}

func (a *sqliteAdapter) getDirtyMigrations(ctx context.Context) ([]appliedMigration, error) {
	rows, err := a.q.GetDirtyMigrations(ctx)
	if err != nil {
		//nolint:wrapcheck // wrapped by the caller
		return nil, err
	}
	out := make([]appliedMigration, len(rows))
	for i, r := range rows {
		out[i] = appliedMigration{
			Version:        r.Version,
			Service:        r.Service,
			Description:    r.Description,
			MigrateVersion: r.MigrateVersion,
			ModuleVersion:  r.ModuleVersion,
			AppliedAt:      parseSQLiteTime(r.AppliedAt),
			Dirty:          r.Dirty > 0,
		}
	}
	return out, nil
}

func (a *sqliteAdapter) markMigrationApplied(ctx context.Context, tx *sql.Tx, version, service, description, migrateVersion, moduleVersion string) error {
	//nolint:wrapcheck // wrapped by the caller
	return a.q.WithTx(tx).MarkMigrationApplied(ctx, litedb.MarkMigrationAppliedParams{
		Version:        version,
		Service:        service,
		Description:    description,
		MigrateVersion: migrateVersion,
		ModuleVersion:  moduleVersion,
	})
}

func (a *sqliteAdapter) markMigrationDirty(ctx context.Context, version, service, description, migrateVersion, moduleVersion string) error {
	//nolint:wrapcheck // wrapped by the caller
	return a.q.MarkMigrationDirty(ctx, litedb.MarkMigrationDirtyParams{
		Version:        version,
		Service:        service,
		Description:    description,
		MigrateVersion: migrateVersion,
		ModuleVersion:  moduleVersion,
	})
}

func (a *sqliteAdapter) removeMigration(ctx context.Context, version string) error {
	//nolint:wrapcheck // wrapped by the caller
	return a.q.RemoveMigration(ctx, version)
}

func parseSQLiteTime(s string) time.Time {
	t, _ := time.Parse("2006-01-02 15:04:05", s)
	return t
}
