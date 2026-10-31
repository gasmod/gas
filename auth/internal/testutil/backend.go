package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	_ "modernc.org/sqlite" // the pure-Go "sqlite" driver SetupSQLite opens

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
)

// connRetryInterval is how long the PostgreSQL database service waits
// between connection attempts while the container starts.
const connRetryInterval = 500 * time.Millisecond

// NewNopLogger returns a gas.Logger that discards everything.
func NewNopLogger() gas.Logger {
	return &gas.NopLogger{}
}

// Backend is a database, with its migration manager, that a store test
// runs against.
type Backend struct {
	// DB is the database service.
	DB *database.Service
	// Migrations is the migration service bound to DB.
	Migrations *migrate.Service
	// Name is `sqlite`, `postgres` or `mysql`.
	Name string
}

// ForEachBackend runs fn in one subtest per backend, named after it:
// sqlite, which needs no Docker and runs under -short, then postgres,
// which skips under -short and without Docker, then mysql, only when
// MySQLEnv is set, which skips under -short and without Docker too.
func ForEachBackend(t *testing.T, fn func(t *testing.T, b Backend)) {
	t.Helper()

	t.Run("sqlite", func(t *testing.T) {
		_, db, migrations := SetupSQLite(t)
		fn(t, Backend{DB: db, Migrations: migrations, Name: "sqlite"})
	})
	t.Run("postgres", func(t *testing.T) {
		fn(t, PostgresBackend(t))
	})

	if mysqlEnabled() {
		t.Run("mysql", func(t *testing.T) {
			fn(t, MySQLBackend(t))
		})
	}
}

// PostgresBackend returns a backend on a fresh PostgreSQL container,
// skipping the test under -short or without Docker. Tests that only hold
// on PostgreSQL use it directly.
func PostgresBackend(tb testing.TB) Backend {
	tb.Helper()

	if testing.Short() {
		tb.Skip("skipping PostgreSQL test in short mode")
	}

	pg := SetupPostgres(tb)

	return Backend{DB: pg.Provider(), Migrations: pg.MigrationManager(), Name: "postgres"}
}

// SetupSQLite opens an in-memory SQLite database wired into real database
// and migrate services. MaxOpenConns is 1 because each connection to
// `:memory:` gets a database of its own. It takes a testing.TB, so a fuzz
// target can call it with its *testing.F or its *testing.T.
func SetupSQLite(tb testing.TB) (rawDB *sql.DB, dbSvc *database.Service, migSvc *migrate.Service) {
	tb.Helper()

	dbSvc = newDatabaseService(tb, func(s *database.Settings) {
		s.Dialect = database.DialectSQLite
		s.DSN = ":memory:"
		s.MaxOpenConns = 1
		s.MaxIdleConns = 1
	})
	migSvc = newMigrateService(tb, dbSvc)

	return dbSvc.DB(), dbSvc, migSvc
}

// PostgresContainer is a running PostgreSQL container with real database
// and migrate services bound to it.
type PostgresContainer struct {
	// Container is the running container.
	Container *postgres.PostgresContainer
	// DB is the database service's connection pool.
	DB *sql.DB
	// dbSvc is the database service.
	dbSvc *database.Service
	// migSvc is the migration service.
	migSvc *migrate.Service
	// ConnStr is the container's connection string.
	ConnStr string
}

// SetupPostgres starts a PostgreSQL container for the test, terminated
// when it ends, and returns real database and migrate services bound to
// it. It skips or fails the test without Docker, as described in the
// package documentation.
func SetupPostgres(tb testing.TB) *PostgresContainer {
	tb.Helper()
	requireDocker(tb)

	ctx := context.Background()

	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		// BasicWaitStrategies waits for the second readiness log line and
		// then for 5432/tcp to be served on the host, within 60s. The log
		// alone is not enough: on Docker Desktop the port proxy can lag
		// the log, and ConnectionString then fails to find the mapping.
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		tb.Fatalf("failed to start postgres container: %v", err)
	}

	tb.Cleanup(func() {
		tErr := pgContainer.Terminate(ctx)
		if tErr != nil {
			tb.Logf("failed to terminate postgres container: %v", tErr)
		}
	})

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		tb.Fatalf(
			"failed to get connection string: %v (%s)",
			err,
			containerStatus(ctx, pgContainer),
		)
	}

	dbSvc := newDatabaseService(tb, func(s *database.Settings) {
		s.Dialect = database.DialectPostgres
		s.DSN = gas.Secret(connStr)
		s.ConnRetries = 5
		s.ConnRetryInterval = connRetryInterval
	})
	migSvc := newMigrateService(tb, dbSvc)

	return &PostgresContainer{
		Container: pgContainer,
		ConnStr:   connStr,
		DB:        dbSvc.DB(),
		dbSvc:     dbSvc,
		migSvc:    migSvc,
	}
}

// Provider returns the container's database service.
func (pc *PostgresContainer) Provider() *database.Service {
	return pc.dbSvc
}

// MigrationManager returns the container's migration service.
func (pc *PostgresContainer) MigrationManager() *migrate.Service {
	return pc.migSvc
}

// containerStatus describes the container's Docker state for a failure
// message, so a failed setup shows whether the container had exited.
func containerStatus(ctx context.Context, c testcontainers.Container) string {
	state, err := c.State(ctx)
	if err != nil {
		return fmt.Sprintf("container state unavailable: %v", err)
	}

	return fmt.Sprintf("container status %q, exit code %d", state.Status, state.ExitCode)
}

// newDatabaseService builds and initializes a database service with the
// default configuration changed by apply, and with opts, closed when the
// test ends.
func newDatabaseService(
	tb testing.TB,
	apply func(*database.Settings),
	opts ...database.Option,
) *database.Service {
	tb.Helper()

	cfg := database.DefaultConfig()
	apply(&cfg.Database)

	opts = append([]database.Option{database.WithConfig(cfg)}, opts...)

	svc, err := database.New(opts...)(&configtest.MockConfig{}, NewNopLogger())
	if err != nil {
		tb.Fatalf("failed to build database service: %v", err)
	}

	err = svc.Init(tb.Context())
	if err != nil {
		tb.Fatalf("failed to init database service: %v", err)
	}

	tb.Cleanup(func() { _ = svc.Close(context.Background()) })

	return svc
}

// newMigrateService builds and initializes a migration service on db,
// closed when the test ends.
func newMigrateService(tb testing.TB, db gas.DatabaseProvider) *migrate.Service {
	tb.Helper()

	svc, err := migrate.New()(db, NewNopLogger())
	if err != nil {
		tb.Fatalf("failed to build migrate service: %v", err)
	}

	err = svc.Init(tb.Context())
	if err != nil {
		tb.Fatalf("failed to init migrate service: %v", err)
	}

	tb.Cleanup(func() { _ = svc.Close(context.Background()) })

	return svc
}
