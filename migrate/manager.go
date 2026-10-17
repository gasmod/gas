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
func (s *Service) Register(service gas.Service, migration gas.Migration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	migration.Service = service
	s.migrations[service.Name()] = append(s.migrations[service.Name()], migration)
}

// RegisterSlice adds multiple migrations at once for the given service.
func (s *Service) RegisterSlice(service gas.Service, migrations []gas.Migration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, migration := range migrations {
		migration.Service = service
		s.migrations[service.Name()] = append(s.migrations[service.Name()], migration)
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
func (s *Service) RegisterFS(service gas.Service, fsys fs.FS) error {
	pairs, err := parseMigrationFS(fsys)
	if err != nil {
		return fmt.Errorf("gas/migrate: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range pairs {
		p.migration.Service = service
		s.migrations[service.Name()] = append(s.migrations[service.Name()], p.migration)
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
// If any migration is marked dirty, it runs nothing and returns an error
// wrapping ErrDirty until the mark is cleared.
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
		return fmt.Errorf("%w block execution: %s", ErrDirty, joinVersions(dirty))
	}

	applied, err := s.getAppliedMigrations(ctx)
	if err != nil {
		return err
	}
	appliedSet := versionSet(applied)

	all, err := s.registeredMigrations()
	if err != nil {
		return err
	}

	for _, migration := range all {
		if _, ok := appliedSet[migration.Version]; ok {
			continue
		}
		if mErr := s.applyUp(ctx, migration); mErr != nil {
			return fmt.Errorf(
				"gas/migrate: failed to apply migration %s: %w",
				migration.Version,
				mErr,
			)
		}
	}

	return nil
}

// Down reverses the last n applied migrations in reverse version order.
// While any migration is dirty it runs nothing and returns an error
// wrapping ErrDirty, since a dirty migration's up SQL was rolled back and
// its down SQL would act on a schema it never changed.
func (s *Service) Down(n int) error {
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
		return fmt.Errorf("%w block rollback: %s", ErrDirty, joinVersions(dirty))
	}

	applied, err := s.getAppliedMigrations(ctx)
	if err != nil {
		return err
	}

	all, err := s.registeredMigrations()
	if err != nil {
		return err
	}
	registered := make(map[string]gas.Migration, len(all))
	for _, mig := range all {
		registered[mig.Version] = mig
	}

	// Reverse order: most recently applied first.
	count := min(n, len(applied))

	for i := len(applied) - 1; i >= len(applied)-count; i-- {
		a := applied[i]
		migration, ok := registered[a.Version]
		if !ok {
			return fmt.Errorf(
				"gas/migrate: applied migration %s not found in registered migrations",
				a.Version,
			)
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
		return fmt.Errorf(
			"gas/migrate: failed to begin transaction for %s: %w",
			migration.Version,
			err,
		)
	}

	if err := s.execUp(ctx, tx, migration); err != nil {
		return err
	}

	return s.recordAndCommit(ctx, tx, migration)
}

// execUp runs migration's up SQL inside tx, rolling back and marking the
// migration dirty on failure.
func (s *Service) execUp(ctx context.Context, tx *sql.Tx, migration gas.Migration) error {
	if _, err := tx.ExecContext(ctx, migration.Up); err != nil {
		_ = tx.Rollback()
		if markErr := s.markMigrationDirty(ctx, migration); markErr != nil {
			return fmt.Errorf(
				"gas/migrate: migration %s failed: %w (also failed to mark dirty: %w)",
				migration.Version,
				err,
				markErr,
			)
		}
		return fmt.Errorf(
			"gas/migrate: migration %q (Version: %s) failed (marked dirty): %w",
			migration.Description,
			migration.Version,
			err,
		)
	}

	return nil
}

// recordAndCommit records migration as applied in tx and commits, marking the
// migration dirty if either step fails.
func (s *Service) recordAndCommit(ctx context.Context, tx *sql.Tx, migration gas.Migration) error {
	// Record the migration as applied inside the same transaction as its DDL,
	// so the schema change and the tracking row commit atomically. If recording
	// fails (or the process crashes here), the whole transaction rolls back and
	// the database is left unchanged — rather than schema-applied-but-unrecorded,
	// which would re-run the non-idempotent DDL on the next pass and wedge the
	// pipeline as dirty.
	if err := s.markApplied(
		ctx,
		tx,
		migration.Version,
		migration.Service.Name(),
		migration.Description,
	); err != nil {
		_ = tx.Rollback()
		if markErr := s.markMigrationDirty(ctx, migration); markErr != nil {
			return fmt.Errorf(
				"gas/migrate: recording migration %s failed: %w (also failed to mark dirty: %w)",
				migration.Version,
				err,
				markErr,
			)
		}
		return fmt.Errorf(
			"gas/migrate: recording migration %q (Version: %s) failed (marked dirty): %w",
			migration.Description,
			migration.Version,
			err,
		)
	}

	if err := tx.Commit(); err != nil {
		if markErr := s.markMigrationDirty(ctx, migration); markErr != nil {
			return fmt.Errorf(
				"gas/migrate: commit failed for %s: %w (also failed to mark dirty: %w)",
				migration.Version,
				err,
				markErr,
			)
		}
		return fmt.Errorf(
			"gas/migrate: commit failed for %s (marked dirty): %w",
			migration.Version,
			err,
		)
	}

	return nil
}

// markMigrationDirty records migration as dirty in the tracking table.
func (s *Service) markMigrationDirty(ctx context.Context, migration gas.Migration) error {
	return s.markDirty(ctx, migration.Version, migration.Service.Name(), migration.Description)
}

func (s *Service) applyDown(ctx context.Context, migration gas.Migration) error {
	tx, err := s.db.DB().BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf(
			"gas/migrate: failed to begin transaction for down %s: %w",
			migration.Version,
			err,
		)
	}

	if _, err := tx.ExecContext(ctx, migration.Down); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("gas/migrate: down migration %s failed: %w", migration.Version, err)
	}

	// Delete the tracking row inside the same transaction as the down SQL,
	// so a failed delete rolls the schema change back rather than leaving
	// the schema reverted under a row that still says the migration is
	// applied, which RunPending would never re-apply.
	if err := s.removeMigration(ctx, tx, migration.Version); err != nil {
		_ = tx.Rollback()
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gas/migrate: commit failed for down %s: %w", migration.Version, err)
	}

	return nil
}

// registeredMigrations returns every registered migration once, sorted by
// version. A service that registers an identical migration (same version,
// description, up SQL, and down SQL) more than once gets one copy. Two
// services registering one version, or one service registering it with
// different content, is an error wrapping ErrDuplicateVersion. Must be
// called with s.mu held.
func (s *Service) registeredMigrations() ([]gas.Migration, error) {
	// Visit services in name order so a conflict is reported the same way
	// on every run.
	names := make([]string, 0, len(s.migrations))
	for name := range s.migrations {
		names = append(names, name)
	}
	sort.Strings(names)

	byVersion := make(map[string]gas.Migration)
	for _, name := range names {
		for _, mig := range s.migrations[name] {
			existing, ok := byVersion[mig.Version]
			if !ok {
				byVersion[mig.Version] = mig
				continue
			}
			if owner := existing.Service.Name(); owner != mig.Service.Name() {
				return nil, fmt.Errorf(
					"%w %q registered by services %q and %q",
					ErrDuplicateVersion,
					mig.Version,
					owner,
					mig.Service.Name(),
				)
			}
			if existing.Description != mig.Description ||
				existing.Up != mig.Up ||
				existing.Down != mig.Down {
				return nil, fmt.Errorf(
					"%w %q registered twice by service %q with different content",
					ErrDuplicateVersion,
					mig.Version,
					name,
				)
			}
		}
	}

	all := make([]gas.Migration, 0, len(byVersion))
	for _, mig := range byVersion {
		all = append(all, mig)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].Version < all[j].Version
	})
	return all, nil
}
