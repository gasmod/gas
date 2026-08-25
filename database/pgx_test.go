package database_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// postgresDSNEnv names the environment variable holding a live PostgreSQL
// DSN. The pgx tests below that need a real server skip without it, since
// neither sqlite nor an in-process fake can stand in for pgxpool.
const postgresDSNEnv = "GAS_TEST_POSTGRES_DSN"

// newPgxService returns a service running in ModePgx against the DSN in
// postgresDSNEnv, skipping the test when it is not set.
func newPgxService(t *testing.T) *database.Service {
	t.Helper()

	dsn := os.Getenv(postgresDSNEnv)
	if dsn == "" {
		t.Skipf("set %s to a PostgreSQL DSN to run pgx tests", postgresDSNEnv)
	}

	cfg := database.DefaultConfig()
	cfg.Database.Mode = database.ModePgx
	cfg.Database.DSN = dsn

	s := database.New(database.WithConfig(cfg))(nil, gas.NewNopLogger()())
	if err := s.Init(); err != nil {
		t.Fatalf("Init in pgx mode: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
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
	_ = s.Close()

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
	_ = s.Close()

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

	_ = s.Close()
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

	if got := pgxValue(t, s, table, 1); got != "committed" {
		t.Errorf("val = %q, want committed", got)
	}
}

func TestPgx_WithPgxTxRollback(t *testing.T) {
	s := newPgxService(t)
	ctx := context.Background()
	table := createPgxTable(t, s)

	sentinel := errors.New("trigger rollback")
	err := s.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO "+table+" (id, val) VALUES ($1, $2)", 1, "rolled-back"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want it to wrap the sentinel", err)
	}

	if got := pgxValue(t, s, table, 1); got != "" {
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
		if got := pgxValue(t, s, table, 1); got != "" {
			t.Errorf("found %q after panic rollback, want no row", got)
		}
	}()

	_ = s.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO "+table+" (id, val) VALUES ($1, $2)", 1, "panic-value"); err != nil {
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
		if _, err := tx.Exec(ctx, "INSERT INTO "+table+" (id, val) VALUES ($1, $2)", 1, "abandoned"); err != nil {
			return err
		}
		// The commit runs on ctx, so cancelling here must fail it rather
		// than persist work the caller abandoned.
		cancel()
		return nil
	})
	if err == nil {
		t.Fatal("expected commit to fail on a canceled context")
	}

	if got := pgxValue(t, s, table, 1); got != "" {
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
	if _, err := tx.Exec(ctx, "INSERT INTO "+table+" (id, val) VALUES ($1, $2)", 1, "manual"); err != nil {
		t.Fatalf("INSERT in tx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if got := pgxValue(t, s, table, 1); got != "manual" {
		t.Errorf("val = %q, want manual", got)
	}
}

// createPgxTable makes a scratch table dropped when the test ends, so the
// pgx tests can share one database without colliding.
func createPgxTable(t *testing.T, s *database.Service) string {
	t.Helper()

	table := "gas_db_test_" + t.Name()
	for i, r := range table {
		if !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || r == '_') {
			table = table[:i] + "_" + table[i+1:]
		}
	}

	ctx := context.Background()
	if _, err := s.Exec(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY, val TEXT)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	t.Cleanup(func() {
		//goland:noinspection SqlNoDataSourceInspection
		if _, err := s.Exec(context.Background(), fmt.Sprintf("DROP TABLE IF EXISTS \"%s\"", table)); err != nil {
			t.Errorf("DROP TABLE: %v", err)
		}
	})
	return table
}

// pgxValue returns the val column for id, or "" when the row is absent.
func pgxValue(t *testing.T, s *database.Service, table string, id int) string {
	t.Helper()

	var val string
	err := s.DB().QueryRowContext(context.Background(), "SELECT val FROM "+table+" WHERE id = $1", id).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	return val
}
