package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// rollbackErr normalizes a rollback failure. A transaction that fn already
// committed or rolled back itself reports sql.ErrTxDone / pgx.ErrTxClosed;
// that is not a failure, so it is dropped. Anything else is logged and
// returned wrapped, since a failed rollback can leave the connection
// holding an open transaction. The returned error wraps rbErr, so callers
// can match the driver's error with errors.Is or errors.As.
func (s *Service) rollbackErr(rbErr error) error {
	if rbErr == nil || errors.Is(rbErr, sql.ErrTxDone) || errors.Is(rbErr, pgx.ErrTxClosed) {
		return nil
	}
	s.logger.Error("failed to roll back transaction").Err("error", rbErr).Send()
	return fmt.Errorf("%s: failed to roll back transaction: %w", s.Name(), rbErr)
}

// commitErr wraps a commit failure with the service name. It returns nil
// when cErr is nil. The returned error wraps cErr, so callers can match
// the driver's error with errors.Is or errors.As.
func (s *Service) commitErr(cErr error) error {
	if cErr == nil {
		return nil
	}
	return fmt.Errorf("%s: failed to commit transaction: %w", s.Name(), cErr)
}

// BeginTx starts a new database transaction. The caller is responsible
// for calling Commit or Rollback on the returned *sql.Tx.
func (s *Service) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	if s.closed.Load() {
		return nil, fmt.Errorf("%s: service is closed", s.Name())
	}
	if s.db == nil {
		return nil, fmt.Errorf("%s: not initialized", s.Name())
	}
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("%s: failed to begin transaction: %w", s.Name(), err)
	}
	return tx, nil
}

// WithTx executes fn within a transaction. If fn returns nil the
// transaction is committed; otherwise it is rolled back and a failing
// rollback is joined onto fn's error. Any panic inside fn also triggers a
// rollback, whose failure is logged rather than returned so the panic
// propagates unchanged.
func (s *Service) WithTx(ctx context.Context, opts *sql.TxOptions, fn func(*sql.Tx) error) (err error) {
	if s.closed.Load() {
		return fmt.Errorf("%s: service is closed", s.Name())
	}
	if s.db == nil {
		return fmt.Errorf("%s: not initialized", s.Name())
	}

	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("%s: failed to begin transaction: %w", s.Name(), err)
	}

	defer func() {
		if p := recover(); p != nil {
			// Logged by rollbackErr; the panic takes precedence over it.
			_ = s.rollbackErr(tx.Rollback())
			panic(p)
		}
		if err != nil {
			if rbErr := s.rollbackErr(tx.Rollback()); rbErr != nil {
				err = errors.Join(err, rbErr)
			}
			return
		}
		err = s.commitErr(tx.Commit())
	}()

	err = fn(tx)
	return err
}

// BeginPgxTx starts a new native pgx transaction. Pass nil opts for pgx
// defaults. The caller is responsible for calling Commit or Rollback on
// the returned pgx.Tx. It returns an error when the service is closed or
// is not running in ModePgx.
func (s *Service) BeginPgxTx(ctx context.Context, opts *pgx.TxOptions) (pgx.Tx, error) {
	if s.closed.Load() {
		return nil, fmt.Errorf("%s: service is closed", s.Name())
	}
	if s.pool == nil {
		return nil, fmt.Errorf("%s: attempt to begin pgx transaction in non-pgx mode '%s'", s.Name(), s.cfg.Database.Mode)
	}

	var txOpts pgx.TxOptions
	if opts != nil {
		txOpts = *opts
	}

	tx, err := s.pool.BeginTx(ctx, txOpts)
	if err != nil {
		return nil, fmt.Errorf("%s: failed to begin pgx transaction: %w", s.Name(), err)
	}
	return tx, nil
}

// WithPgxTx executes fn within a native pgx transaction. If fn returns nil
// the transaction is committed; otherwise it is rolled back and a failing
// rollback is joined onto fn's error. Any panic inside fn also triggers a
// rollback, whose failure is logged rather than returned so the panic
// propagates unchanged. Rollback uses a context detached from ctx's
// cancellation so it still runs when ctx is already done; the commit uses
// ctx unchanged, so an already-canceled ctx fails the commit instead of
// persisting work the caller abandoned. It returns an error when the
// service is closed or is not running in ModePgx.
func (s *Service) WithPgxTx(ctx context.Context, opts *pgx.TxOptions, fn func(pgx.Tx) error) (err error) {
	tx, err := s.BeginPgxTx(ctx, opts)
	if err != nil {
		return err
	}

	defer func() {
		rollbackCtx := context.WithoutCancel(ctx)
		if p := recover(); p != nil {
			// Logged by rollbackErr; the panic takes precedence over it.
			_ = s.rollbackErr(tx.Rollback(rollbackCtx))
			panic(p)
		}
		if err != nil {
			if rbErr := s.rollbackErr(tx.Rollback(rollbackCtx)); rbErr != nil {
				err = errors.Join(err, rbErr)
			}
			return
		}
		err = s.commitErr(tx.Commit(ctx))
	}()

	err = fn(tx)
	return err
}
