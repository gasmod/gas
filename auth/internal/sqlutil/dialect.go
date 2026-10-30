package sqlutil

import (
	"errors"
	"fmt"
)

// ErrUnsupportedDriver indicates that a database driver name maps to no
// dialect the stores support.
var ErrUnsupportedDriver = errors.New("unsupported database driver")

// Dialect is the SQL dialect a database speaks, as described in the
// package documentation.
type Dialect int

const (
	// Postgres is PostgreSQL.
	Postgres Dialect = iota + 1
	// MySQL is MySQL.
	MySQL
	// SQLite is SQLite.
	SQLite
)

// supportedDrivers lists the driver names DialectOf accepts, in the order
// its error names them.
const supportedDrivers = "postgres, pgx, mysql, sqlite, sqlite3"

// String returns the dialect's lowercase name, `postgres`, `mysql` or
// `sqlite`, or `unknown` for any other value.
func (d Dialect) String() string {
	switch d {
	case Postgres:
		return "postgres"
	case MySQL:
		return "mysql"
	case SQLite:
		return "sqlite"
	default:
		return "unknown"
	}
}

// DialectOf returns the dialect of a database/sql driver name. A name it
// does not know is an error wrapping ErrUnsupportedDriver, which lists the
// names it knows and not the one it got.
func DialectOf(driver string) (Dialect, error) {
	switch driver {
	case "postgres", "pgx":
		return Postgres, nil
	case "mysql":
		return MySQL, nil
	case "sqlite", "sqlite3":
		return SQLite, nil
	default:
		return 0, fmt.Errorf("%w: want one of %s", ErrUnsupportedDriver, supportedDrivers)
	}
}
