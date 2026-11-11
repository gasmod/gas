# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.7.0] - 2026-11-11

### Changed

- The internal modules are required at v0.7.0.

## [0.6.3] - 2026-11-10

### Changed

- The internal modules are required at v0.6.3.

## [0.6.2] - 2026-11-10

### Changed

- The internal modules are required at v0.6.2.

## [0.6.1] - 2026-11-10

### Changed

- Dependencies updated, and the internal modules required at v0.6.1.

## [0.6.0] - 2026-11-08

v0.6.0 rewrites the module around a documented contract. The package's
`doc.go` is its behavioral contract and lists its errors verbatim; read it
before upgrading. Most changes are breaking.

### Added

- **MySQL.** The dialect `mysql` opens `github.com/go-sql-driver/mysql`,
  which the module imports. `ParseTime` is always on, so DATETIME and
  TIMESTAMP columns scan into `time.Time` (KI-048); `MultiStatements`
  stays off unless the DSN turns it on. The driver's own log goes to the
  service's logger as one warn event per message, without the driver's
  text, instead of to stderr.
- **`ConnectTimeout`** (`database.connecttimeout`, 1s to 1m, default
  `DefaultConnectTimeout`, 5s) bounds each connect attempt, open and ping
  together, each later ping, and a pgx rollback. It replaces a fixed 5s
  ping timeout.
- **`Service.SafeError`** replaces a connection-level driver error with
  `gas/database: connection: <reason>`, wrapping `ErrConnect`, for code
  that runs statements on `DB()` itself, such as migrate. Other errors
  pass through unchanged.
- **A default SQLite busy timeout.** A SQLite DSN that sets none gets
  `DefaultSQLiteBusyTimeout` (5s), appended as
  `_pragma=busy_timeout(5000)`, so concurrent connections wait for the
  write lock instead of failing at once with `SQLITE_BUSY`.
- **`Service.Config() Config`** returns a copy of the configuration the
  last successful `Init` resolved, defaults included, or the zero
  `Config` before one; a failed `Init` and `Close` keep it.
- **Exported names**: `ServiceName`, the `Default*` constants,
  `MaxConnRetryInterval` (30s), and the sentinel errors `ErrNilLogger`,
  `ErrNilConfig`, `ErrNoConfigProvider`, `ErrInvalidConfig`,
  `ErrNotInitialized`, `ErrAlreadyInitialized`, `ErrDriverNotRegistered`,
  `ErrInvalidDSN`, `ErrConnect`, `ErrModeNotPgx`, `ErrNilFunc` and
  `ErrCloseTimeout`.

### Changed

- **BREAKING: `Settings.Driver` becomes `Settings.Dialect`**, of the new
  type `Dialect`: `DialectPostgres`, `DialectMySQL` or `DialectSQLite`. It
  has no default (it was `postgres`) and is required unless a connector is
  given. `DriverPostgres`, `DriverPgx` and `DriverSQLite` are removed; use
  `postgres` where `pgx` was set. `Settings.Mode` is of the new type
  `Mode`.
- **BREAKING: `Driver()` reports the dialect.** In `ModePgx` it returns
  `postgres`, not `pgx`, and it returns an empty string while the service
  is not initialized.
- **BREAKING: the dialect `postgres` uses pgx's database/sql adapter**,
  which the module imports, instead of whatever driver the app registered
  as `postgres`.
- **BREAKING: `WithConnector(d, c)` takes the connector's dialect**, which
  `Driver` returns. With a connector, the mode must be `sql`, the dialect
  setting empty or the connector's, and the DSN empty.
- **BREAKING: `Config` no longer embeds `gas.WithGasEnv`**, which core
  removed.
- **BREAKING: the configuration is checked the same way on both paths.**
  `WithConfig` takes a copy, which `Init` checks against the `validate`
  tags, as it checks a bound one. `WithConfig(nil)` fails `Init` with
  `ErrNilConfig`, and neither a `WithConfig` nor a `ConfigProvider` with
  `ErrNoConfigProvider`, where the defaults used to apply. `ModePgx`
  requires the dialect `postgres`. `MaxIdleConns` may be 0 and must not
  exceed `MaxOpenConns` in both modes; it used to be 1 to 1000 in
  `ModeSQL` and unchecked in `ModePgx`. `ConnRetries` is 0 to 10, and
  `ConnRetryInterval` 100ms to 30s when `ConnRetries` is positive. A
  failure wraps `ErrInvalidConfig` and names the setting, never its value.
- **BREAKING: the lifecycle has one state.** `Init` on an initialized
  service returns `ErrAlreadyInitialized` and keeps the running handle; a
  failed `Init` closes what it opened; `Close` then `Init` restarts the
  service. Until `Init` succeeds and after `Close`, the methods return
  `ErrNotInitialized`, and `DB` and `Pool` return nil. The constructor
  fails with `ErrNilLogger` for a nil logger, where it used to panic.
- **BREAKING: `Close` honors its context.** When the context ends first,
  it returns an error wrapping `ErrCloseTimeout` and the context's error,
  and the closing goes on in the background.
- **BREAKING: connection errors carry a fixed reason, never the driver's
  error.** A failed connect is `gas/database: connect: <reason>`, wrapping
  `ErrConnect`, the reason one of `authentication failed`,
  `access denied`, `database does not exist`, `timed out`,
  `connection refused`, `unreachable` and `failed`. Query, Exec, the
  transaction methods, Ping and Close replace a connection-level failure
  the same way after their own prefix. A DSN the driver cannot parse is
  `ErrInvalidDSN`, and a missing `sqlite` driver `ErrDriverNotRegistered`;
  neither is retried. Statement errors still pass the driver's error
  through.
- **Only connection attempts are retried**, and the wait doubles up to
  `MaxConnRetryInterval` (30s). The retry log carries `attempt`,
  `retries`, `wait` and `reason`, not the driver's error.
- **`WithTx` and `WithPgxTx` roll back when the function calls
  `runtime.Goexit`**, as `t.FailNow` does, as well as on an error or a
  panic. A nil function is `ErrNilFunc`. A failed rollback after a panic
  or Goexit is logged at warn level as `transaction rollback failed`, with
  a reason. `WithPgxTx` commits with its context, and rolls back with that
  context without its cancellation, bounded by `ConnectTimeout`.
- **`CheckHealth` does no I/O**; it fails only while the service is not
  initialized. `CheckReady` and `Ping` ping within `ConnectTimeout`.
- **The logs change.** `Init` logs `service initialized` with `mode` and
  `dialect`, and `Close` logs `service closed`, or `service close failed`
  at warn level when a timed-out close fails later.
- **Every error starts with `gas/database`** and wraps its sentinel
  (KI-043).

### Fixed

- `Close` closes the pgx pool even when closing the database/sql handle
  fails, and returns that failure (KI-038).
- A second `Init` no longer opens a second connection over the first.

### Security

- **Requires Go 1.27.3.** The `go` directive moves from 1.27 to 1.27.3,
  the first 1.27 release with the fix for GO-2026-6809 (CVE-2026-111943):
  `crypto/x509` now rejects intermediate certificates known to be
  compromised. A build with an older toolchain switches to 1.27.3 under
  `GOTOOLCHAIN=auto` and fails under `GOTOOLCHAIN=local`.
- **The DSN is a `gas.Secret`**, which fmt, encoding/json, YAML encoders
  and log/slog write as `[redacted]`, so printing the configuration no
  longer shows the password.
- **No error the package makes and no log the service writes includes the
  DSN, the password, the database user, the host, the port, the database
  name or TLS material.** pgx's and MySQL's connect errors can hold all of
  them.

## [0.5.6] - 2026-10-19

The tags `database/v0.4.1` to `database/v0.5.6` shipped their changes under
`[Unreleased]`. This section holds them, and covers every change since
0.3.0, which was released from the separate
`github.com/gasmod/gas-database` repository.

### Added

- **Native pgx transaction helpers** — `BeginPgxTx` for manual management and
  `WithPgxTx` for automatic commit/rollback against `pgx.Tx`, for sqlc's pgx
  mode and pgx-only features. Both return an error outside `ModePgx`.
  `WithPgxTx` rolls back on a context detached from the caller's cancellation
  so cleanup still runs when the context is already done.
- **`PoolFrom`** — unwraps a `gas.DatabaseProvider` to its native
  `*pgxpool.Pool`, reporting `false` when the provider is not pgx-backed or is
  running in `ModeSQL`, so consumers no longer need to declare their own
  type-assertion interface.

### Changed

- **BREAKING: import path.** The module moved from
  `github.com/gasmod/gas-database` to `github.com/gasmod/gas/database`, in
  database/v0.4.1.
- **BREAKING: Go 1.27 is required.** The `go` directive moves from 1.26.1
  to 1.27, in database/v0.5.0, as core now uses generic methods.
- **BREAKING: `database.New` returns an error.** It now returns
  `func(gas.ConfigProvider, gas.Logger) (*Service, error)`, the
  `CONVENTIONS.md` shape. It cannot fail yet, so it returns nil; the change is
  the shape. Registering it with `gas.WithSingletonService` is unchanged,
  since the container accepts a trailing error. Code that calls the returned
  function directly must take the second result (gas#106).
- **BREAKING: `Config.Validate` is removed; the rules a tag can express are
  validate tags.** `Mode` carries `oneof=sql pgx`, `MaxOpenConns`
  `min=1,max=1000`, `ConnMaxLifetime` `min=1s,max=2h`, and `ConnMaxIdleTime`
  `min=1s,max=1h,ltefield=ConnMaxLifetime`. `ConfigProvider.Bind` already
  validates the struct it binds, so `Init` no longer runs a tag check of its
  own; a config passed with `WithConfig` is not checked against the tags, as
  `gas.Config` is not when an App has no `ConfigProvider`. The rules that
  depend on the mode or on `WithConnector` stay in code and run in `Init` for
  every config, with their messages unchanged: the driver, DSN and connector checks,
  and the ModeSQL `MaxIdleConns` limits. The errors for the tag rules are now
  the validator's (gas#116).
- `Service` no longer logs an error it also returns: an invalid
  configuration, an unknown mode, open, ping, pgx config and pool failures,
  running out of connection retries, a failed `Close`, and a failed rollback
  joined onto `WithTx`'s error. The caller that handles the error logs it
  once. An invalid configuration is returned as
  `gas/database: invalid configuration: <cause>`. A rollback that fails
  during a panic is still logged, since it is not returned; the per-attempt
  retry warning and the close error after a failed ping also stay.
- **BREAKING:** `Service` implements the context-aware `gas.Service`
  (`Init(ctx context.Context) error`, `Close(ctx context.Context) error`).
  The connection ping (and pgx pool creation) derives from the Init context,
  and connection retries stop when that context is canceled or its deadline
  passes; the returned error wraps `ctx.Err()`.
- **Transaction errors are no longer discarded** — `WithTx` previously dropped
  rollback failures and returned commit errors unwrapped. Rollback failures are
  now joined onto the caller's error with `errors.Join`, except
  `sql.ErrTxDone` / `pgx.ErrTxClosed` (a transaction the callback settled
  itself), which stay silent. A rollback that fails during a panic is logged
  rather than returned, so the panic propagates unchanged.
- `initSQL` now logs the `Close` error when closing a connection after a failed
  ping, instead of discarding it.

### Removed

- **`DBTX` interface** — sqlc generates its own `DBTX` in the target package,
  so the exported copy was redundant and only covered the `database/sql`
  shape. Consumers should use the generated interface, or accept `*sql.DB` /
  `*sql.Tx` directly. **Breaking change.**

### Fixed

- **Panic instead of an error on an uninitialized service** — `Query`, `Exec`,
  `BeginTx` and `WithTx` checked only the closed flag before reaching for the
  connection, so a service that had not been initialized, or whose `Init`
  returned an error and therefore left the connection nil while the service was
  still not closed, panicked with a nil pointer dereference. All four now
  report `not initialized`, the state `Ping` and `CheckHealth` already surface.
- **A `driver.Connector` in `ModePgx` is now rejected** — `WithConnector`
  documents itself as a `ModeSQL` option, but the DSN check ignored the mode, so
  a connector paired with `ModePgx` passed validation. `initPgx` never consults
  the connector and builds its pool from the DSN alone, so the connector was
  dropped and, with no DSN to fall back on, `pgxpool` resolved its own defaults
  and failed against a server the caller never asked for. `Validate` now
  reports the unusable pair.

## 0.3.0 - 2026-07-02

First open source release. Versions prior to 0.3.0 were developed in a private
repository; this entry summarizes the module as published.

### Added

- **Dual backend support** — `database.ModeSQL` (default) wraps `database/sql`
  for any driver (PostgreSQL, SQLite, MySQL, etc.), while `database.ModePgx`
  creates a native `pgxpool.Pool` for PostgreSQL. `DB()` always returns a
  `*sql.DB` in both modes (via the pgx stdlib adapter in `ModePgx`), so sqlc's
  `database/sql` mode always works; `Pool()` additionally exposes the native
  `*pgxpool.Pool` for sqlc's pgx mode.
- **`gas.DatabaseProvider` implementation** for DI-based injection into
  consuming services, plus `gas.HealthReporter` / `gas.ReadyReporter` for
  health and readiness probes — `CheckHealth` reports liveness without
  pinging the database (both backends auto-reconnect), while `CheckReady`
  pings the database to signal when traffic should drain.
- **Transaction helpers** — `BeginTx` for manual transaction management and
  `WithTx` for automatic commit/rollback, including rollback on panic.
- **`DBTX` interface** matching the shape sqlc generates, satisfied by both
  `*sql.DB` and `*sql.Tx`.
- **Connector support** — `WithConnector` accepts a `driver.Connector`
  directly (`sql.OpenDB`) for custom connection setup such as TLS or auth
  tokens, bypassing the `Driver`/`DSN` config fields.
- **Connection retry with exponential backoff** — `ConnRetries` and
  `ConnRetryInterval` config fields control retry attempts on initial
  connection failure.
- **Configuration binding** — automatic binding from the injected
  `gas.ConfigProvider` when `WithConfig` is not supplied, with validated
  settings for mode, driver, DSN, pool sizing (`MaxOpenConns`,
  `MaxIdleConns`), connection lifetime (`ConnMaxLifetime`,
  `ConnMaxIdleTime`), and retry behavior.
- **`Driver()`** accessor reporting the effective database driver name based
  on configured mode and settings.

[Unreleased]: https://github.com/gasmod/gas/compare/database/v0.7.0...HEAD
[0.7.0]: https://github.com/gasmod/gas/releases/tag/database/v0.7.0
[0.6.3]: https://github.com/gasmod/gas/releases/tag/database/v0.6.3
[0.6.2]: https://github.com/gasmod/gas/releases/tag/database/v0.6.2
[0.6.1]: https://github.com/gasmod/gas/releases/tag/database/v0.6.1
[0.6.0]: https://github.com/gasmod/gas/releases/tag/database/v0.6.0
[0.5.6]: https://github.com/gasmod/gas/releases/tag/database/v0.5.6
