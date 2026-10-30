package database

import (
	"errors"
	"time"
)

// Mode selects how the service connects, as described in the package
// documentation.
type Mode string

const (
	// ModeSQL connects through database/sql, with the driver of the
	// dialect or the connector given to WithConnector.
	ModeSQL Mode = "sql"
	// ModePgx connects with a native pgxpool.Pool, for PostgreSQL only.
	// DB is derived from the pool.
	ModePgx Mode = "pgx"
)

// Dialect names the SQL dialect of the database, as described in the
// package documentation. Service.Driver returns it.
type Dialect string

const (
	// DialectPostgres is PostgreSQL, reached through pgx.
	DialectPostgres Dialect = "postgres"
	// DialectMySQL is MySQL, reached through go-sql-driver/mysql.
	DialectMySQL Dialect = "mysql"
	// DialectSQLite is SQLite, reached through the database/sql driver
	// registered as `sqlite`.
	DialectSQLite Dialect = "sqlite"
)

const (
	// DefaultMode is the mode of DefaultConfig.
	DefaultMode = ModeSQL
	// DefaultMaxOpenConns is the open connection limit of DefaultConfig.
	DefaultMaxOpenConns int32 = 25
	// DefaultMaxIdleConns is the idle connection limit of DefaultConfig.
	DefaultMaxIdleConns = 5
	// DefaultConnMaxLifetime is the connection lifetime of DefaultConfig.
	DefaultConnMaxLifetime = 30 * time.Minute
	// DefaultConnMaxIdleTime is the connection idle time of DefaultConfig.
	DefaultConnMaxIdleTime = 5 * time.Minute
	// DefaultConnectTimeout is the per-attempt connect timeout of
	// DefaultConfig.
	DefaultConnectTimeout = 5 * time.Second
	// DefaultConnRetries is the number of connect retries of
	// DefaultConfig.
	DefaultConnRetries = 0
	// DefaultConnRetryInterval is the wait before the first connect retry
	// of DefaultConfig.
	DefaultConnRetryInterval = 2 * time.Second
	// MaxConnRetryInterval is the longest ConnRetryInterval, and caps
	// the doubling of the wait between connect retries.
	MaxConnRetryInterval = 30 * time.Second
)

// minConnRetryInterval is the shortest ConnRetryInterval, checked only
// when ConnRetries is positive. The longest is MaxConnRetryInterval.
const minConnRetryInterval = 100 * time.Millisecond

// Config holds the database service's configuration, read from the
// `database` section.
type Config struct {
	// Database holds the settings.
	Database Settings
}

// Settings are the database service's settings, as described in the
// package documentation.
type Settings struct {
	// Mode is ModeSQL or ModePgx.
	Mode Mode `validate:"oneof=sql pgx"`
	// Dialect is DialectPostgres, DialectMySQL or DialectSQLite. It may be
	// empty only with a connector.
	Dialect Dialect `validate:"omitempty,oneof=postgres mysql sqlite"`
	// DSN is the connection string, in the dialect's own format. It is
	// required without a connector and must be empty with one.
	DSN string
	// ConnMaxLifetime is the longest time a connection is reused.
	ConnMaxLifetime time.Duration `validate:"min=1s,max=2h"`
	// ConnMaxIdleTime is the longest time a connection stays idle. It is
	// at most ConnMaxLifetime.
	ConnMaxIdleTime time.Duration `validate:"min=1s,max=1h,ltefield=ConnMaxLifetime"`
	// ConnectTimeout bounds each connect attempt, open and ping, and each
	// later ping.
	ConnectTimeout time.Duration `validate:"min=1s,max=1m"`
	// ConnRetryInterval is the wait before the first connect retry. It is
	// checked only when ConnRetries is positive.
	ConnRetryInterval time.Duration
	// MaxIdleConns is the most idle connections kept, at most
	// MaxOpenConns. ModeSQL applies it; ModePgx has no such setting.
	MaxIdleConns int `validate:"min=0"`
	// ConnRetries is how many times a failed connect is retried.
	ConnRetries int `validate:"min=0,max=10"`
	// MaxOpenConns is the most open connections.
	MaxOpenConns int32 `validate:"min=1,max=1000"`
}

// DefaultConfig returns the configuration a service starts from before
// binding: DefaultMode, the Default limits and timeouts, no dialect and
// no DSN.
func DefaultConfig() *Config {
	return &Config{
		Database: Settings{
			Mode:              DefaultMode,
			MaxOpenConns:      DefaultMaxOpenConns,
			MaxIdleConns:      DefaultMaxIdleConns,
			ConnMaxLifetime:   DefaultConnMaxLifetime,
			ConnMaxIdleTime:   DefaultConnMaxIdleTime,
			ConnectTimeout:    DefaultConnectTimeout,
			ConnRetries:       DefaultConnRetries,
			ConnRetryInterval: DefaultConnRetryInterval,
		},
	}
}

// The rule errors name the settings, never their values.
var (
	// errNilConnector reports WithConnector given a nil connector.
	errNilConnector = errors.New("WithConnector requires a connector")
	// errConnectorDialect reports WithConnector given a dialect that is
	// not one of the three.
	errConnectorDialect = errors.New(
		"WithConnector requires the dialect postgres, mysql or sqlite",
	)
	// errConnectorMode reports a connector outside ModeSQL.
	errConnectorMode = errors.New("a connector requires Database.Mode sql")
	// errConnectorDialectMismatch reports a Settings.Dialect that differs
	// from the connector's.
	errConnectorDialectMismatch = errors.New(
		"with a connector, Database.Dialect must be empty or the connector's dialect",
	)
	// errConnectorDSN reports a DSN set beside a connector.
	errConnectorDSN = errors.New("with a connector, Database.DSN must be empty")
	// errNoDialect reports no dialect without a connector.
	errNoDialect = errors.New("Database.Dialect is required without a connector")
	// errNoDSN reports no DSN without a connector.
	errNoDSN = errors.New("Database.DSN is required without a connector")
	// errPgxDialect reports ModePgx with a dialect other than postgres.
	errPgxDialect = errors.New("Database.Mode pgx requires Database.Dialect postgres")
	// errIdleConns reports MaxIdleConns above MaxOpenConns.
	errIdleConns = errors.New("Database.MaxIdleConns must not exceed Database.MaxOpenConns")
	// errRetryInterval reports a ConnRetryInterval out of bounds while
	// retries are on.
	errRetryInterval = errors.New(
		"Database.ConnRetryInterval must be 100ms to 30s when Database.ConnRetries is positive",
	)
)

// connectorSpec is what WithConnector recorded.
type connectorSpec struct {
	// dialect is the dialect given with the connector.
	dialect Dialect
	// set reports whether WithConnector was given.
	set bool
	// isNil reports whether the connector given was nil, or a nil
	// pointer, map, slice, func or chan.
	isNil bool
}

// rules returns the rules function for a service with the connector c
// describes: the checks the validate tags cannot express, in the order
// the package documentation lists them.
func rules(c connectorSpec) func(*Config) error {
	return func(cfg *Config) error {
		s := &cfg.Database

		err := checkSource(c, s)
		if err != nil {
			return err
		}

		if s.MaxIdleConns > int(s.MaxOpenConns) {
			return errIdleConns
		}

		if s.ConnRetries > 0 &&
			(s.ConnRetryInterval < minConnRetryInterval || s.ConnRetryInterval > MaxConnRetryInterval) {
			return errRetryInterval
		}

		return nil
	}
}

// checkSource checks the settings that say where to connect: the
// connector's rules when there is one, else the dialect and the DSN, and
// the dialect ModePgx needs.
func checkSource(c connectorSpec, s *Settings) error {
	if c.set {
		return checkConnector(c, s)
	}

	switch {
	case s.Dialect == "":
		return errNoDialect
	case s.DSN == "":
		return errNoDSN
	case s.Mode == ModePgx && s.Dialect != DialectPostgres:
		return errPgxDialect
	}

	return nil
}

// checkConnector checks the connector c describes and the settings that
// must agree with it.
func checkConnector(c connectorSpec, s *Settings) error {
	switch {
	case c.isNil:
		return errNilConnector
	case !validDialect(c.dialect):
		return errConnectorDialect
	case s.Mode != ModeSQL:
		return errConnectorMode
	case s.Dialect != "" && s.Dialect != c.dialect:
		return errConnectorDialectMismatch
	case s.DSN != "":
		return errConnectorDSN
	}

	return nil
}

// validDialect reports whether d is one of the three dialects.
func validDialect(d Dialect) bool {
	switch d {
	case DialectPostgres, DialectMySQL, DialectSQLite:
		return true
	default:
		return false
	}
}
