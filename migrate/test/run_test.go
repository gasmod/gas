package migrate_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/migrate"
)

// RunPending applies every pending migration in version order, writes a
// clean row for each with its owner and description, logs each, and does
// nothing the second time.
func TestRunPending_AppliesInOrder(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)

			e.svc.Register(owner("users"), gas.Migration{
				Version:     "002",
				Description: "add name",
				Up:          "ALTER TABLE users ADD COLUMN name VARCHAR(10)",
				Down:        "ALTER TABLE users DROP COLUMN name",
			})
			e.svc.Register(owner("users"), mig("001", "users"))

			require.NoError(t, e.svc.RunPending())
			assert.True(t, e.exists(t, "users"))
			assert.Equal(t, []migrate.AppliedMigration{
				{Version: "001", Service: "users", Description: "create users"},
				{Version: "002", Service: "users", Description: "add name"},
			}, rows(t, e.svc))

			info := e.log.messages("info")
			applied := info[len(info)-2:]

			for i, v := range []string{"001", "002"} {
				assert.Equal(t, "migration applied", applied[i].msg)
				assert.Equal(t, v, applied[i].fields["version"])
				assert.Equal(t, "users", applied[i].fields["owner"])
				assert.Equal(t, migrate.ServiceName, applied[i].fields["service"])
				assert.IsType(t, time.Duration(0), applied[i].fields["duration"])
			}

			require.NoError(t, e.svc.RunPending())

			debug := e.log.messages("debug")
			require.Len(t, debug, 1)
			assert.Equal(t, "no pending migrations", debug[0].msg)
		})
	}
}

// A migration that fails is rolled back with the insert of its row, then
// marked dirty, and stops the run: the migrations before it stay
// applied, the ones after it do not run. On PostgreSQL and SQLite the
// failed migration's first statement is undone too; MySQL is pinned in
// TestMySQL_PartialApply.
func TestRunPending_FailureMarksDirty(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			if b.name == "mysql" {
				t.Skip("MySQL commits DDL; see TestMySQL_PartialApply")
			}

			e := newEnv(t, b)

			e.svc.RegisterSlice(app, []gas.Migration{
				mig("001", "a"),
				{Version: "002", Up: "CREATE TABLE b (id INT); SELECT * FROM missing"},
				mig("003", "c"),
			})

			err := e.svc.RunPending()
			require.ErrorIs(t, err, migrate.ErrMigrationFailed)
			assert.True(t, strings.HasPrefix(err.Error(),
				"gas/migrate: apply 002 (owner app): migration failed: "), err.Error())
			assert.True(t, e.exists(t, "a"))
			assert.False(t, e.exists(t, "b"))
			assert.False(t, e.exists(t, "c"))
			assert.Equal(t, []migrate.AppliedMigration{
				{Version: "001", Service: "app", Description: "create a"},
				{Version: "002", Service: "app", Dirty: true},
			}, rows(t, e.svc))

			warns := e.log.messages("warn")
			require.Len(t, warns, 1)
			assert.Equal(t, "migration marked dirty", warns[0].msg)
			assert.Equal(
				t,
				map[string]any{"service": migrate.ServiceName, "version": "002", "owner": "app"},
				warns[0].fields,
			)

			err = e.svc.RunPending()
			require.ErrorIs(t, err, migrate.ErrDirty)
			require.EqualError(t, err, "gas/migrate: dirty migrations: 002")
			require.ErrorIs(t, e.svc.Down(1), migrate.ErrDirty)
			require.ErrorIs(t, e.svc.CheckReady(t.Context()), migrate.ErrDirty)
		})
	}
}

// After ClearDirty, the fixed migration applies: on PostgreSQL and SQLite
// the failed one left nothing behind.
func TestRunPending_ClearDirtyThenApply(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			if b.name == "mysql" {
				t.Skip("MySQL commits DDL; see TestMySQL_PartialApply")
			}

			e := newEnv(t, b)
			broken := gas.Migration{
				Version: "001",
				Up:      "CREATE TABLE a (id INT); CREATE TABLE a (id INT)",
			}

			e.svc.Register(app, broken)
			require.ErrorIs(t, e.svc.RunPending(), migrate.ErrMigrationFailed)

			require.NoError(t, e.svc.ClearDirty(t.Context(), "001"))
			assert.True(t, e.log.has("dirty mark cleared"))
			assert.Empty(t, rows(t, e.svc))

			fixed, _ := e.peer(t)
			fixed.Register(app, mig("001", "a"))
			require.NoError(t, fixed.RunPending())
			assert.True(t, e.exists(t, "a"))
		})
	}
}

// A failure to record a migration is ErrMigrationFailed too, and a dirty
// mark that fails as well is joined to it. On MySQL, which marks the
// migration dirty before it runs, a failed mark fails the call with
// nothing run, and a failure to mark it clean afterwards leaves it dirty.
func TestRunPending_RecordAndMarkFail(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			f := &faults{}
			svc, log := started(t, faultDB(t, b, f))

			svc.Register(app, gas.Migration{Version: "001", Up: "CREATE TABLE a (id INT)"})
			f.add("exec", "INSERT INTO __gas_migrations", errBoom)

			if b.name == "mysql" {
				err := svc.RunPending()
				require.ErrorIs(t, err, errBoom)
				require.NotErrorIs(t, err, migrate.ErrMigrationFailed)
				require.EqualError(t, err, "gas/migrate: apply 001 (owner app): mark dirty: boom")

				f.clear()
				assert.Empty(t, rows(t, svc))
				f.add("exec", "UPDATE __gas_migrations SET dirty = FALSE", errBoom)

				err = svc.RunPending()
				require.ErrorIs(t, err, migrate.ErrMigrationFailed)
				require.EqualError(
					t,
					err,
					"gas/migrate: apply 001 (owner app): migration failed: record: boom",
				)
				assert.True(t, log.has("migration marked dirty"))

				f.clear()
				assert.Equal(
					t,
					[]migrate.AppliedMigration{{Version: "001", Service: "app", Dirty: true}},
					rows(t, svc),
				)

				return
			}

			err := svc.RunPending()
			require.ErrorIs(t, err, migrate.ErrMigrationFailed)
			require.ErrorIs(t, err, errBoom)
			require.EqualError(
				t,
				err,
				"gas/migrate: apply 001 (owner app): migration failed: record: boom; mark dirty: boom",
			)
			assert.False(t, log.has("migration marked dirty"))

			f.clear()
			assert.Empty(t, rows(t, svc))
		})
	}
}

// A version that has a row when its migration fails keeps that row as it
// is. The lock makes that impossible between instances of migrate, so
// here, on PostgreSQL, another connection writes a clean row while the
// migration waits at a gate inside its transaction: recording it then
// fails on the key, and the row stays clean and owned by the other
// writer, with no dirty mark logged. MySQL marks a migration dirty before
// it runs, which fails on such a row, and SQLite reads the row again
// inside the migration's transaction, as other tests show.
func TestRunPending_DirtyMarkKeepsCleanRow(t *testing.T) {
	t.Parallel()

	e := newEnv(t, postgresBackend)
	g := newGate(t, e)

	e.svc.Register(app, gas.Migration{Version: "001", Up: g.sql})

	done := make(chan error, 1)

	go func() { done <- e.svc.RunPending() }()

	g.blocked()
	insertRow(t, e, "001", "other", false)
	g.release()

	err := <-done
	require.ErrorIs(t, err, migrate.ErrMigrationFailed)
	assert.Contains(
		t,
		err.Error(),
		"gas/migrate: apply 001 (owner app): migration failed: record: ",
	)
	assert.Equal(
		t,
		[]migrate.AppliedMigration{{Version: "001", Service: "other"}},
		rows(t, e.svc),
	)
	assert.False(t, e.log.has("migration marked dirty"))
}

// errBoom is an error the faults inject.
var errBoom = errors.New("boom")

// A migration registered with a version below an applied one is applied
// by the next run, and versions that are all digits run in numeric
// order: 9 before 10.
func TestRunPending_NumericOrder(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)

	e.svc.RegisterSlice(app, []gas.Migration{
		{Version: "10", Up: "ALTER TABLE x ADD COLUMN b INT"},
		{Version: "9", Up: "CREATE TABLE x (a INT)"},
		{Version: "0011", Up: "ALTER TABLE x ADD COLUMN c INT"},
	})
	require.NoError(t, e.svc.RunPending())
	assert.Equal(t, []string{"9", "10", "0011"}, versions(rows(t, e.svc)))

	e.svc.Register(app, gas.Migration{Version: "5", Up: "CREATE TABLE y (a INT)"})
	require.ErrorIs(t, e.svc.CheckReady(t.Context()), migrate.ErrPending)
	require.NoError(t, e.svc.RunPending())
	assert.True(t, e.exists(t, "y"))
	assert.Equal(t, []string{"5", "9", "10", "0011"}, versions(rows(t, e.svc)))
}

// Rows of versions no service registers are left alone by RunPending and
// CheckReady.
func TestRunPending_UnregisteredRowsIgnored(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)
	insertRow(t, e, "999", "gone", false)

	e.svc.Register(app, mig("001", "a"))
	require.NoError(t, e.svc.RunPending())
	require.NoError(t, e.svc.CheckReady(t.Context()))
	assert.Equal(t, []string{"001", "999"}, versions(rows(t, e.svc)))
}

// A run's context that ends while a migration runs fails it, rolls it
// back and marks it dirty with a context of its own.
func TestRunPending_ContextEndsDuringMigration(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			if b.name == "mysql" {
				t.Skip("MySQL commits DDL; the dirty mark is pinned in TestMySQL_PartialApply")
			}

			e := newEnv(t, b)
			g := newGate(t, e)

			e.svc.Register(
				app,
				gas.Migration{Version: "001", Up: g.sql + " CREATE TABLE a (id INT)"},
			)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			done := make(chan error, 1)

			go func() { done <- e.svc.RunPendingContext(ctx) }()

			g.blocked()
			cancel()

			if b.name == "sqlite" {
				// A Go function SQLite runs is not interrupted.
				g.release()
			}

			err := <-done
			require.ErrorIs(t, err, migrate.ErrMigrationFailed)
			require.ErrorIs(t, err, context.Canceled)
			g.release()
			assert.False(t, e.exists(t, "a"))

			// The dirty mark is written unless the connection broke with the
			// context: pgx closes it, and SQLite's interrupt can outlive the
			// statement it was meant for.
			for _, r := range rows(t, e.svc) {
				assert.True(t, r.Dirty)
			}
		})
	}
}
