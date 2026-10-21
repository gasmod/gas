package guidesdatabase_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
)

// holdConns checks out n connections from db at once and returns them to the
// pool together.
func holdConns(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	conns := make([]*sql.Conn, 0, n)
	for range n {
		c, err := db.Conn(t.Context())
		if err != nil {
			t.Fatalf("check out connection: %v", err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		if err := c.Close(); err != nil {
			t.Fatalf("return connection: %v", err)
		}
	}
}

// Docs: guides/database.mdx#config
// "| `Database.MaxIdleConns` | `5` | Maximum idle connections, ModeSQL only |"
//
// database/sql does not report the idle limit itself, so the test observes
// it: after more connections than the limit are returned to the pool at once,
// only the limit stay idle and the rest are closed.
func TestMaxIdleConnsApplied(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		set  int // 0 keeps the default
		want int
	}{
		{"Default", 0, 5},
		{"Set", 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := sqliteConfig(t)
			cfg.Database.MaxOpenConns = 8
			if tc.set != 0 {
				cfg.Database.MaxIdleConns = tc.set
			}
			db := startService(t, cfg, nil).DB()
			holdConns(t, db, 8)
			st := db.Stats()
			if st.Idle != tc.want {
				t.Errorf("Idle after returning 8 connections = %d, want %d", st.Idle, tc.want)
			}
			if st.MaxIdleClosed != int64(8-tc.want) {
				t.Errorf("MaxIdleClosed = %d, want %d", st.MaxIdleClosed, 8-tc.want)
			}
		})
	}
}

// Docs: guides/database.mdx#config
// "| `Database.ConnMaxLifetime` | `30m` | Maximum connection reuse time |"
//
// In ModeSQL the applied value is observed through DB().Stats(): a
// connection held past the lifetime is closed when it is returned.
func TestSQLConnMaxLifetimeApplied(t *testing.T) {
	t.Parallel()
	cfg := sqliteConfig(t)
	cfg.Database.ConnMaxLifetime = time.Second
	cfg.Database.ConnMaxIdleTime = time.Second
	db := startService(t, cfg, nil).DB()

	c, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("check out connection: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if err := c.Close(); err != nil {
		t.Fatalf("return connection: %v", err)
	}
	if got := db.Stats().MaxLifetimeClosed; got < 1 {
		t.Fatalf("MaxLifetimeClosed after holding a connection past a 1s lifetime = %d, want at least 1", got)
	}
}

// Docs: guides/database.mdx#config
// "| `Database.ConnMaxIdleTime` | `5m` | Maximum connection idle time |"
//
// In ModeSQL the applied value is observed through DB().Stats(): an idle
// connection is closed once it has been idle past the limit.
func TestSQLConnMaxIdleTimeApplied(t *testing.T) {
	t.Parallel()
	cfg := sqliteConfig(t)
	cfg.Database.ConnMaxLifetime = time.Hour
	cfg.Database.ConnMaxIdleTime = time.Second
	db := startService(t, cfg, nil).DB()
	holdConns(t, db, 1)

	deadline := time.Now().Add(5 * time.Second)
	for db.Stats().MaxIdleTimeClosed < 1 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if got := db.Stats().MaxIdleTimeClosed; got < 1 {
		t.Fatalf("MaxIdleTimeClosed 5s after a connection went idle with a 1s idle limit = %d, want at least 1", got)
	}
}

// Docs: guides/database.mdx#config
// "| `Database.MaxOpenConns` | `25` | Maximum open connections |"
func TestPgxMaxOpenConnsSet(t *testing.T) {
	t.Parallel()
	cfg := database.DefaultConfig()
	cfg.Database.Mode = database.ModePgx
	cfg.Database.DSN = postgresDSN(t)
	cfg.Database.MaxOpenConns = 7
	cfg.Database.MaxIdleConns = 7
	if got := startService(t, cfg, nil).Pool().Config().MaxConns; got != 7 {
		t.Fatalf("pgx mode MaxConns = %d, want 7", got)
	}
}

// Docs: guides/database.mdx#config
// "| `Database.Driver` | `postgres` | `database/sql` driver name, ModeSQL only |"
// and "| `Database.MaxIdleConns` | `5` | Maximum idle connections, ModeSQL only |"
//
// Assumption: "ModeSQL only" means pgx mode ignores the field, so a value
// ModeSQL would reject (an unknown driver, or more idle than open connections)
// does not stop a pgx-mode service from starting. The SQL-mode cases confirm
// those values are rejected there, so the pgx cases are not vacuous.
func TestModeSQLOnlyFieldsIgnoredInPgx(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		edit func(*database.Config)
	}{
		{"Driver", func(c *database.Config) { c.Database.Driver = "no-such-driver" }},
		{"MaxIdleConns", func(c *database.Config) {
			c.Database.MaxOpenConns = 5
			c.Database.MaxIdleConns = 100
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sqlCfg := sqliteConfig(t)
			tc.edit(sqlCfg)
			sqlSvc, err := database.New(database.WithConfig(sqlCfg))(nil, &gas.NopLogger{})
			if err != nil {
				t.Fatalf("database.New: %v", err)
			}
			t.Cleanup(func() { _ = sqlSvc.Close(context.Background()) })
			if err := sqlSvc.Init(t.Context()); err == nil {
				t.Fatalf("precondition: ModeSQL Init with the %s value succeeded, want it rejected", tc.name)
			}

			pgxCfg := database.DefaultConfig()
			pgxCfg.Database.Mode = database.ModePgx
			pgxCfg.Database.DSN = postgresDSN(t)
			tc.edit(pgxCfg)
			svc, err := database.New(database.WithConfig(pgxCfg))(nil, &gas.NopLogger{})
			if err != nil {
				t.Fatalf("database.New: %v", err)
			}
			t.Cleanup(func() { _ = svc.Close(context.Background()) })
			if err := svc.Init(t.Context()); err != nil {
				t.Fatalf("ModePgx Init with the %s value: %v, want nil (field is ModeSQL only)", tc.name, err)
			}
		})
	}
}

// sqlDBTX is the DBTX interface sqlc generates for its database/sql output.
type sqlDBTX interface {
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	PrepareContext(context.Context, string) (*sql.Stmt, error)
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

// pgxDBTX is the DBTX interface sqlc generates for its pgx/v5 output. sqlc
// adds CopyFrom and SendBatch only for queries that use them; both are on
// *pgxpool.Pool and pgx.Tx, so they are included.
type pgxDBTX interface {
	Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error)
	Query(context.Context, string, ...interface{}) (pgx.Rows, error)
	QueryRow(context.Context, string, ...interface{}) pgx.Row
	CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error)
	SendBatch(context.Context, *pgx.Batch) pgx.BatchResults
}

// Docs: guides/database.mdx#sqlc
// "sqlc generates a `DBTX` interface in its own output package, satisfied by
// `*sql.DB` and `*sql.Tx` in `database/sql` mode and by `*pgxpool.Pool` and
// `pgx.Tx` in pgx mode. Pass `DB()`, `Pool()`, or a transaction straight into
// the generated constructor."
func TestSqlcDBTXShape(t *testing.T) {
	t.Parallel()
	var (
		_ sqlDBTX = (*sql.DB)(nil)
		_ sqlDBTX = (*sql.Tx)(nil)
		_ pgxDBTX = (*pgxpool.Pool)(nil)
		_ pgxDBTX = pgx.Tx(nil)
	)

	t.Run("sql", func(t *testing.T) {
		t.Parallel()
		svc := newSQLite(t)
		var q sqlDBTX = svc.DB()
		var one int
		if err := q.QueryRowContext(t.Context(), "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("DB() as DBTX: SELECT 1 = %d, %v", one, err)
		}
		tx, err := svc.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		q = tx
		if err := q.QueryRowContext(t.Context(), "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("*sql.Tx as DBTX: SELECT 1 = %d, %v", one, err)
		}
	})

	t.Run("pgx", func(t *testing.T) {
		t.Parallel()
		svc := newPgx(t)
		var q pgxDBTX = svc.Pool()
		var one int
		if err := q.QueryRow(t.Context(), "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("Pool() as DBTX: SELECT 1 = %d, %v", one, err)
		}
		tx, err := svc.BeginPgxTx(t.Context(), nil)
		if err != nil {
			t.Fatalf("BeginPgxTx: %v", err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		q = tx
		if err := q.QueryRow(t.Context(), "SELECT 1").Scan(&one); err != nil || one != 1 {
			t.Fatalf("pgx.Tx as DBTX: SELECT 1 = %d, %v", one, err)
		}
	})
}
