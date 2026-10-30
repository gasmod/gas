package database_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/database"
)

// KI-038: Close returned before closing the pgx pool when closing the
// sql.DB failed, leaking the pool. Close now closes the pool whatever
// closing the handle returns, and returns that error. The handle pgx
// derives from the pool keeps no connections of its own, so its close
// cannot be made to fail here; this pins that Close closes both in
// ModePgx, and TestClose_Error that a failed close is returned with the
// service left uninitialized.
func TestKI038_ClosePgxClosesPool(t *testing.T) {
	t.Parallel()

	dsn := postgresDSN(t)
	cfg := fakeConfig(func(s *database.Settings) {
		s.Mode, s.Dialect, s.DSN = database.ModePgx, database.DialectPostgres, dsn
	})
	s, _ := build(t, database.WithConfig(cfg))
	require.NoError(t, s.Init(t.Context()))

	pool, db := s.Pool(), s.DB()
	require.NoError(t, s.Close(t.Context()))

	require.Error(t, pool.Ping(t.Context()), "the pool is closed")
	require.Error(t, db.PingContext(t.Context()), "the handle is closed")
	assert.Nil(t, s.Pool())
}

// KI-043: the health errors were `database: closed` and `database: not
// initialized`, without the service name every other error starts with.
// They now start with `gas/database: `, before Init and after Close.
func TestKI043_HealthErrorsPrefixed(t *testing.T) {
	t.Parallel()

	s, _ := build(t, database.WithConfig(sqliteConfig(t)))

	for _, err := range []error{s.CheckHealth(t.Context()), s.CheckReady(t.Context())} {
		require.EqualError(t, err, "gas/database: service not initialized")
	}

	require.NoError(t, s.Init(t.Context()))
	require.NoError(t, s.Close(t.Context()))

	for _, err := range []error{s.CheckHealth(t.Context()), s.CheckReady(t.Context())} {
		require.EqualError(t, err, "gas/database: service not initialized")
	}
}

// KI-048: MySQL DATETIME and TIMESTAMP values scanned into []byte, not
// time.Time, unless every app remembered `parseTime=true` in its DSN, so
// migrate's tracking table failed to scan. The dialect mysql now turns
// ParseTime on whatever the DSN says.
func TestKI048_MySQLParseTime(t *testing.T) {
	t.Parallel()

	s := mysqlService(t, nil)

	var now time.Time

	require.NoError(t, s.DB().QueryRowContext(t.Context(), "SELECT NOW()").Scan(&now))
	assert.WithinDuration(t, time.Now(), now, time.Hour)
}
