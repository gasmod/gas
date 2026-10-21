package guidesauth_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/postgres"
	_ "modernc.org/sqlite" // pure-Go "sqlite" driver

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/session"
	"github.com/gasmod/gas/auth/token"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
)

// backend is one database the credential stores run against. open returns a
// fresh database service on it; every call reaches the same data, so a second
// service instance can observe what the first one persisted.
type backend struct {
	name string
	open func(t *testing.T) *database.Service
}

func nopLogger() gas.Logger { return &gas.NopLogger{} }

func newDatabase(t *testing.T, driver, dsn string, maxConns int32) *database.Service {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.Database.Driver = driver
	cfg.Database.DSN = dsn
	cfg.Database.MaxOpenConns = maxConns
	cfg.Database.MaxIdleConns = int(maxConns)
	cfg.Database.ConnRetries = 5
	cfg.Database.ConnRetryInterval = 200 * time.Millisecond
	svc, err := database.New(database.WithConfig(cfg))(&configtest.MockConfig{}, nopLogger())
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("database Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

func newMigrations(t *testing.T, db gas.DatabaseProvider) *migrate.Service {
	t.Helper()
	svc, err := migrate.New()(db)
	if err != nil {
		t.Fatalf("migrate.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("migrate Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

// sqliteBackend is a SQLite file in a temp directory. One connection keeps
// writers from contending for the file lock.
func sqliteBackend(t *testing.T) backend {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.db")
	return backend{name: "sqlite", open: func(t *testing.T) *database.Service {
		return newDatabase(t, database.DriverSQLite, path, 1)
	}}
}

var (
	pgOnce sync.Once
	pgCtr  *postgres.PostgresContainer
	pgErr  error
	pgSeq  atomic.Int64
)

func TestMain(m *testing.M) {
	code := m.Run()
	if pgCtr != nil {
		_ = pgCtr.Terminate(context.Background())
	}
	if myCtr != nil {
		_ = myCtr.Terminate(context.Background())
	}
	os.Exit(code)
}

// postgresBackend is a fresh database on a PostgreSQL container shared by
// the package, opened with the given database/sql driver name.
func postgresBackend(t *testing.T, driver string) backend {
	t.Helper()
	requireDocker(t)
	ctx := context.Background()
	pgOnce.Do(func() {
		pgCtr, pgErr = postgres.Run(ctx,
			"postgres:16-alpine",
			postgres.WithDatabase("testdb"),
			postgres.WithUsername("testuser"),
			postgres.WithPassword("testpass"),
			postgres.BasicWaitStrategies(),
		)
	})
	if pgErr != nil {
		t.Fatalf("start postgres: %v", pgErr)
	}
	admin, err := pgCtr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	name := fmt.Sprintf("claims_%d", pgSeq.Add(1))
	adminDB := newDatabase(t, database.DriverPgx, admin, 2)
	if _, err := adminDB.DB().ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	host, err := pgCtr.Host(ctx)
	if err != nil {
		t.Fatalf("postgres host: %v", err)
	}
	port, err := pgCtr.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("postgres port: %v", err)
	}
	dsn := fmt.Sprintf("postgres://testuser:testpass@%s:%s/%s?sslmode=disable", host, port.Port(), name)
	return backend{name: "postgres-" + driver, open: func(t *testing.T) *database.Service {
		return newDatabase(t, driver, dsn, 4)
	}}
}

// allBackends returns SQLite and, with Docker, PostgreSQL under both of its
// driver names.
func allBackends(t *testing.T) []func(*testing.T) backend {
	t.Helper()
	return []func(*testing.T) backend{
		sqliteBackend,
		func(t *testing.T) backend { return postgresBackend(t, database.DriverPgx) },
		func(t *testing.T) backend { return postgresBackend(t, database.DriverPostgres) },
	}
}

var backendNames = []string{"sqlite", "postgres-pgx", "postgres-postgres"}

func newSessionOn(t *testing.T, db gas.DatabaseProvider) *session.Service {
	t.Helper()
	mig := newMigrations(t, db)
	svc, err := session.New(session.WithConfig(session.DefaultConfig()))(db, nopLogger(), mig, &configtest.MockConfig{})
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("session Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	if err := mig.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	return svc
}

func newAPIKeyOn(t *testing.T, db gas.DatabaseProvider) *apikey.Service {
	t.Helper()
	mig := newMigrations(t, db)
	svc, err := apikey.New(apikey.WithConfig(apikey.DefaultConfig()))(db, nopLogger(), mig, &configtest.MockConfig{})
	if err != nil {
		t.Fatalf("apikey.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("apikey Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	if err := mig.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	return svc
}

func newTokenOn(t *testing.T, db gas.DatabaseProvider) *token.Service {
	t.Helper()
	mig := newMigrations(t, db)
	svc, err := token.New(token.WithConfig(token.DefaultConfig()))(db, nopLogger(), mig, &configtest.MockConfig{})
	if err != nil {
		t.Fatalf("token.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("token Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	if err := mig.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	return svc
}
