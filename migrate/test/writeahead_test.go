package migrate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/migrate"
)

// A row written ahead of a migration that is deleted from outside the
// service while the migration runs makes clearing the mark change no
// row: the run fails with ErrTrackingRowLost, logs no dirty mark, and
// leaves no row. MySQL writes ahead of every migration and revert;
// PostgreSQL of a no-transaction one.
func TestWriteAhead_RowLost(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, want string
		b          backend
		revert     bool
	}{
		{
			name: "mysql apply", b: mysqlBackend,
			want: "gas/migrate: apply 001 (owner app): migration failed: record: tracking row lost",
		},
		{
			name:   "mysql revert",
			b:      mysqlBackend,
			revert: true,
			want:   "gas/migrate: revert 001 (owner app): migration failed: remove row: tracking row lost",
		},
		{
			name: "postgres no-transaction apply", b: postgresBackend,
			want: "gas/migrate: apply 001 (owner app): migration failed: record: tracking row lost",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, c.b)
			g := newGate(t, e)

			e.svc.Register(app, gatedMigration(c.b, g, c.revert))

			errs := make(chan error, 1)

			if c.revert {
				require.NoError(t, e.svc.RunPending())

				go func() { errs <- e.svc.Down(1) }()
			} else {
				go func() { errs <- e.svc.RunPending() }()
			}

			g.blocked()

			deleteRow(t, e, "001")
			g.release()

			err := <-errs
			require.ErrorIs(t, err, migrate.ErrTrackingRowLost)
			require.ErrorIs(t, err, migrate.ErrMigrationFailed)
			require.EqualError(t, err, c.want)
			assert.False(t, e.log.has("migration marked dirty"))
			assert.Empty(t, rows(t, e.svc))
		})
	}
}

// gatedMigration returns migration 001 whose up SQL, or down SQL when
// revert is set, waits at g; on PostgreSQL it runs with no-transaction,
// where PostgreSQL writes its row ahead of it.
func gatedMigration(b backend, g gate, revert bool) gas.Migration {
	gated := g.sql
	if b.name == "postgres" {
		gated = "-- gas:no-transaction\n" + gated
	}

	if revert {
		return gas.Migration{Version: "001", Up: "SELECT 1", Down: gated}
	}

	return gas.Migration{Version: "001", Up: gated, Down: "SELECT 1"}
}

// deleteRow deletes version's row of the tracking table on e's database.
func deleteRow(t *testing.T, e *env, version string) {
	t.Helper()

	q := "DELETE FROM __gas_migrations WHERE version = ?"
	if e.b.name == "postgres" {
		q = "DELETE FROM __gas_migrations WHERE version = $1"
	}

	_, err := e.db.DB().ExecContext(t.Context(), q, version)
	require.NoError(t, err)
}

// While another session holds the migration lock, CheckReady takes dirty
// rows as in flight: a run may have written them ahead of the migration
// it runs. It logs that and is ready. Another lock of the database held
// does not count. Once the lock is free, the dirty rows fail it again.
func TestCheckReady_DirtyInFlight(t *testing.T) {
	t.Parallel()

	for _, b := range []backend{postgresBackend, mysqlBackend} {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)
			e.svc.Register(app, mig("001", "a"))
			markDirty(t, e, "001")

			holder := holdConn(t, e)

			var lock, unlock, other string

			if b.name == "postgres" {
				lock = "SELECT pg_advisory_lock($1)"
				unlock = "SELECT pg_advisory_unlock($1)"
				other = "SELECT pg_advisory_lock(424242)"
			} else {
				lock = "DO GET_LOCK(CONCAT(?, MD5(DATABASE())), 0)"
				unlock = "DO RELEASE_LOCK(CONCAT(?, MD5(DATABASE())))"
				other = "DO GET_LOCK('gas/migrate/other', 0)"
			}

			key := any(migrate.PostgresLockKey)
			if b.name == "mysql" {
				key = migrate.MySQLLockPrefix
			}

			_, err := holder.ExecContext(t.Context(), other)
			require.NoError(t, err)
			require.ErrorIs(t, e.svc.CheckReady(t.Context()), migrate.ErrDirty,
				"another lock is not the migration lock")

			_, err = holder.ExecContext(t.Context(), lock, key)
			require.NoError(t, err)
			require.NoError(t, e.svc.CheckReady(t.Context()))

			logged := e.log.messages("debug")
			require.NotEmpty(t, logged)
			assert.Equal(t, "dirty migrations in flight", logged[len(logged)-1].msg)
			assert.Equal(t, "001", logged[len(logged)-1].fields["versions"])

			e.svc.Register(app, mig("002", "b"))
			require.ErrorIs(t, e.svc.CheckReady(t.Context()), migrate.ErrPending,
				"pending migrations still count")

			_, err = holder.ExecContext(t.Context(), unlock, key)
			require.NoError(t, err)

			err = e.svc.CheckReady(t.Context())
			require.ErrorIs(t, err, migrate.ErrDirty)
			require.EqualError(t, err, "gas/migrate: dirty migrations: 001")
		})
	}
}

// A failure to read the migration lock is CheckReady's error, after
// `read migration lock: `.
func TestCheckReady_LockReadFails(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		b     backend
		match string
	}{
		{postgresBackend, "pg_locks"},
		{mysqlBackend, "IS_USED_LOCK"},
	} {
		t.Run(c.b.name, func(t *testing.T) {
			t.Parallel()

			f := &faults{}
			db := faultDB(t, c.b, f)
			svc, _ := started(t, db)

			e := &env{b: c.b, db: db}
			markDirty(t, e, "001")

			f.add("query", c.match, errBoom)

			err := svc.CheckReady(t.Context())
			require.ErrorIs(t, err, errBoom)
			require.EqualError(t, err, "gas/migrate: read migration lock: boom")
		})
	}
}

// On SQLite a dirty row is always ErrDirty: SQLite writes no row ahead of
// a migration, so none is in flight, and it has no lock to read.
func TestCheckReady_SQLiteDirtyFails(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)
	markDirty(t, e, "001")

	err := e.svc.CheckReady(t.Context())
	require.ErrorIs(t, err, migrate.ErrDirty)
	assert.False(t, e.log.has("dirty migrations in flight"))
}
