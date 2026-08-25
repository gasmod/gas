// Package testutil provides shared test helpers and mock implementations
// for gas/auth integration and unit tests.
package testutil

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	// Registers the pure-Go "sqlite" driver used by SQLite-backed test helpers.
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// NopLogger helper
// ---------------------------------------------------------------------------

// NewNopLogger creates a gas.Logger that discards all output.
func NewNopLogger() gas.Logger {
	return gas.NewNopLogger()()
}

// ---------------------------------------------------------------------------
// Real service builders
// ---------------------------------------------------------------------------

func newDatabaseService(t *testing.T, apply func(*database.Settings)) *database.Service {
	t.Helper()
	cfg := database.DefaultConfig()
	apply(&cfg.Database)
	svc := database.New(database.WithConfig(cfg))(
		&configtest.MockConfig{},
		NewNopLogger(),
	)
	if err := svc.Init(); err != nil {
		t.Fatalf("failed to init database service: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func newMigrateService(t *testing.T, db gas.DatabaseProvider) *migrate.Service {
	t.Helper()
	svc := migrate.New()(db)
	if err := svc.Init(); err != nil {
		t.Fatalf("failed to init migrate service: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
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
	ctx := context.Background()

	pgContainer, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second),
		),
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
		t.Fatalf("failed to get connection string: %v", err)
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

// Provider returns the real database service for this container.
func (pc *PostgresContainer) Provider() *database.Service {
	return pc.dbSvc
}

// MigrationManager returns the real migration service for this container.
func (pc *PostgresContainer) MigrationManager() *migrate.Service {
	return pc.migSvc
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
