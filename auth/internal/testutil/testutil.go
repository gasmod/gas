// Package testutil provides shared test helpers and mock implementations
// for gas/auth integration and unit tests.
package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	_ "modernc.org/sqlite" // pure-Go "sqlite" driver for SQLite-backed test helpers

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
)

// ---------------------------------------------------------------------------
// NopLogger helper
// ---------------------------------------------------------------------------

// NewNopLogger creates a gas.Logger that discards all output.
func NewNopLogger() gas.Logger {
	return &gas.NopLogger{}
}

// ---------------------------------------------------------------------------
// Real service builders
// ---------------------------------------------------------------------------

func newDatabaseService(t *testing.T, apply func(*database.Settings)) *database.Service {
	t.Helper()
	cfg := database.DefaultConfig()
	apply(&cfg.Database)
	svc, err := database.New(database.WithConfig(cfg))(
		&configtest.MockConfig{},
		NewNopLogger(),
	)
	if err != nil {
		t.Fatalf("failed to build database service: %v", err)
	}
	if err = svc.Init(t.Context()); err != nil {
		t.Fatalf("failed to init database service: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

func newMigrateService(t *testing.T, db gas.DatabaseProvider) *migrate.Service {
	t.Helper()
	svc, err := migrate.New()(db)
	if err != nil {
		t.Fatalf("failed to build migrate service: %v", err)
	}
	if err = svc.Init(t.Context()); err != nil {
		t.Fatalf("failed to init migrate service: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

// ---------------------------------------------------------------------------
// Testcontainer Helpers
// ---------------------------------------------------------------------------

// PostgresContainer holds a running PostgreSQL testcontainer plus real
// gas/database and gas/migrate services bound to it.
type PostgresContainer struct {
	Container *postgres.PostgresContainer
	DB        *sql.DB
	dbSvc     *database.Service
	migSvc    *migrate.Service
	ConnStr   string
}

// SetupPostgres starts a PostgreSQL container and returns a real database
// and migrate service backed by it.
func SetupPostgres(t *testing.T) *PostgresContainer {
	t.Helper()
	requireDocker(t)
	ctx := context.Background()

	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		// BasicWaitStrategies waits for the second readiness log line and
		// then for 5432/tcp to be served on the host, within a 60s deadline.
		// The log alone is not enough: on Docker Desktop the port proxy can
		// lag the log, and ConnectionString then fails to find the mapping.
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("failed to start postgres container: %v", err)
	}

	t.Cleanup(func() {
		if terr := pgContainer.Terminate(ctx); terr != nil {
			t.Logf("failed to terminate postgres container: %v", terr)
		}
	})

	connStr, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("failed to get connection string: %v (%s)", err, containerStatus(ctx, pgContainer))
	}

	dbSvc := newDatabaseService(t, func(s *database.Settings) {
		s.Driver = database.DriverPgx
		s.DSN = connStr
		s.ConnRetries = 5
		s.ConnRetryInterval = 500 * time.Millisecond
	})
	migSvc := newMigrateService(t, dbSvc)

	return &PostgresContainer{
		Container: pgContainer,
		ConnStr:   connStr,
		DB:        dbSvc.DB(),
		dbSvc:     dbSvc,
		migSvc:    migSvc,
	}
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

// Provider returns the real database service for this container.
func (pc *PostgresContainer) Provider() *database.Service {
	return pc.dbSvc
}

// MigrationManager returns the real migration service for this container.
func (pc *PostgresContainer) MigrationManager() *migrate.Service {
	return pc.migSvc
}

// ---------------------------------------------------------------------------
// Backends
// ---------------------------------------------------------------------------

// Backend is a database, with its migration manager, that a store test runs
// against.
type Backend struct {
	DB         *database.Service
	Migrations *migrate.Service
	Name       string
}

// ForEachBackend runs fn in one subtest per backend, named after it. The
// postgres subtest skips under -short.
func ForEachBackend(t *testing.T, fn func(t *testing.T, b Backend)) {
	t.Helper()
	t.Run("postgres", func(t *testing.T) {
		fn(t, PostgresBackend(t))
	})
}

// PostgresBackend returns a backend on a fresh PostgreSQL container, skipping
// the test under -short. Tests that only hold on PostgreSQL use it directly.
func PostgresBackend(t *testing.T) Backend {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping PostgreSQL test in short mode")
	}
	pg := SetupPostgres(t)
	return Backend{DB: pg.Provider(), Migrations: pg.MigrationManager(), Name: "postgres"}
}

// ---------------------------------------------------------------------------
// SQLite Helpers
// ---------------------------------------------------------------------------

// SetupSQLite creates an in-memory SQLite database wired into real gas/database
// and gas/migrate services.
//
// MaxOpenConns is pinned to 1 because each connection to a ":memory:" database
// gets its own private schema; a larger pool would see inconsistent state.
func SetupSQLite(t *testing.T) (rawDB *sql.DB, dbSvc *database.Service, migSvc *migrate.Service) {
	t.Helper()

	dbSvc = newDatabaseService(t, func(s *database.Settings) {
		s.Driver = database.DriverSQLite
		s.DSN = ":memory:"
		s.MaxOpenConns = 1
		s.MaxIdleConns = 1
	})
	migSvc = newMigrateService(t, dbSvc)
	rawDB = dbSvc.DB()
	return rawDB, dbSvc, migSvc
}
