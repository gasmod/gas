package migrate_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/migrate"
)

// Down reverts the n highest versions, highest first, each with the
// delete of its row, and logs each; n larger than the rows reverts them
// all.
func TestDown_RevertsHighestFirst(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)

			e.svc.RegisterSlice(app, []gas.Migration{mig("9", "a"), mig("10", "b"), mig("11", "c")})
			require.NoError(t, e.svc.RunPending())

			require.NoError(t, e.svc.Down(2))
			assert.Equal(t, []string{"9"}, versions(rows(t, e.svc)))
			assert.True(t, e.exists(t, "a"))
			assert.False(t, e.exists(t, "b"))
			assert.False(t, e.exists(t, "c"))

			var reverted []any

			for _, en := range e.log.messages("info") {
				if en.msg == "migration reverted" {
					reverted = append(reverted, en.fields["version"])

					assert.Equal(t, "app", en.fields["owner"])
					assert.IsType(t, time.Duration(0), en.fields["duration"])
				}
			}

			assert.Equal(t, []any{"11", "10"}, reverted)

			require.NoError(t, e.svc.Down(5))
			assert.Empty(t, rows(t, e.svc))
			assert.False(t, e.exists(t, "a"))
		})
	}
}

// Down with n of zero or less does nothing and returns nil, even with a
// dirty row or a registration problem; before Init it is still
// ErrNotInitialized.
func TestDown_NonPositive(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)
	markDirty(t, e, "001")
	e.svc.Register(nil, mig("002", "b"))

	require.NoError(t, e.svc.Down(0))
	require.NoError(t, e.svc.DownContext(t.Context(), -1))

	svc, _ := newService(t, e.db)
	require.ErrorIs(t, svc.Down(0), migrate.ErrNotInitialized)
}

// KI P8: Down checks every version it is to revert before it reverts any,
// so a version without a registration or without down SQL leaves every
// version applied.
func TestDown_ChecksBeforeReverting(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)

			e.svc.RegisterSlice(app, []gas.Migration{
				{Version: "1", Up: "CREATE TABLE one (id INT)"},
				mig("2", "two"),
			})
			require.NoError(t, e.svc.RunPending())
			insertRow(t, e, "3", "gone", false)

			err := e.svc.Down(2)
			require.ErrorIs(t, err, migrate.ErrUnregisteredVersion)
			require.EqualError(t, err, "gas/migrate: revert 3 (owner gone): version not registered")
			assert.Equal(t, []string{"1", "2", "3"}, versions(rows(t, e.svc)))
			assert.True(t, e.exists(t, "two"))

			_, err = e.db.DB().
				ExecContext(t.Context(), "DELETE FROM __gas_migrations WHERE version = '3'")
			require.NoError(t, err)

			err = e.svc.Down(2)
			require.ErrorIs(t, err, migrate.ErrNoDownSQL)
			require.EqualError(t, err, "gas/migrate: revert 1 (owner app): no down SQL")
			assert.Equal(t, []string{"1", "2"}, versions(rows(t, e.svc)))
			assert.True(t, e.exists(t, "two"))
		})
	}
}

// A revert that fails is rolled back, marks nothing dirty and stops the
// call; the reverts before it stay. On MySQL, which commits DDL and so
// marks a migration dirty before reverting it, the failed revert stays
// marked dirty, which is logged.
func TestDown_FailureStops(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			e := newEnv(t, b)

			e.svc.RegisterSlice(app, []gas.Migration{
				mig("1", "one"),
				{Version: "2", Up: "CREATE TABLE two (id INT)", Down: "DELETE FROM missing"},
				mig("3", "three"),
			})
			require.NoError(t, e.svc.RunPending())

			err := e.svc.Down(3)
			require.ErrorIs(t, err, migrate.ErrMigrationFailed)
			assert.Contains(t, err.Error(), "gas/migrate: revert 2 (owner app): migration failed: ")

			mysql := b.name == "mysql"

			assert.Equal(t, []migrate.AppliedMigration{
				{
					Version:     "1",
					Service:     "app",
					Description: "create one",
					Checksum:    sum("CREATE TABLE one (id INT)"),
				},
				{
					Version:  "2",
					Service:  "app",
					Dirty:    mysql,
					Checksum: sum("CREATE TABLE two (id INT)"),
				},
			}, rows(t, e.svc))
			assert.False(t, e.exists(t, "three"))

			warns := e.log.messages("warn")
			if mysql {
				require.Len(t, warns, 1)
				assert.Equal(t, "migration marked dirty", warns[0].msg)
			} else {
				assert.Empty(t, warns)
			}
		})
	}
}

// A failed delete of the row rolls the down SQL back with it, so the
// schema and the table agree.
func TestDown_RowDeleteFails(t *testing.T) {
	t.Parallel()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			t.Parallel()

			if b.name == "mysql" {
				t.Skip("MySQL commits DDL")
			}

			f := &faults{}
			db := faultDB(t, b, f)
			svc, _ := started(t, db)

			svc.Register(app, mig("1", "one"))
			require.NoError(t, svc.RunPending())

			f.add("exec", "DELETE FROM __gas_migrations", errBoom)

			err := svc.Down(1)
			require.ErrorIs(t, err, errBoom)
			require.EqualError(
				t,
				err,
				"gas/migrate: revert 1 (owner app): migration failed: remove row: boom",
			)

			f.clear()
			assert.Equal(t, []string{"1"}, versions(rows(t, svc)))

			_, err = db.DB().ExecContext(t.Context(), "SELECT id FROM one")
			require.NoError(t, err, "the down SQL was rolled back")
		})
	}
}
