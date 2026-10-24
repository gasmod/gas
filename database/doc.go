// Package database manages a SQL database connection for a Gas app, with a
// database/sql backend for any registered driver and a native pgx backend
// for PostgreSQL. It adds transaction helpers, sqlc support in both modes,
// and connection retries with exponential backoff.
//
// # Role in the framework
//
// Service implements the core interfaces gas.Service, gas.DatabaseProvider,
// gas.HealthReporter and gas.ReadyReporter. Other modules and app services
// depend on gas.DatabaseProvider, not on this package, and reach the
// *sql.DB through DB. In ModePgx, Pool returns the native *pgxpool.Pool,
// and PoolFrom recovers it from a gas.DatabaseProvider without a type
// assertion in the caller.
//
// # Wiring
//
// New takes Options (WithConfig, WithConnector) and returns a constructor
// that the DI container calls with gas.ConfigProvider and gas.Logger. The
// app registers that constructor; the container builds the Service. The
// package registers nothing itself.
//
// # Configuration
//
// Unless WithConfig is given, Init binds Config from the gas.ConfigProvider
// over DefaultConfig, so a key that is not set keeps its default. The keys
// sit under database: mode ("sql" by default, or "pgx"), driver
// ("postgres" by default; "pgx" and "sqlite" are also accepted; sql mode
// only), dsn (required unless a connector is set), maxopenconns (25,
// range 1 to 1000), maxidleconns (5, range 1 to 1000 and at most
// maxopenconns; sql mode only), connmaxlifetime (30m, range 1s to 2h),
// connmaxidletime (5m, range 1s to 1h and at most connmaxlifetime),
// connretries (0, no retry) and connretryinterval (2s). WithConfig
// replaces the whole Config and skips the provider, so start from
// DefaultConfig. WithConnector supplies a driver.Connector, which is used
// instead of driver and dsn and is rejected outside sql mode.
// Binding checks the range rules through the validate tags, and Init checks
// the driver, dsn, connector and maxidleconns rules for any Config, so a
// Config given with WithConfig skips only the range rules. A rule that does
// not hold fails Init.
//
// # Lifecycle
//
// Init runs at startup. It validates the configuration, opens the
// connection (sql.Open, or sql.OpenDB with a connector, in sql mode; a
// pgxpool.Pool, with a *sql.DB derived from it, in pgx mode), applies the
// pool limits and pings the database with a 5-second timeout. A failed
// attempt is retried connretries times, waiting connretryinterval before
// the first retry and doubling the wait each time; Init blocks while it
// waits. The ping runs under Init's context, and when that context ends
// during a wait, Init stops retrying and returns an error wrapping both the
// context's error and the last connection error. When every attempt fails,
// Init returns the last error.
//
// Close runs at shutdown. It marks the Service closed and closes the
// *sql.DB and, in pgx mode, the pool. After Close, Query, Exec, BeginTx,
// WithTx, BeginPgxTx and WithPgxTx return an error instead of touching the
// connection, and CheckHealth and CheckReady report the Service as closed.
//
// The Service starts no goroutines of its own. database/sql and pgxpool
// manage their connection pools in the background until Close.
//
// # Health
//
// CheckHealth fails only when the Service is closed or not initialized,
// states a restart would fix; a lost connection does not fail it, because
// both backends reconnect on their own. CheckReady pings the database, so
// it fails while the database is unreachable.
//
// # Transactions
//
// WithTx and WithPgxTx run a function in a transaction: they commit when it
// returns nil and roll back when it returns an error or panics. A failed
// rollback is joined onto the function's error; after a panic it is logged
// and the panic propagates unchanged. A rollback that finds the transaction
// already finished is not an error. WithPgxTx rolls back with a context
// detached from cancellation and commits with the caller's context, so a
// canceled context fails the commit. BeginPgxTx and WithPgxTx return an
// error outside pgx mode.
//
// # Errors
//
// Errors from an operation start with the service name, "gas/database",
// and wrap the driver's error with %w, so callers can match it with
// errors.Is or errors.As. CheckHealth and CheckReady report a closed or
// uninitialized service as "database: closed" or "database: not
// initialized". Configuration errors also name the offending field, for
// example Database.DSN. The package defines no sentinel errors.
//
// Guides with usage examples are at https://gasmod.github.io/gas/modules/database/.
//
// SPDX-License-Identifier: MIT
package database
