package modulesmigrations_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql" // also registers the "mysql" database/sql driver

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/migrate"
)

// mysqlTableExists reports whether a table exists in the connection's current
// MySQL database.
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

// Docs: modules/migrations.mdx#when-migrations-run
// "On MySQL, DDL such as `CREATE TABLE`, `DROP TABLE` and `ALTER TABLE` commits
// any open transaction implicitly. A migration containing DDL is therefore not
// atomic with its tracking row in either direction: a failure after the DDL
// leaves the schema changed whatever the tracking table says."
// multiStatements=true lets go-sql-driver run two statements in one Exec, and
// parseTime=true lets the tracking table be read back (see TestDialects).
func TestMySQLDDLNotAtomic(t *testing.T) {
	t.Parallel()

	t.Run("Up", func(t *testing.T) {
		t.Parallel()
		p := openProvider(t, "mysql", mysqlDSN(t, "multiStatements=true&parseTime=true"), "mysql")
		m := newMigrator(t, p)
		m.Register(&owner{name: "ddl"}, gas.Migration{
			Version: "001", Description: "half",
			Up:   "CREATE TABLE half (x INT); INSERT INTO missing_table VALUES (1)",
			Down: "DROP TABLE half",
		})
		if err := m.RunPending(); err == nil {
			t.Fatal("RunPending with a failing migration: err = nil")
		}
		var dirty bool
		if err := p.db.QueryRowContext(t.Context(), "SELECT dirty FROM __gas_migrations WHERE version = '001'").Scan(&dirty); err != nil || !dirty {
			t.Fatalf("tracking row for 001: dirty = %v, %v; want dirty", dirty, err)
		}
		if !mysqlTableExists(t, p.db, "half") {
			t.Error("table from the failed migration is absent, want the DDL committed despite the dirty row")
		}
	})

	t.Run("Down", func(t *testing.T) {
		t.Parallel()
		p := openProvider(t, "mysql", mysqlDSN(t, "multiStatements=true&parseTime=true"), "mysql")
		m := newMigrator(t, p)
		m.Register(&owner{name: "ddl"}, gas.Migration{
			Version: "001", Description: "table",
			Up:   "CREATE TABLE gone (x INT)",
			Down: "DROP TABLE gone; INSERT INTO missing_table VALUES (1)",
		})
		if err := m.RunPending(); err != nil {
			t.Fatalf("RunPending: %v", err)
		}
		if err := m.Down(1); err == nil {
			t.Fatal("Down with a failing down SQL: err = nil")
		}
		var n int
		if err := p.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM __gas_migrations WHERE version = '001'").Scan(&n); err != nil || n != 1 {
			t.Fatalf("tracking rows for 001 = %d, %v; want 1 (still recorded as applied)", n, err)
		}
		if mysqlTableExists(t, p.db, "gone") {
			t.Error("table dropped by the failed down SQL still exists, want the DDL committed")
		}
	})
}

// Docs: modules/migrations.mdx#when-migrations-run
// "The up and down SQL run as a single `Exec` call, so whether a migration can
// hold several statements depends on the driver."
// go-sql-driver rejects several statements in one Exec unless
// multiStatements is set; were the SQL split into statements, both would
// succeed.
func TestMultiStatementDependsOnDriver(t *testing.T) {
	t.Parallel()
	run := func(t *testing.T, params string) error {
		p := openProvider(t, "mysql", mysqlDSN(t, params), "mysql")
		m := newMigrator(t, p)
		m.Register(&owner{name: "multi"}, gas.Migration{
			Version: "001", Description: "two",
			Up:   "CREATE TABLE one (x INT); CREATE TABLE two (x INT)",
			Down: "DROP TABLE two; DROP TABLE one",
		})
		return m.RunPending()
	}
	if err := run(t, ""); err == nil {
		t.Error("two statements without multiStatements: err = nil, want the driver to reject them")
	}
	if err := run(t, "multiStatements=true"); err != nil {
		t.Errorf("two statements with multiStatements: %v", err)
	}
}

// mysqlConnectorDB builds a gas/database service in sql mode over a
// go-sql-driver connector for dsn, with the given driver name.
func mysqlConnectorDB(t *testing.T, dsn, driverName string) (*database.Service, error) {
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
	cfg.Database.Driver = driverName
	cfg.Database.DSN = ""
	svc, err := database.New(database.WithConfig(cfg), database.WithConnector(conn))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc, svc.Init(t.Context())
}

// Docs: modules/migrations.mdx#mysql
// "`gas/database` accepts `postgres`, `pgx` and `sqlite` as its own drivers.
// For MySQL, give it a connector with `database.WithConnector` and set
// `database.driver` to `mysql`, or register your own `gas.DatabaseProvider`
// whose `Driver()` returns `mysql`"
func TestMySQLRoutes(t *testing.T) {
	t.Parallel()

	t.Run("NoConnector", func(t *testing.T) {
		t.Parallel()
		cfg := database.DefaultConfig()
		cfg.Database.Driver = "mysql"
		cfg.Database.DSN = "root:pw@tcp(127.0.0.1:1)/x"
		svc, err := database.New(database.WithConfig(cfg))(nil, &gas.NopLogger{})
		if err != nil {
			t.Fatalf("database.New: %v", err)
		}
		t.Cleanup(func() { _ = svc.Close(context.Background()) })
		if err := svc.Init(t.Context()); err == nil || !strings.Contains(err.Error(), "Database.Driver") {
			t.Fatalf("gas/database Init with driver mysql and no connector = %v, want the driver rejected", err)
		}
	})

	routes := map[string]func(t *testing.T, dsn string) gas.DatabaseProvider{
		"Connector": func(t *testing.T, dsn string) gas.DatabaseProvider {
			svc, err := mysqlConnectorDB(t, dsn, "mysql")
			if err != nil {
				t.Fatalf("Init over a mysql connector: %v", err)
			}
			return svc
		},
		"OwnProvider": func(t *testing.T, dsn string) gas.DatabaseProvider {
			return openProvider(t, "mysql", dsn, "mysql")
		},
	}
	for name, open := range routes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := open(t, mysqlDSN(t, "parseTime=true"))
			if p.Driver() != "mysql" {
				t.Fatalf("Driver() = %q, want mysql", p.Driver())
			}
			m := newMigrator(t, p)
			m.Register(&owner{name: "route"}, gas.Migration{
				Version: "001", Description: "t", Up: "CREATE TABLE routed (x INT)", Down: "DROP TABLE routed",
			})
			if err := m.RunPending(); err != nil {
				t.Fatalf("RunPending: %v", err)
			}
			if err := m.CheckReady(t.Context()); err != nil {
				t.Fatalf("CheckReady: %v", err)
			}
			if err := m.Down(1); err != nil {
				t.Fatalf("Down: %v", err)
			}
		})
	}
}

// Docs: modules/migrations.mdx#mysql
// "The MySQL DSN needs `parseTime=true`, because the module scans the tracking
// table's `applied_at` column into `time.Time`. Without it the first
// `RunPending` succeeds, but every later read of the tracking table fails:
// `CheckReady`, `Down`, and the next `RunPending`, so the next start of the app.
// The error is `gas/migrate: failed to query applied migrations: sql: Scan
// error on column index 5, name "applied_at": unsupported Scan, storing
// driver.Value type []uint8 into type *time.Time` (KI-048"
func TestMySQLWithoutParseTime(t *testing.T) {
	t.Parallel()
	const want = `gas/migrate: failed to query applied migrations: sql: Scan error on column index 5, name "applied_at": unsupported Scan, storing driver.Value type []uint8 into type *time.Time`
	dsn := mysqlDSN(t, "")
	mig := gas.Migration{Version: "001", Description: "t", Up: "CREATE TABLE np (x INT)", Down: "DROP TABLE np"}

	p := openProvider(t, "mysql", dsn, "mysql")
	m := newMigrator(t, p)
	m.Register(&owner{name: "np"}, mig)
	if err := m.RunPending(); err != nil {
		t.Fatalf("first RunPending: %v, want nil", err)
	}
	for op, err := range map[string]error{
		"CheckReady": m.CheckReady(t.Context()),
		"Down":       m.Down(1),
		"RunPending": m.RunPending(),
	} {
		if err == nil || err.Error() != want {
			t.Errorf("%s = %v, want %q", op, err, want)
		}
	}

	w := gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](gas.Logger(&gas.NopLogger{})),
		gas.WithServiceInstance[gas.DatabaseProvider](gas.DatabaseProvider(openProvider(t, "mysql", dsn, "mysql"))),
		gas.WithSingletonService[gas.MigrationManager](migrate.New()),
		gas.WithSingletonService[*registrar](func(mgr gas.MigrationManager) (*registrar, error) {
			return &registrar{mgr: mgr, mig: mig}, nil
		}),
	)
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if err := w.Start(t.Context()); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("next Start = %v, want it to fail with %q", err, want)
	}
}

// registrar registers one migration in Init.
type registrar struct {
	mgr gas.MigrationManager
	mig gas.Migration
}

func (r *registrar) Name() string { return "np" }
func (r *registrar) Init(context.Context) error {
	r.mgr.Register(r, r.mig)
	return nil
}
func (r *registrar) Close(context.Context) error { return nil }
