package modulesdatabase_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"modernc.org/sqlite"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
)

// databaseDir is the gas/database module, relative to this package.
const databaseDir = "../../../../database"

// Docs: modules/database.mdx#what-it-is-for
// "Its `*database.Service` implements `gas.DatabaseProvider`, the core
// interface services inject to reach the database, along with `gas.Service`,
// `gas.HealthReporter` and `gas.ReadyReporter`."
var (
	_ gas.DatabaseProvider = (*database.Service)(nil)
	_ gas.Service          = (*database.Service)(nil)
	_ gas.HealthReporter   = (*database.Service)(nil)
	_ gas.ReadyReporter    = (*database.Service)(nil)
)

// Docs: modules/database.mdx#install
// "The module imports `github.com/jackc/pgx/v5/stdlib`, so the `pgx` driver
// name is always registered."
// This package does not import pgx's stdlib itself.
func TestPgxDriverNameRegistered(t *testing.T) {
	t.Parallel()
	if !slices.Contains(sql.Drivers(), "pgx") {
		t.Fatalf("sql.Drivers() = %v, want it to include pgx", sql.Drivers())
	}
}

// Docs: modules/database.mdx#install
// "The default driver name is `postgres`, which only a third-party driver such
// as `lib/pq` registers"
// This package imports no driver that registers postgres, so the default
// fails when the connection is opened.
func TestDefaultDriverNameUnregistered(t *testing.T) {
	t.Parallel()
	if slices.Contains(sql.Drivers(), "postgres") {
		t.Fatalf("precondition: a driver registered postgres in this test binary: %v", sql.Drivers())
	}
	cfg := database.DefaultConfig()
	cfg.Database.DSN = "postgres://claims@127.0.0.1:1/claims"
	err := build(t, nil, nil, database.WithConfig(cfg)).Init(t.Context())
	if err == nil || !strings.HasPrefix(err.Error(), "gas/database: open: ") || !strings.Contains(err.Error(), `"postgres"`) {
		t.Fatalf("Init with the default postgres driver name = %v, want a gas/database: open: error naming postgres", err)
	}
}

// Docs: modules/database.mdx#install
// "For SQLite, import a driver that registers the name `sqlite`, such as
// `modernc.org/sqlite`."
func TestSQLiteDriverName(t *testing.T) {
	t.Parallel()
	svc := newSQLite(t)
	if _, err := svc.Exec(t.Context(), "CREATE TABLE t (x INTEGER)"); err != nil {
		t.Fatalf("Exec on sqlite: %v", err)
	}
}

// Docs: modules/database.mdx#wiring
// "`database.New(opts...)` returns a constructor that the container calls with
// `gas.ConfigProvider` and `gas.Logger`, so both must be registered."
func TestConstructorNeedsConfigAndLogger(t *testing.T) {
	t.Parallel()
	cfgOpt := func(t *testing.T) gas.Option {
		return gas.WithServiceInstance[gas.ConfigProvider](jsonConfig(t, map[string]any{}))
	}
	logOpt := gas.WithServiceInstance[gas.Logger](gas.Logger(&gas.NopLogger{}))
	start := func(t *testing.T, opts ...gas.Option) error {
		t.Helper()
		dbOpt := gas.WithSingletonService[gas.DatabaseProvider](database.New(database.WithConfig(sqliteConfig(t))))
		app := gas.NewApp(append(opts, dbOpt)...)
		err := app.Start(t.Context())
		t.Cleanup(func() { _ = app.Stop(context.Background()) })
		return err
	}

	t.Run("Both", func(t *testing.T) {
		t.Parallel()
		if err := start(t, cfgOpt(t), logOpt); err != nil {
			t.Fatalf("Start with both registered: %v", err)
		}
	})
	t.Run("NoConfigProvider", func(t *testing.T) {
		t.Parallel()
		if err := start(t, logOpt); err == nil {
			t.Fatal("Start without gas.ConfigProvider: err = nil, want a startup error")
		}
	})
	t.Run("NoLogger", func(t *testing.T) {
		t.Parallel()
		if err := start(t, cfgOpt(t)); err == nil {
			t.Fatal("Start without gas.Logger: err = nil, want a startup error")
		}
	})
}

// Docs: modules/database.mdx#wiring
// "The constructor never returns an error and does no I/O; the connection is
// opened in `Init`, when the app starts."
func TestConstructorDoesNoIO(t *testing.T) {
	t.Parallel()
	conn := &fakeConnector{failConnect: true}
	// Settings that Init rejects, to show the constructor checks nothing.
	cfg := connectorConfig()
	cfg.Database.Mode = "bogus"
	if _, err := database.New(database.WithConfig(cfg), database.WithConnector(conn))(nil, &gas.NopLogger{}); err != nil {
		t.Fatalf("constructor with an invalid config and a failing connector: %v, want nil", err)
	}
	if n := conn.connects.Load(); n != 0 {
		t.Fatalf("connector Connect calls during construction = %d, want 0", n)
	}

	ok := &fakeConnector{}
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](jsonConfig(t, map[string]any{})),
		gas.WithServiceInstance[gas.Logger](gas.Logger(&gas.NopLogger{})),
		gas.WithSingletonService[gas.DatabaseProvider](database.New(database.WithConfig(connectorConfig()), database.WithConnector(ok))),
	)
	if n := ok.connects.Load(); n != 0 {
		t.Fatalf("Connect calls before Start = %d, want 0", n)
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if n := ok.connects.Load(); n == 0 {
		t.Fatal("Connect calls after Start = 0, want the connection opened when the app starts")
	}
}

// Docs: modules/database.mdx#wiring
// "Register the constructor under `gas.DatabaseProvider`. The container matches
// by exact type, so registering it as `*database.Service` leaves every
// `gas.DatabaseProvider` dependency unresolved, including the one
// `migrate.New()` takes, and startup fails."
func TestRegisterUnderInterface(t *testing.T) {
	t.Parallel()
	start := func(t *testing.T, register gas.Option) error {
		t.Helper()
		app := gas.NewApp(
			gas.WithServiceInstance[gas.ConfigProvider](jsonConfig(t, map[string]any{})),
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

// Docs: modules/database.mdx#wiring
// "`WithConnector(c)` supplies a `driver.Connector`, opened with `sql.OpenDB`
// instead of `sql.Open(driver, dsn)`."
func TestWithConnectorUsed(t *testing.T) {
	t.Parallel()
	conn := &fakeConnector{}
	// A driver and DSN that sql.Open would fail to connect with.
	cfg := database.DefaultConfig()
	cfg.Database.Driver = database.DriverSQLite
	cfg.Database.DSN = "file:" + filepath.Join(t.TempDir(), "missing", "dir", "x.db")
	svc := startService(t, cfg, nil, database.WithConnector(conn))
	if conn.connects.Load() == 0 {
		t.Fatal("Init with WithConnector never called the connector")
	}
	if err := svc.Ping(t.Context()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// Docs: modules/database.mdx#queries
// "`Query` and `Exec` run against the pool; the `gas.Rows` and `gas.Result`
// they return are the `*sql.Rows` and `sql.Result` from `database/sql`."
func TestQueryAndExecReturnDatabaseSQLTypes(t *testing.T) {
	t.Parallel()
	svc := newSQLite(t)
	ctx := t.Context()
	res, err := svc.Exec(ctx, "CREATE TABLE q (x INTEGER)")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if _, ok := res.(sql.Result); !ok {
		t.Errorf("Exec result is %T, want a sql.Result", res)
	}
	rows, err := svc.Query(ctx, "SELECT x FROM q")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if _, ok := rows.(*sql.Rows); !ok {
		t.Errorf("Query rows are %T, want *sql.Rows", rows)
	}
}

// txTable creates a one-column table on a fresh SQLite service and returns
// the service and functions to insert a value in a transaction and to check
// whether a value is present.
func txTable(t *testing.T, log gas.Logger) (*database.Service, func(*sql.Tx, string), func(string) bool) {
	t.Helper()
	svc := startService(t, sqliteConfig(t), log)
	ctx := t.Context()
	if _, err := svc.Exec(ctx, "CREATE TABLE txt (v TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	insert := func(tx *sql.Tx, v string) {
		if _, err := tx.ExecContext(ctx, "INSERT INTO txt (v) VALUES (?)", v); err != nil {
			t.Fatalf("insert %s: %v", v, err)
		}
	}
	present := func(v string) bool {
		var n int
		if err := svc.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM txt WHERE v = ?", v).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", v, err)
		}
		return n == 1
	}
	return svc, insert, present
}

// Docs: modules/database.mdx#transactions
// "`WithTx` runs a function in a transaction. It commits when the function
// returns nil and rolls back when it returns an error or panics."
// and "When the function returns an error, `WithTx` rolls back and returns that
// error."
// and "When the function panics, `WithTx` rolls back and panics again with the
// original value."
func TestWithTxCommitRollbackPanic(t *testing.T) {
	t.Parallel()
	svc, insert, present := txTable(t, nil)
	ctx := t.Context()

	if err := svc.WithTx(ctx, nil, func(tx *sql.Tx) error { insert(tx, "commit"); return nil }); err != nil {
		t.Fatalf("WithTx returning nil: %v", err)
	}
	if !present("commit") {
		t.Error("row written by a fn that returned nil is absent, want committed")
	}

	errFn := errors.New("fn failed")
	err := svc.WithTx(ctx, nil, func(tx *sql.Tx) error { insert(tx, "error"); return errFn })
	if err != errFn { //nolint:errorlint // the claim is that the fn error itself is returned
		t.Errorf("WithTx err = %v, want the fn error itself", err)
	}
	if present("error") {
		t.Error("row written by a fn that returned an error is present, want rolled back")
	}

	want := &panicValue{msg: "boom"}
	got := catch(func() {
		_ = svc.WithTx(ctx, nil, func(tx *sql.Tx) error { insert(tx, "panic"); panic(want) })
	})
	if got != want {
		t.Errorf("recovered %#v, want the original panic value %#v", got, want)
	}
	if present("panic") {
		t.Error("row written by a fn that panicked is present, want rolled back")
	}
}

// Docs: modules/database.mdx#transactions
// "A rollback that itself fails is joined onto it with `errors.Join`."
func TestWithTxFailedRollbackJoined(t *testing.T) {
	t.Parallel()
	conn := &fakeConnector{rollbackErr: errRollback}
	svc := startService(t, connectorConfig(), nil, database.WithConnector(conn))

	errFn := errors.New("fn failed")
	err := svc.WithTx(t.Context(), nil, func(*sql.Tx) error { return errFn })
	if !errors.Is(err, errFn) || !errors.Is(err, errRollback) {
		t.Fatalf("WithTx err = %v, want it to wrap both the fn error and the rollback error", err)
	}
	joined, ok := err.(interface{ Unwrap() []error })                    //nolint:errorlint // checking the shape errors.Join produces
	if !ok || len(joined.Unwrap()) != 2 || joined.Unwrap()[0] != errFn { //nolint:errorlint // identity of the first joined error
		t.Fatalf("WithTx err = %#v, want errors.Join(fn error, rollback error)", err)
	}
}

// Docs: modules/database.mdx#transactions
// "A failed rollback is logged at error level instead of returned."
func TestWithTxPanicFailedRollbackLogged(t *testing.T) {
	t.Parallel()
	conn := &fakeConnector{rollbackErr: errRollback}
	log := &recLogger{}
	svc := startService(t, connectorConfig(), log, database.WithConnector(conn))

	want := &panicValue{msg: "boom"}
	got := catch(func() {
		_ = svc.WithTx(t.Context(), nil, func(*sql.Tx) error { panic(want) })
	})
	if got != want {
		t.Errorf("recovered %#v, want the original panic value %#v", got, want)
	}
	logged := false
	for _, l := range log.at("ERROR") {
		if strings.Contains(l, errRollback.Error()) {
			logged = true
		}
	}
	if !logged {
		t.Errorf("no error-level entry carries the rollback error; logged %q", log.lines())
	}
}

// Docs: modules/database.mdx#transactions
// "A rollback that finds the transaction already finished (`sql.ErrTxDone`,
// because the function committed or rolled back itself) is not an error."
func TestWithTxRollbackAfterFnFinishedTx(t *testing.T) {
	t.Parallel()
	svc, insert, present := txTable(t, nil)
	errFn := errors.New("fn failed")

	err := svc.WithTx(t.Context(), nil, func(tx *sql.Tx) error {
		insert(tx, "committed")
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit inside fn: %v", err)
		}
		return errFn
	})
	if err != errFn { //nolint:errorlint // the claim is that nothing is joined onto the fn error
		t.Errorf("WithTx after fn committed itself: err = %v, want the fn error alone", err)
	}
	if !present("committed") {
		t.Error("row committed by fn is absent")
	}

	err = svc.WithTx(t.Context(), nil, func(tx *sql.Tx) error {
		if err := tx.Rollback(); err != nil {
			t.Fatalf("rollback inside fn: %v", err)
		}
		return errFn
	})
	if err != errFn { //nolint:errorlint // the claim is that nothing is joined onto the fn error
		t.Errorf("WithTx after fn rolled back itself: err = %v, want the fn error alone", err)
	}
}

// Docs: modules/database.mdx#transactions
// "A commit failure is returned wrapped:
// `gas/database: failed to commit transaction: <cause>`."
func TestWithTxCommitFailureWrapped(t *testing.T) {
	t.Parallel()
	conn := &fakeConnector{commitErr: errCommit}
	svc := startService(t, connectorConfig(), nil, database.WithConnector(conn))

	err := svc.WithTx(t.Context(), nil, func(*sql.Tx) error { return nil })
	if !errors.Is(err, errCommit) {
		t.Fatalf("WithTx err = %v, want it to wrap the commit error", err)
	}
	if want := "gas/database: failed to commit transaction: " + errCommit.Error(); err.Error() != want {
		t.Fatalf("WithTx err = %q, want %q", err, want)
	}
}

// Docs: modules/database.mdx#transactions
// "`opts` is passed to `BeginTx` as given; nil uses the driver's defaults."
func TestWithTxPassesOptions(t *testing.T) {
	t.Parallel()
	conn := &fakeConnector{}
	svc := startService(t, connectorConfig(), nil, database.WithConnector(conn))
	ctx := t.Context()

	opts := &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true}
	if err := svc.WithTx(ctx, opts, func(*sql.Tx) error { return nil }); err != nil {
		t.Fatalf("WithTx with options: %v", err)
	}
	if err := svc.WithTx(ctx, nil, func(*sql.Tx) error { return nil }); err != nil {
		t.Fatalf("WithTx with nil options: %v", err)
	}
	_, _, got := conn.record()
	want := []driver.TxOptions{
		{Isolation: driver.IsolationLevel(sql.LevelSerializable), ReadOnly: true},
		{Isolation: driver.IsolationLevel(sql.LevelDefault), ReadOnly: false},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("driver BeginTx options = %+v, want %+v", got, want)
	}
}

// Docs: modules/database.mdx#transactions
// "`BeginTx` starts a transaction you manage yourself: you call `Commit` or
// `Rollback` on the returned `*sql.Tx`."
func TestBeginTxIsManual(t *testing.T) {
	t.Parallel()
	svc, insert, present := txTable(t, nil)
	tx, err := svc.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	insert(tx, "a")
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if present("a") {
		t.Fatal("row from a rolled-back BeginTx transaction is present")
	}

	tx, err = svc.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	insert(tx, "b")
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !present("b") {
		t.Fatal("row from a committed BeginTx transaction is absent")
	}
}

// dbtxDecls returns the non-test files of the gas/database module, in any
// package, that declare a type named DBTX.
func dbtxDecls(t *testing.T) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(databaseDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				if spec.(*ast.TypeSpec).Name.Name == "DBTX" {
					found = append(found, path)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", databaseDir, err)
	}
	return found
}

// Docs: modules/database.mdx#sqlc
// "This module declares no `DBTX` of its own."
func TestNoDBTX(t *testing.T) {
	t.Parallel()
	if found := dbtxDecls(t); len(found) > 0 {
		t.Fatalf("gas/database declares DBTX in %v", found)
	}
}

// Docs: modules/database.mdx#custom-connections
// "A connector is only accepted in sql mode; in pgx mode `Init` fails rather
// than ignore it."
func TestConnectorRejectedInPgxMode(t *testing.T) {
	t.Parallel()
	cfg := connectorConfig()
	cfg.Database.Mode = database.ModePgx
	cfg.Database.DSN = "postgres://claims@127.0.0.1:1/claims"
	conn := &fakeConnector{}
	err := build(t, nil, nil, database.WithConfig(cfg), database.WithConnector(conn)).Init(t.Context())
	if err == nil || !strings.HasPrefix(err.Error(), "gas/database: invalid configuration: ") {
		t.Fatalf("Init with a connector in pgx mode = %v, want an invalid configuration error", err)
	}
}

// Docs: modules/database.mdx#custom-connections
// "`Driver()` reports `database.driver` in sql mode, whether or not a connector
// is set, and `pgx` in pgx mode."
func TestDriverName(t *testing.T) {
	t.Parallel()
	cfg := connectorConfig()
	cfg.Database.Driver = "mysql"
	if got := build(t, nil, nil, database.WithConfig(cfg), database.WithConnector(&fakeConnector{})).Driver(); got != "mysql" {
		t.Errorf("sql mode with a connector: Driver() = %q, want mysql", got)
	}
	if got := build(t, nil, nil, database.WithConfig(sqliteConfig(t))).Driver(); got != "sqlite" {
		t.Errorf("sql mode: Driver() = %q, want sqlite", got)
	}
	pgxCfg := database.DefaultConfig()
	pgxCfg.Database.Mode = database.ModePgx
	pgxCfg.Database.Driver = "postgres"
	if got := build(t, nil, nil, database.WithConfig(pgxCfg)).Driver(); got != "pgx" {
		t.Errorf("pgx mode with driver postgres: Driver() = %q, want pgx", got)
	}
}

// Docs: modules/database.mdx#custom-connections
// "gas/migrate picks its SQL dialect from that name, so set `database.driver`
// to the dialect the connector speaks"
func TestMigrateDialectFromDriverName(t *testing.T) {
	t.Parallel()
	start := func(t *testing.T, driverName string) error {
		t.Helper()
		cfg := connectorConfig()
		cfg.Database.Driver = driverName
		svc := startService(t, cfg, nil, database.WithConnector(newSQLiteConnector(t)))
		m, err := migrate.New()(svc)
		if err != nil {
			t.Fatalf("migrate.New: %v", err)
		}
		t.Cleanup(func() { _ = m.Close(context.Background()) })
		return m.Init(t.Context())
	}
	if err := start(t, "sqlite"); err != nil {
		t.Errorf("migrate Init over a SQLite connector named sqlite: %v, want nil", err)
	}
	if err := start(t, "nosuch"); err == nil || !strings.Contains(err.Error(), `"nosuch"`) {
		t.Errorf("migrate Init over a connector named nosuch = %v, want an error naming the driver", err)
	}
}

// Docs: modules/database.mdx#lifecycle
// "`Close` marks the service closed and closes the `*sql.DB`"
// and "After `Close`, `Query`, `Exec`, `BeginTx`, `WithTx`, `BeginPgxTx`
// and `WithPgxTx` return an error without touching the connection."
// and
// Docs: modules/database.mdx#errors
// "| `gas/database: service is closed` | `Query`, `Exec`, `BeginTx`, `WithTx`, `BeginPgxTx` or `WithPgxTx` after `Close` |"
func TestClosedSQLMode(t *testing.T) {
	t.Parallel()
	svc := newSQLite(t)
	db := svc.DB()
	if err := svc.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := db.PingContext(t.Context()); err == nil {
		t.Error("the *sql.DB answers a ping after Close, want it closed")
	}
	for op, err := range closedOps(t, svc) {
		if err == nil || err.Error() != "gas/database: service is closed" {
			t.Errorf("%s after Close = %v, want exactly gas/database: service is closed", op, err)
		}
	}
}

// closedOps runs every operation the page lists on svc and returns its error.
func closedOps(t *testing.T, svc *database.Service) map[string]error {
	t.Helper()
	ctx := t.Context()
	errs := map[string]error{}
	_, errs["Query"] = svc.Query(ctx, "SELECT 1")
	_, errs["Exec"] = svc.Exec(ctx, "SELECT 1")
	_, errs["BeginTx"] = svc.BeginTx(ctx, nil)
	errs["WithTx"] = svc.WithTx(ctx, nil, func(*sql.Tx) error { return nil })
	_, errs["BeginPgxTx"] = svc.BeginPgxTx(ctx, nil)
	errs["WithPgxTx"] = svc.WithPgxTx(ctx, nil, nil)
	return errs
}

// Docs: modules/database.mdx#lifecycle
// "The service starts no goroutines of its own"
func TestNoGoroutinesOfItsOwn(t *testing.T) {
	t.Parallel()
	svc := newSQLite(t)
	if _, err := svc.Exec(t.Context(), "CREATE TABLE g (x INTEGER)"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	assertNoServiceGoroutines(t)
}

// assertNoServiceGoroutines fails when any goroutine was started from the
// gas/database package.
func assertNoServiceGoroutines(t *testing.T) {
	t.Helper()
	buf := make([]byte, 1<<22)
	stacks := string(buf[:runtime.Stack(buf, true)])
	for g := range strings.SplitSeq(stacks, "\n\n") {
		if strings.Contains(g, "created by github.com/gasmod/gas/database.") {
			t.Errorf("goroutine started by gas/database:\n%s", g)
		}
	}
}

// Docs: modules/database.mdx#errors
// "Every error from an operation starts with the service name, `gas/database`,
// and wraps the driver's error with `%w`, so `errors.Is` and `errors.As` reach
// it"
// and "| `gas/database: query: ...`, `gas/database: exec: ...` | `Query` or `Exec` failed |"
func TestQueryExecErrorsWrapDriverError(t *testing.T) {
	t.Parallel()
	svc := newSQLite(t)
	_, qErr := svc.Query(t.Context(), "SELECT * FROM no_such_table")
	_, eErr := svc.Exec(t.Context(), "INSERT INTO no_such_table VALUES (1)")
	for prefix, err := range map[string]error{"gas/database: query: ": qErr, "gas/database: exec: ": eErr} {
		if err == nil || !strings.HasPrefix(err.Error(), prefix) {
			t.Errorf("err = %v, want prefix %q", err, prefix)
			continue
		}
		var sqErr *sqlite.Error
		if !errors.As(err, &sqErr) {
			t.Errorf("err %v does not wrap the driver's *sqlite.Error", err)
		}
	}
}

// Docs: modules/database.mdx#errors
// "| `gas/database: open: ...`, `gas/database: pgxpool: ...`, `gas/database: ping: ...` | A connection attempt failed |"
func TestConnectionErrors(t *testing.T) {
	t.Parallel()

	t.Run("SQLPing", func(t *testing.T) {
		t.Parallel()
		conn := &fakeConnector{failConnect: true}
		err := build(t, nil, nil, database.WithConfig(connectorConfig()), database.WithConnector(conn)).Init(t.Context())
		if err == nil || !strings.HasPrefix(err.Error(), "gas/database: ping: ") || !errors.Is(err, errConnect) {
			t.Fatalf("Init with a failing connector = %v, want gas/database: ping: wrapping the driver error", err)
		}
	})
	t.Run("PgxPing", func(t *testing.T) {
		t.Parallel()
		cfg := database.DefaultConfig()
		cfg.Database.Mode = database.ModePgx
		cfg.Database.DSN = "postgres://claims@127.0.0.1:1/claims?connect_timeout=2"
		err := build(t, nil, nil, database.WithConfig(cfg)).Init(t.Context())
		if err == nil || !strings.HasPrefix(err.Error(), "gas/database: ping: ") {
			t.Fatalf("pgx Init against a closed port = %v, want a gas/database: ping: error", err)
		}
	})
}

// Docs: modules/database.mdx#errors
// "| `gas/database: failed to begin transaction: ...` | `BeginTx` or `WithTx` could not start a transaction |"
func TestBeginFailure(t *testing.T) {
	t.Parallel()
	conn := &fakeConnector{beginErr: errBegin}
	svc := startService(t, connectorConfig(), nil, database.WithConnector(conn))
	_, bErr := svc.BeginTx(t.Context(), nil)
	wErr := svc.WithTx(t.Context(), nil, func(*sql.Tx) error { return nil })
	for op, err := range map[string]error{"BeginTx": bErr, "WithTx": wErr} {
		if err == nil || !strings.HasPrefix(err.Error(), "gas/database: failed to begin transaction: ") || !errors.Is(err, errBegin) {
			t.Errorf("%s with a failing begin = %v, want gas/database: failed to begin transaction: wrapping the driver error", op, err)
		}
	}
}

// Docs: modules/database.mdx#errors
// "| `gas/database: not initialized` | `Query`, `Exec`, `BeginTx` or `WithTx` before `Init` |"
func TestNotInitialized(t *testing.T) {
	t.Parallel()
	svc := build(t, nil, nil, database.WithConfig(sqliteConfig(t)))
	ctx := t.Context()
	errs := map[string]error{}
	_, errs["Query"] = svc.Query(ctx, "SELECT 1")
	_, errs["Exec"] = svc.Exec(ctx, "SELECT 1")
	_, errs["BeginTx"] = svc.BeginTx(ctx, nil)
	errs["WithTx"] = svc.WithTx(ctx, nil, func(*sql.Tx) error { return nil })
	for op, err := range errs {
		if err == nil || err.Error() != "gas/database: not initialized" {
			t.Errorf("%s before Init = %v, want exactly gas/database: not initialized", op, err)
		}
	}
}

// Docs: modules/database.mdx#pgx-mode
// "both return an error outside pgx mode"
// and
// Docs: modules/database.mdx#errors
// "| `gas/database: attempt to begin pgx transaction in non-pgx mode ...` | `BeginPgxTx` or `WithPgxTx` in sql mode |"
func TestPgxTxInSQLMode(t *testing.T) {
	t.Parallel()
	svc := newSQLite(t)
	_, bErr := svc.BeginPgxTx(t.Context(), nil)
	wErr := svc.WithPgxTx(t.Context(), nil, nil)
	for op, err := range map[string]error{"BeginPgxTx": bErr, "WithPgxTx": wErr} {
		if err == nil || !strings.HasPrefix(err.Error(), "gas/database: attempt to begin pgx transaction in non-pgx mode") {
			t.Errorf("%s in sql mode = %v, want the non-pgx mode error", op, err)
		}
	}
}

// Docs: modules/database.mdx#errors
// "`CheckHealth` and `CheckReady` are the exception: they report a closed
// service as `database: closed`, and `CheckHealth` reports an uninitialized one
// as `database: not initialized`, without the `gas/` prefix (KI-043)."
func TestHealthMessages(t *testing.T) {
	t.Parallel()
	svc := build(t, nil, nil, database.WithConfig(sqliteConfig(t)))
	ctx := t.Context()
	if err := svc.CheckHealth(ctx); err == nil || err.Error() != "database: not initialized" {
		t.Errorf("CheckHealth before Init = %v, want database: not initialized", err)
	}
	if err := svc.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := svc.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := svc.CheckHealth(ctx); err == nil || err.Error() != "database: closed" {
		t.Errorf("CheckHealth after Close = %v, want database: closed", err)
	}
	if err := svc.CheckReady(ctx); err == nil || err.Error() != "database: closed" {
		t.Errorf("CheckReady after Close = %v, want database: closed", err)
	}
}

// Docs: modules/database.mdx#health-and-readiness
// "`CheckHealth` (liveness) fails only when the service is closed or not
// initialized, states a restart would fix. It does not ping"
// and "`CheckReady` pings the database with the caller's context, so while the
// database is unreachable the instance reports not ready"
func TestHealthAndReadiness(t *testing.T) {
	t.Parallel()
	conn := &fakeConnector{}
	svc := startService(t, connectorConfig(), nil, database.WithConnector(conn))
	ctx := t.Context()

	if err := svc.CheckHealth(ctx); err != nil {
		t.Errorf("CheckHealth when up: %v, want nil", err)
	}
	if err := svc.CheckReady(ctx); err != nil {
		t.Errorf("CheckReady when up: %v, want nil", err)
	}
	conn.pingFail.Store(true)
	if err := svc.CheckHealth(ctx); err != nil {
		t.Errorf("CheckHealth with a failing ping: %v, want nil", err)
	}
	if err := svc.CheckReady(ctx); !errors.Is(err, errPing) {
		t.Errorf("CheckReady with a failing ping = %v, want the ping error", err)
	}
	conn.pingFail.Store(false)

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := svc.CheckReady(canceled); !errors.Is(err, context.Canceled) {
		t.Errorf("CheckReady with a canceled context = %v, want context.Canceled", err)
	}
}

// Docs: modules/database.mdx#health-and-readiness
// "Both results appear, under the name `gas/database`, in the maps the app's
// `CheckHealth` and `CheckReady` return"
func TestHealthInAppMaps(t *testing.T) {
	t.Parallel()
	conn := &fakeConnector{}
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](jsonConfig(t, map[string]any{})),
		gas.WithServiceInstance[gas.Logger](gas.Logger(&gas.NopLogger{})),
		gas.WithSingletonService[gas.DatabaseProvider](database.New(database.WithConfig(connectorConfig()), database.WithConnector(conn))),
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	conn.pingFail.Store(true)
	health := app.CheckHealth(t.Context())
	ready := app.CheckReady(t.Context())
	if err, ok := health["gas/database"]; !ok || err != nil {
		t.Errorf("app CheckHealth = %v, want gas/database present and nil", health)
	}
	if err, ok := ready["gas/database"]; !ok || !errors.Is(err, errPing) {
		t.Errorf("app CheckReady = %v, want gas/database present with the ping error", ready)
	}
}

// Docs: modules/database.mdx#testing
// "There is no mock package for `gas.DatabaseProvider`."
func TestNoMockPackage(t *testing.T) {
	t.Parallel()
	var pkgs []string
	err := filepath.WalkDir(databaseDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") && filepath.Dir(path) != filepath.Clean(databaseDir) {
			pkgs = append(pkgs, filepath.Dir(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", databaseDir, err)
	}
	if len(pkgs) > 0 {
		t.Fatalf("gas/database has Go packages besides its root: %v", slices.Compact(pkgs))
	}
}

// Docs: modules/database.mdx#testing
// "Use a file DSN, not `:memory:`: with a pool, each connection to an in-memory
// SQLite database opens a separate, empty database."
func TestInMemorySQLitePerConnection(t *testing.T) {
	t.Parallel()
	cfg := sqliteConfig(t)
	cfg.Database.DSN = ":memory:"
	svc := startService(t, cfg, nil)
	ctx := t.Context()

	c1, err := svc.DB().Conn(ctx)
	if err != nil {
		t.Fatalf("conn 1: %v", err)
	}
	defer func() { _ = c1.Close() }()
	c2, err := svc.DB().Conn(ctx)
	if err != nil {
		t.Fatalf("conn 2: %v", err)
	}
	defer func() { _ = c2.Close() }()

	if _, err := c1.ExecContext(ctx, "CREATE TABLE mem (x INTEGER)"); err != nil {
		t.Fatalf("create table on conn 1: %v", err)
	}
	if _, err := c2.ExecContext(ctx, "SELECT * FROM mem"); err == nil {
		t.Fatal("conn 2 sees the table conn 1 created, want a separate empty database")
	}
}

// Docs: modules/database.mdx#errors
// "| `gas/database: unknown mode "..."` | `Init` with a `Mode` other than `sql`, `pgx` or empty, possible only through `WithConfig` |"
func TestUnknownMode(t *testing.T) {
	t.Parallel()
	cfg := sqliteConfig(t)
	cfg.Database.Mode = "bogus"
	err := build(t, nil, nil, database.WithConfig(cfg)).Init(t.Context())
	if err == nil || err.Error() != `gas/database: unknown mode "bogus"` {
		t.Errorf("Init with mode bogus through WithConfig = %v, want %q", err, `gas/database: unknown mode "bogus"`)
	}

	// Bound, the same mode fails the validate tag before Init reaches it.
	svc := build(t, jsonConfig(t, map[string]any{
		"database": map[string]any{"mode": "bogus", "driver": "sqlite", "dsn": sqliteDSN(t)},
	}), nil)
	if err := svc.Init(t.Context()); err == nil || !strings.HasPrefix(err.Error(), "gas/database: config binding: ") {
		t.Errorf("Init with a bound mode bogus = %v, want a config binding error", err)
	}

	// An empty mode is accepted and runs as sql.
	empty := sqliteConfig(t)
	empty.Database.Mode = ""
	if err := build(t, nil, nil, database.WithConfig(empty)).Init(t.Context()); err != nil {
		t.Errorf("Init with an empty mode = %v, want nil", err)
	}
}

// Docs: modules/database.mdx#errors
// "| `gas/database: parse pgx config: ...` | In pgx mode, the DSN could not be parsed |"
func TestParsePgxConfig(t *testing.T) {
	t.Parallel()
	cfg := database.DefaultConfig()
	cfg.Database.Mode = database.ModePgx
	cfg.Database.DSN = "postgres://claims@%zz/claims"
	err := build(t, nil, nil, database.WithConfig(cfg)).Init(t.Context())
	if err == nil || !strings.HasPrefix(err.Error(), "gas/database: parse pgx config: ") {
		t.Fatalf("pgx Init with an unparsable DSN = %v, want a gas/database: parse pgx config: error", err)
	}
}
