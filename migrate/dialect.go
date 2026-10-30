package migrate

import (
	"fmt"
	"strings"
)

// The dialects, by the names gas/database reports.
const (
	// dialectPostgres is PostgreSQL.
	dialectPostgres = "postgres"
	// dialectMySQL is MySQL.
	dialectMySQL = "mysql"
	// dialectSQLite is SQLite.
	dialectSQLite = "sqlite"
)

// PostgresLockKey is the key of the session-level advisory lock the
// service holds on PostgreSQL while it changes the schema, as described
// in the package documentation. It spells `gas_migr` in ASCII.
const PostgresLockKey int64 = 0x6761735f6d696772

// MySQLLockPrefix starts the name of the user-level lock the service holds
// on MySQL while it changes the schema; the MD5 of the database name, in
// hex, follows it, as described in the package documentation.
const MySQLLockPrefix = "gas/migrate/"

// dialect is the SQL of one dialect: the statements on the tracking table
// and how the migration lock and a migration's transaction work.
type dialect struct {
	// lock is how the migration lock is taken and released, or nil where
	// each write takes the database's write lock itself.
	lock locker
	// name is the dialect, as Init logs it.
	name string
	// create creates the tracking table when it does not exist.
	create string
	// selectRows reads the tracking table, never applied_at.
	selectRows string
	// insert records a version as applied: version, service, description.
	insert string
	// insertDirty records a version as dirty, unless it has a row.
	insertDirty string
	// claim records a version as dirty before its SQL runs, failing when
	// it has a row.
	claim string
	// markClean clears a version's dirty mark.
	markClean string
	// markDirty sets a version's dirty mark.
	markDirty string
	// delete removes a version's row.
	delete string
	// clearDirty removes a version's row when it is dirty.
	clearDirty string
	// split sends a migration as its statements, one at a time, as the
	// session's SQL mode reads them.
	split bool
	// writeAhead marks a migration dirty before its SQL runs, and clean
	// once it succeeded, since a failure can leave part of it applied.
	writeAhead bool
	// reuseConn returns a call's connection to the pool, unless its state
	// is unknown, instead of closing it: closing a connection to an
	// in-memory SQLite database loses the database.
	reuseConn bool
	// noTransaction supports the no-transaction directive.
	noTransaction bool
}

// supportedDrivers names the drivers dialectFor accepts, for its error.
const supportedDrivers = "postgres, pgx, mysql, sqlite and sqlite3"

// dialectFor returns the dialect of a provider's driver name, or an error
// wrapping ErrUnsupportedDriver that lists the supported names, not the
// name given.
func dialectFor(driver string) (*dialect, error) {
	switch driver {
	case dialectPostgres, "pgx":
		return postgres, nil
	case dialectMySQL:
		return mysql, nil
	case dialectSQLite, "sqlite3":
		return sqlite, nil
	default:
		return nil, fmt.Errorf(
			"%s: %w: supported are %s",
			ServiceName,
			ErrUnsupportedDriver,
			supportedDrivers,
		)
	}
}

// columns are the tracking table's columns that a row is inserted with;
// migrate_version and module_version are written empty.
const columns = "(version, service, description, migrate_version, module_version, dirty)"

// selectRows is the query of the tracking table's rows, the same in every
// dialect.
const selectRows = "SELECT version, service, description, dirty FROM " + TableName

// postgres is PostgreSQL.
var postgres = &dialect{
	name: dialectPostgres,
	create: "CREATE TABLE IF NOT EXISTS " + TableName + ` (
	version         TEXT PRIMARY KEY,
	service         TEXT        NOT NULL,
	description     TEXT        NOT NULL DEFAULT '',
	migrate_version TEXT        NOT NULL DEFAULT '',
	module_version  TEXT        NOT NULL DEFAULT '',
	applied_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	dirty           BOOLEAN     NOT NULL DEFAULT FALSE
)`,
	selectRows: selectRows,
	insert: "INSERT INTO " + TableName + " " + columns +
		" VALUES ($1, $2, $3, '', '', FALSE)",
	insertDirty: "INSERT INTO " + TableName + " " + columns +
		" VALUES ($1, $2, $3, '', '', TRUE) ON CONFLICT (version) DO NOTHING",
	claim: "INSERT INTO " + TableName + " " + columns +
		" VALUES ($1, $2, $3, '', '', TRUE)",
	markClean:     "UPDATE " + TableName + " SET dirty = FALSE WHERE version = $1",
	markDirty:     "UPDATE " + TableName + " SET dirty = TRUE WHERE version = $1",
	delete:        "DELETE FROM " + TableName + " WHERE version = $1",
	clearDirty:    "DELETE FROM " + TableName + " WHERE version = $1 AND dirty",
	lock:          postgresLock{},
	noTransaction: true,
}

// mysql is MySQL.
var mysql = &dialect{
	name: dialectMySQL,
	create: "CREATE TABLE IF NOT EXISTS " + TableName + ` (
	version         VARCHAR(255) PRIMARY KEY,
	service         TEXT         NOT NULL,
	description     TEXT         NOT NULL,
	migrate_version TEXT         NOT NULL,
	module_version  TEXT         NOT NULL,
	applied_at      DATETIME     NOT NULL DEFAULT NOW(),
	dirty           BOOLEAN      NOT NULL DEFAULT FALSE
)`,
	selectRows: selectRows,
	insert: "INSERT INTO " + TableName + " " + columns +
		" VALUES (?, ?, ?, '', '', FALSE)",
	insertDirty: "INSERT INTO " + TableName + " " + columns +
		" VALUES (?, ?, ?, '', '', TRUE) ON DUPLICATE KEY UPDATE version = version",
	claim: "INSERT INTO " + TableName + " " + columns +
		" VALUES (?, ?, ?, '', '', TRUE)",
	markClean:     "UPDATE " + TableName + " SET dirty = FALSE WHERE version = ?",
	markDirty:     "UPDATE " + TableName + " SET dirty = TRUE WHERE version = ?",
	delete:        "DELETE FROM " + TableName + " WHERE version = ?",
	clearDirty:    "DELETE FROM " + TableName + " WHERE version = ? AND dirty",
	lock:          mysqlLock{},
	split:         true,
	writeAhead:    true,
	noTransaction: true,
}

// sqlite is SQLite.
var sqlite = &dialect{
	name: dialectSQLite,
	create: "CREATE TABLE IF NOT EXISTS " + TableName + ` (
	version         TEXT PRIMARY KEY,
	service         TEXT    NOT NULL,
	description     TEXT    NOT NULL DEFAULT '',
	migrate_version TEXT    NOT NULL DEFAULT '',
	module_version  TEXT    NOT NULL DEFAULT '',
	applied_at      TEXT    NOT NULL DEFAULT (datetime('now')),
	dirty           INTEGER NOT NULL DEFAULT 0
)`,
	selectRows: selectRows,
	insert: "INSERT INTO " + TableName + " " + columns +
		" VALUES (?, ?, ?, '', '', 0)",
	insertDirty: "INSERT INTO " + TableName + " " + columns +
		" VALUES (?, ?, ?, '', '', 1) ON CONFLICT (version) DO NOTHING",
	claim: "INSERT INTO " + TableName + " " + columns +
		" VALUES (?, ?, ?, '', '', 1)",
	markClean:  "UPDATE " + TableName + " SET dirty = 0 WHERE version = ?",
	markDirty:  "UPDATE " + TableName + " SET dirty = 1 WHERE version = ?",
	delete:     "DELETE FROM " + TableName + " WHERE version = ?",
	clearDirty: "DELETE FROM " + TableName + " WHERE version = ? AND dirty",
	reuseConn:  true,
}

// isBlank reports whether sql holds nothing but white space.
func isBlank(sql string) bool {
	return strings.TrimSpace(sql) == ""
}
