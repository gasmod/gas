package modulesdatabase_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/postgres"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/database"
)

// The PostgreSQL tests share one container per test run, terminated by
// TestMain once every test has finished.
var (
	pgOnce      sync.Once
	pgDSN       string
	pgSetupErr  error
	pgTerminate func()
)

func TestMain(m *testing.M) {
	code := m.Run()
	if pgTerminate != nil {
		pgTerminate()
	}
	os.Exit(code)
}

// postgresDSN checks Docker, starts the shared PostgreSQL container on first
// use, and returns its DSN.
func postgresDSN(t *testing.T) string {
	t.Helper()
	requireDocker(t)
	pgOnce.Do(func() {
		ctx := context.Background()
		ctr, err := postgres.Run(ctx,
			"postgres:16-alpine",
			postgres.WithDatabase("claims"),
			postgres.WithUsername("claims"),
			postgres.WithPassword("claims"),
			postgres.BasicWaitStrategies(),
		)
		if err != nil {
			pgSetupErr = fmt.Errorf("start postgres: %w", err)
			return
		}
		pgTerminate = func() { _ = ctr.Terminate(context.Background()) }
		pgDSN, err = ctr.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			pgSetupErr = fmt.Errorf("postgres connection string: %w", err)
		}
	})
	if pgSetupErr != nil {
		t.Fatal(pgSetupErr)
	}
	return pgDSN
}

// pgTableSeq makes pgTable names unique across repeated runs (-count) in one
// process, since the PostgreSQL container is shared by every run.
var pgTableSeq atomic.Int64

// pgTable returns a table name unique to the test and to this run of it.
func pgTable(t *testing.T) string {
	t.Helper()
	r := strings.NewReplacer("/", "_", "-", "_")
	return strings.ToLower(fmt.Sprintf("t_%s_%d", r.Replace(t.Name()), pgTableSeq.Add(1)))
}

// build constructs a Service from opts without starting it, and closes it at
// the end of the test.
func build(t *testing.T, cfg gas.ConfigProvider, log gas.Logger, opts ...database.Option) *database.Service {
	t.Helper()
	if log == nil {
		log = &gas.NopLogger{}
	}
	svc, err := database.New(opts...)(cfg, log)
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

// startService builds a Service from cfg and opts and runs Init.
func startService(t *testing.T, cfg *database.Config, log gas.Logger, opts ...database.Option) *database.Service {
	t.Helper()
	svc := build(t, nil, log, append([]database.Option{database.WithConfig(cfg)}, opts...)...)
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("database Init: %v", err)
	}
	return svc
}

// sqliteDSN returns a DSN for a fresh SQLite file.
func sqliteDSN(t *testing.T) string {
	t.Helper()
	return "file:" + filepath.Join(t.TempDir(), "claims.db")
}

// sqliteConfig returns a ModeSQL config for a fresh SQLite file.
func sqliteConfig(t *testing.T) *database.Config {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.Database.Driver = database.DriverSQLite
	cfg.Database.DSN = sqliteDSN(t)
	return cfg
}

// newSQLite starts a ModeSQL service on a fresh SQLite file.
func newSQLite(t *testing.T) *database.Service {
	t.Helper()
	return startService(t, sqliteConfig(t), nil)
}

// pgxConfig returns a ModePgx config for the shared PostgreSQL container.
func pgxConfig(t *testing.T) *database.Config {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.Database.Mode = database.ModePgx
	cfg.Database.DSN = postgresDSN(t)
	return cfg
}

// newPgx starts a ModePgx service against the shared PostgreSQL container.
func newPgx(t *testing.T, log gas.Logger) *database.Service {
	t.Helper()
	return startService(t, pgxConfig(t), log)
}

// jsonConfig returns a loaded gas/config backed by a JSON document holding
// doc.
func jsonConfig(t *testing.T, doc map[string]any) *config.Config {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	cfg := config.New(config.WithProvider(providers.NewJSONProvider(
		providers.WithJSONFileFS(fstest.MapFS{"config.json": {Data: raw}}),
		providers.WithJSONFilePath("config.json"),
	)))
	if err := cfg.Load(); err != nil {
		t.Fatalf("config Load: %v", err)
	}
	return cfg
}

// countingConfig is a gas.ConfigProvider that counts calls to Bind and
// otherwise defers to the real config it wraps.
type countingConfig struct {
	gas.ConfigProvider
	binds atomic.Int32
}

func (c *countingConfig) Bind(dest any, options ...gas.BindOption) error {
	c.binds.Add(1)
	return c.ConfigProvider.Bind(dest, options...)
}

// recLogger is a gas.Logger that records every event it sends as
// "LEVEL message key=value ...".
type recLogger struct {
	mu      sync.Mutex
	entries []string
}

func (l *recLogger) event(level, msg string) gas.LogEvent {
	return &recEvent{l: l, line: level + " " + msg}
}

func (l *recLogger) Trace(msg string) gas.LogEvent { return l.event("TRACE", msg) }
func (l *recLogger) Debug(msg string) gas.LogEvent { return l.event("DEBUG", msg) }
func (l *recLogger) Info(msg string) gas.LogEvent  { return l.event("INFO", msg) }
func (l *recLogger) Warn(msg string) gas.LogEvent  { return l.event("WARN", msg) }
func (l *recLogger) Error(msg string) gas.LogEvent { return l.event("ERROR", msg) }
func (l *recLogger) With() gas.LoggerContext       { return &recContext{l: l} }
func (l *recLogger) Flush()                        {}

func (l *recLogger) lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.entries...)
}

// at returns the recorded lines logged at level.
func (l *recLogger) at(level string) []string {
	var out []string
	for _, line := range l.lines() {
		if strings.HasPrefix(line, level+" ") {
			out = append(out, line)
		}
	}
	return out
}

type recEvent struct {
	l    *recLogger
	line string
}

func (e *recEvent) add(k string, v any) gas.LogEvent {
	e.line += fmt.Sprintf(" %s=%v", k, v)
	return e
}

func (e *recEvent) Str(k, v string) gas.LogEvent                    { return e.add(k, v) }
func (e *recEvent) Int(k string, v int) gas.LogEvent                { return e.add(k, v) }
func (e *recEvent) Int64(k string, v int64) gas.LogEvent            { return e.add(k, v) }
func (e *recEvent) Float64(k string, v float64) gas.LogEvent        { return e.add(k, v) }
func (e *recEvent) Bool(k string, v bool) gas.LogEvent              { return e.add(k, v) }
func (e *recEvent) Err(k string, v error) gas.LogEvent              { return e.add(k, v) }
func (e *recEvent) Duration(k string, v time.Duration) gas.LogEvent { return e.add(k, v) }
func (e *recEvent) Any(k string, v any) gas.LogEvent                { return e.add(k, v) }
func (e *recEvent) Send() {
	e.l.mu.Lock()
	defer e.l.mu.Unlock()
	e.l.entries = append(e.l.entries, e.line)
}

type recContext struct{ l *recLogger }

func (c *recContext) Str(string, string) gas.LoggerContext             { return c }
func (c *recContext) Int(string, int) gas.LoggerContext                { return c }
func (c *recContext) Int64(string, int64) gas.LoggerContext            { return c }
func (c *recContext) Float64(string, float64) gas.LoggerContext        { return c }
func (c *recContext) Bool(string, bool) gas.LoggerContext              { return c }
func (c *recContext) Err(string, error) gas.LoggerContext              { return c }
func (c *recContext) Duration(string, time.Duration) gas.LoggerContext { return c }
func (c *recContext) Any(string, any) gas.LoggerContext                { return c }
func (c *recContext) Logger() gas.Logger                               { return c.l }

// Errors the fake driver returns.
var (
	errConnect  = errors.New("fake: connect refused")
	errPing     = errors.New("fake: ping failed")
	errBegin    = errors.New("fake: begin failed")
	errCommit   = errors.New("fake: commit failed")
	errRollback = errors.New("fake: rollback failed")
)

// fakeConnector is a driver.Connector whose connect, ping, begin, commit,
// and rollback outcomes the test controls, and which records what it was
// asked to do. It lets the claims be tested without a database server.
type fakeConnector struct {
	failConnect bool
	beginErr    error
	commitErr   error
	rollbackErr error
	pingFail    atomic.Bool
	connects    atomic.Int32

	mu sync.Mutex
	// connectAt is when each Connect call happened.
	connectAt []time.Time
	// remaining is the time left on each Connect call's context deadline,
	// or -1 when the context had none.
	remaining []time.Duration
	// txOpts holds the options of each BeginTx call.
	txOpts []driver.TxOptions
}

func (c *fakeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	c.connects.Add(1)
	c.mu.Lock()
	c.connectAt = append(c.connectAt, time.Now())
	left := time.Duration(-1)
	if dl, ok := ctx.Deadline(); ok {
		left = time.Until(dl)
	}
	c.remaining = append(c.remaining, left)
	c.mu.Unlock()
	if c.failConnect {
		return nil, errConnect
	}
	return &fakeConn{c: c}, nil
}

func (c *fakeConnector) Driver() driver.Driver { return fakeDriver{} }

func (c *fakeConnector) record() ([]time.Time, []time.Duration, []driver.TxOptions) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Time(nil), c.connectAt...),
		append([]time.Duration(nil), c.remaining...),
		append([]driver.TxOptions(nil), c.txOpts...)
}

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fake: use the connector")
}

type fakeConn struct{ c *fakeConnector }

func (*fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("fake: no statements") }
func (*fakeConn) Close() error                        { return nil }
func (f *fakeConn) Begin() (driver.Tx, error) {
	return f.BeginTx(context.Background(), driver.TxOptions{})
}

func (f *fakeConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	f.c.mu.Lock()
	f.c.txOpts = append(f.c.txOpts, opts)
	f.c.mu.Unlock()
	if f.c.beginErr != nil {
		return nil, f.c.beginErr
	}
	return &fakeTx{c: f.c}, nil
}

func (f *fakeConn) Ping(context.Context) error {
	if f.c.pingFail.Load() {
		return errPing
	}
	return nil
}

type fakeTx struct{ c *fakeConnector }

func (t *fakeTx) Commit() error   { return t.c.commitErr }
func (t *fakeTx) Rollback() error { return t.c.rollbackErr }

// connectorConfig returns a ModeSQL config with no driver or DSN, for use
// with WithConnector.
func connectorConfig() *database.Config {
	cfg := database.DefaultConfig()
	cfg.Database.Driver = ""
	cfg.Database.DSN = ""
	return cfg
}

// sqliteConnector is a driver.Connector that opens SQLite connections, so a
// connector-backed service can run real SQL.
type sqliteConnector struct {
	drv driver.Driver
	dsn string
}

func newSQLiteConnector(t *testing.T) *sqliteConnector {
	t.Helper()
	db, err := sql.Open("sqlite", sqliteDSN(t))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	drv := db.Driver()
	_ = db.Close()
	return &sqliteConnector{drv: drv, dsn: sqliteDSN(t)}
}

func (c *sqliteConnector) Connect(context.Context) (driver.Conn, error) { return c.drv.Open(c.dsn) }
func (c *sqliteConnector) Driver() driver.Driver                        { return c.drv }

// panicValue is a distinct type so a test can check that the recovered value
// is the one the function panicked with, unchanged.
type panicValue struct{ msg string }

// catch runs fn and returns what it panicked with, or nil.
func catch(fn func()) (p any) {
	defer func() { p = recover() }()
	fn()
	return nil
}
