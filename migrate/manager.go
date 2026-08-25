package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gasmod/gas"
)

// Register adds a migration owned by the given service.
func (s *Service) Register(service string, migration gas.Migration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	migration.Service = service
	s.migrations[service] = append(s.migrations[service], migration)
}

// RegisterSlice adds multiple migrations at once for the given service.
func (s *Service) RegisterSlice(service string, migrations []gas.Migration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, migration := range migrations {
		migration.Service = service
		s.migrations[service] = append(s.migrations[service], migration)
	}
}

// RegisterFS reads migration files from an fs.FS and registers them for the
// given service. Files must follow the naming convention:
//
//	{version}_{description}.up.sql   — the up (apply) SQL
//	{version}_{description}.down.sql — the down (rollback) SQL
//
// The version is the first underscore-delimited segment, and the description
// is the remaining underscored segments converted to spaces.
// Every .up.sql file must have a matching .down.sql file.
func (s *Service) RegisterFS(service string, fsys fs.FS) error {
	pairs, err := parseMigrationFS(fsys)
	if err != nil {
		return fmt.Errorf("gas/migrate: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range pairs {
		p.migration.Service = service
		s.migrations[service] = append(s.migrations[service], p.migration)
	}
	return nil
}

type migrationPair struct {
	migration gas.Migration
}

func parseMigrationFS(fsys fs.FS) ([]migrationPair, error) {
	var upFiles []string
	if err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".up.sql") {
			upFiles = append(upFiles, path)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to walk migration files: %w", err)
	}

	sort.Strings(upFiles)

	pairs := make([]migrationPair, 0, len(upFiles))
	for _, upFile := range upFiles {
		stem := strings.TrimSuffix(upFile, ".up.sql")
		downFile := stem + ".down.sql"

		upSQL, err := fs.ReadFile(fsys, upFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", upFile, err)
		}

		downSQL, err := fs.ReadFile(fsys, downFile)
		if err != nil {
			return nil, fmt.Errorf("missing down file for %s: %w", upFile, err)
		}

		version, description := parseStem(stem)

		pairs = append(pairs, migrationPair{
			migration: gas.Migration{
				Version:     version,
				Description: description,
				Up:          string(upSQL),
				Down:        string(downSQL),
			},
		})
	}

	return pairs, nil
}

// parseStem extracts version and description from a migration filename stem.
// Given "20250216001_create_users_table", it returns version="20250216001"
// and description="create users table".
func parseStem(stem string) (version, description string) {
	// Strip any directory prefix.
	stem = filepath.Base(stem)

	// The version is the first underscore-delimited segment.
	if idx := strings.IndexByte(stem, '_'); idx >= 0 {
		version = stem[:idx]
		description = strings.ReplaceAll(stem[idx+1:], "_", " ")
	} else {
		version = stem
	}
	return version, description
}

// RunPending applies all unapplied migrations in global version order.
// If any migration is marked dirty, execution is blocked until resolved.
func (s *Service) RunPending() error {
	if s.closed.Load() {
		return fmt.Errorf("gas/migrate: service is closed")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()

	dirty, err := s.getDirtyMigrations(ctx)
	if err != nil {
		return err
	}
	if len(dirty) > 0 {
		versions := make([]string, len(dirty))
		for i, d := range dirty {
			versions[i] = d.Version
		}
		return fmt.Errorf("gas/migrate: dirty migrations block execution: %s", strings.Join(versions, ", "))
	}

	applied, err := s.getAppliedMigrations(ctx)
	if err != nil {
		return err
	}
	appliedSet := make(map[string]struct{}, len(applied))
	for _, a := range applied {
		appliedSet[a.Version] = struct{}{}
	}

	all, err := s.allMigrationsSorted()
	if err != nil {
		return err
	}

	for _, migration := range all {
		if _, ok := appliedSet[migration.Version]; ok {
			continue
		}
		if mErr := s.applyUp(ctx, migration); mErr != nil {
			return fmt.Errorf("gas/migrate: failed to apply migration %s: %w", migration.Version, mErr)
		}
	}

	return nil
}

// Down reverses the last n applied migrations in reverse version order.
func (s *Service) Down(n int) error {
	if s.closed.Load() {
		return fmt.Errorf("gas/migrate: service is closed")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()

	applied, err := s.getAppliedMigrations(ctx)
	if err != nil {
		return err
	}

	registered, err := s.migrationsByVersion()
	if err != nil {
		return err
	}

	// Reverse order: most recently applied first.
	count := n
	if count > len(applied) {
		count = len(applied)
	}

	for i := len(applied) - 1; i >= len(applied)-count; i-- {
		a := applied[i]
		migration, ok := registered[a.Version]
		if !ok {
			return fmt.Errorf("gas/migrate: applied migration %s not found in registered migrations", a.Version)
		}
		if err := s.applyDown(ctx, migration); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) applyUp(ctx context.Context, migration gas.Migration) error {
	tx, err := s.db.DB().BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("gas/migrate: failed to begin transaction for %s: %w", migration.Version, err)
	}

	if _, err := tx.ExecContext(ctx, migration.Up); err != nil {
		_ = tx.Rollback()
		if markErr := s.markDirty(ctx, migration.Version, migration.Service, migration.Description); markErr != nil {
			return fmt.Errorf("gas/migrate: migration %s failed: %w (also failed to mark dirty: %w)",
				migration.Version, err, markErr)
		}
		return fmt.Errorf("gas/migrate: migration %q (Version: %s) failed (marked dirty): %w", migration.Description, migration.Version, err)
	}

	// Record the migration as applied inside the same transaction as its DDL,
	// so the schema change and the tracking row commit atomically. If recording
	// fails (or the process crashes here), the whole transaction rolls back and
	// the database is left unchanged — rather than schema-applied-but-unrecorded,
	// which would re-run the non-idempotent DDL on the next pass and wedge the
	// pipeline as dirty.
	if err := s.markApplied(ctx, tx, migration.Version, migration.Service, migration.Description); err != nil {
		_ = tx.Rollback()
		if markErr := s.markDirty(ctx, migration.Version, migration.Service, migration.Description); markErr != nil {
			return fmt.Errorf("gas/migrate: recording migration %s failed: %w (also failed to mark dirty: %w)",
				migration.Version, err, markErr)
		}
		return fmt.Errorf("gas/migrate: recording migration %q (Version: %s) failed (marked dirty): %w", migration.Description, migration.Version, err)
	}

	if err := tx.Commit(); err != nil {
		if markErr := s.markDirty(ctx, migration.Version, migration.Service, migration.Description); markErr != nil {
			return fmt.Errorf("gas/migrate: commit failed for %s: %w (also failed to mark dirty: %w)",
				migration.Version, err, markErr)
		}
		return fmt.Errorf("gas/migrate: commit failed for %s (marked dirty): %w", migration.Version, err)
	}

	return nil
}

func (s *Service) applyDown(ctx context.Context, migration gas.Migration) error {
	tx, err := s.db.DB().BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("gas/migrate: failed to begin transaction for down %s: %w", migration.Version, err)
	}

	if _, err := tx.ExecContext(ctx, migration.Down); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("gas/migrate: down migration %s failed: %w", migration.Version, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gas/migrate: commit failed for down %s: %w", migration.Version, err)
	}

	return s.removeMigration(ctx, migration.Version)
}

func (s *Service) allMigrationsSorted() ([]gas.Migration, error) {
	total := 0
	for _, migs := range s.migrations {
		total += len(migs)
	}
	all := make([]gas.Migration, 0, total)
	for _, migs := range s.migrations {
		all = append(all, migs...)
	}

	// Check for duplicate versions across services.
	seen := make(map[string]string, len(all)) // version → service
	for _, mig := range all {
		if owner, ok := seen[mig.Version]; ok && owner != mig.Service {
			return nil, fmt.Errorf("gas/migrate: duplicate migration version %q registered by services %q and %q",
				mig.Version, owner, mig.Service)
		}
		seen[mig.Version] = mig.Service
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Version < all[j].Version
	})
	return all, nil
}

func (s *Service) migrationsByVersion() (map[string]gas.Migration, error) {
	byVersion := make(map[string]gas.Migration)
	for _, migs := range s.migrations {
		for _, mig := range migs {
			if existing, ok := byVersion[mig.Version]; ok && existing.Service != mig.Service {
				return nil, fmt.Errorf("gas/migrate: duplicate migration version %q registered by services %q and %q",
					mig.Version, existing.Service, mig.Service)
			}
			byVersion[mig.Version] = mig
		}
	}
	return byVersion, nil
}
