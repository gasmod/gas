package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gasmod/gas/migrate/internal/mysqlsplit"
)

// sqliteRecheck reads, inside a SQLite write, the rows that decide whether
// a change is still to be made: its version's and the dirty ones.
const sqliteRecheck = "SELECT version, dirty FROM " + TableName + " WHERE version = ? OR dirty"

// RunPending is RunPendingContext with context.Background().
func (s *Service) RunPending() error {
	return s.RunPendingContext(context.Background())
}

// RunPendingContext applies the registered migrations that are not
// applied, in version order, under the migration lock, as described in
// the package documentation. It stops at the first failure, which marks
// that migration dirty. While a migration is dirty it applies nothing and
// returns an error wrapping ErrDirty.
func (s *Service) RunPendingContext(ctx context.Context) error {
	st, err := s.loaded()
	if err != nil {
		return err
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

		pending := pendingOf(regs, rows)
		if len(pending) == 0 {
			s.logger.Debug("no pending migrations").Send()

			return nil
		}

		for _, reg := range pending {
			applyErr := s.change(ctx, sess, &change{reg: reg, verb: verbApply})
			if applyErr != nil {
				return applyErr
			}
		}

		return nil
	})
}

// The verbs of a change, as its errors name it.
const (
	// verbApply applies a migration.
	verbApply = "apply"
	// verbRevert reverts one.
	verbRevert = "revert"
)

// change is one migration to apply or revert.
type change struct {
	// reg is the migration.
	reg *registered
	// verb is verbApply or verbRevert.
	verb string
}

// sqlText returns the SQL the change runs.
func (c *change) sqlText() string {
	if c.verb == verbApply {
		return c.reg.mig.Up
	}

	return c.reg.mig.Down
}

// dirs returns the directives of the SQL the change runs.
func (c *change) dirs() directives {
	if c.verb == verbApply {
		return c.reg.up
	}

	return c.reg.down
}

// rowWrite is a statement on the tracking table and its arguments.
type rowWrite struct {
	// query is the statement.
	query string
	// step names it in errors.
	step string
	// args are its arguments.
	args []any
}

// record is the write that ends a change run as a unit: an apply inserts
// its row, a revert deletes it.
func (c *change) record(d *dialect) rowWrite {
	if c.verb == verbApply {
		return rowWrite{
			query: d.insert,
			step:  "record",
			args:  []any{c.reg.mig.Version, c.reg.owner, c.reg.mig.Description},
		}
	}

	return rowWrite{query: d.delete, step: "remove row", args: []any{c.reg.mig.Version}}
}

// mark is the write that starts a change run write-ahead: an apply
// inserts its row dirty, a revert marks it dirty.
func (c *change) mark(d *dialect) rowWrite {
	if c.verb == verbApply {
		return rowWrite{
			query: d.claim,
			step:  "mark dirty",
			args:  []any{c.reg.mig.Version, c.reg.owner, c.reg.mig.Description},
		}
	}

	return rowWrite{query: d.markDirty, step: "mark dirty", args: []any{c.reg.mig.Version}}
}

// clear is the write that ends a change run write-ahead: an apply marks
// its row clean, a revert deletes it.
func (c *change) clear(d *dialect) rowWrite {
	if c.verb == verbApply {
		return rowWrite{query: d.markClean, step: "record", args: []any{c.reg.mig.Version}}
	}

	return c.record(d)
}

// exec runs w on x, its error after w's step. Each write of a row must
// change exactly that row: any other count is an error wrapping
// ErrTrackingRowLost.
func (s *Service) exec(ctx context.Context, x runner, w rowWrite) error {
	res, err := x.ExecContext(ctx, w.query, w.args...)
	if err != nil {
		return fmt.Errorf("%s: %w", w.step, s.safeErr(err))
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", w.step, s.safeErr(err))
	}

	if n != 1 {
		return fmt.Errorf("%s: %w", w.step, ErrTrackingRowLost)
	}

	return nil
}

// subject returns err after the change's subject: the service, the verb,
// the version and its owner. It returns nil for a nil err.
func (c *change) subject(err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf(
		"%s: %s %s (owner %s): %w",
		ServiceName, c.verb, c.reg.mig.Version, c.reg.owner, err,
	)
}

// failure returns err as the change's failure, wrapping
// ErrMigrationFailed.
func (c *change) failure(err error) error {
	return c.subject(fmt.Errorf("%w: %w", ErrMigrationFailed, err))
}

// change applies or reverts one migration, as described in the package
// documentation, and logs it when it ran.
func (s *Service) change(ctx context.Context, sess *session, c *change) error {
	start := time.Now()

	stmts, err := s.statements(ctx, sess, c.sqlText(), c.dirs())
	if err != nil {
		return c.subject(err)
	}

	ran := true

	if sess.dialect.writeAhead || c.dirs().noTransaction {
		err = s.changeWriteAhead(ctx, sess, c, stmts)
	} else {
		ran, err = s.changeInUnit(ctx, sess, c, stmts)
	}

	if err != nil || !ran {
		return err
	}

	msg := "migration applied"
	if c.verb == verbRevert {
		msg = "migration reverted"
	}

	s.logger.Info(msg).
		Str("version", c.reg.mig.Version).
		Str("owner", c.reg.owner).
		Duration("duration", time.Since(start)).
		Send()

	return nil
}

// changeInUnit runs the change and the write of its row as one unit. It
// reports false, with no error, when on SQLite another runner made the
// change first. A failure is rolled back; a failed apply is then marked
// dirty.
func (s *Service) changeInUnit(
	ctx context.Context,
	sess *session,
	c *change,
	stmts []string,
) (bool, error) {
	u, err := s.begin(ctx, sess)
	if err != nil {
		if sess.dialect.lock == nil {
			// On SQLite beginning is taking the lock: nothing ran.
			return false, c.subject(err)
		}

		return false, s.unitFailed(ctx, sess, c, err)
	}

	defer u.abandon()

	if sess.dialect.lock == nil {
		due, dueErr := s.due(ctx, u.x, c)
		if dueErr != nil || !due {
			return false, c.subject(errors.Join(dueErr, u.rollback()))
		}
	}

	err = runAll(ctx, u.x, stmts, s.safeErr)
	if err == nil {
		err = s.exec(ctx, u.x, c.record(sess.dialect))
	}

	if err == nil {
		err = u.commit()
		if err == nil {
			return true, nil
		}
	}

	return false, s.unitFailed(ctx, sess, c, errors.Join(err, u.rollback()))
}

// unitFailed returns the failure of a change run as a unit, which was
// rolled back. A failed apply is marked dirty, unless its version has a
// row or the connection is broken; a mark that fails is joined to the
// error.
func (s *Service) unitFailed(ctx context.Context, sess *session, c *change, cause error) error {
	err := c.failure(cause)

	if c.verb != verbApply || sess.broken {
		return err
	}

	markCtx, cancel := detached(ctx)
	defer cancel()

	var marked bool

	markErr := s.write(markCtx, sess, func(ctx context.Context, x runner) error {
		res, execErr := x.ExecContext(
			ctx, sess.dialect.insertDirty, c.reg.mig.Version, c.reg.owner, c.reg.mig.Description,
		)
		if execErr != nil {
			return s.safeErr(execErr)
		}

		n, execErr := res.RowsAffected()
		if execErr != nil {
			return s.safeErr(execErr)
		}

		marked = n == 1

		return nil
	})
	if markErr != nil {
		return fmt.Errorf("%w; mark dirty: %w", err, markErr)
	}

	if marked {
		s.warnDirty(c)
	}

	return err
}

// warnDirty logs that the change's migration is marked dirty.
func (s *Service) warnDirty(c *change) {
	s.logger.Warn("migration marked dirty").
		Str("version", c.reg.mig.Version).
		Str("owner", c.reg.owner).
		Send()
}

// changeWriteAhead marks the change's row dirty, then runs the change,
// then clears the mark, by marking an applied row clean or deleting a
// reverted one, so that a failure, a cancel or a crash between the two
// leaves the row dirty. On MySQL the change and the clearing run in one
// transaction; with the no-transaction directive they run as they come.
// It runs only under the migration lock: SQLite, which has none, never
// writes ahead. A clearing that finds the row gone fails with
// ErrTrackingRowLost, and logs no dirty mark, since there is none.
func (s *Service) changeWriteAhead(
	ctx context.Context,
	sess *session,
	c *change,
	stmts []string,
) error {
	err := s.exec(ctx, sess.conn, c.mark(sess.dialect))
	if err != nil {
		return c.subject(err)
	}

	if c.dirs().noTransaction {
		err = runAll(ctx, sess.conn, stmts, s.safeErr)
		if err == nil {
			err = s.exec(ctx, sess.conn, c.clear(sess.dialect))
		}
	} else {
		err = s.inTx(ctx, sess, func(ctx context.Context, x runner) error {
			runErr := runAll(ctx, x, stmts, s.safeErr)
			if runErr != nil {
				return runErr
			}

			return s.exec(ctx, x, c.clear(sess.dialect))
		})
	}

	if err != nil {
		if !errors.Is(err, ErrTrackingRowLost) {
			s.warnDirty(c)
		}

		return c.failure(err)
	}

	return nil
}

// inTx runs fn in a unit on sess's connection, which commits when fn
// succeeds and rolls back otherwise. A rollback that fails is joined to
// the error.
func (s *Service) inTx(
	ctx context.Context,
	sess *session,
	fn func(context.Context, runner) error,
) error {
	u, err := s.begin(ctx, sess)
	if err != nil {
		return err
	}

	defer u.abandon()

	err = fn(ctx, u.x)
	if err == nil {
		err = u.commit()
		if err == nil {
			return nil
		}
	}

	return errors.Join(err, u.rollback())
}

// due reports, inside a SQLite write, whether the change is still to be
// made: an apply while its version has no row, a revert while it has one.
// A dirty row is an error wrapping ErrDirty, as RunPending's own check
// returns it, without the service name.
func (s *Service) due(ctx context.Context, x runner, c *change) (bool, error) {
	hasRow, dirty, err := s.recheck(ctx, x, c.reg.mig.Version)
	if err != nil {
		return false, fmt.Errorf("read tracking table: %w", s.safeErr(err))
	}

	if len(dirty) > 0 {
		return false, fmt.Errorf("%w: %s", ErrDirty, strings.Join(dirty, ", "))
	}

	return hasRow == (c.verb == verbRevert), nil
}

// recheck reads with x whether version has a row, and the dirty versions.
func (*Service) recheck(
	ctx context.Context,
	x runner,
	version string,
) (hasRow bool, dirty []string, err error) {
	rows, err := x.QueryContext(ctx, sqliteRecheck, version)
	if err != nil {
		return false, nil, err //nolint:wrapcheck // Service.due wraps it.
	}

	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var (
			v string
			d bool
		)

		err = rows.Scan(&v, &d)
		if err != nil {
			return false, nil, err //nolint:wrapcheck // Service.due wraps it.
		}

		hasRow = hasRow || v == version

		if d {
			dirty = append(dirty, v)
		}
	}

	return hasRow, dirty, rows.Err() //nolint:wrapcheck // Service.due wraps it.
}

// runner runs statements and queries, as *sql.Conn and *sql.Tx do.
type runner interface {
	// ExecContext runs a statement that returns no rows.
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	// QueryContext runs a query that returns rows.
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// runAll runs stmts on x.
func runAll(ctx context.Context, x runner, stmts []string, safe func(error) error) error {
	for _, stmt := range stmts {
		_, err := x.ExecContext(ctx, stmt)
		if err != nil {
			return safe(err)
		}
	}

	return nil
}

// statements returns the statements to send for sqlText: on MySQL, unless
// dirs says no-split, its statements as mysqlsplit reads them in the
// session's SQL mode, which it reads first; elsewhere sqlText whole.
func (s *Service) statements(
	ctx context.Context,
	sess *session,
	sqlText string,
	dirs directives,
) ([]string, error) {
	if !sess.dialect.split || dirs.noSplit {
		return []string{sqlText}, nil
	}

	var mode string

	err := sess.conn.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode").Scan(&mode)
	if err != nil {
		return nil, fmt.Errorf("read sql_mode: %w", s.safeErr(err))
	}

	return mysqlsplit.Split(sqlText, mysqlsplit.ParseMode(mode)), nil
}

// write runs fn as one write on sess's connection: on SQLite in a unit of
// its own, which takes the database's write lock; elsewhere as it comes,
// under the migration lock the session holds.
func (s *Service) write(
	ctx context.Context,
	sess *session,
	fn func(context.Context, runner) error,
) error {
	if sess.dialect.lock != nil {
		return fn(ctx, sess.conn)
	}

	return s.inTx(ctx, sess, fn)
}

// unit is the transaction a migration, or one write, runs in.
type unit struct {
	// x runs the unit's statements.
	x runner
	// commitFn ends the unit, keeping its work.
	commitFn func() error
	// rollbackFn ends the unit, undoing its work.
	rollbackFn func() error
	// ended reports that the unit committed or was rolled back.
	ended bool
}

// commit ends the unit, keeping its work.
func (u *unit) commit() error {
	err := u.commitFn()
	if err == nil {
		u.ended = true
	}

	return err
}

// rollback ends the unit, undoing its work.
func (u *unit) rollback() error {
	u.ended = true

	return u.rollbackFn()
}

// abandon rolls back a unit that neither committed nor was rolled back,
// which happens only when the function that began it panics; the panic
// goes on. Deferred where a unit begins, it keeps an open transaction
// from reaching the pool with a SQLite connection. A rollback that fails
// marks a SQLite session broken, so its connection is closed; elsewhere
// the connection is always closed.
func (u *unit) abandon() {
	if !u.ended {
		_ = u.rollback()
	}
}

// begin starts a unit on sess's connection: on SQLite a transaction that
// BEGIN IMMEDIATE starts, taking the database's write lock, waited for
// as the migration lock is, so that a failure is `acquire lock: ` and its
// cause; elsewhere a database/sql transaction.
func (s *Service) begin(ctx context.Context, sess *session) (*unit, error) {
	if sess.dialect.lock == nil {
		return s.beginImmediate(ctx, sess)
	}

	tx, err := sess.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", s.safeErr(err))
	}

	return &unit{
		x: tx,
		commitFn: func() error {
			commitErr := tx.Commit()
			if commitErr != nil {
				return fmt.Errorf("commit: %w", s.safeErr(commitErr))
			}

			return nil
		},
		rollbackFn: func() error {
			rbErr := tx.Rollback()
			if rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				return fmt.Errorf("roll back: %w", s.safeErr(rbErr))
			}

			return nil
		},
	}, nil
}

// beginImmediate starts a SQLite write transaction, trying BEGIN
// IMMEDIATE again while another connection holds the write lock. Its
// COMMIT is tried again while readers hold the database, which leaves
// the transaction open. A ROLLBACK that fails marks the session broken.
func (s *Service) beginImmediate(ctx context.Context, sess *session) (*unit, error) {
	conn := sess.conn

	err := s.retryBusy(ctx, stepLock, func(ctx context.Context) error {
		_, execErr := conn.ExecContext(ctx, "BEGIN IMMEDIATE")

		return execErr //nolint:wrapcheck // Service.wait wraps it.
	})
	if err != nil {
		return nil, err
	}

	return &unit{
		x: conn,
		commitFn: func() error {
			return s.retryBusy(ctx, stepCommit, func(ctx context.Context) error {
				_, execErr := conn.ExecContext(ctx, "COMMIT")

				return execErr //nolint:wrapcheck // Service.wait wraps it.
			})
		},
		rollbackFn: func() error {
			rbCtx, cancel := detached(ctx)
			defer cancel()

			_, rbErr := conn.ExecContext(rbCtx, "ROLLBACK")
			if rbErr != nil {
				sess.broken = true

				return fmt.Errorf("roll back: %w", s.safeErr(rbErr))
			}

			return nil
		},
	}, nil
}

// retryBusy runs fn until it does not fail with SQLITE_BUSY, which the
// driver error's Code method tells, within ctx and the lock timeout, as
// a wait of step.
func (s *Service) retryBusy(
	ctx context.Context,
	step string,
	fn func(context.Context) error,
) error {
	lockCtx, cancel := context.WithTimeout(ctx, s.lockTimeout)
	defer cancel()

	return s.wait(ctx, lockCtx, step, false, func(ctx context.Context, _ int) (bool, error) {
		err := fn(ctx)
		if err == nil {
			return true, nil
		}

		var coded interface{ Code() int }

		if errors.As(err, &coded) && coded.Code()&0xff == sqliteBusy {
			return false, nil
		}

		return false, err
	})
}
