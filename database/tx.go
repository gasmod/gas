package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// BeginTx starts a transaction on DB. The caller commits or rolls it
// back. Its errors are as Query's, after `failed to begin transaction: `.
func (s *Service) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	st, err := s.loaded()
	if err != nil {
		return nil, err
	}

	tx, err := st.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, runtimeError(ctx, opBegin, err)
	}

	return tx, nil
}

// WithTx runs fn in a transaction on DB and commits it only when fn
// returns nil, as described in the package documentation. An error, a
// panic or runtime.Goexit in fn rolls it back.
func (s *Service) WithTx(ctx context.Context, opts *sql.TxOptions, fn func(*sql.Tx) error) error {
	if fn == nil {
		return fmt.Errorf("%s: %w", ServiceName, ErrNilFunc)
	}

	tx, err := s.BeginTx(ctx, opts)
	if err != nil {
		return err
	}

	return s.runTx(ctx, sqlTxn{tx: tx}, func() error { return fn(tx) })
}

// BeginPgxTx starts a native pgx transaction on the pool; nil opts are
// pgx's defaults. The caller commits or rolls it back. Outside ModePgx it
// returns ErrModeNotPgx. Its other errors are as BeginTx's.
func (s *Service) BeginPgxTx(ctx context.Context, opts *pgx.TxOptions) (pgx.Tx, error) {
	tx, _, err := s.beginPgxTx(ctx, opts)

	return tx, err
}

// beginPgxTx is BeginPgxTx, returning the state it began the transaction
// on too.
func (s *Service) beginPgxTx(ctx context.Context, opts *pgx.TxOptions) (pgx.Tx, *state, error) {
	st, err := s.loaded()
	if err != nil {
		return nil, nil, err
	}

	if st.pool == nil {
		return nil, nil, fmt.Errorf("%s: %w", ServiceName, ErrModeNotPgx)
	}

	var txOpts pgx.TxOptions

	if opts != nil {
		txOpts = *opts
	}

	tx, err := st.pool.BeginTx(ctx, txOpts)
	if err != nil {
		return nil, nil, runtimeError(ctx, opBegin, err)
	}

	return tx, st, nil
}

// WithPgxTx runs fn in a native pgx transaction and commits it only when
// fn returns nil, as WithTx does. The commit uses ctx; the rollback uses
// ctx without its cancellation, bounded by Settings.ConnectTimeout.
func (s *Service) WithPgxTx(ctx context.Context, opts *pgx.TxOptions, fn func(pgx.Tx) error) error {
	if fn == nil {
		return fmt.Errorf("%s: %w", ServiceName, ErrNilFunc)
	}

	tx, st, err := s.beginPgxTx(ctx, opts)
	if err != nil {
		return err
	}

	t := pgxTxn{ctx: ctx, tx: tx, timeout: st.settings.ConnectTimeout}

	return s.runTx(ctx, t, func() error { return fn(tx) })
}

// txn is a transaction runTx ends.
type txn interface {
	// commit commits the transaction.
	commit() error
	// rollback rolls the transaction back.
	rollback() error
}

// sqlTxn is a database/sql transaction.
type sqlTxn struct {
	// tx is the transaction.
	tx *sql.Tx
}

// commit commits tx.
func (t sqlTxn) commit() error {
	//nolint:wrapcheck // runTx wraps it.
	return t.tx.Commit()
}

// rollback rolls tx back. database/sql takes no context for it.
func (t sqlTxn) rollback() error {
	//nolint:wrapcheck // runTx wraps it.
	return t.tx.Rollback()
}

// pgxTxn is a pgx transaction begun with ctx.
type pgxTxn struct {
	// ctx is the context of WithPgxTx.
	//nolint:containedctx // kept for the commit and rollback of one call.
	ctx context.Context
	// tx is the transaction.
	tx pgx.Tx
	// timeout bounds the rollback.
	timeout time.Duration
}

// commit commits tx with ctx.
func (t pgxTxn) commit() error {
	//nolint:wrapcheck // runTx wraps it.
	return t.tx.Commit(t.ctx)
}

// rollback rolls tx back with ctx without its cancellation, bounded by
// the timeout, so that it runs when ctx has ended and cannot hang on a
// dead connection.
func (t pgxTxn) rollback() error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.ctx), t.timeout)
	defer cancel()

	//nolint:wrapcheck // runTx wraps it.
	return t.tx.Rollback(ctx)
}

// The operations of the transaction errors, after the service name.
const (
	// opBegin is a failed begin.
	opBegin = "failed to begin transaction"
	// opCommit is a failed commit.
	opCommit = "failed to commit transaction"
	// opRollback is a failed rollback.
	opRollback = "failed to roll back transaction"
)

// runTx runs fn, called with ctx, and ends t: it commits only when fn
// returned nil, and rolls back when fn returned an error, panicked or
// called runtime.Goexit. A panic or Goexit goes on once the rollback ran,
// and a failed rollback is then logged with its reason; after an error
// return it is joined to the error.
func (s *Service) runTx(ctx context.Context, t txn, fn func() error) (err error) {
	completed := false

	defer func() {
		if completed && err == nil {
			cErr := t.commit()
			if cErr != nil {
				err = runtimeError(ctx, opCommit, cErr)
			}

			return
		}

		rbErr := t.rollback()
		if !rollbackFailed(rbErr) {
			return
		}

		if !completed {
			s.logger.Warn("transaction rollback failed").Str("reason", reason(rbErr)).Send()

			return
		}

		err = errors.Join(err, runtimeError(ctx, opRollback, rbErr))
	}()

	err = fn()
	completed = true

	return err
}

// rollbackFailed reports whether err, of a rollback, is a failure: not
// nil, and not the transaction having ended already, as when fn committed
// or rolled it back itself.
func rollbackFailed(err error) bool {
	return err != nil && !errors.Is(err, sql.ErrTxDone) && !errors.Is(err, pgx.ErrTxClosed)
}
