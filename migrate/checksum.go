package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// checksumOf returns the checksum of up SQL, as described in the package
// documentation: the SHA-256, in lowercase hex, of up with every CRLF
// replaced by LF.
func checksumOf(up string) string {
	sum := sha256.Sum256([]byte(strings.ReplaceAll(up, "\r\n", "\n")))

	return hex.EncodeToString(sum[:])
}

// addChecksumColumn adds the checksum column to the tracking table, in
// one write on sess, when the table lacks it, as described in the package
// documentation, and logs that it did. On PostgreSQL and MySQL the wait
// for the table's lock is bounded first.
func (s *Service) addChecksumColumn(ctx context.Context, sess *session) error {
	d := sess.dialect

	var added bool

	err := s.write(ctx, sess, func(ctx context.Context, x runner) error {
		has, err := hasRow(ctx, x, d.hasChecksum)
		if err != nil {
			return s.safeErr(err)
		}

		if has {
			return nil
		}

		if d.setLockWait != "" {
			_, err = x.ExecContext(ctx, d.setLockWait, d.lockWait(s.lockTimeout))
			if err != nil {
				return s.safeErr(err)
			}
		}

		_, err = x.ExecContext(ctx, d.addChecksum)
		if err != nil {
			return s.safeErr(err)
		}

		added = true

		return nil
	})
	if err != nil {
		return err
	}

	if added {
		s.logger.Info("checksum column added").Send()
	}

	return nil
}

// hasRow reports whether query, run with x, returns a row.
func hasRow(ctx context.Context, x runner, query string) (has bool, err error) {
	rows, err := x.QueryContext(ctx, query)
	if err != nil {
		return false, err //nolint:wrapcheck // Service.addChecksumColumn wraps it.
	}

	defer func() { err = errors.Join(err, rows.Close()) }()

	has = rows.Next()

	return has, rows.Err() //nolint:wrapcheck // Service.addChecksumColumn wraps it.
}

// recordChecksums writes the registered checksum of each row of rows, in
// their order, that has none and whose version regs holds, each in a
// write of its own that changes the row only while its checksum is still
// empty, and logs how many rows it changed. When it wrote any, it reads
// the tracking table again and returns those rows, or an error wrapping
// ErrDirty when one is dirty now; otherwise it returns rows.
func (s *Service) recordChecksums(
	ctx context.Context,
	sess *session,
	regs []*registered,
	rows []AppliedMigration,
) ([]AppliedMigration, error) {
	byVersion := registeredByVersion(regs)

	var (
		wrote    bool
		recorded int
	)

	for _, row := range rows {
		reg, ok := byVersion[row.Version]
		if !ok || row.Checksum != "" {
			continue
		}

		wrote = true

		n, err := s.recordChecksum(ctx, sess, reg)

		recorded += n

		if err != nil {
			s.logRecorded(recorded)

			return nil, fmt.Errorf(
				"%s: record checksum %s (owner %s): %w",
				ServiceName, reg.mig.Version, reg.owner, err,
			)
		}
	}

	s.logRecorded(recorded)

	if !wrote {
		return rows, nil
	}

	rows, err := s.readRows(ctx, sess.conn, sess.dialect)
	if err != nil {
		return nil, err
	}

	err = dirtyError(rows)
	if err != nil {
		return nil, err
	}

	return rows, nil
}

// recordChecksum writes reg's checksum into its version's row, in a write
// of its own, while the row's checksum is empty, and returns how many rows
// that changed: none when another instance wrote it first.
func (s *Service) recordChecksum(ctx context.Context, sess *session, reg *registered) (int, error) {
	var recorded int

	err := s.write(ctx, sess, func(ctx context.Context, x runner) error {
		res, err := x.ExecContext(ctx, sess.dialect.backfill, reg.checksum, reg.mig.Version)
		if err != nil {
			return s.safeErr(err)
		}

		n, err := res.RowsAffected()
		if err != nil {
			return s.safeErr(err)
		}

		recorded = int(n)

		return nil
	})
	if err != nil {
		return 0, err
	}

	return recorded, nil
}

// logRecorded logs that recordChecksums wrote the checksums of n rows,
// when n is positive.
func (s *Service) logRecorded(n int) {
	if n > 0 {
		s.logger.Info("checksums recorded").Int("count", n).Send()
	}
}

// verifyChecksums returns an error wrapping ErrChecksumMismatch that
// lists, in version order, each row of rows whose version regs holds and
// whose checksum is not empty and differs from the registered one, and
// logs each; or nil when there is none.
func (s *Service) verifyChecksums(regs []*registered, rows []AppliedMigration) error {
	byVersion := registeredByVersion(regs)

	var items []string

	for _, row := range rows {
		reg, ok := byVersion[row.Version]
		if !ok || row.Checksum == "" || row.Checksum == reg.checksum {
			continue
		}

		s.logger.Warn("checksum mismatch").
			Str("version", row.Version).
			Str("owner", reg.owner).
			Str("stored", row.Checksum).
			Str("registered", reg.checksum).
			Send()

		items = append(items, fmt.Sprintf(
			"%s (owner %s) stored %s registered %s",
			row.Version, reg.owner, row.Checksum, reg.checksum,
		))
	}

	if len(items) == 0 {
		return nil
	}

	return fmt.Errorf("%s: %w: %s", ServiceName, ErrChecksumMismatch, strings.Join(items, ", "))
}

// registeredByVersion returns regs by their version.
func registeredByVersion(regs []*registered) map[string]*registered {
	out := make(map[string]*registered, len(regs))

	for _, reg := range regs {
		out[reg.mig.Version] = reg
	}

	return out
}
