package modulesauth_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql" // also registers the "mysql" database/sql driver
	"github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/database"
)

// mysqlImage is the MySQL release the MySQL claims run against, pinned by
// digest so a retagged image cannot change the behavior under test.
const mysqlImage = "mysql:8.4@sha256:0f51e63e537a1f176423fa03ef559aabe6fa4372a53160a44bc7e75da7d15364"

var (
	myOnce sync.Once
	myCtr  *mysql.MySQLContainer
	myErr  error
	mySeq  atomic.Int64
)

// mysqlDSN starts the MySQL container shared by the package on first use,
// creates a fresh database on it, and returns a plain go-sql-driver DSN for
// that database, with params appended as its query string when set.
func mysqlDSN(t *testing.T, params string) string {
	t.Helper()
	requireDocker(t)
	ctx := context.Background()
	myOnce.Do(func() {
		myCtr, myErr = mysql.Run(ctx, mysqlImage,
			mysql.WithDatabase("testdb"),
			mysql.WithUsername("root"),
			mysql.WithPassword("testpass"),
		)
	})
	if myErr != nil {
		t.Fatalf("start mysql: %v", myErr)
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
	dsn := admin[:strings.LastIndex(admin, "/")+1] + name
	if params != "" {
		dsn += "?" + params
	}
	return dsn
}

// sqlProvider is a minimal gas.DatabaseProvider over a *sql.DB that reports
// the given driver name: the "your own provider" route to MySQL.
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

// openMySQLConnector builds the gas/database route to MySQL: ModeSQL over a
// go-sql-driver/mysql Connector for dsn, with Database.Driver "mysql".
func openMySQLConnector(t *testing.T, dsn string) gas.DatabaseProvider {
	t.Helper()
	mcfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse mysql DSN: %v", err)
	}
	conn, err := mysqldriver.NewConnector(mcfg)
	if err != nil {
		t.Fatalf("mysql connector: %v", err)
	}
	cfg := database.DefaultConfig()
	cfg.Database.Mode = database.ModeSQL
	cfg.Database.Driver = "mysql"
	cfg.Database.DSN = ""
	svc, err := database.New(database.WithConfig(cfg), database.WithConnector(conn))(&configtest.MockConfig{}, nopLogger())
	if err != nil {
		t.Fatalf("database.New with WithConnector: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("database Init with WithConnector: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

// openMySQLOwnProvider builds the "your own provider" route to MySQL.
func openMySQLOwnProvider(t *testing.T, dsn string) gas.DatabaseProvider {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatalf("ping mysql: %v", err)
	}
	return &sqlProvider{db: db, driver: "mysql"}
}

// mysqlRoutes are the two routes the page names for MySQL.
var mysqlRoutes = []struct {
	name string
	open func(t *testing.T, dsn string) gas.DatabaseProvider
}{
	{"WithConnector", openMySQLConnector},
	{"own provider", openMySQLOwnProvider},
}

// ki044 is the skip message for the apikey store on MySQL.
const ki044 = "KI-044: auth apikey migration fails on MySQL; see KNOWN_ISSUES.md"

// mysqlParams is the DSN query the page requires for the stores on MySQL.
const mysqlParams = "multiStatements=true&parseTime=true"
