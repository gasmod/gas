package database_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/database"
)

// The dialect sqlite adds DefaultSQLiteBusyTimeout to a DSN that sets no
// busy timeout, in either DSN form and beside other parameters, and keeps
// one the DSN sets in any of the three ways modernc.org/sqlite reads.
func TestSQLite_BusyTimeout(t *testing.T) {
	t.Parallel()

	assert.Equal(t, int64(5000), database.DefaultSQLiteBusyTimeout.Milliseconds())

	cases := []struct {
		query string
		want  int64
	}{
		{query: "", want: 5000},
		{query: "?", want: 5000},
		{query: "?_pragma=foreign_keys(1)", want: 5000},
		{query: "?_pragma=busy_timeout(0)", want: 0},
		{query: "?_pragma=%20BUSY_TIMEOUT=250", want: 250},
		{query: "?_busy_timeout=300", want: 300},
		{query: "?_timeout=400&_pragma=foreign_keys(1)", want: 400},
	}

	for _, c := range cases {
		for _, prefix := range []string{"", "file:"} {
			cfg := database.DefaultConfig()

			cfg.Database.Dialect = database.DialectSQLite
			cfg.Database.DSN = prefix + filepath.Join(t.TempDir(), "app.db") + c.query

			s := initialized(t, database.WithConfig(cfg))

			var got int64

			require.NoError(
				t,
				s.DB().QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&got),
				cfg.Database.DSN,
			)
			assert.Equal(t, c.want, got, cfg.Database.DSN)
		}
	}

	cfg := database.DefaultConfig()

	cfg.Database.Dialect = database.DialectSQLite
	cfg.Database.DSN = filepath.Join(t.TempDir(), "app.db") + "?_pragma=foreign_keys(1)"

	s := initialized(t, database.WithConfig(cfg))

	var fk int

	require.NoError(t, s.DB().QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&fk))
	assert.Equal(t, 1, fk, "the DSN's own parameters still apply")
}

// A DSN whose query does not parse is left as it is, and the driver
// rejects it.
func TestSQLite_BusyTimeoutBadQuery(t *testing.T) {
	t.Parallel()

	cfg := database.DefaultConfig()

	cfg.Database.Dialect = database.DialectSQLite
	cfg.Database.DSN = filepath.Join(t.TempDir(), "app.db") + "?_pragma=%zz"

	require.Error(t, initErr(t, database.WithConfig(cfg)))
}
