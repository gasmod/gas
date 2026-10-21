package modulestemplates_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	_ "github.com/go-sql-driver/mysql" // registers the "mysql" database/sql driver
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
	templatedb "github.com/gasmod/gas/template/db"
)

// sqlProvider is a gas.DatabaseProvider over a *sql.DB that reports the
// given driver name, so one database can stand in for several driver names.
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

// openSQL opens a *sql.DB and closes it at the end of the test.
func openSQL(t *testing.T, driver, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatalf("open %s: %v", driver, err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// sqlitePath is a fresh SQLite file.
func sqlitePath(t *testing.T) string {
	t.Helper()
	return "file:" + filepath.Join(t.TempDir(), "templates.db")
}

// sqliteProvider is a database.Service on a fresh SQLite file.
func sqliteProvider(t *testing.T, path string) *database.Service {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.Database.Driver = database.DriverSQLite
	cfg.Database.DSN = path
	cfg.Database.MaxOpenConns = 1
	cfg.Database.MaxIdleConns = 1
	svc, err := database.New(database.WithConfig(cfg))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("database Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

// newDBStore starts a db store and its migrations on p and runs them.
func newDBStore(t *testing.T, p gas.DatabaseProvider, opts ...templatedb.Option) *templatedb.Store {
	t.Helper()
	mig, err := migrate.New()(p)
	if err != nil {
		t.Fatalf("migrate.New: %v", err)
	}
	if err := mig.Init(t.Context()); err != nil {
		t.Fatalf("migrate Init: %v", err)
	}
	t.Cleanup(func() { _ = mig.Close(context.Background()) })
	s, err := templatedb.NewStore(opts...)(p, &gas.NopLogger{}, mig)
	if err != nil {
		t.Fatalf("templatedb.NewStore: %v", err)
	}
	if err := s.Init(t.Context()); err != nil {
		t.Fatalf("db store Init: %v", err)
	}
	if err := mig.RunPending(); err != nil {
		t.Fatalf("RunPending: %v", err)
	}
	return s
}

// The PostgreSQL and MySQL containers are shared by the package and
// terminated by TestMain.
var (
	pgOnce sync.Once
	pgCtr  *postgres.PostgresContainer
	pgErr  error
	myOnce sync.Once
	myCtr  *mysql.MySQLContainer
	myErr  error
	dbSeq  atomic.Int64
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

// postgresDB returns a *sql.DB, through the pgx driver, on a fresh database
// of the shared PostgreSQL container.
func postgresDB(t *testing.T) *sql.DB {
	t.Helper()
	requireDocker(t)
	ctx := context.Background()
	pgOnce.Do(func() {
		pgCtr, pgErr = postgres.Run(ctx, "postgres:16-alpine",
			postgres.WithDatabase("testdb"), postgres.WithUsername("testuser"), postgres.WithPassword("testpass"),
			postgres.BasicWaitStrategies())
	})
	if pgErr != nil {
		t.Fatalf("start postgres: %v", pgErr)
	}
	admin, err := pgCtr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	name := fmt.Sprintf("tpl_%d", dbSeq.Add(1))
	if _, err := openSQL(t, "pgx", admin).ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	host, _ := pgCtr.Host(ctx)
	port, _ := pgCtr.MappedPort(ctx, "5432/tcp")
	return openSQL(t, "pgx", fmt.Sprintf("postgres://testuser:testpass@%s:%s/%s?sslmode=disable", host, port.Port(), name))
}

// mysqlDB returns a *sql.DB on a fresh database of the shared MySQL
// container, with params as the DSN's query string.
func mysqlDB(t *testing.T, params string) *sql.DB {
	t.Helper()
	requireDocker(t)
	ctx := context.Background()
	myOnce.Do(func() {
		myCtr, myErr = mysql.Run(ctx,
			"mysql:8.4@sha256:0f51e63e537a1f176423fa03ef559aabe6fa4372a53160a44bc7e75da7d15364",
			mysql.WithDatabase("testdb"), mysql.WithUsername("root"), mysql.WithPassword("testpass"))
	})
	if myErr != nil {
		t.Fatalf("start mysql: %v", myErr)
	}
	admin, err := myCtr.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("mysql connection string: %v", err)
	}
	name := fmt.Sprintf("tpl_%d", dbSeq.Add(1))
	if _, err := openSQL(t, "mysql", admin).ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	return openSQL(t, "mysql", admin[:strings.LastIndex(admin, "/")+1]+name+"?"+params)
}

// brokenFS lists bad.html but fails to open it.
type brokenFS struct{ fstest.MapFS }

var errUnreadable = errors.New("unreadable")

func (b brokenFS) Open(name string) (fs.File, error) {
	if name == "bad.html" {
		return nil, errUnreadable
	}
	return b.MapFS.Open(name)
}

func (b brokenFS) ReadFile(name string) ([]byte, error) {
	if name == "bad.html" {
		return nil, errUnreadable
	}
	return b.MapFS.ReadFile(name)
}

func (b brokenFS) ReadDir(name string) ([]fs.DirEntry, error) { return b.MapFS.ReadDir(name) }
