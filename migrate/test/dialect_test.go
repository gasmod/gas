package migrate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/migrate"
)

// On MySQL, DDL commits as it runs, so a migration that fails after its
// first CREATE TABLE leaves that table behind and is marked dirty. After
// ClearDirty, applying it again fails on the table it left, and is
// marked dirty again; once the table is dropped by hand, the migration
// applies.
func TestMySQL_PartialApply(t *testing.T) {
	t.Parallel()

	e := newEnv(t, mysqlBackend)
	m := gas.Migration{Version: "001", Up: "CREATE TABLE p (id INT); CREATE TABLE p (id INT);"}

	e.svc.Register(app, m)

	err := e.svc.RunPending()
	require.ErrorIs(t, err, migrate.ErrMigrationFailed)
	require.EqualError(t, err, "gas/migrate: apply 001 (owner app): migration failed: "+
		"Error 1050 (42S01): Table 'p' already exists")
	assert.True(t, e.exists(t, "p"), "the first statement's DDL committed")
	assert.Equal(
		t,
		[]migrate.AppliedMigration{
			{Version: "001", Service: "app", Dirty: true, Checksum: sum(m.Up)},
		},
		rows(t, e.svc),
	)

	require.NoError(t, e.svc.ClearDirty(t.Context(), "001"))

	fixed, _ := e.peer(t)
	fixed.Register(app, gas.Migration{Version: "001", Up: "CREATE TABLE p (id INT)"})
	require.ErrorIs(t, fixed.RunPending(), migrate.ErrMigrationFailed)
	assert.True(t, rows(t, fixed)[0].Dirty)

	_, err = e.db.DB().ExecContext(t.Context(), "DROP TABLE p")
	require.NoError(t, err)
	require.NoError(t, fixed.ClearDirty(t.Context(), "001"))
	require.NoError(t, fixed.RunPending())
	assert.Equal(
		t,
		[]migrate.AppliedMigration{
			{Version: "001", Service: "app", Checksum: sum("CREATE TABLE p (id INT)")},
		},
		rows(t, fixed),
	)
}

// On MySQL, a migration of several statements runs through gas/database's
// default DSN, which allows one statement per query: the service sends
// them one at a time, semicolons in strings, identifiers and comments
// included. Its down SQL is split too.
func TestMySQL_Splits(t *testing.T) {
	t.Parallel()

	e := newEnv(t, mysqlBackend)

	e.svc.Register(app, gas.Migration{
		Version: "001",
		Up: "-- a comment; not a statement\n" +
			"CREATE TABLE `semi;colon` (v VARCHAR(20) DEFAULT 'a;b');\n" +
			"# another; comment\n" +
			"INSERT INTO `semi;colon` (v) VALUES ('it''s;'), (\"x\\\";\") /* ; */;\n" +
			"CREATE INDEX i ON `semi;colon` (v);\n",
		Down: "DROP INDEX i ON `semi;colon`; DROP TABLE `semi;colon`;",
	})
	require.NoError(t, e.svc.RunPending())

	var n int

	require.NoError(t, e.db.DB().QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM `semi;colon` WHERE v IN ('it''s;', 'x\";')").Scan(&n))
	assert.Equal(t, 2, n)

	require.NoError(t, e.svc.Down(1))
	assert.False(t, e.exists(t, "semi;colon"))
}

// `-- gas:no-split` sends the SQL whole, for a body with semicolons such
// as a trigger's.
func TestMySQL_NoSplit(t *testing.T) {
	t.Parallel()

	e := newEnv(t, mysqlBackend)

	e.svc.RegisterSlice(app, []gas.Migration{
		{Version: "001", Up: "CREATE TABLE n (v INT); CREATE TABLE audit (v INT)"},
		{
			Version: "002",
			Up: "-- gas:no-split\n" +
				"CREATE TRIGGER n_audit AFTER INSERT ON n FOR EACH ROW BEGIN\n" +
				"  INSERT INTO audit (v) VALUES (NEW.v);\n" +
				"  INSERT INTO audit (v) VALUES (NEW.v + 1);\n" +
				"END",
			Down: "DROP TRIGGER n_audit",
		},
	})
	require.NoError(t, e.svc.RunPending())

	_, err := e.db.DB().ExecContext(t.Context(), "INSERT INTO n (v) VALUES (1)")
	require.NoError(t, err)

	var n int

	require.NoError(
		t,
		e.db.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM audit").Scan(&n),
	)
	assert.Equal(t, 2, n)
}

// `-- gas:no-transaction` runs a migration outside a transaction, which
// is the only way PostgreSQL runs CREATE INDEX CONCURRENTLY; a migration
// without it fails on that statement and is rolled back.
func TestPostgres_NoTransaction(t *testing.T) {
	t.Parallel()

	e := newEnv(t, postgresBackend)

	e.svc.RegisterSlice(app, []gas.Migration{
		{Version: "001", Up: "CREATE TABLE ci (id INT)"},
		{
			Version: "002",
			Up:      "-- gas:no-transaction\nCREATE INDEX CONCURRENTLY ci_id ON ci (id)",
			Down:    "-- gas:no-transaction\nDROP INDEX CONCURRENTLY ci_id",
		},
	})
	require.NoError(t, e.svc.RunPending())
	assert.Equal(t, []string{"001", "002"}, versions(rows(t, e.svc)))
	require.NoError(t, e.svc.Down(1))

	plain, _ := e.peer(t)
	plain.RegisterSlice(app, []gas.Migration{
		{Version: "001", Up: "CREATE TABLE ci (id INT)"},
		{Version: "002", Up: "CREATE INDEX CONCURRENTLY ci_id ON ci (id)"},
	})

	err := plain.RunPending()
	require.ErrorIs(t, err, migrate.ErrMigrationFailed)
	require.EqualError(t, err, "gas/migrate: apply 002 (owner app): migration failed: "+
		"ERROR: CREATE INDEX CONCURRENTLY cannot run inside a transaction block (SQLSTATE 25001)")
}

// A no-transaction migration that fails keeps what ran before the failure
// and is marked dirty: its row is written dirty before it runs.
func TestNoTransaction_FailureKeepsWork(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			switch b.name {
			case "postgres":
				t.Skip("PostgreSQL runs a query of several statements as one transaction")
			case "sqlite":
				t.Skip("SQLite does not support no-transaction")
			}

			e := newEnv(t, b)

			const up = "-- gas:no-transaction\nCREATE TABLE kept (id INT); SELECT id FROM missing;"

			e.svc.Register(app, gas.Migration{Version: "001", Up: up})

			require.ErrorIs(t, e.svc.RunPending(), migrate.ErrMigrationFailed)
			assert.True(t, e.exists(t, "kept"))
			assert.Equal(
				t,
				[]migrate.AppliedMigration{
					{Version: "001", Service: "app", Dirty: true, Checksum: sum(up)},
				},
				rows(t, e.svc),
			)
		})
	}
}

// A no-transaction migration that succeeds writes its row after it.
func TestNoTransaction_Applies(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			if b.name == "sqlite" {
				t.Skip("SQLite does not support no-transaction")
			}

			e := newEnv(t, b)

			e.svc.Register(app, gas.Migration{
				Version: "001",
				Up:      "-- gas:no-transaction\n\n-- plain comment\nCREATE TABLE nt (id INT)",
				Down:    "-- gas:no-transaction\nDROP TABLE nt",
			})

			require.NoError(t, e.svc.RunPending())
			assert.True(t, e.exists(t, "nt"))
			require.NoError(t, e.svc.Down(1))
			assert.False(t, e.exists(t, "nt"))
			assert.Empty(t, rows(t, e.svc))
		})
	}
}

// SQLite does not support no-transaction: a migration that has it, in its
// up or down SQL, is a registration problem that RunPending, Down and
// CheckReady report, naming the first such migration, and nothing runs.
// The same registration is fine on the other dialects.
func TestSQLite_NoTransactionUnsupported(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)

	e.svc.RegisterSlice(app, []gas.Migration{
		mig("001", "a"),
		{Version: "003", Up: "-- gas:no-transaction\nVACUUM"},
		{Version: "002", Up: "SELECT 1", Down: "-- gas:no-transaction\nVACUUM"},
	})

	const want = "gas/migrate: register 002 (owner app): down SQL: " +
		"unsupported directive: no-transaction on sqlite"

	err := e.svc.RunPending()
	require.ErrorIs(t, err, migrate.ErrUnsupportedDirective)
	require.EqualError(t, err, want)
	require.EqualError(t, e.svc.Down(1), want)
	require.EqualError(t, e.svc.CheckReady(t.Context()), want)
	assert.Empty(t, rows(t, e.svc))
	assert.False(t, e.exists(t, "a"))
}

// A no-transaction revert that fails leaves its row marked dirty, on
// PostgreSQL and MySQL, since what ran of it stays: the row is marked
// before the down SQL runs and deleted only after it succeeds.
func TestNoTransaction_FailedRevertDirty(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			if b.name == "sqlite" {
				t.Skip("SQLite does not support no-transaction")
			}

			e := newEnv(t, b)

			e.svc.Register(app, gas.Migration{
				Version: "001",
				Up:      "CREATE TABLE a (id INT)",
				Down:    "-- gas:no-transaction\nDROP TABLE a",
			})
			e.svc.Register(app, gas.Migration{
				Version: "002",
				Up:      "CREATE TABLE b (id INT)",
				Down:    "-- gas:no-transaction\nSELECT * FROM missing",
			})
			require.NoError(t, e.svc.RunPending())

			err := e.svc.Down(2)
			require.ErrorIs(t, err, migrate.ErrMigrationFailed)
			assert.Contains(
				t,
				err.Error(),
				"gas/migrate: revert 002 (owner app): migration failed: ",
			)
			assert.True(t, e.log.has("migration marked dirty"))
			assert.Equal(t, []migrate.AppliedMigration{
				{Version: "001", Service: "app", Checksum: sum("CREATE TABLE a (id INT)")},
				{
					Version:  "002",
					Service:  "app",
					Dirty:    true,
					Checksum: sum("CREATE TABLE b (id INT)"),
				},
			}, rows(t, e.svc))
			require.ErrorIs(t, e.svc.Down(1), migrate.ErrDirty)
		})
	}
}
