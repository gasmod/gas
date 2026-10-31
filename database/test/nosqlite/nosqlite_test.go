// Package nosqlite_test runs in a test binary without a `sqlite` driver,
// which the main test package cannot, since it imports one.
package nosqlite_test

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/database"
)

// Without a driver registered as `sqlite`, Init of the dialect sqlite
// fails at once with ErrDriverNotRegistered, naming the import, and is not
// retried. The error is the one the package documentation lists.
func TestInit_SQLiteNotRegistered(t *testing.T) {
	t.Parallel()

	require.False(t, slices.Contains(sql.Drivers(), "sqlite"), "no sqlite driver in this binary")

	cfg := database.DefaultConfig()

	cfg.Database.Dialect = database.DialectSQLite
	cfg.Database.DSN = gas.Secret(filepath.Join(t.TempDir(), "test.db"))
	cfg.Database.ConnRetries = 3

	s, err := database.New(database.WithConfig(cfg))(nil, &gas.NopLogger{})
	require.NoError(t, err)

	start := time.Now()

	err = s.Init(t.Context())

	require.ErrorIs(t, err, database.ErrDriverNotRegistered)
	require.EqualError(t, err,
		"gas/database: driver not registered: dialect sqlite needs the import modernc.org/sqlite")
	assert.Less(t, time.Since(start), time.Second)
	assert.Nil(t, s.DB())
}
