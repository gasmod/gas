// Package sqlutil holds what the auth module's stores share about SQL
// databases: which dialect a driver speaks, how SQLite and MySQL store a
// time, and how a nullable time maps to a pointer.
//
// # Usage
//
// A store picks its dialect from the database provider's driver name, then
// converts times on the way in and out:
//
//	d, err := sqlutil.DialectOf(db.Driver())
//	if err != nil {
//		return err
//	}
//	if d == sqlutil.SQLite {
//		stored := sqlutil.FormatSQLiteTime(expiresAt)
//		// ... write stored, read it back ...
//		expiresAt, err = sqlutil.ParseSQLiteTime(stored)
//	}
//
// # Dialects
//
// A [Dialect] is one of [Postgres], [MySQL] and [SQLite]; the zero
// Dialect is none of them. [DialectOf] maps a database/sql driver name to
// its dialect: `postgres` and `pgx` are Postgres, `mysql` is MySQL, and
// `sqlite` and `sqlite3` are SQLite. Names are matched exactly, so
// `Postgres` is not one of them. Any other name is an error.
//
// # SQLite times
//
// SQLite has no time type, so the stores keep times as text in
// [SQLiteTimeLayout], `2006-01-02 15:04:05`, the form SQLite's own
// `datetime('now')` writes. [FormatSQLiteTime] converts a time to UTC and
// formats it, dropping anything finer than a second. [ParseSQLiteTime]
// reads such a text back as a UTC time; like [time.Parse], it accepts a
// fractional second after the seconds. Text in any other form is an
// error, never a zero time, so a damaged row cannot read as long expired.
// [FormatSQLiteTimePtr] and [ParseSQLiteTimePtr] do the same for a
// nullable column, mapping nil to nil.
//
// MySQL and Postgres scan times into [time.Time] themselves. For MySQL,
// that needs `parseTime=true` in the DSN.
//
// # MySQL times
//
// The stores keep MySQL times in `DATETIME` columns of whole seconds.
// MySQL rounds a fraction to the nearest second by default, which would
// store a time up to half a second later than the one written, so an
// expiry could pass late. The MySQL stores pass every time they write,
// and every cutoff they compare with, through [TruncateToSecond] first,
// which drops anything finer than a second as SQLite does. A stored time
// is then up to a second earlier than the one written, never later.
// [TruncateToSecondPtr] does the same for a nullable column, mapping nil
// to nil.
//
// # Null times
//
// [NullTimeFrom] turns a *time.Time into a [sql.NullTime], valid when the
// pointer is not nil, and [TimeFromNull] turns it back. The pointer
// TimeFromNull returns is new and shares nothing with its argument.
//
// # Errors
//
// Errors wrap one of the package's sentinel errors, to be tested with
// [errors.Is]:
//
//   - [ErrUnsupportedDriver]: DialectOf got a driver name it does not know.
//   - [ErrInvalidTime]: ParseSQLiteTime or ParseSQLiteTimePtr got text that
//     is not in SQLiteTimeLayout.
//
// Neither error includes the text it got: the driver name comes from the
// configuration, and the time from a stored row. They say what was wanted
// instead:
//
//	unsupported database driver: want one of postgres, pgx, mysql, sqlite, sqlite3
//	invalid stored time: want the layout 2006-01-02 15:04:05
package sqlutil
