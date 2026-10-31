package db_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/fstest"

	_ "github.com/jackc/pgx/v5/stdlib" // the pgx driver, for newDatabase.
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite" // the SQLite driver gas/database opens.

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
	"github.com/gasmod/gas/template/db"
	"github.com/gasmod/gas/template/internal/conformance"
)

// TestMain runs the tests, then terminates the shared containers.
func TestMain(m *testing.M) {
	code := m.Run()

	terminateContainers()
	os.Exit(code)
}

// backend is a database the tests run on: SQLite always, PostgreSQL with
// Docker, MySQL with Docker and mysqlEnv.
type backend struct {
	// fresh returns the DSN of a new, empty database.
	fresh func(t *testing.T) string
	// name is the dialect, `sqlite`, `postgres` or `mysql`.
	name string
	// dialect is the dialect gas/database opens it with.
	dialect database.Dialect
}

// The backends.
var (
	// sqliteBackend is SQLite on a file in the test's directory.
	sqliteBackend = backend{
		name:    "sqlite",
		dialect: database.DialectSQLite,
		fresh: func(t *testing.T) string {
			t.Helper()

			return "file:" + filepath.Join(t.TempDir(), "app.db")
		},
	}
	// postgresBackend is a new database of the shared PostgreSQL container.
	postgresBackend = backend{
		name:    "postgres",
		dialect: database.DialectPostgres,
		fresh:   postgresDSN,
	}
	// mysqlBackend is a new database of the shared MySQL container.
	mysqlBackend = backend{name: "mysql", dialect: database.DialectMySQL, fresh: mysqlDSN}
)

// backends are the backends a test runs on, each as a parallel subtest
// that skips when its database is not available.
var backends = []backend{sqliteBackend, postgresBackend, mysqlBackend}

// openDB returns an initialized gas/database service on dsn, closed when
// the test ends. edit, when given, changes the configuration first.
func openDB(
	t *testing.T,
	b backend,
	dsn string,
	edit ...func(*database.Settings),
) *database.Service {
	t.Helper()

	cfg := database.DefaultConfig()

	cfg.Database.Dialect = b.dialect
	cfg.Database.DSN = gas.Secret(dsn)

	for _, e := range edit {
		e(&cfg.Database)
	}

	d, err := database.New(database.WithConfig(cfg))(nil, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, d.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, d.Close(context.Background())) })

	return d
}

// startMigrate returns an initialized migrate service on d, closed when
// the test ends.
func startMigrate(t *testing.T, d gas.DatabaseProvider) *migrate.Service {
	t.Helper()

	m, err := migrate.New()(d, &gas.NopLogger{})
	require.NoError(t, err)
	require.NoError(t, m.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, m.Close(context.Background())) })

	return m
}

// env is a database of a backend with a migrate service on it.
type env struct {
	// db is the database service.
	db *database.Service
	// migrations is the migrate service, initialized.
	migrations *migrate.Service
	// b is the backend.
	b backend
	// dsn is the database's DSN.
	dsn string
}

// newEnv returns an env on a fresh database of b.
func newEnv(t *testing.T, b backend, edit ...func(*database.Settings)) *env {
	t.Helper()

	dsn := b.fresh(t)
	d := openDB(t, b, dsn, edit...)

	return &env{b: b, dsn: dsn, db: d, migrations: startMigrate(t, d)}
}

// store returns a store on e's database with opts, initialized, its
// migrations applied, and closed when the test ends.
func (e *env) store(t *testing.T, opts ...db.Option) *db.Store {
	t.Helper()

	s, err := db.NewStore(opts...)(e.db, &gas.NopLogger{}, e.migrations)
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })
	require.NoError(t, e.migrations.RunPendingContext(t.Context()))

	return s
}

// namespaces counts the namespaces the tests used, to name the next.
var namespaces atomic.Int64

// conformanceStore returns a store with the conformance extensions in a
// new namespace of e's database, holding seed.
func (e *env) conformanceStore(t *testing.T, seed fstest.MapFS) gas.TemplateProvider {
	t.Helper()

	s := e.store(t,
		db.WithNamespace(fmt.Sprintf("ns%d", namespaces.Add(1))),
		db.WithExtensions(conformance.Extensions...),
	)
	require.NoError(t, s.RegisterFS(t.Context(), seed))

	return s
}

// exec runs a statement on e's database.
func (e *env) exec(t *testing.T, query string, args ...any) {
	t.Helper()

	_, err := e.db.DB().ExecContext(t.Context(), query, args...)
	require.NoError(t, err)
}

// queryString returns the one string a query on e's database selects.
func (e *env) queryString(t *testing.T, query string) string {
	t.Helper()

	var s string

	require.NoError(t, e.db.DB().QueryRowContext(t.Context(), query).Scan(&s))

	return s
}

// owner is a gas.Service that owns migrations.
type owner string

// Name returns the owner's name.
func (o owner) Name() string { return string(o) }

// Init does nothing.
func (owner) Init(context.Context) error { return nil }

// Close does nothing.
func (owner) Close(context.Context) error { return nil }

// readMigration returns the SQL of a migration file of the store's
// dialect directory.
func readMigration(t *testing.T, dialect, file string) string {
	t.Helper()

	path := filepath.Join("..", dialect, file)

	b, err := os.ReadFile(path) //nolint:gosec // G304: the store's own migration files.
	require.NoError(t, err)

	return string(b)
}
