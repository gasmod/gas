package modulesmigrations_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
)

// The PostgreSQL and MySQL tests each share one container per test run,
// terminated by TestMain once every test has finished.
var (
	pgOnce      sync.Once
	pgDSN       string
	pgSetupErr  error
	pgTerminate = func() {}

	myOnce      sync.Once
	myCtr       *mysql.MySQLContainer
	mySetupErr  error
	mySeq       atomic.Int64
	myTerminate = func() {}
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgTerminate()
	myTerminate()
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

// mysqlImage is the MySQL release the MySQL claims run against, pinned by
// digest so a retagged image cannot change the behavior under test.
const mysqlImage = "mysql:8.4@sha256:0f51e63e537a1f176423fa03ef559aabe6fa4372a53160a44bc7e75da7d15364"

// mysqlDSN checks Docker, starts the shared MySQL container on first use,
// creates a fresh database, and returns a go-sql-driver DSN for it with the
// given query parameters appended.
func mysqlDSN(t *testing.T, params string) string {
	t.Helper()
	requireDocker(t)
	ctx := context.Background()
	myOnce.Do(func() {
		myCtr, mySetupErr = mysql.Run(ctx, mysqlImage,
			mysql.WithDatabase("claims"),
			mysql.WithUsername("root"),
			mysql.WithPassword("claims"),
		)
		if mySetupErr != nil {
			mySetupErr = fmt.Errorf("start mysql: %w", mySetupErr)
			return
		}
		ctr := myCtr
		myTerminate = func() { _ = ctr.Terminate(context.Background()) }
	})
	if mySetupErr != nil {
		t.Fatal(mySetupErr)
	}
	admin, err := myCtr.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("mysql connection string: %v", err)
	}
	adminDB, err := sql.Open("mysql", admin)
	if err != nil {
		t.Fatalf("open mysql admin connection: %v", err)
	}
	defer func() { _ = adminDB.Close() }()
	name := fmt.Sprintf("claims_%d", mySeq.Add(1))
	if _, err := adminDB.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	// The admin DSN ends in "/claims" plus any parameters; point it at the
	// fresh database instead.
	base := admin[:strings.LastIndex(admin, "/")+1] + name
	if params != "" {
		base += "?" + params
	}
	return base
}

// sqlProvider is a minimal gas.DatabaseProvider over a *sql.DB that reports
// the given driver name.
type sqlProvider struct {
	db     *sql.DB
	driver string
}

func (p *sqlProvider) DB() *sql.DB                    { return p.db }
func (p *sqlProvider) Driver() string                 { return p.driver }
func (p *sqlProvider) Ping(ctx context.Context) error { return p.db.PingContext(ctx) }
func (p *sqlProvider) Query(ctx context.Context, q string, args ...any) (gas.Rows, error) {
	return p.db.QueryContext(ctx, q, args...)
}

func (p *sqlProvider) Exec(ctx context.Context, q string, args ...any) (gas.Result, error) {
	return p.db.ExecContext(ctx, q, args...)
}

func (p *sqlProvider) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return p.db.BeginTx(ctx, opts)
}

func (p *sqlProvider) WithTx(ctx context.Context, opts *sql.TxOptions, fn func(*sql.Tx) error) error {
	tx, err := p.db.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// openProvider opens dsn with the named database/sql driver and wraps it as
// a provider whose Driver() reports reported.
func openProvider(t *testing.T, driverName, dsn, reported string) *sqlProvider {
	t.Helper()
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		t.Fatalf("open %s: %v", driverName, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatalf("ping %s: %v", driverName, err)
	}
	return &sqlProvider{db: db, driver: reported}
}

// sqliteDSN returns a DSN for a fresh SQLite file.
func sqliteDSN(t *testing.T) string {
	t.Helper()
	return "file:" + filepath.Join(t.TempDir(), "claims.db")
}

// sqliteConfig returns a gas/database config for a fresh SQLite file.
func sqliteConfig(t *testing.T) *database.Config {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.Database.Driver = database.DriverSQLite
	cfg.Database.DSN = sqliteDSN(t)
	return cfg
}

// newSQLite starts a gas/database service on a fresh SQLite file, with the
// runlog and downlog tables the logging migrations write to.
func newSQLite(t *testing.T) *database.Service {
	t.Helper()
	svc, err := database.New(database.WithConfig(sqliteConfig(t)))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("database Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	mustExec(t, svc.DB(), "CREATE TABLE runlog (seq INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)")
	mustExec(t, svc.DB(), "CREATE TABLE downlog (seq INTEGER PRIMARY KEY AUTOINCREMENT, v TEXT)")
	return svc
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), q); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// buildMigrator constructs a migrate.Service over db without starting it, and
// closes it at the end of the test.
func buildMigrator(t *testing.T, db gas.DatabaseProvider) *migrate.Service {
	t.Helper()
	m, err := migrate.New()(db)
	if err != nil {
		t.Fatalf("migrate.New: %v", err)
	}
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	return m
}

// newMigrator builds and starts a migrate.Service over db.
func newMigrator(t *testing.T, db gas.DatabaseProvider) *migrate.Service {
	t.Helper()
	m := buildMigrator(t, db)
	if err := m.Init(t.Context()); err != nil {
		t.Fatalf("migrate Init: %v", err)
	}
	return m
}

// setup starts a SQLite database and a migrator over it.
func setup(t *testing.T) (*database.Service, *migrate.Service) {
	t.Helper()
	db := newSQLite(t)
	return db, newMigrator(t, db)
}

// owner is a minimal gas.Service that owns migrations.
type owner struct{ name string }

func (o *owner) Name() string                { return o.name }
func (o *owner) Init(context.Context) error  { return nil }
func (o *owner) Close(context.Context) error { return nil }

// logMig is a migration whose up SQL appends its version to runlog and whose
// down SQL appends it to downlog, so tests can see what ran and in which
// order.
func logMig(version string) gas.Migration {
	return gas.Migration{
		Version:     version,
		Description: "log " + version,
		Up:          "INSERT INTO runlog (v) VALUES ('" + version + "')",
		Down:        "INSERT INTO downlog (v) VALUES ('" + version + "')",
	}
}

// logged returns the versions in runlog or downlog, in the order written.
func logged(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT v FROM "+table+" ORDER BY seq")
	if err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s: %v", table, err)
	}
	return out
}

// trackRow is one __gas_migrations row.
type trackRow struct {
	service, description, migrateVersion, moduleVersion, appliedAt string
	dirty                                                          bool
}

// tracking returns the SQLite __gas_migrations rows by version.
func tracking(t *testing.T, db *sql.DB) map[string]trackRow {
	t.Helper()
	rows, err := db.QueryContext(t.Context(),
		"SELECT version, service, description, migrate_version, module_version, applied_at, dirty FROM __gas_migrations")
	if err != nil {
		t.Fatalf("read __gas_migrations: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]trackRow{}
	for rows.Next() {
		var v string
		var r trackRow
		if err := rows.Scan(&v, &r.service, &r.description, &r.migrateVersion, &r.moduleVersion, &r.appliedAt, &r.dirty); err != nil {
			t.Fatalf("scan __gas_migrations: %v", err)
		}
		out[v] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read __gas_migrations: %v", err)
	}
	return out
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

// recLogger is a gas.Logger that records each message it sends.
type recLogger struct {
	gas.NopLogger
	mu   sync.Mutex
	msgs []string
}

func (l *recLogger) add(msg string) gas.LogEvent {
	l.mu.Lock()
	l.msgs = append(l.msgs, msg)
	l.mu.Unlock()
	return l.NopLogger.Info(msg)
}

func (l *recLogger) Trace(msg string) gas.LogEvent { return l.add(msg) }
func (l *recLogger) Debug(msg string) gas.LogEvent { return l.add(msg) }
func (l *recLogger) Info(msg string) gas.LogEvent  { return l.add(msg) }
func (l *recLogger) Warn(msg string) gas.LogEvent  { return l.add(msg) }
func (l *recLogger) Error(msg string) gas.LogEvent { return l.add(msg) }
func (l *recLogger) With() gas.LoggerContext       { return recLoggerContext{l: l} }

func (l *recLogger) messages() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.msgs...)
}

// recLoggerContext keeps sub-loggers recording into the same recLogger.
type recLoggerContext struct{ l *recLogger }

func (c recLoggerContext) Str(string, string) gas.LoggerContext             { return c }
func (c recLoggerContext) Int(string, int) gas.LoggerContext                { return c }
func (c recLoggerContext) Int64(string, int64) gas.LoggerContext            { return c }
func (c recLoggerContext) Float64(string, float64) gas.LoggerContext        { return c }
func (c recLoggerContext) Bool(string, bool) gas.LoggerContext              { return c }
func (c recLoggerContext) Err(string, error) gas.LoggerContext              { return c }
func (c recLoggerContext) Duration(string, time.Duration) gas.LoggerContext { return c }
func (c recLoggerContext) Any(string, any) gas.LoggerContext                { return c }
func (c recLoggerContext) Logger() gas.Logger                               { return c.l }
