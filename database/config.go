package database

import (
	"errors"
	"fmt"
	"time"

	"github.com/gasmod/gas"
)

// Mode selects the database backend.
const (
	// ModeSQL uses database/sql with any registered driver.
	ModeSQL = "sql"

	// ModePgx uses native pgxpool.Pool for PostgreSQL. DB() still works
	// via the pgx stdlib adapter. Pool() returns the native pool for
	// sqlc pgx mode.
	ModePgx = "pgx"
)

const (
	// DriverPostgres defines the constant for the PostgreSQL database driver.
	DriverPostgres = "postgres"

	// DriverPgx represents the const string identifier for the "pgx" database driver.
	DriverPgx = "pgx"

	// DriverSQLite represents the identifier for the SQLite database driver.
	DriverSQLite = "sqlite"
)

const (
	defaultMode              = ModeSQL
	defaultDriver            = "postgres"
	defaultMaxOpenConns      = 25
	defaultMaxIdleConns      = 5
	defaultConnMaxLifetime   = 30 * time.Minute
	defaultConnMaxIdleTime   = 5 * time.Minute
	defaultPingTimeout       = 5 * time.Second
	defaultConnRetries       = 0
	defaultConnRetryInterval = 2 * time.Second
)

// Config holds database connection settings.
type Config struct {
	gas.WithGasEnv

	Database Settings

	// hasConnector is set internally when a driver.Connector is provided.
	// When true, Driver and DSN are not required in ModeSQL. A connector is
	// rejected outside ModeSQL, so Validate passing implies ModeSQL here.
	hasConnector bool
}

// Settings represents the configuration required to establish and manage database connections.
type Settings struct {
	// Mode selects the backend: ModeSQL (default) or ModePgx.
	Mode string `validate:"oneof=sql pgx"`

	// Driver is the database/sql driver name (e.g., "postgres", "pgx", "sqlite").
	// Only used in ModeSQL.
	Driver string

	// DSN is the data source name (connection string).
	DSN string

	// MaxOpenConns is the maximum number of open connections to the database.
	MaxOpenConns int32 `validate:"min=1,max=1000"`

	// MaxIdleConns is the maximum number of idle connections in the pool.
	// Only used in ModeSQL; pgx manages idle connections internally.
	MaxIdleConns int

	// ConnMaxLifetime is the maximum amount of time a connection may be reused.
	ConnMaxLifetime time.Duration `validate:"min=1s,max=2h"`

	// ConnMaxIdleTime is the maximum amount of time a connection may be idle.
	ConnMaxIdleTime time.Duration `validate:"min=1s,max=1h,ltefield=ConnMaxLifetime"`

	// ConnRetries is the number of times to retry connecting to the database
	// on failure. 0 means no retries (fail immediately).
	ConnRetries int

	// ConnRetryInterval is the base interval between connection retry attempts.
	// The interval doubles after each failed attempt (exponential backoff).
	ConnRetryInterval time.Duration
}

// DefaultConfig returns a Config with sensible defaults using database/sql.
func DefaultConfig() *Config {
	return &Config{
		Database: Settings{
			Mode:              defaultMode,
			Driver:            defaultDriver,
			MaxOpenConns:      defaultMaxOpenConns,
			MaxIdleConns:      defaultMaxIdleConns,
			ConnMaxLifetime:   defaultConnMaxLifetime,
			ConnMaxIdleTime:   defaultConnMaxIdleTime,
			ConnRetries:       defaultConnRetries,
			ConnRetryInterval: defaultConnRetryInterval,
		},
	}
}

var validDrivers = map[string]bool{DriverPostgres: true, DriverPgx: true, DriverSQLite: true}

// The MaxIdleConns limits, checked in code because they apply only in
// ModeSQL. The other limits are validate tags on Settings.
const (
	minMaxIdleConns = 1
	maxMaxIdleConns = 1000
)

// checkRules checks what the validate tags cannot: the rules that depend on
// the mode or on a connector passed with WithConnector, which is not a config
// field. The mode, the open-connection range, and the lifetime and idle-time
// ranges are tags.
func checkRules(cfg *Config) error {
	if err := cfg.validateSource(); err != nil {
		return err
	}
	return cfg.validateIdleConns()
}

// validateSource checks the connector, driver and DSN settings.
func (c *Config) validateSource() error {
	// A driver.Connector is a database/sql concept. initPgx never consults
	// one and builds its pool from the DSN alone, so accepting the pair
	// would drop the connector silently and connect somewhere the caller
	// did not ask for.
	if c.Database.Mode != ModeSQL && c.hasConnector {
		return fmt.Errorf(
			"WithConnector is only supported in Database.Mode %q, got %q",
			ModeSQL,
			c.Database.Mode,
		)
	}
	if c.Database.Mode == ModeSQL && !c.hasConnector && !validDrivers[c.Database.Driver] {
		return fmt.Errorf(
			"Database.Driver must be one of [%s, %s, %s], got %q",
			DriverPostgres,
			DriverPgx,
			DriverSQLite,
			c.Database.Driver,
		)
	}
	// Only reachable with hasConnector in ModeSQL, per the check above.
	if c.Database.DSN == "" && !c.hasConnector {
		return errors.New("Database.DSN must not be empty")
	}
	return nil
}

// validateIdleConns checks the idle-connection limit, which only ModeSQL uses:
// pgx manages idle connections itself.
func (c *Config) validateIdleConns() error {
	if c.Database.Mode != ModeSQL {
		return nil
	}
	if c.Database.MaxIdleConns < minMaxIdleConns || c.Database.MaxIdleConns > maxMaxIdleConns {
		return fmt.Errorf(
			"Database.MaxIdleConns must be between %d and %d, got %d",
			minMaxIdleConns,
			maxMaxIdleConns,
			c.Database.MaxIdleConns,
		)
	}
	if c.Database.MaxIdleConns > int(c.Database.MaxOpenConns) {
		return fmt.Errorf(
			"Database.MaxIdleConns (%d) must not exceed Database.MaxOpenConns (%d)",
			c.Database.MaxIdleConns,
			c.Database.MaxOpenConns,
		)
	}
	return nil
}
