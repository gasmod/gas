package sqlutil

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrInvalidTime indicates that a stored SQLite time is not in
// SQLiteTimeLayout.
var ErrInvalidTime = errors.New("invalid stored time")

// SQLiteTimeLayout is the layout the stores keep SQLite times in, the form
// SQLite's `datetime('now')` writes.
const SQLiteTimeLayout = "2006-01-02 15:04:05"

// FormatSQLiteTime returns t in UTC, formatted with SQLiteTimeLayout. It
// drops anything finer than a second.
func FormatSQLiteTime(t time.Time) string {
	return t.UTC().Format(SQLiteTimeLayout)
}

// ParseSQLiteTime parses s, written in SQLiteTimeLayout, as a UTC time.
// Text in any other form is an error wrapping ErrInvalidTime that does not
// include s.
func ParseSQLiteTime(s string) (time.Time, error) {
	t, err := time.Parse(SQLiteTimeLayout, s)
	if err != nil {
		// time.Parse quotes its input, so its error is replaced.
		return time.Time{}, fmt.Errorf("%w: want the layout %s", ErrInvalidTime, SQLiteTimeLayout)
	}

	return t, nil
}

// FormatSQLiteTimePtr is FormatSQLiteTime for a nullable column: it
// returns nil for a nil t.
func FormatSQLiteTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}

	return new(FormatSQLiteTime(*t))
}

// ParseSQLiteTimePtr is ParseSQLiteTime for a nullable column: it returns
// nil, and no error, for a nil s.
func ParseSQLiteTimePtr(s *string) (*time.Time, error) {
	if s == nil {
		return nil, nil
	}

	t, err := ParseSQLiteTime(*s)
	if err != nil {
		return nil, err
	}

	return &t, nil
}

// TruncateToSecond returns t without anything finer than a second, the
// time a MySQL `DATETIME` column of whole seconds keeps. Truncating
// before a write keeps MySQL from rounding the fraction up, so a stored
// time is never later than the one written.
func TruncateToSecond(t time.Time) time.Time {
	return t.Truncate(time.Second)
}

// TruncateToSecondPtr is TruncateToSecond for a nullable column: it
// returns nil for a nil t, and otherwise a new pointer that shares
// nothing with t.
func TruncateToSecondPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	return new(TruncateToSecond(*t))
}

// NullTimeFrom returns t as a sql.NullTime, valid when t is not nil.
func NullTimeFrom(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}

	return sql.NullTime{Time: *t, Valid: true}
}

// TimeFromNull returns a new pointer to n's time when n is valid, and nil
// otherwise.
func TimeFromNull(n sql.NullTime) *time.Time {
	if !n.Valid {
		return nil
	}

	return new(n.Time)
}
