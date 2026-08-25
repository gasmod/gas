package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// appliedMigration represents a row in the __gas_migrations tracking table.
type appliedMigration struct {
	AppliedAt      time.Time
	Version        string
	Service        string
	Description    string
	MigrateVersion string
	ModuleVersion  string
	Dirty          bool
}

func (s *Service) getAppliedMigrations(ctx context.Context) ([]appliedMigration, error) {
	applied, err := s.q.getAppliedMigrations(ctx)
	if err != nil {
		return nil, fmt.Errorf("gas/migrate: failed to query applied migrations: %w", err)
	}
	return applied, nil
}

func (s *Service) getDirtyMigrations(ctx context.Context) ([]appliedMigration, error) {
	dirty, err := s.q.getDirtyMigrations(ctx)
	if err != nil {
		return nil, fmt.Errorf("gas/migrate: failed to query dirty migrations: %w", err)
	}
	return dirty, nil
}

func (s *Service) markApplied(ctx context.Context, tx *sql.Tx, version, service, description string) error {
	if err := s.q.markMigrationApplied(ctx, tx, version, service, description, migrateVersion(), resolveModuleVersion(service)); err != nil {
		return fmt.Errorf("gas/migrate: failed to mark migration %s applied: %w", version, err)
	}
	return nil
}

func (s *Service) markDirty(ctx context.Context, version, service, description string) error {
	if err := s.q.markMigrationDirty(ctx, version, service, description, migrateVersion(), resolveModuleVersion(service)); err != nil {
		return fmt.Errorf("gas/migrate: failed to mark migration %s dirty: %w", version, err)
	}
	return nil
}

func (s *Service) removeMigration(ctx context.Context, version string) error {
	if err := s.q.removeMigration(ctx, version); err != nil {
		return fmt.Errorf("gas/migrate: failed to remove migration %s: %w", version, err)
	}
	return nil
}
