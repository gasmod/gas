package modulesdatabase_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/database"
)

// Docs: modules/database.mdx#what-it-is-for
// "`DB()` returns a `*sql.DB` in both modes; in pgx mode it is derived from the
// pool through pgx's `stdlib` adapter."
// The derivation is observed through the pool's acquire count: a query on
// DB() checks a connection out of the pool.
func TestDBInPgxModeUsesPool(t *testing.T) {
	t.Parallel()
	svc := newPgx(t, nil)
	pool := svc.Pool()
	before := pool.Stat().AcquireCount()
	var one int
	if err := svc.DB().QueryRowContext(t.Context(), "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("DB() SELECT 1 = %d, %v; want 1, nil", one, err)
	}
	if after := pool.Stat().AcquireCount(); after <= before {
		t.Fatalf("pool acquire count went %d -> %d across a DB() query, want it to grow", before, after)
	}
}

// Docs: modules/database.mdx#install
// "with no such driver imported, set `database.driver` to `pgx`."
func TestSQLModeWithPgxDriver(t *testing.T) {
	t.Parallel()
	cfg := database.DefaultConfig()
	cfg.Database.Driver = database.DriverPgx
	cfg.Database.DSN = postgresDSN(t)
	svc := startService(t, cfg, nil)
	if _, err := svc.Exec(t.Context(), "SELECT 1"); err != nil {
		t.Fatalf("Exec in sql mode with the pgx driver: %v", err)
	}
	if svc.Pool() != nil {
		t.Fatal("Pool() in sql mode is not nil")
	}
}

// Docs: modules/database.mdx#pgx-mode
// "Set `database.mode` to `pgx` and the service builds a `*pgxpool.Pool` from
// the DSN alone; `database.driver` is ignored."
// and
// Docs: modules/database.mdx#pgx-transactions-and-pool-limits
// "`maxopenconns`, `connmaxlifetime` and `connmaxidletime` are applied to the
// pgx pool. `maxidleconns` is not, because pgxpool manages idle connections
// itself."
// and
// Docs: modules/database.mdx#configuration
// "Maximum idle connections, 1 to 1000 and at most `maxopenconns`. Sql mode only"
func TestPgxModeSettings(t *testing.T) {
	t.Parallel()
	cfg := pgxConfig(t)
	cfg.Database.Driver = "no-such-driver"
	cfg.Database.MaxOpenConns = 7
	cfg.Database.ConnMaxLifetime = 10 * time.Minute
	cfg.Database.ConnMaxIdleTime = 3 * time.Minute
	// Out of range and above maxopenconns, which sql mode rejects.
	cfg.Database.MaxIdleConns = 5000
	pc := startService(t, cfg, nil).Pool().Config()
	if pc.MaxConns != 7 || pc.MaxConnLifetime != 10*time.Minute || pc.MaxConnIdleTime != 3*time.Minute {
		t.Errorf("pool MaxConns, MaxConnLifetime, MaxConnIdleTime = %d, %v, %v; want 7, 10m, 3m",
			pc.MaxConns, pc.MaxConnLifetime, pc.MaxConnIdleTime)
	}
	if pc.MinConns != 0 || pc.MinIdleConns != 0 {
		t.Errorf("pool MinConns, MinIdleConns = %d, %d; want pgxpool's defaults 0, 0", pc.MinConns, pc.MinIdleConns)
	}
}

// pageDSN is the DSN the page's pgx-mode example sets.
const pageDSN = "postgres://app@localhost:5432/app?sslmode=disable"

// envConfig is a config loaded from the environment provider, as the page's
// wiring loads it.
func envConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		t.Fatalf("load env config: %v", err)
	}
	return cfg
}

// Docs: modules/database.mdx#pgx-mode
// "With the environment provider, two variables select it:" and
// "DATABASE__MODE=pgx DATABASE__DSN=postgres://app@localhost:5432/app?sslmode=disable"
// The service binds its config with Bind over DefaultConfig in Init; this
// binds the same way without opening a connection.
func TestPgxModeEnvVarsBind(t *testing.T) {
	t.Setenv("DATABASE__MODE", "pgx")
	t.Setenv("DATABASE__DSN", pageDSN)
	got := database.DefaultConfig()
	if err := envConfig(t).Bind(got); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if got.Database.Mode != database.ModePgx || got.Database.DSN != pageDSN {
		t.Errorf("Mode, DSN = %q, %q; want pgx, %q", got.Database.Mode, got.Database.DSN, pageDSN)
	}
}

// Docs: modules/database.mdx#pgx-mode
// "With the environment provider, two variables select it:" and "The wiring is
// the same `database.New()` as in Wiring."
// The DSN points at the test container instead of the page's placeholder.
func TestPgxModeFromEnvVars(t *testing.T) {
	dsn := postgresDSN(t)
	t.Setenv("DATABASE__MODE", "pgx")
	t.Setenv("DATABASE__DSN", dsn)
	svc := build(t, envConfig(t), nil)
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	pool := svc.Pool()
	if pool == nil {
		t.Fatal("Pool() is nil, want the pgx pool selected by DATABASE__MODE=pgx")
	}
	if err := pool.Ping(t.Context()); err != nil {
		t.Fatalf("pool Ping: %v", err)
	}
	if got := pool.Config().ConnConfig.Database; got != "claims" {
		t.Errorf("pool database = %q, want claims from DATABASE__DSN", got)
	}
}

// fakeProvider is a gas.DatabaseProvider that is not this module's Service.
type fakeProvider struct{ gas.DatabaseProvider }

// Docs: modules/database.mdx#pgx-mode
// "`Pool()` on `*database.Service` returns it, or nil in sql mode. A service
// that holds only a `gas.DatabaseProvider` calls `database.PoolFrom(provider)`,
// which returns the pool and `true`, or `nil` and `false` when the provider is
// not pgx-backed"
func TestPoolAndPoolFrom(t *testing.T) {
	t.Parallel()
	sqlSvc := newSQLite(t)
	if sqlSvc.Pool() != nil {
		t.Error("Pool() in sql mode is not nil")
	}
	for name, p := range map[string]gas.DatabaseProvider{"sql mode": sqlSvc, "other provider": fakeProvider{}} {
		if pool, ok := database.PoolFrom(p); ok || pool != nil {
			t.Errorf("PoolFrom(%s) = %p, %v; want nil, false", name, pool, ok)
		}
	}

	pgxSvc := newPgx(t, nil)
	if pgxSvc.Pool() == nil {
		t.Fatal("Pool() in pgx mode is nil")
	}
	var provider gas.DatabaseProvider = pgxSvc
	if pool, ok := database.PoolFrom(provider); !ok || pool != pgxSvc.Pool() {
		t.Fatalf("PoolFrom(pgx service) = %p, %v; want %p, true", pool, ok, pgxSvc.Pool())
	}
}

// Docs: modules/database.mdx#pgx-transactions-and-pool-limits
// "Nil options use pgx's defaults."
// pgx's default TxOptions leave the isolation level to the server, which is
// read committed on a stock PostgreSQL.
func TestBeginPgxTxOptions(t *testing.T) {
	t.Parallel()
	svc := newPgx(t, nil)
	level := func(opts *pgx.TxOptions) string {
		tx, err := svc.BeginPgxTx(t.Context(), opts)
		if err != nil {
			t.Fatalf("BeginPgxTx(%+v): %v", opts, err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		var got string
		if err := tx.QueryRow(t.Context(), "SHOW transaction_isolation").Scan(&got); err != nil {
			t.Fatalf("SHOW transaction_isolation: %v", err)
		}
		return got
	}
	if got := level(nil); got != "read committed" {
		t.Errorf("isolation with nil options = %q, want read committed", got)
	}
	if got := level(&pgx.TxOptions{IsoLevel: pgx.Serializable}); got != "serializable" {
		t.Errorf("isolation with Serializable = %q, want serializable", got)
	}
}

// pgxTable creates a one-column table and returns functions to insert a value
// in a pgx transaction and to check whether a value is present.
func pgxTable(t *testing.T, svc *database.Service) (func(pgx.Tx, string), func(string) bool) {
	t.Helper()
	table := pgTable(t)
	if _, err := svc.Exec(t.Context(), "CREATE TABLE "+table+" (v TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	insert := func(tx pgx.Tx, v string) {
		if _, err := tx.Exec(context.Background(), "INSERT INTO "+table+" (v) VALUES ($1)", v); err != nil {
			t.Fatalf("insert %s: %v", v, err)
		}
	}
	present := func(v string) bool {
		var n int
		if err := svc.Pool().QueryRow(context.Background(), "SELECT COUNT(*) FROM "+table+" WHERE v = $1", v).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", v, err)
		}
		return n == 1
	}
	return insert, present
}

// Docs: modules/database.mdx#pgx-transactions-and-pool-limits
// "`BeginPgxTx` and `WithPgxTx` are the pgx counterparts of `BeginTx` and
// `WithTx`, on `pgx.Tx`."
// and "`WithPgxTx` follows the same commit, rollback, panic and
// already-finished rules as `WithTx` (here the finished error is
// `pgx.ErrTxClosed`)"
func TestWithPgxTxRules(t *testing.T) {
	t.Parallel()
	svc := newPgx(t, nil)
	insert, present := pgxTable(t, svc)
	ctx := t.Context()
	errFn := errors.New("fn failed")

	if err := svc.WithPgxTx(ctx, nil, func(tx pgx.Tx) error { insert(tx, "commit"); return nil }); err != nil {
		t.Fatalf("WithPgxTx returning nil: %v", err)
	}
	if !present("commit") {
		t.Error("row written by a fn that returned nil is absent, want committed")
	}

	err := svc.WithPgxTx(ctx, nil, func(tx pgx.Tx) error { insert(tx, "error"); return errFn })
	if err != errFn { //nolint:errorlint // the claim is that the fn error itself is returned
		t.Errorf("WithPgxTx err = %v, want the fn error itself", err)
	}
	if present("error") {
		t.Error("row written by a fn that returned an error is present, want rolled back")
	}

	want := &panicValue{msg: "boom"}
	got := catch(func() {
		_ = svc.WithPgxTx(ctx, nil, func(tx pgx.Tx) error { insert(tx, "panic"); panic(want) })
	})
	if got != want {
		t.Errorf("recovered %#v, want the original panic value %#v", got, want)
	}
	if present("panic") {
		t.Error("row written by a fn that panicked is present, want rolled back")
	}

	err = svc.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
		insert(tx, "self-committed")
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit inside fn: %v", err)
		}
		return errFn
	})
	if err != errFn { //nolint:errorlint // the claim is that nothing is joined onto the fn error
		t.Errorf("WithPgxTx after fn committed itself: err = %v, want the fn error alone", err)
	}
	if !present("self-committed") {
		t.Error("row committed by fn is absent")
	}
}

// breakConn closes the connection under tx so that a later rollback fails
// with an error other than pgx.ErrTxClosed.
func breakConn(t *testing.T, tx pgx.Tx) {
	t.Helper()
	if err := tx.Conn().Close(context.Background()); err != nil {
		t.Fatalf("close the transaction's connection: %v", err)
	}
}

// Docs: modules/database.mdx#pgx-transactions-and-pool-limits
// "`WithPgxTx` follows the same commit, rollback, panic and
// already-finished rules as `WithTx`"
// The rules taken from WithTx here are "A rollback that itself fails is joined
// onto it with `errors.Join`" and "A failed rollback is logged at error level
// instead of returned".
func TestWithPgxTxFailedRollback(t *testing.T) {
	t.Parallel()
	log := &recLogger{}
	svc := newPgx(t, log)
	errFn := errors.New("fn failed")

	err := svc.WithPgxTx(t.Context(), nil, func(tx pgx.Tx) error { breakConn(t, tx); return errFn })
	joined, ok := err.(interface{ Unwrap() []error })                    //nolint:errorlint // checking the shape errors.Join produces
	if !ok || len(joined.Unwrap()) != 2 || joined.Unwrap()[0] != errFn { //nolint:errorlint // identity of the first joined error
		t.Errorf("WithPgxTx with a failing rollback = %#v, want errors.Join(fn error, rollback error)", err)
	}

	want := &panicValue{msg: "boom"}
	got := catch(func() {
		_ = svc.WithPgxTx(t.Context(), nil, func(tx pgx.Tx) error { breakConn(t, tx); panic(want) })
	})
	if got != want {
		t.Errorf("recovered %#v, want the original panic value %#v", got, want)
	}
	if len(log.at("ERROR")) == 0 {
		t.Errorf("no error-level entry for the failed rollback after a panic; logged %q", log.lines())
	}
}

// Docs: modules/database.mdx#pgx-transactions-and-pool-limits
// "it rolls back on a context detached from cancellation, so the rollback
// still runs when the caller's context is already done;"
// Assumption: a rollback that ran leaves no rollback error joined onto the fn
// error, and the row unwritten.
func TestWithPgxTxRollbackDetached(t *testing.T) {
	t.Parallel()
	svc := newPgx(t, nil)
	insert, present := pgxTable(t, svc)
	ctx, cancel := context.WithCancel(t.Context())
	errFn := errors.New("fn failed")

	err := svc.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
		insert(tx, "x")
		cancel()
		return errFn
	})
	if err != errFn { //nolint:errorlint // a rollback failure would be joined onto the fn error
		t.Fatalf("WithPgxTx with the context canceled in fn = %v, want the fn error alone", err)
	}
	if present("x") {
		t.Fatal("row is present, want rolled back")
	}
}

// Docs: modules/database.mdx#pgx-transactions-and-pool-limits
// "it commits with the caller's context unchanged, so an already-canceled
// context fails the commit instead of persisting work the caller abandoned."
func TestWithPgxTxCommitUsesCallerContext(t *testing.T) {
	t.Parallel()
	svc := newPgx(t, nil)
	insert, present := pgxTable(t, svc)
	ctx, cancel := context.WithCancel(t.Context())

	err := svc.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
		insert(tx, "x")
		cancel()
		return nil
	})
	if err == nil || !strings.HasPrefix(err.Error(), "gas/database: failed to commit transaction: ") || !errors.Is(err, context.Canceled) {
		t.Fatalf("WithPgxTx with a canceled context = %v, want a wrapped commit failure from context.Canceled", err)
	}
	if present("x") {
		t.Fatal("row is present, want the work not persisted")
	}
}

// Docs: modules/database.mdx#lifecycle
// "`Close` marks the service closed and closes the `*sql.DB` and, in pgx mode,
// the pool. After `Close`, `Query`, `Exec`, `BeginTx`, `WithTx`, `BeginPgxTx`
// and `WithPgxTx` return an error without touching the connection."
func TestClosedPgxMode(t *testing.T) {
	t.Parallel()
	svc := newPgx(t, nil)
	db, pool := svc.DB(), svc.Pool()
	if err := svc.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := db.PingContext(t.Context()); err == nil {
		t.Error("the *sql.DB answers a ping after Close, want it closed")
	}
	if err := pool.Ping(t.Context()); err == nil {
		t.Error("the pool answers a ping after Close, want it closed")
	}
	for op, err := range closedOps(t, svc) {
		if err == nil || err.Error() != "gas/database: service is closed" {
			t.Errorf("%s after Close = %v, want exactly gas/database: service is closed", op, err)
		}
	}
}

// Docs: modules/database.mdx#lifecycle
// "The service starts no goroutines of its own"
func TestNoGoroutinesOfItsOwnPgx(t *testing.T) {
	t.Parallel()
	svc := newPgx(t, nil)
	if err := svc.WithPgxTx(t.Context(), nil, func(pgx.Tx) error { return nil }); err != nil {
		t.Fatalf("WithPgxTx: %v", err)
	}
	assertNoServiceGoroutines(t)
}

// sqlDBTX is the DBTX interface sqlc generates for its database/sql output.
type sqlDBTX interface {
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	PrepareContext(context.Context, string) (*sql.Stmt, error)
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

// pgxDBTX is the DBTX interface sqlc generates for its pgx/v5 output.
type pgxDBTX interface {
	Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error)
	Query(context.Context, string, ...interface{}) (pgx.Rows, error)
	QueryRow(context.Context, string, ...interface{}) pgx.Row
}

// Docs: modules/database.mdx#sqlc
// "In `database/sql` output it is satisfied by `*sql.DB` and `*sql.Tx`; in pgx
// output, by `*pgxpool.Pool` and `pgx.Tx`. Pass `DB()`, the pool from
// `PoolFrom`, or a transaction straight into the generated `New`."
func TestSqlcDBTX(t *testing.T) {
	t.Parallel()
	var (
		_ sqlDBTX = (*sql.DB)(nil)
		_ sqlDBTX = (*sql.Tx)(nil)
		_ pgxDBTX = (*pgxpool.Pool)(nil)
		_ pgxDBTX = pgx.Tx(nil)
	)
	svc := newPgx(t, nil)
	pool, ok := database.PoolFrom(svc)
	if !ok {
		t.Fatal("PoolFrom(pgx service) = false")
	}
	var one int
	for name, q := range map[string]pgxDBTX{"pool from PoolFrom": pool} {
		if err := q.QueryRow(t.Context(), "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Errorf("%s as DBTX: SELECT 1 = %d, %v", name, one, err)
		}
	}
	var sq sqlDBTX = svc.DB()
	if err := sq.QueryRowContext(t.Context(), "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Errorf("DB() as DBTX: SELECT 1 = %d, %v", one, err)
	}
}

// Docs: modules/database.mdx#errors
// "| `gas/database: failed to begin pgx transaction: ...` | `BeginPgxTx` or `WithPgxTx` could not start a transaction |"
// A canceled context keeps the pool from handing out a connection, so the
// transaction cannot start.
func TestBeginPgxTxFailure(t *testing.T) {
	t.Parallel()
	svc := newPgx(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, bErr := svc.BeginPgxTx(ctx, nil)
	ran := false
	wErr := svc.WithPgxTx(ctx, nil, func(pgx.Tx) error { ran = true; return nil })
	for op, err := range map[string]error{"BeginPgxTx": bErr, "WithPgxTx": wErr} {
		if err == nil || !strings.HasPrefix(err.Error(), "gas/database: failed to begin pgx transaction: ") || !errors.Is(err, context.Canceled) {
			t.Errorf("%s with a canceled context = %v, want gas/database: failed to begin pgx transaction: wrapping the cause", op, err)
		}
	}
	if ran {
		t.Error("WithPgxTx ran fn although the transaction did not start")
	}
}
