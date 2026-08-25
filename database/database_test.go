package database_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gasmod/gas"
	database "github.com/gasmod/gas/database"

	_ "modernc.org/sqlite"
)

// Compile-time interface checks.
var (
	_ gas.Service          = (*database.Service)(nil)
	_ gas.DatabaseProvider = (*database.Service)(nil)
	_ gas.HealthReporter   = (*database.Service)(nil)
	_ gas.ReadyReporter    = (*database.Service)(nil)
)

func newTestService(t *testing.T) *database.Service {
	t.Helper()

	cfg := database.DefaultConfig()
	cfg.Database.Driver = "sqlite"
	cfg.Database.DSN = filepath.Join(t.TempDir(), "test.db")

	s := database.New(database.WithConfig(cfg))(nil, gas.NewNopLogger()())
	if err := s.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestName(t *testing.T) {
	s := database.New()(nil, gas.NewNopLogger()())
	if s.Name() != "gas/database" {
		t.Fatalf("expected gas/database, got %s", s.Name())
	}
}

func TestInit_NoDSN(t *testing.T) {
	s := database.New()(nil, gas.NewNopLogger()())
	if err := s.Init(); err == nil {
		t.Fatal("expected error for missing DSN")
	}
}

func TestInit_Close_Lifecycle(t *testing.T) {
	s := newTestService(t)
	if s.DB() == nil {
		t.Fatal("DB() should not be nil after Init")
	}
}

func TestDB_ReturnsConnection(t *testing.T) {
	s := newTestService(t)
	db := s.DB()
	if db == nil {
		t.Fatal("DB() returned nil")
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("Ping via DB(): %v", err)
	}
}

func TestPing(t *testing.T) {
	s := newTestService(t)
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestPing_NotInitialized(t *testing.T) {
	s := database.New()(nil, gas.NewNopLogger()())
	if err := s.Ping(context.Background()); err == nil {
		t.Fatal("expected error for uninitialized service")
	}
}

func TestQuery(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	_, err := s.Exec(ctx, "CREATE TABLE test (id INTEGER PRIMARY KEY, name TEXT)")
	if err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	_, err = s.Exec(ctx, "INSERT INTO test (id, name) VALUES (?, ?)", 1, "alice")
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	rows, err := s.Query(ctx, "SELECT id, name FROM test WHERE id = ?", 1)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatal("expected a row")
	}

	var id int
	var name string
	if err = rows.Scan(&id, &name); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if id != 1 || name != "alice" {
		t.Errorf("got (%d, %q), want (1, alice)", id, name)
	}

	if rows.Next() {
		t.Error("expected no more rows")
	}
	if err = rows.Err(); err != nil {
		t.Fatalf("Rows.Err: %v", err)
	}
}

func TestExec(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	_, err := s.Exec(ctx, "CREATE TABLE test2 (id INTEGER PRIMARY KEY, val TEXT)")
	if err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	result, err := s.Exec(ctx, "INSERT INTO test2 (id, val) VALUES (?, ?)", 1, "hello")
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("RowsAffected: %v", err)
	}
	if affected != 1 {
		t.Errorf("RowsAffected = %d, want 1", affected)
	}
}

func TestQuery_Closed(t *testing.T) {
	s := newTestService(t)
	s.Close()

	_, err := s.Query(context.Background(), "SELECT 1")
	if err == nil {
		t.Fatal("expected error when service is closed")
	}
}

func TestExec_Closed(t *testing.T) {
	s := newTestService(t)
	s.Close()

	_, err := s.Exec(context.Background(), "SELECT 1")
	if err == nil {
		t.Fatal("expected error when service is closed")
	}
}

func TestBeginTx(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	_, err := s.Exec(ctx, "CREATE TABLE tx_test (id INTEGER PRIMARY KEY, val TEXT)")
	if err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	_, err = tx.ExecContext(ctx, "INSERT INTO tx_test (id, val) VALUES (?, ?)", 1, "tx-value")
	if err != nil {
		t.Fatalf("INSERT in tx: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	rows, err := s.Query(ctx, "SELECT val FROM tx_test WHERE id = ?", 1)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatal("expected a row after commit")
	}
	var val string
	if err := rows.Scan(&val); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if val != "tx-value" {
		t.Errorf("val = %q, want tx-value", val)
	}
}

func TestBeginTx_Closed(t *testing.T) {
	s := newTestService(t)
	s.Close()

	_, err := s.BeginTx(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error when service is closed")
	}
}

func TestWithTx_Commit(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	_, err := s.Exec(ctx, "CREATE TABLE withtx_test (id INTEGER PRIMARY KEY, val TEXT)")
	if err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	err = s.WithTx(ctx, nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO withtx_test (id, val) VALUES (?, ?)", 1, "committed")
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	rows, err := s.Query(ctx, "SELECT val FROM withtx_test WHERE id = ?", 1)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatal("expected row after WithTx commit")
	}
	var val string
	if err := rows.Scan(&val); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if val != "committed" {
		t.Errorf("val = %q, want committed", val)
	}
}

func TestWithTx_Rollback(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	_, err := s.Exec(ctx, "CREATE TABLE withtx_rb (id INTEGER PRIMARY KEY, val TEXT)")
	if err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	err = s.WithTx(ctx, nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO withtx_rb (id, val) VALUES (?, ?)", 1, "rolled-back")
		if err != nil {
			return err
		}
		return sql.ErrNoRows // simulate an error to trigger rollback
	})
	if err == nil {
		t.Fatal("expected error from WithTx")
	}

	rows, err := s.Query(ctx, "SELECT val FROM withtx_rb WHERE id = ?", 1)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()

	if rows.Next() {
		t.Error("expected no rows after rollback")
	}
}

func TestWithTx_Panic(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	_, err := s.Exec(ctx, "CREATE TABLE withtx_panic (id INTEGER PRIMARY KEY, val TEXT)")
	if err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic to propagate")
		}

		// Verify the insert was rolled back.
		rows, err := s.Query(ctx, "SELECT val FROM withtx_panic WHERE id = ?", 1)
		if err != nil {
			t.Fatalf("Query after panic: %v", err)
		}
		defer rows.Close()
		if rows.Next() {
			t.Error("expected no rows after panic rollback")
		}
	}()

	_ = s.WithTx(ctx, nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO withtx_panic (id, val) VALUES (?, ?)", 1, "panic-value")
		if err != nil {
			return err
		}
		panic("test panic")
	})
}

func TestWithTx_Closed(t *testing.T) {
	s := newTestService(t)
	s.Close()

	err := s.WithTx(context.Background(), nil, func(_ *sql.Tx) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected error when service is closed")
	}
}

func TestCheckHealth(t *testing.T) {
	// Before Init: not initialized.
	s := database.New()(nil, gas.NewNopLogger()())
	if err := s.CheckHealth(context.Background()); err == nil {
		t.Fatal("expected error for uninitialized service")
	}

	// After Init: healthy.
	s = newTestService(t)
	if err := s.CheckHealth(context.Background()); err != nil {
		t.Fatalf("CheckHealth: %v", err)
	}

	// After Close: unhealthy.
	s.Close()
	if err := s.CheckHealth(context.Background()); err == nil {
		t.Fatal("expected error after Close")
	}
}

func TestCheckReady(t *testing.T) {
	s := newTestService(t)
	if err := s.CheckReady(context.Background()); err != nil {
		t.Fatalf("CheckReady: %v", err)
	}

	s.Close()
	if err := s.CheckReady(context.Background()); err == nil {
		t.Fatal("expected error after Close")
	}
}

func TestInit_RetrySuccess(t *testing.T) {
	cfg := database.DefaultConfig()
	cfg.Database.Driver = "sqlite"
	cfg.Database.DSN = filepath.Join(t.TempDir(), "retry-test.db")
	cfg.Database.ConnRetries = 3
	cfg.Database.ConnRetryInterval = 1 * time.Millisecond

	s := database.New(database.WithConfig(cfg))(nil, gas.NewNopLogger()())
	if err := s.Init(); err != nil {
		t.Fatalf("Init with retries enabled should succeed: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if s.DB() == nil {
		t.Fatal("DB() should not be nil")
	}
}

func TestInit_RetryExhausted(t *testing.T) {
	cfg := database.DefaultConfig()
	cfg.Database.Mode = "pgx"
	cfg.Database.DSN = "postgres://invalid:invalid@localhost:1/nonexistent"
	cfg.Database.ConnRetries = 2
	cfg.Database.ConnRetryInterval = 1 * time.Millisecond

	s := database.New(database.WithConfig(cfg))(nil, gas.NewNopLogger()())
	err := s.Init()
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}

func TestDriver_SQLMode(t *testing.T) {
	s := newTestService(t)
	if s.Driver() != database.DriverSQLite {
		t.Errorf("Driver() = %q, want %q", s.Driver(), database.DriverSQLite)
	}
}

func TestDriver_PgxMode(t *testing.T) {
	// Driver reports from configuration, so this needs no connection.
	cfg := database.DefaultConfig()
	cfg.Database.Mode = database.ModePgx
	cfg.Database.Driver = database.DriverPostgres
	cfg.Database.DSN = "postgres://user:pass@localhost:5432/db"

	s := database.New(database.WithConfig(cfg))(nil, gas.NewNopLogger()())
	if s.Driver() != database.DriverPgx {
		t.Errorf("Driver() = %q, want %q", s.Driver(), database.DriverPgx)
	}
}

// sqliteConnector adapts the registered sqlite driver to driver.Connector so
// WithConnector can be exercised without a network database.
type sqliteConnector struct {
	drv driver.Driver
	dsn string
}

func (c sqliteConnector) Connect(context.Context) (driver.Conn, error) { return c.drv.Open(c.dsn) }
func (c sqliteConnector) Driver() driver.Driver                        { return c.drv }

func TestWithConnector(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "connector.db")

	// Borrow the registered sqlite driver.
	probe, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	drv := probe.Driver()
	probe.Close()

	// Driver and DSN are deliberately left unset: the connector supplies both.
	cfg := database.DefaultConfig()
	cfg.Database.Driver = ""
	cfg.Database.DSN = ""

	s := database.New(
		database.WithConfig(cfg),
		database.WithConnector(sqliteConnector{drv: drv, dsn: dsn}),
	)(nil, gas.NewNopLogger()())

	if err := s.Init(); err != nil {
		t.Fatalf("Init with connector: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if _, err := s.Exec(context.Background(), "CREATE TABLE conn_test (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("Exec through connector-backed DB: %v", err)
	}
}

func TestInit_UnregisteredDriver(t *testing.T) {
	// "postgres" passes validation but no such driver is registered in this
	// test binary, so sql.Open must fail.
	cfg := database.DefaultConfig()
	cfg.Database.Driver = database.DriverPostgres
	cfg.Database.DSN = "postgres://user:pass@localhost:5432/db"

	s := database.New(database.WithConfig(cfg))(nil, gas.NewNopLogger()())
	if err := s.Init(); err == nil {
		t.Fatal("expected error for an unregistered driver")
	}
}

func TestInit_PingFailure(t *testing.T) {
	// A directory is not a usable sqlite file: sql.Open succeeds lazily and
	// the ping fails.
	cfg := database.DefaultConfig()
	cfg.Database.Driver = "sqlite"
	cfg.Database.DSN = t.TempDir()

	s := database.New(database.WithConfig(cfg))(nil, gas.NewNopLogger()())
	if err := s.Init(); err == nil {
		t.Fatal("expected error when the ping fails")
	}
}

func TestInit_PgxInvalidDSN(t *testing.T) {
	cfg := database.DefaultConfig()
	cfg.Database.Mode = database.ModePgx
	cfg.Database.DSN = "::::not a dsn::::"

	s := database.New(database.WithConfig(cfg))(nil, gas.NewNopLogger()())
	if err := s.Init(); err == nil {
		t.Fatal("expected error for an unparseable pgx DSN")
	}
}

func TestClose_Uninitialized(t *testing.T) {
	s := database.New()(nil, gas.NewNopLogger()())
	if err := s.Close(); err != nil {
		t.Fatalf("Close on an uninitialized service: %v", err)
	}
}

func TestClose_Repeated(t *testing.T) {
	s := newTestService(t)

	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestPing_Closed(t *testing.T) {
	s := newTestService(t)
	s.Close()

	if err := s.Ping(context.Background()); err == nil {
		t.Fatal("expected error pinging a closed service")
	}
}

func TestQuery_InvalidSQL(t *testing.T) {
	s := newTestService(t)

	if _, err := s.Query(context.Background(), "SELECT * FROM does_not_exist"); err == nil {
		t.Fatal("expected error for a query against a missing table")
	}
}

func TestExec_InvalidSQL(t *testing.T) {
	s := newTestService(t)

	if _, err := s.Exec(context.Background(), "NOT VALID SQL"); err == nil {
		t.Fatal("expected error for invalid SQL")
	}
}

func TestWithTx_FnCommitsThenReturnsNil(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	if _, err := s.Exec(ctx, "CREATE TABLE withtx_dbl (id INTEGER PRIMARY KEY, val TEXT)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	// A callback that commits and then reports success leaves WithTx nothing
	// to commit, so the redundant commit surfaces as sql.ErrTxDone.
	err := s.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO withtx_dbl (id, val) VALUES (?, ?)", 1, "inner"); err != nil {
			return err
		}
		return tx.Commit()
	})
	if !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("err = %v, want it to wrap sql.ErrTxDone", err)
	}

	// The callback's own commit still stands.
	rows, err := s.Query(ctx, "SELECT val FROM withtx_dbl WHERE id = ?", 1)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("expected the row committed by fn to survive")
	}
}

func TestWithTx_FnRollsBackThenReturnsError(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()

	if _, err := s.Exec(ctx, "CREATE TABLE withtx_self_rb (id INTEGER PRIMARY KEY, val TEXT)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	sentinel := errors.New("failed after rolling back")
	err := s.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO withtx_self_rb (id, val) VALUES (?, ?)", 1, "gone"); err != nil {
			return err
		}
		if err := tx.Rollback(); err != nil {
			return err
		}
		return sentinel
	})

	// WithTx's own rollback reports sql.ErrTxDone, which is expected rather
	// than a failure, so the caller sees only fn's error.
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the sentinel", err)
	}
	if errors.Is(err, sql.ErrTxDone) {
		t.Errorf("err = %v, should not report the already-settled transaction", err)
	}

	rows, err := s.Query(ctx, "SELECT val FROM withtx_self_rb WHERE id = ?", 1)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Error("expected no rows after the callback rolled back")
	}
}

// A service whose Init never ran, or whose Init returned an error, holds a
// nil *sql.DB while closed is still false. The closed guard does not cover
// that state, so every method that reaches for s.db has to check it, the way
// Ping and CheckHealth already report it.

func TestQuery_NotInitialized(t *testing.T) {
	s := database.New()(nil, gas.NewNopLogger()())

	if _, err := s.Query(context.Background(), "SELECT 1"); err == nil {
		t.Fatal("expected error querying an uninitialized service")
	}
}

func TestExec_NotInitialized(t *testing.T) {
	s := database.New()(nil, gas.NewNopLogger()())

	if _, err := s.Exec(context.Background(), "SELECT 1"); err == nil {
		t.Fatal("expected error executing against an uninitialized service")
	}
}

func TestBeginTx_NotInitialized(t *testing.T) {
	s := database.New()(nil, gas.NewNopLogger()())

	if _, err := s.BeginTx(context.Background(), nil); err == nil {
		t.Fatal("expected error beginning a transaction on an uninitialized service")
	}
}

func TestWithTx_NotInitialized(t *testing.T) {
	s := database.New()(nil, gas.NewNopLogger()())

	called := false
	err := s.WithTx(context.Background(), nil, func(*sql.Tx) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("expected error running a transaction on an uninitialized service")
	}
	if called {
		t.Error("fn should not run when the service has no connection")
	}
}

func TestQuery_AfterFailedInit(t *testing.T) {
	// A directory is not a usable sqlite file, so Init fails at the ping and
	// leaves the service without a connection. It is not closed either, so a
	// caller that logged the Init error and carried on still reaches Query.
	cfg := database.DefaultConfig()
	cfg.Database.Driver = "sqlite"
	cfg.Database.DSN = t.TempDir()

	s := database.New(database.WithConfig(cfg))(nil, gas.NewNopLogger()())
	if err := s.Init(); err == nil {
		t.Fatal("expected Init to fail")
	}

	if _, err := s.Query(context.Background(), "SELECT 1"); err == nil {
		t.Fatal("expected error querying a service whose Init failed")
	}
}
