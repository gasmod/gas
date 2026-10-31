package db

import (
	_ "embed" // the migrations are embedded.
	"fmt"

	"github.com/gasmod/gas"
)

// The migrations of each dialect, as described in the package
// documentation.
var (
	//go:embed postgres/20260322235959_create_templates.up.sql
	postgresCreateUp string
	//go:embed postgres/20260322235959_create_templates.down.sql
	postgresCreateDown string
	//go:embed postgres/20261031120000_exact_template_names.up.sql
	postgresExactUp string
	//go:embed postgres/20261031120000_exact_template_names.down.sql
	postgresExactDown string

	//go:embed mysql/20260322235959_create_templates.up.sql
	mysqlCreateUp string
	//go:embed mysql/20260322235959_create_templates.down.sql
	mysqlCreateDown string
	//go:embed mysql/20261031120000_exact_template_names.up.sql
	mysqlExactUp string
	//go:embed mysql/20261031120000_exact_template_names.down.sql
	mysqlExactDown string

	//go:embed sqlite/20260322235959_create_templates.up.sql
	sqliteCreateUp string
	//go:embed sqlite/20260322235959_create_templates.down.sql
	sqliteCreateDown string
	//go:embed sqlite/20261031120000_exact_template_names.up.sql
	sqliteExactUp string
	//go:embed sqlite/20261031120000_exact_template_names.down.sql
	sqliteExactDown string
)

// The versions and descriptions of the migrations, the same in every
// dialect.
const (
	// createVersion creates the table.
	createVersion = "20260322235959"
	// createDescription describes createVersion.
	createDescription = "create templates table"
	// exactVersion makes names exact and drops the redundant index.
	exactVersion = "20261031120000"
	// exactDescription describes exactVersion.
	exactDescription = "exact template names"
)

// maxName is the most characters of a name or a namespace, on every
// dialect: MySQL's columns hold 255, and on PostgreSQL 255 four-byte
// characters of each keep a row of the unique index under its B-tree
// limit, as described in the package documentation.
const maxName = 255

// dialect is the SQL of one dialect.
type dialect struct {
	// name is the dialect, as the provider's Driver reports it.
	name string
	// get selects a template's content, or NULL when it is longer than
	// the store's maximum size, and its length in bytes: maximum size,
	// namespace, name.
	get string
	// list selects the names of a namespace's templates, in no order.
	list string
	// upsert inserts or replaces a template: namespace, name, content.
	upsert string
	// count counts the templates of a name: namespace, name.
	count string
	// remove deletes a template: namespace, name.
	remove string
	// migrations are the table's migrations, without their owner.
	migrations []gas.Migration
}

// The dialects, by the names gas/database reports.
const (
	// dialectPostgres is PostgreSQL.
	dialectPostgres = "postgres"
	// dialectMySQL is MySQL.
	dialectMySQL = "mysql"
	// dialectSQLite is SQLite.
	dialectSQLite = "sqlite"
)

// migrations returns the two migrations of a dialect.
func migrations(createUp, createDown, exactUp, exactDown string) []gas.Migration {
	return []gas.Migration{
		{Version: createVersion, Description: createDescription, Up: createUp, Down: createDown},
		{Version: exactVersion, Description: exactDescription, Up: exactUp, Down: exactDown},
	}
}

// postgres is PostgreSQL.
var postgres = &dialect{
	name: dialectPostgres,
	get: "SELECT CASE WHEN octet_length(content) > $1 THEN NULL ELSE content END," +
		" octet_length(content) FROM " + TableName + " WHERE namespace = $2 AND name = $3",
	list: "SELECT name FROM " + TableName + " WHERE namespace = $1",
	upsert: "INSERT INTO " + TableName + " (namespace, name, content, created_at, updated_at)" +
		" VALUES ($1, $2, $3, NOW(), NOW())" +
		" ON CONFLICT (namespace, name) DO UPDATE SET content = EXCLUDED.content, updated_at = NOW()",
	count:  "SELECT COUNT(*) FROM " + TableName + " WHERE namespace = $1 AND name = $2",
	remove: "DELETE FROM " + TableName + " WHERE namespace = $1 AND name = $2",
	migrations: migrations(
		postgresCreateUp,
		postgresCreateDown,
		postgresExactUp,
		postgresExactDown,
	),
}

// mysql is MySQL. The upsert names the inserted row with an alias, since
// the VALUES() function is deprecated.
var mysql = &dialect{
	name: dialectMySQL,
	get: "SELECT CASE WHEN OCTET_LENGTH(content) > ? THEN NULL ELSE content END," +
		" OCTET_LENGTH(content) FROM " + TableName + " WHERE namespace = ? AND name = ?",
	list: "SELECT name FROM " + TableName + " WHERE namespace = ?",
	upsert: "INSERT INTO " + TableName + " (namespace, name, content, created_at, updated_at)" +
		" VALUES (?, ?, ?, NOW(), NOW()) AS new" +
		" ON DUPLICATE KEY UPDATE content = new.content, updated_at = NOW()",
	count:      "SELECT COUNT(*) FROM " + TableName + " WHERE namespace = ? AND name = ?",
	remove:     "DELETE FROM " + TableName + " WHERE namespace = ? AND name = ?",
	migrations: migrations(mysqlCreateUp, mysqlCreateDown, mysqlExactUp, mysqlExactDown),
}

// sqlite is SQLite. A BLOB's length is its bytes; the cast makes a TEXT
// value, which length counts in characters, a BLOB first.
var sqlite = &dialect{
	name: dialectSQLite,
	get: "SELECT CASE WHEN length(CAST(content AS BLOB)) > ? THEN NULL ELSE content END," +
		" length(CAST(content AS BLOB)) FROM " + TableName + " WHERE namespace = ? AND name = ?",
	list: "SELECT name FROM " + TableName + " WHERE namespace = ?",
	upsert: "INSERT INTO " + TableName + " (namespace, name, content, created_at, updated_at)" +
		" VALUES (?, ?, ?, datetime('now'), datetime('now'))" +
		" ON CONFLICT (namespace, name) DO UPDATE SET content = excluded.content," +
		" updated_at = datetime('now')",
	count:      "SELECT COUNT(*) FROM " + TableName + " WHERE namespace = ? AND name = ?",
	remove:     "DELETE FROM " + TableName + " WHERE namespace = ? AND name = ?",
	migrations: migrations(sqliteCreateUp, sqliteCreateDown, sqliteExactUp, sqliteExactDown),
}

// supportedDrivers names the drivers dialectFor accepts, for its error.
const supportedDrivers = "postgres, mysql and sqlite"

// dialectFor returns the dialect of a provider's driver name, or an error
// wrapping ErrUnsupportedDriver that lists the supported names, not the
// name given.
func dialectFor(driver string) (*dialect, error) {
	switch driver {
	case dialectPostgres:
		return postgres, nil
	case dialectMySQL:
		return mysql, nil
	case dialectSQLite:
		return sqlite, nil
	default:
		return nil, fmt.Errorf("%w: supported are %s", ErrUnsupportedDriver, supportedDrivers)
	}
}
