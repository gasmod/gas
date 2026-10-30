package sqlutil_test

import (
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth/internal/sqlutil"
)

// DialectOf maps each driver name the stores have always accepted to its
// dialect, and names the dialect in lowercase.
func TestDialectOf_KnownDrivers(t *testing.T) {
	t.Parallel()

	for driver, want := range map[string]sqlutil.Dialect{
		"postgres": sqlutil.Postgres,
		"pgx":      sqlutil.Postgres,
		"mysql":    sqlutil.MySQL,
		"sqlite":   sqlutil.SQLite,
		"sqlite3":  sqlutil.SQLite,
	} {
		got, err := sqlutil.DialectOf(driver)
		require.NoError(t, err, driver)
		assert.Equal(t, want, got, driver)
	}

	assert.Equal(t, "postgres", sqlutil.Postgres.String())
	assert.Equal(t, "mysql", sqlutil.MySQL.String())
	assert.Equal(t, "sqlite", sqlutil.SQLite.String())
	assert.Equal(t, "unknown", sqlutil.Dialect(0).String())
}

// Names are matched exactly, and any other name is an error that lists the
// known names and not the one it got, which came from the configuration.
func TestDialectOf_UnknownDriver(t *testing.T) {
	t.Parallel()

	for _, driver := range []string{"", "Postgres", "mssql", "secret-driver-name"} {
		d, err := sqlutil.DialectOf(driver)
		require.ErrorIs(t, err, sqlutil.ErrUnsupportedDriver)
		require.EqualError(t, err,
			"unsupported database driver: want one of postgres, pgx, mysql, sqlite, sqlite3")
		assert.Equal(t, sqlutil.Dialect(0), d)

		if driver != "" {
			assert.NotContains(t, err.Error(), driver)
		}
	}
}

// FormatSQLiteTime writes the time in UTC to the second, and
// ParseSQLiteTime reads it back as the same instant, in UTC.
func TestSQLiteTime_RoundTrip(t *testing.T) {
	t.Parallel()

	zone := time.FixedZone("EET", 2*60*60)
	in := time.Date(2026, 10, 30, 14, 5, 9, 987654321, zone)

	s := sqlutil.FormatSQLiteTime(in)
	assert.Equal(t, "2026-10-30 12:05:09", s)

	got, err := sqlutil.ParseSQLiteTime(s)
	require.NoError(t, err)
	assert.True(t, got.Equal(in.Truncate(time.Second)))
	assert.Equal(t, time.UTC, got.Location())
}

// ParseSQLiteTime reads the text SQLite's datetime('now') writes, and, like
// time.Parse, a fractional second after the seconds.
func TestParseSQLiteTime_Accepted(t *testing.T) {
	t.Parallel()

	got, err := sqlutil.ParseSQLiteTime("2026-01-02 03:04:05")
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), got)

	got, err = sqlutil.ParseSQLiteTime("2026-01-02 03:04:05.25")
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 1, 2, 3, 4, 5, 250_000_000, time.UTC), got)
}

// Text in any other form is an error, never a zero time, and the error
// does not include the text.
func TestParseSQLiteTime_Invalid(t *testing.T) {
	t.Parallel()

	for _, s := range []string{"", "yesterday", "2026-01-02T03:04:05Z", "2026-13-02 03:04:05", "hunter2"} {
		got, err := sqlutil.ParseSQLiteTime(s)
		require.ErrorIs(t, err, sqlutil.ErrInvalidTime, s)
		require.EqualError(t, err, "invalid stored time: want the layout 2006-01-02 15:04:05")
		assert.True(t, got.IsZero())

		if s != "" {
			assert.NotContains(t, err.Error(), s)
		}
	}
}

// The pointer variants map nil to nil and otherwise behave as the plain
// ones.
func TestSQLiteTimePtr(t *testing.T) {
	t.Parallel()

	assert.Nil(t, sqlutil.FormatSQLiteTimePtr(nil))

	got, err := sqlutil.ParseSQLiteTimePtr(nil)
	require.NoError(t, err)
	assert.Nil(t, got)

	in := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	s := sqlutil.FormatSQLiteTimePtr(&in)
	require.NotNil(t, s)
	assert.Equal(t, "2026-05-06 07:08:09", *s)

	got, err = sqlutil.ParseSQLiteTimePtr(s)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, in, *got)

	bad := "not a time"

	got, err = sqlutil.ParseSQLiteTimePtr(&bad)
	require.ErrorIs(t, err, sqlutil.ErrInvalidTime)
	assert.Nil(t, got)
}

// NullTimeFrom is valid exactly when the pointer is not nil, and
// TimeFromNull returns a new pointer for a valid value and nil otherwise.
func TestNullTime(t *testing.T) {
	t.Parallel()

	assert.Equal(t, sql.NullTime{}, sqlutil.NullTimeFrom(nil))
	assert.Nil(t, sqlutil.TimeFromNull(sql.NullTime{}))

	in := time.Date(2026, 5, 6, 7, 8, 9, 10, time.UTC)
	n := sqlutil.NullTimeFrom(&in)
	assert.Equal(t, sql.NullTime{Time: in, Valid: true}, n)

	out := sqlutil.TimeFromNull(n)
	require.NotNil(t, out)
	assert.Equal(t, in, *out)

	*out = out.Add(time.Hour)

	assert.Equal(t, in, n.Time, "the pointer shares nothing with its argument")
}

// TruncateToSecond drops the fraction, keeping the location, so the
// result is never later than its argument, and leaves a whole second as
// it is. A fraction of .9s goes down, not up as MySQL would round it.
func TestTruncateToSecond(t *testing.T) {
	t.Parallel()

	zone := time.FixedZone("EET", 2*60*60)
	second := time.Date(2026, 5, 6, 7, 8, 9, 0, zone)

	for _, frac := range []time.Duration{
		0, time.Nanosecond, 400 * time.Millisecond, 500 * time.Millisecond,
		900 * time.Millisecond, time.Second - time.Nanosecond,
	} {
		in := second.Add(frac)
		got := sqlutil.TruncateToSecond(in)
		assert.Equal(t, second, got, "%v", frac)
		assert.False(t, got.After(in), "%v", frac)
		assert.Equal(t, zone, got.Location(), "%v", frac)
	}
}

// TruncateToSecondPtr maps nil to nil and otherwise returns a new pointer
// to the truncated time.
func TestTruncateToSecondPtr(t *testing.T) {
	t.Parallel()

	assert.Nil(t, sqlutil.TruncateToSecondPtr(nil))

	in := time.Date(2026, 5, 6, 7, 8, 9, 900_000_000, time.UTC)
	got := sqlutil.TruncateToSecondPtr(&in)
	require.NotNil(t, got)
	assert.Equal(t, time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC), *got)
	assert.NotSame(t, &in, got)
	assert.Equal(t, 900_000_000, in.Nanosecond(), "the argument is unchanged")
}

// errShape is the only error ParseSQLiteTime returns, so an error can
// never quote its input.
var errShape = regexp.MustCompile(`^invalid stored time: want the layout 2006-01-02 15:04:05$`)

// FuzzParseSQLiteTime checks that ParseSQLiteTime never panics, is
// deterministic, returns a UTC time that formats back to the input's
// instant when it succeeds, and returns only the documented error, so the
// error never quotes the input.
func FuzzParseSQLiteTime(f *testing.F) {
	for _, seed := range []string{
		"", "2026-01-02 03:04:05", "2026-01-02 03:04:05.999", "0000-00-00 00:00:00",
		"9999-12-31 23:59:59", "2026-01-02T03:04:05Z", "hunter2", "\x00\xff",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		got, err := sqlutil.ParseSQLiteTime(s)
		again, againErr := sqlutil.ParseSQLiteTime(s)

		assert.Equal(t, got, again)
		assert.Equal(t, err == nil, againErr == nil)

		if err != nil {
			assert.Regexp(t, errShape, err.Error())
			assert.True(t, got.IsZero())

			return
		}

		assert.Equal(t, time.UTC, got.Location())

		back, err := sqlutil.ParseSQLiteTime(sqlutil.FormatSQLiteTime(got))
		require.NoError(t, err)
		assert.True(t, back.Equal(got.Truncate(time.Second)))
	})
}
