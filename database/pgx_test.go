package database_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
)

// pgxImage is the PostgreSQL image the pgx tests run against, the same one
// gas/auth's integration tests use.
const pgxImage = "postgres:16-alpine"

// The pgx tests share one PostgreSQL container per test run; createPgxTable
// gives each test its own table so they do not collide. TestMain terminates
// the container.
var (
	pgxOnce      sync.Once
	pgxDSN       string
	pgxSetupErr  error
	pgxTerminate func()
)

// TestMain terminates the shared PostgreSQL container once every test in the
// package has finished with it.
func TestMain(m *testing.M) {
	m.Run()
	if pgxTerminate != nil {
		pgxTerminate()
	}
}

// postgresDSN skips under -short, checks Docker with requireDocker, starts
// the shared PostgreSQL container on first use, and returns its DSN.
func postgresDSN(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping pgx integration test in short mode")
	}
	requireDocker(t)

	pgxOnce.Do(func() {
		ctx := context.Background()
		ctr, err := postgres.Run(ctx,
			pgxImage,
			postgres.WithDatabase("testdb"),
			postgres.WithUsername("testuser"),
			postgres.WithPassword("testpass"),
			postgres.BasicWaitStrategies(),
		)
		if err != nil {
			pgxSetupErr = fmt.Errorf("start postgres: %w", err)
			return
		}
		pgxTerminate = func() { _ = ctr.Terminate(context.Background()) }

		pgxDSN, err = ctr.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			pgxSetupErr = fmt.Errorf("postgres connection string: %w", err)
		}
	})
	if pgxSetupErr != nil {
		t.Fatal(pgxSetupErr)
	}
	return pgxDSN
}

// newPgxService returns a service running in ModePgx against the shared
// PostgreSQL container.
func newPgxService(t *testing.T) *database.Service {
	t.Helper()

	dsn := postgresDSN(t)

	cfg := database.DefaultConfig()
	cfg.Database.Mode = database.ModePgx
	cfg.Database.DSN = dsn

	s, err := database.New(database.WithConfig(cfg))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Init(t.Context()); err != nil {
		t.Fatalf("Init in pgx mode: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

// nonPgxProvider is a gas.DatabaseProvider with no Pool method, so PoolFrom
// cannot unwrap it.
type nonPgxProvider struct{ gas.DatabaseProvider }

// nilPoolProvider has a Pool method that reports no pool, standing in for a
// pgx-aware provider running in ModeSQL.
type nilPoolProvider struct{ gas.DatabaseProvider }

func (nilPoolProvider) Pool() *pgxpool.Pool { return nil }

func TestPool_NilInSQLMode(t *testing.T) {
	s := newTestService(t)

	if pool := s.Pool(); pool != nil {
		t.Fatalf("Pool() = %v, want nil in ModeSQL", pool)
	}
}

func TestPoolFrom_SQLModeService(t *testing.T) {
	s := newTestService(t)

	pool, ok := database.PoolFrom(s)
	if ok {
		t.Error("PoolFrom reported ok for a ModeSQL service")
	}
	if pool != nil {
		t.Errorf("PoolFrom returned pool %v, want nil", pool)
	}
}

func TestPoolFrom_ProviderWithoutPoolMethod(t *testing.T) {
	pool, ok := database.PoolFrom(nonPgxProvider{})
	if ok {
		t.Error("PoolFrom reported ok for a provider with no Pool method")
	}
	if pool != nil {
		t.Errorf("PoolFrom returned pool %v, want nil", pool)
	}
}

func TestPoolFrom_ProviderWithNilPool(t *testing.T) {
	pool, ok := database.PoolFrom(nilPoolProvider{})
	if ok {
		t.Error("PoolFrom reported ok for a provider whose Pool is nil")
	}
	if pool != nil {
		t.Errorf("PoolFrom returned pool %v, want nil", pool)
	}
}

func TestBeginPgxTx_NotPgxMode(t *testing.T) {
	s := newTestService(t)

	tx, err := s.BeginPgxTx(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error when not running in ModePgx")
	}
	if tx != nil {
		t.Errorf("expected nil tx, got %v", tx)
	}
}

func TestBeginPgxTx_Closed(t *testing.T) {
	s := newTestService(t)
	_ = s.Close(context.Background())

	if _, err := s.BeginPgxTx(context.Background(), nil); err == nil {
		t.Fatal("expected error when service is closed")
	}
}

func TestWithPgxTx_NotPgxMode(t *testing.T) {
	s := newTestService(t)

	called := false
	err := s.WithPgxTx(context.Background(), nil, func(pgx.Tx) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("expected error when not running in ModePgx")
	}
	if called {
		t.Error("fn should not run when the transaction cannot be started")
	}
}

func TestWithPgxTx_Closed(t *testing.T) {
	s := newTestService(t)
	_ = s.Close(context.Background())

	called := false
	err := s.WithPgxTx(context.Background(), nil, func(pgx.Tx) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("expected error when service is closed")
	}
	if called {
		t.Error("fn should not run when the service is closed")
	}
}

func TestPgx_PoolAndDriver(t *testing.T) {
	s := newPgxService(t)

	if s.Pool() == nil {
		t.Fatal("Pool() is nil in ModePgx")
	}
	// ModePgx derives *sql.DB from the pool, so both accessors work.
	if s.DB() == nil {
		t.Fatal("DB() is nil in ModePgx")
	}
	if s.Driver() != database.DriverPgx {
		t.Errorf("Driver() = %q, want %q", s.Driver(), database.DriverPgx)
	}

	pool, ok := database.PoolFrom(s)
	if !ok {
		t.Fatal("PoolFrom did not report ok for a ModePgx service")
	}
	if pool != s.Pool() {
		t.Error("PoolFrom returned a different pool than Pool()")
	}
}

func TestPgx_Probes(t *testing.T) {
	s := newPgxService(t)
	ctx := context.Background()

	// Ping and CheckReady take the pool branch in ModePgx rather than the
	// *sql.DB one.
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if err := s.CheckHealth(ctx); err != nil {
		t.Fatalf("CheckHealth: %v", err)
	}
	if err := s.CheckReady(ctx); err != nil {
		t.Fatalf("CheckReady: %v", err)
	}

	_ = s.Close(context.Background())
	if err := s.CheckHealth(ctx); err == nil {
		t.Error("expected CheckHealth to fail after Close")
	}
	if err := s.CheckReady(ctx); err == nil {
		t.Error("expected CheckReady to fail after Close")
	}
}

func TestPgx_WithPgxTxCommit(t *testing.T) {
	s := newPgxService(t)
	ctx := context.Background()
	table := createPgxTable(t, s)

	err := s.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO "+table+" (id, val) VALUES ($1, $2)", 1, "committed")
		return err
	})
	if err != nil {
		t.Fatalf("WithPgxTx: %v", err)
	}

	if got := pgxValue(t, s, table); got != "committed" {
		t.Errorf("val = %q, want committed", got)
	}
}

func TestPgx_WithPgxTxRollback(t *testing.T) {
	s := newPgxService(t)
	ctx := context.Background()
	table := createPgxTable(t, s)

	sentinel := errors.New("trigger rollback")
	err := s.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
		if _, err := tx.Exec(
			ctx,
			"INSERT INTO "+table+" (id, val) VALUES ($1, $2)",
			1,
			"rolled-back",
		); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want it to wrap the sentinel", err)
	}

	if got := pgxValue(t, s, table); got != "" {
		t.Errorf("found %q after rollback, want no row", got)
	}
}

func TestPgx_WithPgxTxPanic(t *testing.T) {
	s := newPgxService(t)
	ctx := context.Background()
	table := createPgxTable(t, s)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic to propagate")
		}
		if got := pgxValue(t, s, table); got != "" {
			t.Errorf("found %q after panic rollback, want no row", got)
		}
	}()

	_ = s.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
		if _, err := tx.Exec(
			ctx,
			"INSERT INTO "+table+" (id, val) VALUES ($1, $2)",
			1,
			"panic-value",
		); err != nil {
			return err
		}
		panic("test panic")
	})
}

func TestPgx_WithPgxTxCanceledContext(t *testing.T) {
	s := newPgxService(t)
	table := createPgxTable(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	err := s.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
		if _, err := tx.Exec(
			ctx,
			"INSERT INTO "+table+" (id, val) VALUES ($1, $2)",
			1,
			"abandoned",
		); err != nil {
			return err
		}
		// The commit runs on ctx, so canceling here must fail it rather
		// than persist work the caller abandoned.
		cancel()
		return nil
	})
	if err == nil {
		t.Fatal("expected commit to fail on a canceled context")
	}

	if got := pgxValue(t, s, table); got != "" {
		t.Errorf("found %q after a failed commit, want no row", got)
	}
}

func TestPgx_BeginPgxTxManual(t *testing.T) {
	s := newPgxService(t)
	ctx := context.Background()
	table := createPgxTable(t, s)

	tx, err := s.BeginPgxTx(ctx, &pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatalf("BeginPgxTx: %v", err)
	}
	if _, err := tx.Exec(
		ctx,
		"INSERT INTO "+table+" (id, val) VALUES ($1, $2)",
		1,
		"manual",
	); err != nil {
		t.Fatalf("INSERT in tx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if got := pgxValue(t, s, table); got != "manual" {
		t.Errorf("val = %q, want manual", got)
	}
}

// createPgxTable makes a scratch table dropped when the test ends, so the
// pgx tests can share one database without colliding.
func createPgxTable(t *testing.T, s *database.Service) string {
	t.Helper()

	table := "gas_db_test_" + t.Name()
	for i, r := range table {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			table = table[:i] + "_" + table[i+1:]
		}
	}

	ctx := context.Background()
	if _, err := s.Exec(
		ctx,
		"CREATE TABLE "+table+" (id INTEGER PRIMARY KEY, val TEXT)",
	); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	t.Cleanup(func() {
		//goland:noinspection SqlNoDataSourceInspection
		if _, err := s.Exec(
			context.Background(),
			"DROP TABLE IF EXISTS "+table,
		); err != nil {
			t.Errorf("DROP TABLE: %v", err)
		}
	})
	return table
}

// pgxValue returns the val column for row 1, or "" when the row is absent.
func pgxValue(t *testing.T, s *database.Service, table string) string {
	t.Helper()

	var val string
	err := s.DB().
		QueryRowContext(context.Background(), "SELECT val FROM "+table+" WHERE id = $1", 1).
		Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	return val
}
