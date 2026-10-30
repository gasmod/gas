package migrate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/migrate"
)

// CheckReady is nil when every registered migration has a clean row, and
// otherwise reports, in order, a registration problem, a dirty row, then
// the pending count.
func TestCheckReady_Order(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)
	require.NoError(t, e.svc.CheckReady(t.Context()), "nothing registered is ready")

	e.svc.RegisterSlice(app, []gas.Migration{mig("001", "a"), mig("002", "b")})

	err := e.svc.CheckReady(t.Context())
	require.ErrorIs(t, err, migrate.ErrPending)
	require.EqualError(t, err, "gas/migrate: pending migrations: 2")

	require.NoError(t, e.svc.RunPending())
	require.NoError(t, e.svc.CheckReady(t.Context()))

	markDirty(t, e, "003")
	e.svc.Register(app, mig("004", "d"))
	require.EqualError(t, e.svc.CheckReady(t.Context()), "gas/migrate: dirty migrations: 003")

	e.svc.Register(app, mig("", "e"))
	require.ErrorIs(t, e.svc.CheckReady(t.Context()), migrate.ErrInvalidVersion)
}

// Applied returns the rows in version order, numeric for all-digit
// versions, with the dirty mark; rows that mix the two kinds put the
// all-digit versions first.
func TestApplied_Order(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)

	for _, v := range []string{"b", "10", "9", "a", "010"} {
		insertRow(t, e, v, "app", v == "a")
	}

	got := rows(t, e.svc)
	assert.Equal(t, []string{"9", "010", "10", "a", "b"}, versions(got))
	assert.True(t, got[3].Dirty)
}

// Versions that are not digits are ordered by their text.
func TestVersions_Text(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)

	e.svc.RegisterSlice(app, []gas.Migration{
		{Version: "v2", Up: "ALTER TABLE x ADD COLUMN b INT"},
		{Version: "v1", Up: "CREATE TABLE x (a INT)"},
	})
	require.NoError(t, e.svc.RunPending())
	assert.Equal(t, []string{"v1", "v2"}, versions(rows(t, e.svc)))
}

// One value written two ways, `1` and `01`, is two versions, ordered by
// text.
func TestVersions_LeadingZeros(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t)

	e.svc.RegisterSlice(app, []gas.Migration{
		{Version: "1", Up: "ALTER TABLE x ADD COLUMN b INT"},
		{Version: "01", Up: "CREATE TABLE x (a INT)"},
		{Version: "002", Up: "ALTER TABLE x ADD COLUMN c INT"},
	})
	require.NoError(t, e.svc.RunPending())
	assert.Equal(t, []string{"01", "1", "002"}, versions(rows(t, e.svc)))
}
