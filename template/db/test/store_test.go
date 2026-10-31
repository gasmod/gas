package db_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate/migratetest"
	"github.com/gasmod/gas/template"
	"github.com/gasmod/gas/template/db"
	"github.com/gasmod/gas/template/internal/conformance"
)

// The db store keeps the contract of the template package on every
// dialect, each store in a namespace of its own on one database.
func TestStore_Conformance(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)

			conformance.Run(t, conformance.Subject{
				New: func(t *testing.T, seed fstest.MapFS) gas.TemplateProvider {
					t.Helper()

					return e.conformanceStore(t, seed)
				},
			})
		})
	}
}

// The constructor refuses nil dependencies, typed nil pointers included,
// a bad namespace and a bad extension set.
func TestNewStore_Errors(t *testing.T) {
	t.Parallel()

	e := newEnv(t, sqliteBackend)
	log := &gas.NopLogger{}

	_, err := db.NewStore()(nil, log, e.migrations)
	require.ErrorIs(t, err, db.ErrNilDatabase)

	_, err = db.NewStore()((*database.Service)(nil), log, e.migrations)
	require.ErrorIs(t, err, db.ErrNilDatabase)

	_, err = db.NewStore()(e.db, nil, e.migrations)
	require.ErrorIs(t, err, db.ErrNilLogger)

	_, err = db.NewStore()(e.db, log, nil)
	require.ErrorIs(t, err, db.ErrNilMigrations)

	for _, ns := range []string{"", "\xff", "a\x00", strings.Repeat("é", 256)} {
		_, err = db.NewStore(db.WithNamespace(ns))(e.db, log, e.migrations)
		require.ErrorIs(t, err, db.ErrInvalidNamespace, ns)
	}

	_, err = db.NewStore(db.WithNamespace(strings.Repeat("é", 255)))(e.db, log, e.migrations)
	require.NoError(t, err)

	_, err = db.NewStore(db.WithExtensions())(e.db, log, e.migrations)
	require.ErrorIs(t, err, template.ErrInvalidExtension)
}

// driverOverride is a provider that reports another driver, or a nil DB.
type driverOverride struct {
	*database.Service

	// driver is the driver it reports.
	driver string
	// nilDB makes DB return nil.
	nilDB bool
}

// Driver returns the driver it reports.
func (d driverOverride) Driver() string { return d.driver }

// DB returns nil, or the service's DB.
func (d driverOverride) DB() *sql.DB {
	if d.nilDB {
		return nil
	}

	return d.Service.DB()
}

// Init refuses a driver it has no SQL for, the dead `pgx` and `sqlite3`
// names and gas/database's empty driver before its Init included, and a
// provider whose DB is nil; a failed Init leaves the store uninitialized.
func TestStore_InitErrors(t *testing.T) {
	t.Parallel()

	e := newEnv(t, sqliteBackend)

	for _, driver := range []string{"oracle", "pgx", "sqlite3", ""} {
		s, err := db.NewStore()(
			driverOverride{Service: e.db, driver: driver},
			&gas.NopLogger{},
			e.migrations,
		)
		require.NoError(t, err)

		err = s.Init(t.Context())
		require.ErrorIs(t, err, db.ErrUnsupportedDriver, driver)
		require.EqualError(
			t,
			err,
			"template: unsupported driver: supported are postgres, mysql and sqlite",
		)

		_, err = s.Get(t.Context(), "a.html")
		require.ErrorIs(t, err, template.ErrNotInitialized)
	}

	s, err := db.NewStore()(
		driverOverride{Service: e.db, driver: "sqlite", nilDB: true},
		&gas.NopLogger{},
		e.migrations,
	)
	require.NoError(t, err)
	require.ErrorIs(t, s.Init(t.Context()), db.ErrNilDatabase)
}

// Before Init every method but Init and Close returns ErrNotInitialized;
// after Close, ErrClosed; Init on an open store ErrAlreadyInitialized; and
// Init after Close works again, with the rows kept (survey D4).
func TestStore_Lifecycle(t *testing.T) {
	t.Parallel()

	e := newEnv(t, sqliteBackend)
	ctx := t.Context()

	s, err := db.NewStore()(e.db, &gas.NopLogger{}, e.migrations)
	require.NoError(t, err)
	assert.Equal(t, "gas/template/db", s.Name())

	require.NoError(t, s.Close(ctx))
	requireState(t, s, template.ErrNotInitialized)

	require.NoError(t, s.Init(ctx))
	require.NoError(t, e.migrations.RunPendingContext(ctx))
	require.ErrorIs(t, s.Init(ctx), template.ErrAlreadyInitialized)
	require.NoError(t, s.Register(ctx, "a.html", []byte("a")))

	require.NoError(t, s.Close(ctx))
	requireState(t, s, template.ErrClosed)

	require.NoError(t, s.Init(ctx))

	b, err := s.Get(ctx, "a.html")
	require.NoError(t, err)
	assert.Equal(t, "a", string(b))
	require.NoError(t, s.Close(ctx))
}

// requireState checks that every method but Init and Close returns want.
func requireState(t *testing.T, s *db.Store, want error) {
	t.Helper()

	ctx := t.Context()

	_, err := s.Get(ctx, "a.html")
	require.ErrorIs(t, err, want)

	_, err = s.List(ctx)
	require.ErrorIs(t, err, want)

	require.ErrorIs(t, s.Register(ctx, "a.html", nil), want)
	require.ErrorIs(t, s.RegisterFS(ctx, fstest.MapFS{}), want)

	_, err = s.Exists(ctx, "a.html")
	require.ErrorIs(t, err, want)
	require.ErrorIs(t, s.Delete(ctx, "a.html"), want)
}

// Init registers the two migrations, owned by the store, with the
// manager, and two stores register the same ones.
func TestStore_RegistersMigrations(t *testing.T) {
	t.Parallel()

	e := newEnv(t, sqliteBackend)
	m := &migratetest.MockMigrationManager{}

	s, err := db.NewStore()(e.db, &gas.NopLogger{}, m)
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))

	calls := m.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "RegisterSlice", calls[0].Method)
	assert.Equal(t, s, calls[0].Args[0])

	migs, ok := calls[0].Args[1].([]gas.Migration)
	require.True(t, ok)
	require.Len(t, migs, 2)
	assert.Equal(t, "20260322235959", migs[0].Version)
	assert.Equal(t, "create templates table", migs[0].Description)
	assert.Equal(t, "20261031120000", migs[1].Version)
	assert.Equal(t, "exact template names", migs[1].Description)
	assert.Equal(t, s, migs[1].Service)
}

// Exists and Delete take a context, check the name, and Delete of a
// missing template is not found.
func TestStore_ExistsDelete(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			s := newEnv(t, b).store(t)
			ctx := t.Context()

			require.NoError(t, s.Register(ctx, "a.html", []byte("a")))

			ok, err := s.Exists(ctx, "a.html")
			require.NoError(t, err)
			assert.True(t, ok)

			ok, err = s.Exists(ctx, "A.html")
			require.NoError(t, err)
			assert.False(t, ok)

			require.NoError(t, s.Delete(ctx, "a.html"))

			err = s.Delete(ctx, "a.html")
			require.ErrorIs(t, err, template.ErrTemplateNotFound)
			require.EqualError(t, err, `template: not found: "a.html"`)

			_, err = s.Exists(ctx, "../a.html")
			require.ErrorIs(t, err, template.ErrInvalidName)
			require.ErrorIs(t, s.Delete(ctx, "a.txt"), template.ErrInvalidName)

			canceled, cancel := context.WithCancel(ctx)
			cancel()

			_, err = s.Exists(canceled, "a.html")
			require.ErrorIs(t, err, context.Canceled)
			require.ErrorIs(t, s.Delete(canceled, "a.html"), context.Canceled)
		})
	}
}

// Namespaces are isolated: a store reads and lists only its own rows.
func TestStore_Namespaces(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)
			ctx := t.Context()
			emails := e.store(t, db.WithNamespace("emails"))
			pages := e.store(t, db.WithNamespace("pages"))
			upper := e.store(t, db.WithNamespace("Pages"))

			require.NoError(t, emails.Register(ctx, "a.html", []byte("email")))
			require.NoError(t, pages.Register(ctx, "a.html", []byte("page")))

			got, err := emails.Get(ctx, "a.html")
			require.NoError(t, err)
			assert.Equal(t, "email", string(got))

			_, err = upper.Get(ctx, "a.html")
			require.ErrorIs(t, err, template.ErrTemplateNotFound)

			names, err := pages.List(ctx)
			require.NoError(t, err)
			assert.Equal(t, []string{"a.html"}, names)
		})
	}
}

// List leaves out a row whose name the store would refuse, such as one a
// store of other extensions wrote, so that List and Get agree.
func TestStore_ListSkipsRefusedNames(t *testing.T) {
	t.Parallel()

	e := newEnv(t, sqliteBackend)
	html := e.store(t, db.WithNamespace("mixed"))
	text := e.store(t, db.WithNamespace("mixed"), db.WithExtensions(".txt"))

	require.NoError(t, text.Register(t.Context(), "a.txt", nil))
	require.NoError(t, html.Register(t.Context(), "a.html", nil))

	names, err := html.List(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"a.html"}, names)
}

// List sorts in byte order whatever the database's collation, which on
// PostgreSQL here is en_US and sorts otherwise (survey D2).
func TestStore_ListByteOrder(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			s := newEnv(t, b).store(t)
			want := []string{
				"B.html",
				"Z.html",
				"_a.html",
				"a.html",
				"a/b.html",
				"b.html",
				"é.html",
			}

			for _, n := range []string{"é.html", "b.html", "a/b.html", "a.html", "_a.html", "Z.html", "B.html"} {
				require.NoError(t, s.Register(t.Context(), n, nil))
			}

			names, err := s.List(t.Context())
			require.NoError(t, err)
			assert.Equal(t, want, names)
		})
	}
}

// A failed statement wraps ErrQuery, names the operation and the template,
// and gives the driver's code, here for a table that is gone.
func TestStore_QueryErrors(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)
			s := e.store(t)
			ctx := t.Context()

			e.exec(t, "DROP TABLE "+db.TableName)

			_, err := s.Get(ctx, "a.html")
			require.ErrorIs(t, err, db.ErrQuery)
			assert.True(
				t,
				strings.HasPrefix(err.Error(), `template: query failed: get "a.html": `),
				err.Error(),
			)

			_, err = s.List(ctx)
			require.ErrorIs(t, err, db.ErrQuery)

			require.ErrorIs(t, s.Register(ctx, "a.html", nil), db.ErrQuery)

			_, err = s.Exists(ctx, "a.html")
			require.ErrorIs(t, err, db.ErrQuery)
			require.ErrorIs(t, s.Delete(ctx, "a.html"), db.ErrQuery)

			err = s.RegisterFS(ctx, fstest.MapFS{"a.html": {}, "b.html": {}})
			require.ErrorIs(t, err, db.ErrQuery)
			assert.True(
				t,
				strings.HasPrefix(err.Error(), `template: query failed: register "a.html": `),
				err.Error(),
			)
		})
	}
}

// RegisterFS stores all files in one transaction: a failure on a later
// file leaves none of them (here a name too long for MySQL is refused
// before the transaction, and a canceled context stops it).
func TestStore_RegisterFSAllOrNothing(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			s := newEnv(t, b).store(t)
			ctx := t.Context()

			canceled, cancel := context.WithCancel(ctx)
			cancel()

			err := s.RegisterFS(canceled, fstest.MapFS{"a.html": {}})
			require.ErrorIs(t, err, context.Canceled)

			names, err := s.List(ctx)
			require.NoError(t, err)
			assert.Empty(t, names)
		})
	}
}

// recordingSafe is a provider whose SafeError replaces every error and
// counts its calls.
type recordingSafe struct {
	*database.Service

	// calls counts the calls of SafeError.
	calls *atomic.Int64
}

// errSafe replaces every error recordingSafe sees.
var errSafe = errors.New("safe")

// SafeError counts the call and returns errSafe.
func (r recordingSafe) SafeError(error) error {
	r.calls.Add(1)

	return errSafe
}

// plain is a provider without SafeError.
type plain struct{ gas.DatabaseProvider }

// The driver's errors pass through the provider's SafeError when it has
// one, which here replaces every error, and give only the driver's code
// otherwise; not found never reaches it.
func TestStore_SafeError(t *testing.T) {
	t.Parallel()

	e := newEnv(t, sqliteBackend)
	calls := &atomic.Int64{}

	s, err := db.NewStore()(
		recordingSafe{Service: e.db, calls: calls},
		&gas.NopLogger{},
		e.migrations,
	)
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))
	require.NoError(t, e.migrations.RunPendingContext(t.Context()))

	_, err = s.Get(t.Context(), "missing.html")
	require.ErrorIs(t, err, template.ErrTemplateNotFound)
	assert.Zero(t, calls.Load())

	e.exec(t, "DROP TABLE "+db.TableName)

	_, err = s.Get(t.Context(), "a.html")
	require.ErrorIs(t, err, errSafe)
	require.EqualError(t, err, `template: query failed: get "a.html": safe`)
	assert.Equal(t, int64(1), calls.Load())

	p, err := db.NewStore()(plain{e.db}, &gas.NopLogger{}, e.migrations)
	require.NoError(t, err)
	require.NoError(t, p.Init(t.Context()))

	_, err = p.Get(t.Context(), "a.html")
	require.EqualError(t, err, `template: query failed: get "a.html": sqlite error 1`)
}

// With the database gone under a running store, the error holds no user,
// host, port or database name: gas/database's SafeError replaces the
// driver's connection error (survey D5).
func TestStore_SafeErrorPostgres(t *testing.T) {
	t.Parallel()

	e := newEnv(t, postgresBackend)
	s := e.store(t)
	require.NoError(t, s.Register(t.Context(), "a.html", nil))

	name := e.queryString(t, "SELECT current_database()")
	admin := openDB(t, postgresBackend, pgShared.dsn)

	_, err := admin.DB().ExecContext(t.Context(), "DROP DATABASE "+name+" WITH (FORCE)")
	require.NoError(t, err)

	// The pool's idle connections were terminated by the drop, so the
	// first Gets may fail on one of them before one connects anew.
	for range 5 {
		_, err = s.Get(t.Context(), "a.html")
		require.ErrorIs(t, err, db.ErrQuery)

		for _, secret := range []string{name, containerUser, containerPassword, "127.0.0.1", "localhost"} {
			assert.NotContains(t, err.Error(), secret)
		}

		if errors.Is(err, database.ErrConnect) {
			break
		}
	}

	require.ErrorIs(t, err, database.ErrConnect)
	assert.True(
		t,
		strings.HasPrefix(
			err.Error(),
			`template: query failed: get "a.html": gas/database: connection: `,
		),
		err.Error(),
	)
}
