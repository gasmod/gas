package guidesdatabase_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/postgres"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
)

// The PostgreSQL tests share one container per test run. TestMain terminates
// it, and the MySQL one, once every test has finished.
var (
	pgOnce      sync.Once
	pgDSN       string
	pgSetupErr  error
	pgTerminate func()
)

func TestMain(m *testing.M) {
	m.Run()
	if pgTerminate != nil {
		pgTerminate()
	}
	myTerminate()
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

// startService builds a database.Service from cfg and opts, runs Init, and
// closes it at the end of the test.
func startService(t *testing.T, cfg *database.Config, log gas.Logger, opts ...database.Option) *database.Service {
	t.Helper()
	if log == nil {
		log = &gas.NopLogger{}
	}
	opts = append([]database.Option{database.WithConfig(cfg)}, opts...)
	svc, err := database.New(opts...)(nil, log)
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("database Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

// sqliteConfig returns a ModeSQL config for a fresh SQLite file.
func sqliteConfig(t *testing.T) *database.Config {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.Database.Driver = database.DriverSQLite
	cfg.Database.DSN = "file:" + filepath.Join(t.TempDir(), "claims.db")
	return cfg
}

// newSQLite starts a ModeSQL service on a fresh SQLite file.
func newSQLite(t *testing.T) *database.Service {
	t.Helper()
	return startService(t, sqliteConfig(t), nil)
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

// tableExists reports whether an SQLite table exists.
func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	err := db.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&n)
	if err != nil {
		t.Fatalf("look up table %s: %v", name, err)
	}
	return n == 1
}

// count returns the number of rows in an SQLite table.
func count(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// newMigrator builds and starts a migrate.Service over db.
func newMigrator(t *testing.T, db gas.DatabaseProvider) *migrate.Service {
	t.Helper()
	m, err := migrate.New()(db)
	if err != nil {
		t.Fatalf("migrate.New: %v", err)
	}
	if err := m.Init(t.Context()); err != nil {
		t.Fatalf("migrate Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	return m
}

// owner is a minimal gas.Service that owns migrations.
type owner struct{ name string }

func (o *owner) Name() string                { return o.name }
func (o *owner) Init(context.Context) error  { return nil }
func (o *owner) Close(context.Context) error { return nil }

// recLogger is a gas.Logger that records every event it sends.
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
	errCommit   = errors.New("fake: commit failed")
	errRollback = errors.New("fake: rollback failed")
)

// fakeConnector is a driver.Connector whose connect, ping, commit, and
// rollback outcomes the test controls. It lets transaction and health claims
// be tested without a real database.
type fakeConnector struct {
	failConnect bool
	commitErr   error
	rollbackErr error
	pingFail    atomic.Bool
	connects    atomic.Int32
}

func (c *fakeConnector) Connect(context.Context) (driver.Conn, error) {
	c.connects.Add(1)
	if c.failConnect {
		return nil, errConnect
	}
	return &fakeConn{c: c}, nil
}

func (c *fakeConnector) Driver() driver.Driver { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fake: use the connector")
}

type fakeConn struct{ c *fakeConnector }

func (*fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("fake: no statements") }
func (*fakeConn) Close() error                        { return nil }
func (f *fakeConn) Begin() (driver.Tx, error)         { return &fakeTx{c: f.c}, nil }
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
