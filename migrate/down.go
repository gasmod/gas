package migrate

import (
	"context"
	"fmt"
	"slices"
)

// Down is DownContext with context.Background().
func (s *Service) Down(n int) error {
	return s.DownContext(context.Background(), n)
}

// DownContext reverts the n highest applied versions, highest first, under
// the migration lock, as described in the package documentation. It
// checks that each has registered down SQL before it reverts any. An n
// of zero or less does nothing and returns nil. While a migration is
// dirty it reverts nothing and returns an error wrapping ErrDirty.
func (s *Service) DownContext(ctx context.Context, n int) error {
	st, err := s.loaded()
	if err != nil {
		return err
	}

	if n <= 0 {
		return nil
	}

	regs, err := s.reg.snapshotFor(st.dialect)
	if err != nil {
		return err
	}

	return s.locked(ctx, st, func(sess *session) error {
		rows, readErr := s.readRows(ctx, sess.conn, sess.dialect)
		if readErr != nil {
			return readErr
		}

		readErr = dirtyError(rows)
		if readErr != nil {
			return readErr
		}

		targets, readErr := downTargets(regs, rows, n)
		if readErr != nil {
			return readErr
		}

		for _, reg := range targets {
			revertErr := s.change(ctx, sess, &change{reg: reg, verb: verbRevert})
			if revertErr != nil {
				return revertErr
			}
		}

		return nil
	})
}

// downTargets returns the registered migrations of the n highest versions
// of rows, highest first, or an error naming the first that has no
// registration or no down SQL.
func downTargets(regs []*registered, rows []AppliedMigration, n int) ([]*registered, error) {
	byVersion := make(map[string]*registered, len(regs))

	for _, reg := range regs {
		byVersion[reg.mig.Version] = reg
	}

	rows = slices.Clone(rows)
	slices.Reverse(rows)

	out := make([]*registered, 0, min(n, len(rows)))

	for _, row := range rows[:min(n, len(rows))] {
		reg, ok := byVersion[row.Version]
		if !ok {
			return nil, fmt.Errorf(
				"%s: revert %s (owner %s): %w",
				ServiceName, row.Version, row.Service, ErrUnregisteredVersion,
			)
		}

		if isBlank(reg.mig.Down) {
			return nil, fmt.Errorf(
				"%s: revert %s (owner %s): %w",
				ServiceName, row.Version, reg.owner, ErrNoDownSQL,
			)
		}

		out = append(out, reg)
	}

	return out, nil
}
