package guidesdatabase_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
)

// Docs: guides/database.mdx
// "`gas/database` owns the connection pool and hands out
// `gas.DatabaseProvider`."
var _ gas.DatabaseProvider = (*database.Service)(nil)

// envConfig returns a loaded config backed by the environment, as the page's
// wiring snippet builds it.
func envConfig(t *testing.T) gas.ConfigProvider {
	t.Helper()
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		t.Fatalf("config Load: %v", err)
	}
	return cfg
}

// Docs: guides/database.mdx#wiring
// "`migrate.New()` asks the container for `gas.DatabaseProvider`, so
// registering the database as `*database.Service` leaves that dependency
// unresolved and startup fails."
func TestWiringConcreteDatabaseTypeFailsStartup(t *testing.T) {
	t.Parallel()

	start := func(t *testing.T, register gas.WorkerOption) error {
		t.Helper()
		app := gas.NewApp(
			gas.WithServiceInstance[gas.ConfigProvider](envConfig(t)),
			gas.WithServiceInstance[gas.Logger](gas.Logger(&gas.NopLogger{})),
			register,
			gas.WithSingletonService[gas.MigrationManager](migrate.New()),
		)
		err := app.Start(t.Context())
		t.Cleanup(func() { _ = app.Stop(context.Background()) })
		return err
	}

	t.Run("Interface", func(t *testing.T) {
		t.Parallel()
		ctor := database.New(database.WithConfig(sqliteConfig(t)))
		if err := start(t, gas.WithSingletonService[gas.DatabaseProvider](ctor)); err != nil {
			t.Fatalf("Start with gas.DatabaseProvider registration: %v, want nil", err)
		}
	})
	t.Run("Concrete", func(t *testing.T) {
		t.Parallel()
		ctor := database.New(database.WithConfig(sqliteConfig(t)))
		if err := start(t, gas.WithSingletonService[*database.Service](ctor)); err == nil {
			t.Fatal("Start with *database.Service registration: err = nil, want a startup error")
		}
	})
}

// Docs: guides/database.mdx#wiring
// "Settings bind from the config provider, so a DSN can come from
// `DATABASE__DSN` in the environment."
func TestDSNFromEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env.db")
	t.Setenv("DATABASE__DSN", "file:"+path)
	t.Setenv("DATABASE__DRIVER", database.DriverSQLite)

	svc, err := database.New()(envConfig(t), &gas.NopLogger{})
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("Init with DSN from DATABASE__DSN: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	if _, err := svc.Exec(t.Context(), "CREATE TABLE envcheck (x INTEGER)"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file at the DATABASE__DSN path: %v", err)
	}
}

// Docs: guides/database.mdx#choosing-a-mode
// "| `ModeSQL` (default) | `database/sql` |"
func TestModeSQLIsDefault(t *testing.T) {
	t.Parallel()
	if got := database.DefaultConfig().Database.Mode; got != database.ModeSQL {
		t.Fatalf("DefaultConfig().Database.Mode = %q, want %q", got, database.ModeSQL)
	}
}

// Docs: guides/database.mdx#choosing-a-mode
// "| `ModeSQL` (default) | `database/sql` | PostgreSQL or SQLite. `Database.Driver` must be `postgres`, `pgx`, or `sqlite` unless a connector is supplied with `WithConnector` |"
// This covers "PostgreSQL or SQLite"; the driver-name rule and the
// WithConnector route are TestModeSQLDriverNames.
func TestModeSQLDrivers(t *testing.T) {
	t.Parallel()

	t.Run("SQLite", func(t *testing.T) {
		t.Parallel()
		svc := newSQLite(t)
		var one int
		if err := svc.DB().QueryRowContext(t.Context(), "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("SELECT 1 = %d, %v; want 1, nil", one, err)
		}
	})
	t.Run("PostgreSQL", func(t *testing.T) {
		t.Parallel()
		cfg := database.DefaultConfig()
		cfg.Database.Driver = database.DriverPgx
		cfg.Database.DSN = postgresDSN(t)
		svc := startService(t, cfg, nil)
		var one int
		if err := svc.DB().QueryRowContext(t.Context(), "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("SELECT 1 = %d, %v; want 1, nil", one, err)
		}
	})
}

// newPgx starts a ModePgx service against the shared PostgreSQL container.
func newPgx(t *testing.T) *database.Service {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.Database.Mode = database.ModePgx
	cfg.Database.DSN = postgresDSN(t)
	return startService(t, cfg, nil)
}

// Docs: guides/database.mdx#choosing-a-mode
// "`DB()` returns a `*sql.DB` in both modes, so sqlc's `database/sql` output
// always works. In pgx mode, `Pool()` additionally returns the native pool for
// sqlc's pgx output"
func TestDBInBothModesAndPoolInPgx(t *testing.T) {
	t.Parallel()

	for name, svc := range map[string]*database.Service{
		"sql": newSQLite(t),
		"pgx": newPgx(t),
	} {
		var one int
		if err := svc.DB().QueryRowContext(t.Context(), "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Errorf("%s mode: DB() SELECT 1 = %d, %v; want 1, nil", name, one, err)
		}
	}

	pool := newPgx(t).Pool()
	if pool == nil {
		t.Fatal("pgx mode: Pool() = nil, want the native pool")
	}
	if err := pool.Ping(t.Context()); err != nil {
		t.Fatalf("pgx mode: Pool().Ping: %v", err)
	}
}

// fakeProvider is a gas.DatabaseProvider that is not this module's Service.
type fakeProvider struct{ gas.DatabaseProvider }

// Docs: guides/database.mdx#choosing-a-mode
// "`database.PoolFrom(provider)` unwraps it, reporting `false` when the
// provider is not pgx-backed so you can fall back."
func TestPoolFrom(t *testing.T) {
	t.Parallel()

	pgxSvc := newPgx(t)
	pool, ok := database.PoolFrom(pgxSvc)
	if !ok || pool != pgxSvc.Pool() {
		t.Fatalf("PoolFrom(pgx service) = %p, %v; want %p, true", pool, ok, pgxSvc.Pool())
	}

	for name, p := range map[string]gas.DatabaseProvider{
		"sql mode":       newSQLite(t),
		"other provider": fakeProvider{},
	} {
		if pool, ok := database.PoolFrom(p); ok || pool != nil {
			t.Errorf("PoolFrom(%s) = %p, %v; want nil, false", name, pool, ok)
		}
	}
}

// Docs: guides/database.mdx#transactions
// "`WithTx` commits when the function returns nil and rolls back otherwise,
// including on panic."
func TestWithTxCommitAndRollback(t *testing.T) {
	t.Parallel()
	svc := newSQLite(t)
	ctx := t.Context()
	if _, err := svc.Exec(ctx, "CREATE TABLE txt (v TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	insert := func(v string) func(*sql.Tx) error {
		return func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO txt (v) VALUES (?)", v)
			return err
		}
	}
	present := func(v string) bool {
		var n int
		if err := svc.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM txt WHERE v = ?", v).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", v, err)
		}
		return n == 1
	}

	if err := svc.WithTx(ctx, nil, insert("commit")); err != nil {
		t.Fatalf("WithTx returning nil: %v", err)
	}
	if !present("commit") {
		t.Error("row written by a fn that returned nil is absent, want committed")
	}

	_ = svc.WithTx(ctx, nil, func(tx *sql.Tx) error {
		if err := insert("error")(tx); err != nil {
			t.Fatalf("insert: %v", err)
		}
		return errors.New("fail")
	})
	if present("error") {
		t.Error("row written by a fn that returned an error is present, want rolled back")
	}

	func() {
		defer func() { _ = recover() }()
		_ = svc.WithTx(ctx, nil, func(tx *sql.Tx) error {
			if err := insert("panic")(tx); err != nil {
				t.Fatalf("insert: %v", err)
			}
			panic("boom")
		})
	}()
	if present("panic") {
		t.Error("row written by a fn that panicked is present, want rolled back")
	}
}

// Docs: guides/database.mdx#transactions
// "If the function returns an error, that error comes back"
func TestWithTxReturnsFnError(t *testing.T) {
	t.Parallel()
	svc := newSQLite(t)
	errFn := errors.New("fn failed")
	err := svc.WithTx(t.Context(), nil, func(*sql.Tx) error { return errFn })
	if !errors.Is(err, errFn) {
		t.Fatalf("WithTx err = %v, want it to wrap the fn error", err)
	}
}

// Docs: guides/database.mdx#transactions
// "If the function returns an error, that error comes back and the rollback is
// attempted; a rollback that itself fails is joined onto it."
func TestWithTxFailedRollbackJoined(t *testing.T) {
	t.Parallel()
	conn := &fakeConnector{rollbackErr: errRollback}
	svc := startService(t, connectorConfig(), nil, database.WithConnector(conn))

	errFn := errors.New("fn failed")
	err := svc.WithTx(t.Context(), nil, func(*sql.Tx) error { return errFn })
	if !errors.Is(err, errFn) || !errors.Is(err, errRollback) {
		t.Errorf("WithTx err = %v, want it to wrap both the fn error and the rollback error", err)
	}
}

// panicValue is a distinct type so the test can check that the recovered
// value is the one the function panicked with, unchanged.
type panicValue struct{ msg string }

// Docs: guides/database.mdx#transactions
// "After a panic, a failed rollback is logged instead, and the panic
// propagates unchanged."
func TestWithTxPanicFailedRollbackLogged(t *testing.T) {
	t.Parallel()
	conn := &fakeConnector{rollbackErr: errRollback}
	log := &recLogger{}
	svc := startService(t, connectorConfig(), log, database.WithConnector(conn))

	want := &panicValue{msg: "boom"}
	var got any
	func() {
		defer func() { got = recover() }()
		_ = svc.WithTx(t.Context(), nil, func(*sql.Tx) error { panic(want) })
	}()
	if got != want {
		t.Errorf("recovered %#v, want the original panic value %#v", got, want)
	}
	logged := false
	for _, l := range log.lines() {
		if strings.Contains(strings.ToLower(l), "rollback") {
			logged = true
		}
	}
	if !logged {
		t.Errorf("no log entry mentions the failed rollback; logged %q", log.lines())
	}
}

// Docs: guides/database.mdx#transactions
// "A commit failure is returned wrapped."
func TestWithTxCommitFailureWrapped(t *testing.T) {
	t.Parallel()
	conn := &fakeConnector{commitErr: errCommit}
	svc := startService(t, connectorConfig(), nil, database.WithConnector(conn))

	err := svc.WithTx(t.Context(), nil, func(*sql.Tx) error { return nil })
	if !errors.Is(err, errCommit) {
		t.Fatalf("WithTx err = %v, want it to wrap the commit error", err)
	}
	if err == errCommit { //nolint:errorlint // checking that the error is wrapped, not bare
		t.Fatalf("WithTx err is the bare commit error, want it wrapped")
	}
}

// Docs: guides/database.mdx#transactions
// "In pgx mode, `WithPgxTx` mirrors this against `pgx.Tx`"
func TestWithPgxTxMirrorsWithTx(t *testing.T) {
	t.Parallel()
	svc := newPgx(t)
	ctx := t.Context()
	table := pgTable(t)
	if _, err := svc.Exec(ctx, "CREATE TABLE "+table+" (v TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	insert := func(tx pgx.Tx, v string) {
		if _, err := tx.Exec(ctx, "INSERT INTO "+table+" (v) VALUES ($1)", v); err != nil {
			t.Fatalf("insert %s: %v", v, err)
		}
	}
	present := func(v string) bool {
		var n int
		if err := svc.Pool().QueryRow(ctx, "SELECT COUNT(*) FROM "+table+" WHERE v = $1", v).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", v, err)
		}
		return n == 1
	}

	if err := svc.WithPgxTx(ctx, nil, func(tx pgx.Tx) error { insert(tx, "commit"); return nil }); err != nil {
		t.Fatalf("WithPgxTx returning nil: %v", err)
	}
	if !present("commit") {
		t.Error("row written by a fn that returned nil is absent, want committed")
	}

	errFn := errors.New("fn failed")
	err := svc.WithPgxTx(ctx, nil, func(tx pgx.Tx) error { insert(tx, "error"); return errFn })
	if !errors.Is(err, errFn) {
		t.Errorf("WithPgxTx err = %v, want it to wrap the fn error", err)
	}
	if present("error") {
		t.Error("row written by a fn that returned an error is present, want rolled back")
	}

	func() {
		defer func() { _ = recover() }()
		_ = svc.WithPgxTx(ctx, nil, func(tx pgx.Tx) error { insert(tx, "panic"); panic("boom") })
	}()
	if present("panic") {
		t.Error("row written by a fn that panicked is present, want rolled back")
	}
}

// Docs: guides/database.mdx#transactions
// "rolling back on a context detached from cancellation so cleanup still runs
// when the caller's context is already done."
// Assumption: a rollback that ran successfully leaves no rollback error
// (context.Canceled) joined onto the fn error and the row unwritten.
func TestWithPgxTxRollbackSurvivesCanceledContext(t *testing.T) {
	t.Parallel()
	svc := newPgx(t)
	table := pgTable(t)
	if _, err := svc.Exec(t.Context(), "CREATE TABLE "+table+" (v TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	errFn := errors.New("fn failed")
	err := svc.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO "+table+" (v) VALUES ('x')"); err != nil {
			t.Fatalf("insert: %v", err)
		}
		cancel()
		return errFn
	})
	if !errors.Is(err, errFn) {
		t.Fatalf("WithPgxTx err = %v, want it to wrap the fn error", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("WithPgxTx err = %v, want no rollback failure from the canceled context", err)
	}
	var n int
	if err := svc.Pool().QueryRow(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows after rollback = %d, %v; want 0, nil", n, err)
	}
}

// Docs: guides/database.mdx#health-and-readiness
// "`CheckHealth` (liveness) fails only when the service is uninitialized or
// closed. It does not ping [...] `CheckReady` does ping, so a failing
// dependency drains traffic off the instance instead of killing it."
func TestHealthAndReadiness(t *testing.T) {
	t.Parallel()
	conn := &fakeConnector{}
	svc, err := database.New(database.WithConfig(connectorConfig()), database.WithConnector(conn))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	ctx := t.Context()

	if err := svc.CheckHealth(ctx); err == nil {
		t.Error("CheckHealth before Init: err = nil, want an error")
	}
	if err := svc.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := svc.CheckHealth(ctx); err != nil {
		t.Errorf("CheckHealth after Init: %v, want nil", err)
	}
	if err := svc.CheckReady(ctx); err != nil {
		t.Errorf("CheckReady with a reachable database: %v, want nil", err)
	}

	conn.pingFail.Store(true)
	if err := svc.CheckHealth(ctx); err != nil {
		t.Errorf("CheckHealth with a failing ping: %v, want nil", err)
	}
	if err := svc.CheckReady(ctx); err == nil {
		t.Error("CheckReady with a failing ping: err = nil, want an error")
	}

	if err := svc.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := svc.CheckHealth(ctx); err == nil {
		t.Error("CheckHealth after Close: err = nil, want an error")
	}
}

// Docs: guides/database.mdx#config
// The Config table's Default column.
func TestConfigDefaults(t *testing.T) {
	t.Parallel()
	got := database.DefaultConfig().Database
	if got.Mode != "sql" {
		t.Errorf("Mode = %q, want sql", got.Mode)
	}
	if got.Driver != "postgres" {
		t.Errorf("Driver = %q, want postgres", got.Driver)
	}
	if got.DSN != "" {
		t.Errorf("DSN = %q, want empty", got.DSN)
	}
	if got.MaxOpenConns != 25 {
		t.Errorf("MaxOpenConns = %d, want 25", got.MaxOpenConns)
	}
	if got.MaxIdleConns != 5 {
		t.Errorf("MaxIdleConns = %d, want 5", got.MaxIdleConns)
	}
	if got.ConnMaxLifetime != 30*time.Minute {
		t.Errorf("ConnMaxLifetime = %v, want 30m", got.ConnMaxLifetime)
	}
	if got.ConnMaxIdleTime != 5*time.Minute {
		t.Errorf("ConnMaxIdleTime = %v, want 5m", got.ConnMaxIdleTime)
	}
	if got.ConnRetries != 0 {
		t.Errorf("ConnRetries = %d, want 0", got.ConnRetries)
	}
	if got.ConnRetryInterval != 2*time.Second {
		t.Errorf("ConnRetryInterval = %v, want 2s", got.ConnRetryInterval)
	}
}

// Docs: guides/database.mdx#config
// "| `Database.DSN` | | Connection string, required unless a connector is
// supplied |"
func TestDSNRequiredWithoutConnector(t *testing.T) {
	t.Parallel()
	cfg := database.DefaultConfig()
	cfg.Database.Driver = database.DriverSQLite
	cfg.Database.DSN = ""
	svc, err := database.New(database.WithConfig(cfg))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	if err := svc.Init(t.Context()); err == nil {
		t.Fatal("Init with an empty DSN and no connector: err = nil, want an error")
	}
}

// Docs: guides/database.mdx#config
// "| `Database.MaxOpenConns` | `25` | Maximum open connections |"
func TestMaxOpenConnsApplied(t *testing.T) {
	t.Parallel()

	t.Run("SQLDefault", func(t *testing.T) {
		t.Parallel()
		if got := newSQLite(t).DB().Stats().MaxOpenConnections; got != 25 {
			t.Fatalf("sql mode MaxOpenConnections = %d, want 25", got)
		}
	})
	t.Run("SQLSet", func(t *testing.T) {
		t.Parallel()
		cfg := sqliteConfig(t)
		cfg.Database.MaxOpenConns = 3
		cfg.Database.MaxIdleConns = 3
		if got := startService(t, cfg, nil).DB().Stats().MaxOpenConnections; got != 3 {
			t.Fatalf("sql mode MaxOpenConnections = %d, want 3", got)
		}
	})
	t.Run("PgxDefault", func(t *testing.T) {
		t.Parallel()
		if got := newPgx(t).Pool().Config().MaxConns; got != 25 {
			t.Fatalf("pgx mode MaxConns = %d, want 25", got)
		}
	})
}

// Docs: guides/database.mdx#config
// "| `Database.ConnMaxLifetime` | `30m` | Maximum connection reuse time |" and
// "| `Database.ConnMaxIdleTime` | `5m` | Maximum connection idle time |"
// pgx mode, read from Pool().Config(); ModeSQL is in pool_test.go.
func TestPgxConnLifetimesApplied(t *testing.T) {
	t.Parallel()
	pc := newPgx(t).Pool().Config()
	if pc.MaxConnLifetime != 30*time.Minute {
		t.Errorf("pgx MaxConnLifetime = %v, want 30m", pc.MaxConnLifetime)
	}
	if pc.MaxConnIdleTime != 5*time.Minute {
		t.Errorf("pgx MaxConnIdleTime = %v, want 5m", pc.MaxConnIdleTime)
	}
}

// Docs: guides/database.mdx#config
// "| `Database.ConnRetries` | `0` | Connection retry attempts |" and
// "| `Database.ConnRetryInterval` | `2s` | Base retry interval, doubling each
// attempt |"
func TestConnRetries(t *testing.T) {
	t.Parallel()

	run := func(t *testing.T, retries int, interval time.Duration) (int32, time.Duration) {
		t.Helper()
		conn := &fakeConnector{failConnect: true}
		cfg := connectorConfig()
		cfg.Database.ConnRetries = retries
		cfg.Database.ConnRetryInterval = interval
		svc, err := database.New(database.WithConfig(cfg), database.WithConnector(conn))(nil, &gas.NopLogger{})
		if err != nil {
			t.Fatalf("database.New: %v", err)
		}
		t.Cleanup(func() { _ = svc.Close(context.Background()) })
		began := time.Now()
		if err := svc.Init(t.Context()); err == nil {
			t.Fatal("Init with a connector that always fails: err = nil, want an error")
		}
		return conn.connects.Load(), time.Since(began)
	}

	t.Run("NoRetries", func(t *testing.T) {
		t.Parallel()
		if n, _ := run(t, 0, 2*time.Second); n != 1 {
			t.Fatalf("connection attempts with ConnRetries 0 = %d, want 1", n)
		}
	})
	t.Run("Doubling", func(t *testing.T) {
		t.Parallel()
		// Two retries at a 200ms base wait 200ms then 400ms when the interval
		// doubles; a constant interval would wait 400ms in total.
		n, elapsed := run(t, 2, 200*time.Millisecond)
		if n != 3 {
			t.Errorf("connection attempts with ConnRetries 2 = %d, want 3", n)
		}
		if elapsed < 600*time.Millisecond {
			t.Errorf("Init with 2 retries at 200ms took %v, want at least 600ms (200ms + 400ms)", elapsed)
		}
	})
}

// Docs: guides/database.mdx#config
// "pass a `driver.Connector` with `database.WithConnector`. Driver and DSN
// are then not required. Connectors are ModeSQL only."
func TestWithConnector(t *testing.T) {
	t.Parallel()

	t.Run("NoDriverOrDSN", func(t *testing.T) {
		t.Parallel()
		conn := &fakeConnector{}
		svc := startService(t, connectorConfig(), nil, database.WithConnector(conn))
		if err := svc.Ping(t.Context()); err != nil {
			t.Fatalf("Ping through the connector: %v", err)
		}
		if conn.connects.Load() == 0 {
			t.Fatal("the connector was never used")
		}
	})
	t.Run("RejectedInPgxMode", func(t *testing.T) {
		t.Parallel()
		cfg := connectorConfig()
		cfg.Database.Mode = database.ModePgx
		// A reachable DSN, so ignoring the connector would let Init succeed.
		cfg.Database.DSN = postgresDSN(t)
		svc, err := database.New(database.WithConfig(cfg), database.WithConnector(&fakeConnector{}))(nil, &gas.NopLogger{})
		if err != nil {
			return // rejected at construction
		}
		t.Cleanup(func() { _ = svc.Close(context.Background()) })
		if err := svc.Init(t.Context()); err == nil {
			t.Fatal("Init with a connector in pgx mode: err = nil, want an error")
		}
	})
}
