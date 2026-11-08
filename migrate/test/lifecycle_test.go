package migrate_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/migrate"
)

// Init creates the tracking table, logs `service initialized` with the
// dialect, and the service's logger carries `service`.
func TestInit_CreatesTable(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)

			assert.True(t, e.exists(t, migrate.TableName))
			assert.Empty(t, rows(t, e.svc))

			info := e.log.messages("info")
			require.NotEmpty(t, info)
			assert.Equal(t, "service initialized", info[0].msg)
			assert.Equal(
				t,
				map[string]any{"service": "gas/migrate", "dialect": b.name},
				info[0].fields,
			)
		})
	}
}

// The driver aliases pgx and sqlite3 pick PostgreSQL and SQLite, for
// providers other than gas/database.
func TestInit_DriverAliases(t *testing.T) {
	t.Parallel()

	svc, _ := newService(t, openRaw(t, "sqlite", sqliteBackend.fresh(t), "sqlite3"))
	require.NoError(t, svc.Init(t.Context()))

	svc.Register(app, mig("001", "a"))
	require.NoError(t, svc.RunPending())

	pg := postgresBackend.fresh(t)

	svc, _ = newService(t, openRaw(t, "pgx", pg, "pgx"))
	require.NoError(t, svc.Init(t.Context()))
	svc.Register(app, mig("001", "a"))
	require.NoError(t, svc.RunPending())
}

// A provider whose DB is nil fails Init with ErrNilDatabase, and the
// service stays uninitialized.
func TestInit_NilDB(t *testing.T) {
	t.Parallel()

	svc, _ := newService(t, &rawProvider{driver: "sqlite"})

	err := svc.Init(t.Context())
	require.ErrorIs(t, err, migrate.ErrNilDatabase)
	require.ErrorIs(t, svc.RunPending(), migrate.ErrNotInitialized)
}

// A failed Init leaves the service uninitialized: here the table cannot
// be created on a read-only database.
func TestInit_FailureLeavesUninitialized(t *testing.T) {
	t.Parallel()

	dsn := sqliteBackend.fresh(t)
	openDB(t, sqliteBackend, dsn) // creates the file

	svc, log := newService(t, openRaw(t, "sqlite", dsn+"?mode=ro", "sqlite"))

	err := svc.Init(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gas/migrate: ")
	require.ErrorIs(t, svc.CheckReady(t.Context()), migrate.ErrNotInitialized)
	assert.False(t, log.has("service initialized"))
}

// Before Init, after a failed Init and after Close, every method that
// reads or changes the database returns ErrNotInitialized. Close then
// Init works, and the registrations stay.
func TestLifecycle_CloseAndInitAgain(t *testing.T) {
	t.Parallel()

	db := openDB(t, sqliteBackend, sqliteBackend.fresh(t))
	svc, log := newService(t, db)

	notInit := func() {
		t.Helper()

		_, appliedErr := svc.Applied(t.Context())

		for _, err := range []error{
			svc.RunPending(),
			svc.Down(1),
			svc.ClearDirty(t.Context(), "001"),
			svc.CheckReady(t.Context()),
			appliedErr,
		} {
			require.ErrorIs(t, err, migrate.ErrNotInitialized)
			require.EqualError(t, err, "gas/migrate: service not initialized")
		}
	}

	notInit()
	require.NoError(t, svc.Close(t.Context()), "Close before Init is a no-op")
	assert.False(t, log.has("service closed"))

	svc.Register(app, mig("001", "a"))
	require.NoError(t, svc.Init(t.Context()))
	require.ErrorIs(t, svc.Init(t.Context()), migrate.ErrAlreadyInitialized)
	require.NoError(t, svc.Close(t.Context()))
	assert.True(t, log.has("service closed"))
	notInit()

	require.NoError(t, svc.Init(t.Context()))
	require.NoError(t, svc.RunPending())
	assert.Equal(t, []string{"001"}, versions(rows(t, svc)))
	require.NoError(t, svc.Close(t.Context()))
}

// The constructor rejects a nil provider and a nil logger, typed nils
// included.
func TestNew_NilDependencies(t *testing.T) {
	t.Parallel()

	db := openDB(t, sqliteBackend, sqliteBackend.fresh(t))

	_, err := migrate.New()(nil, &gas.NopLogger{})
	require.ErrorIs(t, err, migrate.ErrNilDatabase)

	_, err = migrate.New()((*rawProvider)(nil), &gas.NopLogger{})
	require.ErrorIs(t, err, migrate.ErrNilDatabase)

	_, err = migrate.New()(db, nil)
	require.ErrorIs(t, err, migrate.ErrNilLogger)

	_, err = migrate.New()(db, (*recorder)(nil))
	require.ErrorIs(t, err, migrate.ErrNilLogger)
}

// The constructor has the shape the gas container calls, and Name is
// ServiceName.
func TestNew_Shape(t *testing.T) {
	t.Parallel()

	ctor := migrate.New()

	svc, err := ctor(openDB(t, sqliteBackend, sqliteBackend.fresh(t)), &gas.NopLogger{})
	require.NoError(t, err)
	assert.Equal(t, migrate.ServiceName, svc.Name())

	var mgr gas.MigrationManager = svc

	assert.NotNil(t, mgr)
}

// A negative lock timeout fails Init like zero does.
func TestWithLockTimeout_Negative(t *testing.T) {
	t.Parallel()

	svc, _ := newService(t, openDB(t, sqliteBackend, sqliteBackend.fresh(t)),
		migrate.WithLockTimeout(-1))
	require.ErrorIs(t, svc.Init(t.Context()), migrate.ErrInvalidLockTimeout)
}

// A run goes on with the state it started with when Close runs beside
// it.
func TestLifecycle_CloseDuringRun(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)
	g := newGate(t, e)

	e.svc.Register(app, gas.Migration{Version: "001", Up: g.sql + " CREATE TABLE a (id INT)"})

	done := make(chan error, 1)

	go func() { done <- e.svc.RunPending() }()

	g.blocked()
	require.NoError(t, e.svc.Close(context.Background()))
	g.release()

	require.NoError(t, <-done)
	assert.True(t, e.exists(t, "a"))
	require.NoError(t, e.svc.Init(t.Context()))
}

// A failure to get the lock's connection is returned.
func TestLock_ConnectionFails(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", sqliteBackend.fresh(t))
	require.NoError(t, err)

	svc, _ := newService(t, &rawProvider{db: db, driver: "sqlite"})
	require.NoError(t, svc.Init(t.Context()))
	require.NoError(t, db.Close())

	err = svc.RunPending()
	require.EqualError(t, err, "gas/migrate: acquire lock: sql: database is closed")
}
