package migrate

import (
	"context"
	"fmt"
)

// ClearDirty removes the dirty mark of version by deleting its row, under
// the migration lock, so that the next RunPending applies the migration
// again, as described in the package documentation. On MySQL a failed
// migration may have left part of its changes behind, which must be
// repaired by hand before. A version that is not marked dirty, or has no
// row, is an error wrapping ErrNotDirty, and nothing changes.
func (s *Service) ClearDirty(ctx context.Context, version string) error {
	st, err := s.loaded()
	if err != nil {
		return err
	}

	return s.locked(ctx, st, func(sess *session) error {
		var n int64

		err := s.write(ctx, sess, func(ctx context.Context, x runner) error {
			res, execErr := x.ExecContext(ctx, sess.dialect.clearDirty, version)
			if execErr != nil {
				return s.safeErr(execErr)
			}

			n, execErr = res.RowsAffected()

			return s.safeErr(execErr)
		})
		if err != nil {
			return fmt.Errorf("%s: clear %s: %w", ServiceName, version, err)
		}

		if n == 0 {
			return fmt.Errorf("%s: clear %s: %w", ServiceName, version, ErrNotDirty)
		}

		s.logger.Info("dirty mark cleared").Str("version", version).Send()

		return nil
	})
}
