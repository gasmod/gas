package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth/internal/testutil"
)

// SetupSQLite takes a testing.TB, so a fuzz target can call it with its
// *testing.F, and returns working database and migration services on an
// in-memory database.
func FuzzSetupSQLite(f *testing.F) {
	rawDB, db, migrations := testutil.SetupSQLite(f)
	require.NotNil(f, migrations)
	assert.Same(f, rawDB, db.DB())

	f.Add(1)
	f.Fuzz(func(t *testing.T, n int) {
		var got int

		require.NoError(t, rawDB.QueryRowContext(t.Context(), "SELECT ?", n).Scan(&got))
		assert.Equal(t, n, got)
	})
}
