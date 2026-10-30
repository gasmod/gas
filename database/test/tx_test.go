package database_test

import (
	"context"
	"database/sql"
	"errors"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/database"
)

// errRollback is the error the fake's rollbacks fail with.
var errRollback = errors.New("rollback broke")

// countNotes returns the number of rows in the notes table.
func countNotes(t *testing.T, s *database.Service) int {
	t.Helper()

	var n int

	require.NoError(t, s.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM notes").Scan(&n))

	return n
}

// insertNote inserts a note in tx.
func insertNote(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO notes (body) VALUES ('x')")

	return err
}

// WithTx commits when the function returns nil.
func TestWithTx_Commits(t *testing.T) {
	t.Parallel()

	s := sqliteService(t)
	ctx := t.Context()

	require.NoError(t, s.WithTx(ctx, nil, func(tx *sql.Tx) error { return insertNote(ctx, tx) }))
	assert.Equal(t, 1, countNotes(t, s))
}

// WithTx rolls back when the function returns an error, and returns that
// error unchanged.
func TestWithTx_RollsBackOnError(t *testing.T) {
	t.Parallel()

	s := sqliteService(t)
	ctx := t.Context()

	err := s.WithTx(ctx, nil, func(tx *sql.Tx) error {
		require.NoError(t, insertNote(ctx, tx))

		return errFake
	})
	require.Same(t, errFake, err)
	assert.Zero(t, countNotes(t, s))
}

// WithTx rolls back when the function panics, and the panic goes on.
func TestWithTx_RollsBackOnPanic(t *testing.T) {
	t.Parallel()

	s := sqliteService(t)
	ctx := t.Context()

	assert.PanicsWithValue(t, "boom", func() {
		_ = s.WithTx(ctx, nil, func(tx *sql.Tx) error {
			require.NoError(t, insertNote(ctx, tx))
			panic("boom")
		})
	})
	assert.Zero(t, countNotes(t, s))
}

// WithTx rolls back when the function calls runtime.Goexit, as t.FailNow
// does, instead of committing the work done so far.
func TestWithTx_RollsBackOnGoexit(t *testing.T) {
	t.Parallel()

	s := sqliteService(t)
	ctx := t.Context()
	done := make(chan struct{})

	var (
		insertErr error
		exited    = true
		returned  bool
	)

	go func() {
		defer close(done)

		_ = s.WithTx(ctx, nil, func(tx *sql.Tx) error {
			insertErr = insertNote(ctx, tx)
			if insertErr != nil {
				return insertErr
			}

			runtime.Goexit()

			exited = false

			return nil
		})
		returned = true
	}()

	<-done
	require.NoError(t, insertErr, "the insert ran before Goexit")
	assert.True(t, exited, "Goexit ended the function")
	assert.False(t, returned, "Goexit went on past WithTx")
	assert.Zero(t, countNotes(t, s))
}

// A function that commits the transaction itself and returns nil gets an
// error wrapping sql.ErrTxDone from WithTx's commit.
func TestWithTx_FunctionCommits(t *testing.T) {
	t.Parallel()

	s := sqliteService(t)
	ctx := t.Context()

	err := s.WithTx(ctx, nil, func(tx *sql.Tx) error {
		require.NoError(t, insertNote(ctx, tx))

		return tx.Commit()
	})
	require.ErrorIs(t, err, sql.ErrTxDone)
	require.EqualError(
		t,
		err,
		"gas/database: failed to commit transaction: sql: transaction has already been committed or rolled back",
	)
	assert.Equal(t, 1, countNotes(t, s))
}

// A function that rolls the transaction back itself and returns an error
// gets its error alone: the rollback finding the transaction ended is not
// a failure.
func TestWithTx_FunctionRollsBack(t *testing.T) {
	t.Parallel()

	s := sqliteService(t)

	err := s.WithTx(t.Context(), nil, func(tx *sql.Tx) error {
		require.NoError(t, tx.Rollback())

		return errFake
	})
	require.Same(t, errFake, err)
}

// WithTx and WithPgxTx return ErrNilFunc for a nil function, before
// beginning a transaction.
func TestWithTx_NilFunc(t *testing.T) {
	t.Parallel()

	f := &fake{}
	s, _ := fakeService(t, f)

	err := s.WithTx(t.Context(), nil, nil)
	require.ErrorIs(t, err, database.ErrNilFunc)
	require.EqualError(t, err, "gas/database: transaction function is nil")

	err = s.WithPgxTx(t.Context(), nil, nil)
	require.ErrorIs(t, err, database.ErrNilFunc)

	f.get(func(f *fake) { assert.Zero(t, f.commits+f.rollbacks) })
}

// A failed begin is returned wrapped by BeginTx and WithTx.
func TestBeginTx_Error(t *testing.T) {
	t.Parallel()

	f := &fake{beginErr: errFake}
	s, _ := fakeService(t, f)

	_, err := s.BeginTx(t.Context(), nil)
	require.ErrorIs(t, err, errFake)
	require.EqualError(t, err, "gas/database: failed to begin transaction: fake: not supported")

	err = s.WithTx(t.Context(), nil, func(*sql.Tx) error { return nil })
	require.ErrorIs(t, err, errFake)
}

// A failed commit is returned wrapped.
func TestWithTx_CommitError(t *testing.T) {
	t.Parallel()

	f := &fake{commitErr: errFake}
	s, _ := fakeService(t, f)

	err := s.WithTx(t.Context(), nil, func(*sql.Tx) error { return nil })
	require.ErrorIs(t, err, errFake)
	require.EqualError(t, err, "gas/database: failed to commit transaction: fake: not supported")
}

// After an error return, a failed rollback is joined to the error.
func TestWithTx_RollbackErrorJoined(t *testing.T) {
	t.Parallel()

	rbErr := errRollback
	f := &fake{rollbackErr: rbErr}
	s, log := fakeService(t, f)

	err := s.WithTx(t.Context(), nil, func(*sql.Tx) error { return errFake })
	require.ErrorIs(t, err, errFake)
	require.ErrorIs(t, err, rbErr)
	require.EqualError(
		t,
		err,
		"fake: not supported\ngas/database: failed to roll back transaction: rollback broke",
	)
	assert.Empty(t, log.warnings())
}

// After a panic, a failed rollback is logged at warn level with its
// reason, and the panic goes on.
func TestWithTx_RollbackErrorLoggedOnPanic(t *testing.T) {
	t.Parallel()

	rbErr := errRollback
	f := &fake{rollbackErr: rbErr}
	s, log := fakeService(t, f)

	assert.PanicsWithValue(t, "boom", func() {
		_ = s.WithTx(t.Context(), nil, func(*sql.Tx) error { panic("boom") })
	})

	warnings := log.warnings()
	require.Len(t, warnings, 1)
	assert.Equal(t, "transaction rollback failed", warnings[0].msg)
	assert.Equal(
		t,
		map[string]any{"service": "gas/database", "reason": "failed"},
		warnings[0].fields,
		"the reason, not the driver's error",
	)
}
