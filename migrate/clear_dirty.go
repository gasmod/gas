package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
)

// ClearDirty removes the dirty mark from version by deleting its tracking
// row, so the next RunPending applies that migration again. Call it after
// fixing whatever made the migration fail; a dirty migration's up SQL was
// rolled back, so applying it again starts from the schema it expected.
//
// It returns an error wrapping ErrNotDirty, and changes nothing, when
// version is not marked dirty, including when it was applied cleanly or has
// no tracking row. It returns an error if the service is closed or not
// initialized.
func (s *Service) ClearDirty(version string) error {
	if s.closed.Load() {
		return fmt.Errorf("gas/migrate: service is closed")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.q == nil {
		return fmt.Errorf("gas/migrate: not initialized")
	}

	ctx := context.Background()

	dirty, err := s.getDirtyMigrations(ctx)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(dirty, func(m appliedMigration) bool { return m.Version == version }) {
		return fmt.Errorf("%w: %s", ErrNotDirty, version)
	}

	tx, err := s.db.DB().BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("gas/migrate: failed to begin transaction to clear %s: %w", version, err)
	}
	if err := s.removeMigration(ctx, tx, version); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gas/migrate: commit failed clearing %s: %w", version, err)
	}
	return nil
}
