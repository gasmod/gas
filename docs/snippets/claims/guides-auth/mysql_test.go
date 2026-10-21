package guidesauth_test

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
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/session"
	"github.com/gasmod/gas/auth/token"
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

// Docs: guides/auth.mdx#databases
// "They also carry a MySQL dialect, chosen when `Driver()` returns `mysql`.
// MySQL needs a `gas.DatabaseProvider` whose `Driver()` is `mysql`:
// `gas/database` with `WithConnector` and `Database.Driver` set to `mysql`, or
// your own provider." and "With both, `session` and `token` work on MySQL 8.4"
//
// The apikey store is skipped under KI-044; its current failure is
// TestAPIKeyMigrationFailsOnMySQL.
func TestStoresSupportMySQL(t *testing.T) {
	t.Parallel()
	for _, route := range mysqlRoutes {
		t.Run(route.name, func(t *testing.T) {
			t.Parallel()
			checkStores(t, func(t *testing.T) gas.DatabaseProvider {
				return route.open(t, mysqlDSN(t, mysqlParams))
			}, map[string]string{"apikey": ki044})
		})
	}
}

// Docs: guides/auth.mdx#databases
// "Their migrations hold several statements, so the MySQL DSN needs
// `multiStatements=true`"
//
// Without the parameter (parseTime=true still set), each store's migrations
// fail on both routes.
func TestStoreMigrationsNeedMultiStatements(t *testing.T) {
	t.Parallel()
	for _, route := range mysqlRoutes {
		t.Run(route.name, func(t *testing.T) {
			t.Parallel()
			for _, store := range []string{"session", "apikey", "token"} {
				db := route.open(t, mysqlDSN(t, "parseTime=true"))
				if err := runStoreMigrations(t, store, db); err == nil {
					t.Errorf("%s migrations on a DSN without multiStatements=true: err = nil, want an error", store)
				}
			}
		})
	}
}

// Docs: guides/auth.mdx#databases
// "the stores scan `DATETIME` columns into `time.Time`, so it also needs
// `parseTime=true`."
//
// With multiStatements=true but no parseTime=true, the migrations apply and
// reading a stored session or token back fails, on both routes.
func TestStoresNeedParseTime(t *testing.T) {
	t.Parallel()
	for _, route := range mysqlRoutes {
		t.Run(route.name, func(t *testing.T) {
			t.Parallel()
			t.Run("session", func(t *testing.T) {
				sess := newSessionOn(t, route.open(t, mysqlDSN(t, "multiStatements=true")))
				r, _ := sessionRequest(t, sess, "u1", nil)
				if _, err := sess.Authenticate(t.Context(), r); err == nil {
					t.Error("session Authenticate on a DSN without parseTime=true: err = nil, want a scan error")
				}
			})
			t.Run("token", func(t *testing.T) {
				toks := newTokenOn(t, route.open(t, mysqlDSN(t, "multiStatements=true")))
				raw, err := toks.Issue(t.Context(), "u1", "verify", 0)
				if err != nil {
					t.Fatalf("token Issue: %v", err)
				}
				if _, err := toks.Verify(t.Context(), raw, "verify"); err == nil {
					t.Error("token Verify on a DSN without parseTime=true: err = nil, want a scan error")
				}
			})
		})
	}
}

// Docs: guides/auth.mdx#databases
// "With both, `session` and `token` work on MySQL 8.4; the `apikey` migration
// fails there."
//
// This asserts the current behavior the page states; KI-044 tracks the fix,
// after which this test and the page sentence change together.
func TestAPIKeyMigrationFailsOnMySQL(t *testing.T) {
	t.Parallel()
	for _, route := range mysqlRoutes {
		t.Run(route.name, func(t *testing.T) {
			t.Parallel()
			db := route.open(t, mysqlDSN(t, mysqlParams))
			if err := runStoreMigrations(t, "apikey", db); err == nil {
				t.Error("apikey migrations on MySQL 8.4 with multiStatements=true&parseTime=true: err = nil, want the failure the page states")
			}
		})
	}
}

// runStoreMigrations builds and starts one store on db and returns the error
// from applying its migrations.
func runStoreMigrations(t *testing.T, store string, db gas.DatabaseProvider) error {
	t.Helper()
	mig := newMigrations(t, db)
	var (
		svc gas.Service
		err error
	)
	switch store {
	case "session":
		svc, err = session.New(session.WithConfig(session.DefaultConfig()))(db, nopLogger(), mig, &configtest.MockConfig{})
	case "apikey":
		svc, err = apikey.New(apikey.WithConfig(apikey.DefaultConfig()))(db, nopLogger(), mig, &configtest.MockConfig{})
	case "token":
		svc, err = token.New(token.WithConfig(token.DefaultConfig()))(db, nopLogger(), mig, &configtest.MockConfig{})
	default:
		t.Fatalf("unknown store %q", store)
	}
	if err != nil {
		t.Fatalf("%s.New: %v", store, err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("%s Init: %v", store, err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return mig.RunPending()
}
