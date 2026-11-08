package database_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
)

// tableSeq numbers the tables the PostgreSQL tests create in the shared
// container.
var tableSeq atomic.Int64

// postgresService returns an initialized service in mode on the shared
// PostgreSQL container, closed when the test ends, and a table of its own
// with an `id` column.
func postgresService(t *testing.T, mode database.Mode) (*database.Service, string) {
	t.Helper()

	dsn := postgresDSN(t)
	cfg := fakeConfig(func(s *database.Settings) {
		s.Mode, s.Dialect, s.DSN = mode, database.DialectPostgres, gas.Secret(dsn)
		s.ConnectTimeout = 5 * time.Second
	})
	s := initialized(t, database.WithConfig(cfg))

	table := fmt.Sprintf("t%d", tableSeq.Add(1))
	_, err := s.Exec(t.Context(), "CREATE TABLE "+table+" (id INTEGER)")
	require.NoError(t, err)

	return s, table
}

// count returns the number of rows in table.
func count(t *testing.T, s *database.Service, table string) int {
	t.Helper()

	var n int

	require.NoError(t, s.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&n))

	return n
}

// In ModeSQL with the dialect postgres, the service connects through pgx's
// database/sql adapter, and has no pool.
func TestPostgres_SQLMode(t *testing.T) {
	t.Parallel()

	s, table := postgresService(t, database.ModeSQL)

	assert.Equal(t, "postgres", s.Driver())
	assert.Nil(t, s.Pool())

	_, err := s.Exec(t.Context(), "INSERT INTO "+table+" (id) VALUES ($1)", 1)
	require.NoError(t, err)
	assert.Equal(t, 1, count(t, s, table))

	require.NoError(t, s.CheckReady(t.Context()))
}

// In ModePgx the service opens a pool with the configured limits, DB is
// derived from it, and PoolFrom returns it.
func TestPostgres_PgxMode(t *testing.T) {
	t.Parallel()

	s, table := postgresService(t, database.ModePgx)

	assert.Equal(t, "postgres", s.Driver())
	require.NotNil(t, s.Pool())

	pool, ok := database.PoolFrom(s)
	assert.True(t, ok)
	assert.Same(t, s.Pool(), pool)

	cfg := pool.Config()
	assert.Equal(t, database.DefaultMaxOpenConns, cfg.MaxConns)
	assert.Equal(t, database.DefaultConnMaxLifetime, cfg.MaxConnLifetime)
	assert.Equal(t, database.DefaultConnMaxIdleTime, cfg.MaxConnIdleTime)

	_, err := pool.Exec(t.Context(), "INSERT INTO "+table+" (id) VALUES (1)")
	require.NoError(t, err)
	assert.Equal(t, 1, count(t, s, table), "DB sees what the pool wrote")

	require.NoError(t, s.Ping(t.Context()))
	require.NoError(t, s.CheckReady(t.Context()))
}

// WithPgxTx commits when the function returns nil, and rolls back when it
// returns an error, panics or calls runtime.Goexit.
func TestWithPgxTx(t *testing.T) {
	t.Parallel()

	s, table := postgresService(t, database.ModePgx)
	ctx := t.Context()
	insert := func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO "+table+" (id) VALUES (1)")

		return err
	}

	require.NoError(t, s.WithPgxTx(ctx, nil, insert))
	assert.Equal(t, 1, count(t, s, table))

	err := s.WithPgxTx(ctx, &pgx.TxOptions{}, func(tx pgx.Tx) error {
		require.NoError(t, insert(tx))

		return errFake
	})
	require.Same(t, errFake, err)

	assert.PanicsWithValue(t, "boom", func() {
		_ = s.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
			require.NoError(t, insert(tx))
			panic("boom")
		})
	})

	done := make(chan struct{})

	var (
		insertErr error
		exited    = true
		returned  bool
	)

	go func() {
		defer close(done)

		_ = s.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
			insertErr = insert(tx)
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
	assert.False(t, returned, "Goexit went on past WithPgxTx")
	assert.Equal(t, 1, count(t, s, table), "only the first transaction committed")
}

// A function that commits the pgx transaction itself and returns nil gets
// an error wrapping pgx.ErrTxClosed.
func TestWithPgxTx_FunctionCommits(t *testing.T) {
	t.Parallel()

	s, _ := postgresService(t, database.ModePgx)
	ctx := t.Context()

	err := s.WithPgxTx(ctx, nil, func(tx pgx.Tx) error { return tx.Commit(ctx) })
	require.ErrorIs(t, err, pgx.ErrTxClosed)
	require.EqualError(t, err, "gas/database: failed to commit transaction: tx is closed")
}

// WithPgxTx commits with its context, so a context that ended during the
// function fails the commit, and the work is rolled back.
func TestWithPgxTx_ContextEndedBeforeCommit(t *testing.T) {
	t.Parallel()

	s, table := postgresService(t, database.ModePgx)

	ctx, cancel := context.WithCancel(t.Context())

	err := s.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO "+table+" (id) VALUES (1)")

		cancel()

		return err
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, count(t, s, table))
}

// BeginPgxTx returns a transaction the caller ends; a failed begin is
// returned wrapped.
func TestBeginPgxTx(t *testing.T) {
	t.Parallel()

	s, table := postgresService(t, database.ModePgx)
	ctx := t.Context()

	tx, err := s.BeginPgxTx(ctx, nil)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, "INSERT INTO "+table+" (id) VALUES (1)")
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))
	assert.Zero(t, count(t, s, table))

	ended, cancel := context.WithCancel(ctx)
	cancel()

	_, err = s.BeginPgxTx(ended, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "gas/database: failed to begin transaction: ")
}

// withCredentials returns dsn, a URL, with the user, password and
// database changed.
func withCredentials(t *testing.T, dsn, user, password, db string) string {
	t.Helper()

	u, err := url.Parse(dsn)
	require.NoError(t, err)

	u.User = url.UserPassword(user, password)
	u.Path = "/" + db

	return u.String()
}

// A wrong password and a missing database get their own reasons, from the
// server's error codes, and neither the error nor the retry warnings hold
// the user, the password, the database name, the host or the port.
func TestPostgres_ServerReasonsNoSecrets(t *testing.T) {
	t.Parallel()

	dsn := postgresDSN(t)

	u, err := url.Parse(dsn)
	require.NoError(t, err)

	cases := map[string]string{
		withCredentials(t, dsn, containerUser, secretPassword, containerDB): "authentication failed",
		withCredentials(t, dsn, secretUser, secretPassword, containerDB):    "authentication failed",
		withCredentials(t, dsn, containerUser, containerPassword, secretDB): "database does not exist",
	}

	for bad, want := range cases {
		for _, mode := range []database.Mode{database.ModeSQL, database.ModePgx} {
			cfg := fakeConfig(func(s *database.Settings) {
				s.Mode, s.Dialect, s.DSN = mode, database.DialectPostgres, gas.Secret(bad)
				s.ConnRetries = 1
			})
			s, log := build(t, database.WithConfig(cfg))

			err := s.Init(t.Context())
			require.ErrorIs(t, err, database.ErrConnect)
			require.EqualError(t, err, "gas/database: connect: "+want)

			for _, text := range []string{err.Error(), logText(log)} {
				assertNoSecrets(t, text, bad, u.Hostname(), u.Port(), containerUser, containerDB)
			}
		}
	}
}

// Close in ModePgx closes the pool and the handle derived from it, and
// Init then opens a new pool.
func TestPostgres_PgxRestart(t *testing.T) {
	t.Parallel()

	dsn := postgresDSN(t)
	cfg := fakeConfig(func(s *database.Settings) {
		s.Mode, s.Dialect, s.DSN = database.ModePgx, database.DialectPostgres, gas.Secret(dsn)
	})
	s, _ := build(t, database.WithConfig(cfg))

	require.NoError(t, s.Init(t.Context()))

	pool, db := s.Pool(), s.DB()
	require.NoError(t, s.Close(t.Context()))

	require.Error(t, pool.Ping(t.Context()), "the pool is closed")
	require.Error(t, db.PingContext(t.Context()), "the handle is closed")

	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })
	assert.NotSame(t, pool, s.Pool())
	require.NoError(t, s.Ping(t.Context()))
}

// A pgx rollback runs with the context's cancellation removed, so a
// function that fails after its context ended is still rolled back, and
// the connection goes back to the pool clean.
func TestWithPgxTx_RollbackAfterContextEnded(t *testing.T) {
	t.Parallel()

	s, table := postgresService(t, database.ModePgx)

	ctx, cancel := context.WithCancel(t.Context())

	err := s.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO "+table+" (id) VALUES (1)")
		require.NoError(t, err)
		cancel()

		return errFake
	})
	require.True(t, errors.Is(err, errFake) && !strings.Contains(err.Error(), "roll back"), err)
	assert.Zero(t, count(t, s, table))
	assert.Zero(t, s.Pool().Stat().AcquiredConns())
}

// The pool opens its minimum of connections in the background after Init
// returns, on a context that the end of the connect attempt does not
// cancel.
func TestPostgres_PgxMinConns(t *testing.T) {
	t.Parallel()

	u, err := url.Parse(postgresDSN(t))
	require.NoError(t, err)

	q := u.Query()

	q.Set("pool_min_conns", "3")

	u.RawQuery = q.Encode()

	cfg := fakeConfig(func(s *database.Settings) {
		s.Mode, s.Dialect, s.DSN = database.ModePgx, database.DialectPostgres, gas.Secret(
			u.String(),
		)
		s.ConnectTimeout = 5 * time.Second
	})
	s := initialized(t, database.WithConfig(cfg))

	require.Eventually(t, func() bool {
		return s.Pool().Stat().TotalConns() >= 3
	}, 10*time.Second, 20*time.Millisecond)
}
