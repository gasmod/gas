package guidesdatabase_test

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
	"github.com/gasmod/gas/database"
)

// mysqlImage is the MySQL release the MySQL claims run against, pinned by
// digest so a retagged image cannot change the behavior under test.
const mysqlImage = "mysql:8.4@sha256:0f51e63e537a1f176423fa03ef559aabe6fa4372a53160a44bc7e75da7d15364"

// The MySQL tests share one container per test run; each test gets its own
// database on it. TestMain terminates it once every test has finished.
var (
	myOnce      sync.Once
	myCtr       *mysql.MySQLContainer
	mySetupErr  error
	mySeq       atomic.Int64
	myTerminate = func() {}
)

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

// mysqlTableExists reports whether a table exists in the connection's
// current MySQL database.
func mysqlTableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	err := db.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", name).Scan(&n)
	if err != nil {
		t.Fatalf("look up table %s: %v", name, err)
	}
	return n == 1
}

// sqlProvider is a minimal gas.DatabaseProvider over a *sql.DB that reports
// the given driver name: the "gas.DatabaseProvider of your own" route to
// MySQL.
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

// openMySQLProvider opens dsn with go-sql-driver and wraps it as a provider
// whose Driver() is "mysql".
func openMySQLProvider(t *testing.T, dsn string) *sqlProvider {
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

// openMySQLConnector builds a gas/database service in ModeSQL over a
// go-sql-driver/mysql Connector for dsn, with Database.Driver "mysql".
func openMySQLConnector(t *testing.T, dsn string) *database.Service {
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
	return startService(t, cfg, nil, database.WithConnector(conn))
}

// Docs: guides/database.mdx#choosing-a-mode
// "| `ModeSQL` (default) | `database/sql` | PostgreSQL or SQLite. `Database.Driver` must be `postgres`, `pgx`, or `sqlite` unless a connector is supplied with `WithConnector` |"
func TestModeSQLDriverNames(t *testing.T) {
	t.Parallel()

	t.Run("accepted without a connector", func(t *testing.T) {
		t.Parallel()
		for _, driver := range []string{database.DriverPostgres, database.DriverPgx, database.DriverSQLite} {
			cfg := database.DefaultConfig()
			cfg.Database.Driver = driver
			cfg.Database.DSN = "unused"
			if _, err := database.New(database.WithConfig(cfg))(nil, &gas.NopLogger{}); err != nil {
				t.Errorf("database.New with Driver %q: %v, want nil", driver, err)
			}
		}
	})

	t.Run("mysql without a connector is rejected", func(t *testing.T) {
		t.Parallel()
		cfg := database.DefaultConfig()
		cfg.Database.Driver = "mysql"
		cfg.Database.DSN = "root:pw@tcp(127.0.0.1:1)/x"
		svc, err := database.New(database.WithConfig(cfg))(nil, &gas.NopLogger{})
		if err == nil {
			err = svc.Init(t.Context())
			_ = svc.Close(context.Background())
		}
		if err == nil {
			t.Fatal("ModeSQL with Driver mysql and no connector: err = nil, want an error")
		}
	})

	t.Run("mysql with WithConnector on MySQL 8.4", func(t *testing.T) {
		t.Parallel()
		svc := openMySQLConnector(t, mysqlDSN(t, "multiStatements=true&parseTime=true"))
		if got := svc.Driver(); got != "mysql" {
			t.Errorf("Driver() = %q, want %q", got, "mysql")
		}
		var one int
		if err := svc.DB().QueryRowContext(t.Context(), "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("SELECT 1 through DB() = %d, %v; want 1, nil", one, err)
		}
		if _, err := svc.Exec(t.Context(), "CREATE TABLE notes (id INT PRIMARY KEY)"); err != nil {
			t.Fatalf("Exec CREATE TABLE: %v", err)
		}
		err := svc.WithTx(t.Context(), nil, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(t.Context(), "INSERT INTO notes (id) VALUES (1)")
			return err
		})
		if err != nil {
			t.Fatalf("WithTx insert: %v", err)
		}
		var n int
		if err := svc.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM notes").Scan(&n); err != nil || n != 1 {
			t.Fatalf("rows after committed WithTx = %d, %v; want 1, nil", n, err)
		}
	})
}

// Docs: guides/database.mdx#migrations-belong-to-services
// "On MySQL, reached through `WithConnector` or a `gas.DatabaseProvider` of
// your own, DDL statements such as `CREATE TABLE` commit implicitly, so a
// migration containing DDL is not atomic with its tracking row there."
//
// The migration below creates a table and then fails. Were it atomic with
// its tracking row, the table would be rolled back with it; on MySQL the
// table must survive the failure. It runs on both routes the sentence names.
// multiStatements=true lets the driver run the two statements of one
// migration, a precondition checked first.
func TestMySQLDDLMigrationNotAtomic(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		open func(t *testing.T, dsn string) gas.DatabaseProvider
	}{
		{"WithConnector", func(t *testing.T, dsn string) gas.DatabaseProvider { return openMySQLConnector(t, dsn) }},
		{"own provider", func(t *testing.T, dsn string) gas.DatabaseProvider { return openMySQLProvider(t, dsn) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := tc.open(t, mysqlDSN(t, "multiStatements=true"))
			db := p.DB()

			if _, err := db.ExecContext(t.Context(), "CREATE TABLE pre1 (x INT); CREATE TABLE pre2 (x INT)"); err != nil || !mysqlTableExists(t, db, "pre2") {
				t.Fatalf("multi-statement Exec unsupported by the MySQL driver: %v", err)
			}

			m := newMigrator(t, p)
			m.Register(&owner{name: "ddl"}, gas.Migration{
				Version:     "001",
				Description: "half",
				Up:          "CREATE TABLE half (x INT); INSERT INTO missing_table VALUES (1)",
				Down:        "DROP TABLE half",
			})
			if err := m.RunPending(); err == nil {
				t.Fatal("RunPending with a failing migration: err = nil, want an error")
			}
			if !mysqlTableExists(t, db, "half") {
				t.Error("table created by the failed DDL migration is absent, want it committed implicitly (not atomic with the tracking row)")
			}
		})
	}
}
