package database_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
)

// assertUninitialized checks everything the package documentation says of
// a service that is not initialized.
func assertUninitialized(t *testing.T, s *database.Service) {
	t.Helper()

	ctx := t.Context()

	assert.Nil(t, s.DB())
	assert.Nil(t, s.Pool())
	assert.Empty(t, s.Driver())

	pool, ok := database.PoolFrom(s)
	assert.Nil(t, pool)
	assert.False(t, ok)

	_, qErr := s.Query(ctx, "SELECT 1")
	_, eErr := s.Exec(ctx, "SELECT 1")
	_, bErr := s.BeginTx(ctx, nil)
	_, pErr := s.BeginPgxTx(ctx, nil)

	for name, err := range map[string]error{
		"Ping":        s.Ping(ctx),
		"CheckHealth": s.CheckHealth(ctx),
		"CheckReady":  s.CheckReady(ctx),
		"Query":       qErr,
		"Exec":        eErr,
		"BeginTx":     bErr,
		"WithTx":      s.WithTx(ctx, nil, func(*sql.Tx) error { return nil }),
		"BeginPgxTx":  pErr,
		"WithPgxTx":   s.WithPgxTx(ctx, nil, func(pgx.Tx) error { return nil }),
	} {
		require.ErrorIs(t, err, database.ErrNotInitialized, name)
		require.EqualError(t, err, "gas/database: service not initialized", name)
	}
}

// Before Init, every method but Name, Init and Close reports the service
// not initialized, and the accessors return nothing.
func TestService_BeforeInit(t *testing.T) {
	t.Parallel()

	s, _ := build(t, database.WithConfig(sqliteConfig(t)))

	assertUninitialized(t, s)
	require.NoError(t, s.Close(t.Context()), "Close before Init")
}

// After Close the service is uninitialized again: the handle is closed,
// the accessors return nothing, and a second Close returns nil.
func TestService_AfterClose(t *testing.T) {
	t.Parallel()

	s, log := build(t, database.WithConfig(sqliteConfig(t)))
	require.NoError(t, s.Init(t.Context()))

	db := s.DB()
	require.NoError(t, s.Close(t.Context()))

	assertUninitialized(t, s)
	require.Error(t, db.PingContext(t.Context()), "the old handle is closed")
	require.NoError(t, s.Close(t.Context()))

	entries := log.entries()
	require.Len(t, entries, 2)
	assert.Equal(t, "service initialized", entries[0].msg)
	assert.Equal(
		t,
		map[string]any{"service": "gas/database", "mode": "sql", "dialect": "sqlite"},
		entries[0].fields,
	)
	assert.Equal(t, "service closed", entries[1].msg)
	assert.Equal(t, "info", entries[1].level)
}

// A failed Init leaves the service uninitialized.
func TestService_AfterFailedInit(t *testing.T) {
	t.Parallel()

	s, _ := build(
		t,
		database.WithConfig(withSettings(t, func(s *database.Settings) { s.DSN = "" })),
	)

	require.Error(t, s.Init(t.Context()))
	assertUninitialized(t, s)
}

// A failed Init closes every connection it opened, and never closes the
// connector, which the caller owns.
func TestInit_FailureClosesWhatItOpened(t *testing.T) {
	t.Parallel()

	f := &fake{pingErr: errFake}
	s, _ := build(t,
		database.WithConfig(fakeConfig(func(s *database.Settings) { s.ConnRetries = 2 })),
		database.WithConnector(database.DialectPostgres, f.connector()),
	)

	err := s.Init(t.Context())
	require.EqualError(t, err, "gas/database: connect: failed")
	assertUninitialized(t, s)

	f.get(func(f *fake) {
		assert.Equal(t, 3, f.connects, "one connection per attempt")
		assert.Zero(t, f.open, "every connection was closed")
		assert.False(t, f.connectorClosed)
	})
}

// Init on an initialized service returns ErrAlreadyInitialized and
// leaves the running handle open and in place.
func TestInit_Twice(t *testing.T) {
	t.Parallel()

	s := sqliteService(t)
	db := s.DB()

	err := s.Init(t.Context())
	require.ErrorIs(t, err, database.ErrAlreadyInitialized)
	require.EqualError(t, err, "gas/database: service already initialized")

	assert.Same(t, db, s.DB())
	require.NoError(t, db.PingContext(t.Context()))
}

// After Close, Init works again with a new handle: the service restarts.
func TestService_Restart(t *testing.T) {
	t.Parallel()

	cfg := sqliteConfig(t)
	s, _ := build(t, database.WithConfig(cfg))

	require.NoError(t, s.Init(t.Context()))

	_, err := s.Exec(t.Context(), "CREATE TABLE kept (id INTEGER)")
	require.NoError(t, err)

	first := s.DB()
	require.NoError(t, s.Close(t.Context()))
	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	assert.NotSame(t, first, s.DB())

	_, err = s.Exec(t.Context(), "INSERT INTO kept (id) VALUES (1)")
	require.NoError(t, err, "the database file outlives the handle")
}

// Close returns the error of closing the handle, and still leaves the
// service uninitialized.
func TestClose_Error(t *testing.T) {
	t.Parallel()

	f := &fake{}
	s, _ := fakeService(t, f)

	f.set(func(f *fake) { f.closeErr = errFake })

	err := s.Close(t.Context())
	require.ErrorIs(t, err, errFake)
	require.EqualError(t, err, "gas/database: close: fake: not supported")
	assertUninitialized(t, s)

	f.get(func(f *fake) { assert.False(t, f.connectorClosed) })
}

// Init, Close, Ping, CheckHealth, DB, Driver and Query are safe to call
// concurrently, as the race detector checks; a call beside Close may
// fail, but never sees half an Init.
func TestService_Concurrent(t *testing.T) {
	t.Parallel()

	s, _ := build(t, database.WithConfig(sqliteConfig(t)))
	ctx := t.Context()

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			for range 20 {
				err := s.Init(ctx)
				if err != nil && !errors.Is(err, database.ErrAlreadyInitialized) {
					t.Errorf("Init: %v", err)
				}
			}
		})
		wg.Go(func() {
			for range 20 {
				_ = s.Close(ctx)
			}
		})
		wg.Go(func() {
			for range 20 {
				_ = s.Ping(ctx)
				_ = s.CheckHealth(ctx)
				_ = s.CheckReady(ctx)
				_ = s.DB()
				_ = s.Pool()

				if d := s.Driver(); d != "" && d != "sqlite" {
					t.Errorf("Driver() = %q", d)
				}

				rows, err := s.Query(ctx, "SELECT 1")
				if err == nil {
					assert.NoError(t, rows.Close())
				}
			}
		})
	}

	wg.Wait()
	require.NoError(t, s.Close(ctx))
}

// CheckHealth does no I/O: it succeeds on an initialized service even
// when the database does not answer, which CheckReady and Ping report.
func TestCheckHealth_NoIO(t *testing.T) {
	t.Parallel()

	f := &fake{}
	s, _ := fakeService(t, f)

	require.NoError(t, s.CheckHealth(t.Context()))
	require.NoError(t, s.CheckReady(t.Context()))

	f.set(func(f *fake) { f.pingErr = errFake })

	require.NoError(t, s.CheckHealth(t.Context()))

	err := s.CheckReady(t.Context())
	require.ErrorIs(t, err, database.ErrConnect)
	require.EqualError(t, err, "gas/database: ping: failed")

	err = s.Ping(t.Context())
	require.ErrorIs(t, err, database.ErrConnect)
	require.EqualError(t, err, "gas/database: ping: failed")
}

// A ping whose context ended wraps the context's error too.
func TestPing_ContextEnded(t *testing.T) {
	t.Parallel()

	s := sqliteService(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := s.Ping(ctx)
	require.ErrorIs(t, err, database.ErrConnect)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, "gas/database: ping: failed: context canceled")
}

// Driver returns the dialect of the configuration, and with a connector
// the dialect given with it, whatever the dialect setting would default
// to.
func TestDriver(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "sqlite", sqliteService(t).Driver())

	for _, d := range []database.Dialect{database.DialectPostgres, database.DialectMySQL, database.DialectSQLite} {
		s := initialized(t,
			database.WithConfig(fakeConfig(nil)),
			database.WithConnector(d, (&fake{}).connector()),
		)
		assert.Equal(t, string(d), s.Driver())
		assert.Equal(t, fakeDriver{}, s.DB().Driver(), "the handle opens through the connector")
	}
}

// In ModeSQL Pool is nil, PoolFrom reports no pool, and the pgx
// transaction methods return ErrModeNotPgx.
func TestSQLMode_NoPool(t *testing.T) {
	t.Parallel()

	s := sqliteService(t)
	assert.Nil(t, s.Pool())

	pool, ok := database.PoolFrom(s)
	assert.Nil(t, pool)
	assert.False(t, ok)

	_, err := s.BeginPgxTx(t.Context(), nil)
	require.ErrorIs(t, err, database.ErrModeNotPgx)
	require.EqualError(t, err, "gas/database: mode is not pgx")

	err = s.WithPgxTx(t.Context(), nil, func(pgx.Tx) error { return nil })
	require.ErrorIs(t, err, database.ErrModeNotPgx)
}

// otherProvider is a gas.DatabaseProvider without a Pool method.
type otherProvider struct{ gas.DatabaseProvider }

// nilPoolProvider has a Pool method that returns nil.
type nilPoolProvider struct{ gas.DatabaseProvider }

// Pool returns nil.
func (nilPoolProvider) Pool() *pgxpool.Pool { return nil }

// PoolFrom returns false for a provider without a Pool method, and for
// one whose Pool returns nil.
func TestPoolFrom_OtherProviders(t *testing.T) {
	t.Parallel()

	for _, p := range []gas.DatabaseProvider{otherProvider{}, nilPoolProvider{}} {
		pool, ok := database.PoolFrom(p)
		assert.Nil(t, pool)
		assert.False(t, ok)
	}
}

// Query and Exec run on DB, and wrap the driver's errors.
func TestQueryExec(t *testing.T) {
	t.Parallel()

	s := sqliteService(t)
	ctx := t.Context()

	res, err := s.Exec(ctx, "INSERT INTO notes (body) VALUES (?)", "a")
	require.NoError(t, err)

	n, err := res.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	rows, err := s.Query(ctx, "SELECT body FROM notes")
	require.NoError(t, err)

	var bodies []string

	for rows.Next() {
		var b string

		require.NoError(t, rows.Scan(&b))

		bodies = append(bodies, b)
	}

	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	assert.Equal(t, []string{"a"}, bodies)

	_, err = s.Query(ctx, "SELECT nope FROM nowhere")
	require.ErrorContains(t, err, "gas/database: query: ")

	_, err = s.Exec(ctx, "DELETE FROM nowhere")
	require.ErrorContains(t, err, "gas/database: exec: ")
}
