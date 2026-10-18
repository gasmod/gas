# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

- **BREAKING: `Config.Validate` is removed; the rules a tag can express are
  validate tags.** `Mode` carries `oneof=sql pgx`, `MaxOpenConns`
  `min=1,max=1000`, `ConnMaxLifetime` `min=1s,max=2h`, and `ConnMaxIdleTime`
  `min=1s,max=1h,ltefield=ConnMaxLifetime`. `ConfigProvider.Bind` already
  validates the struct it binds, so `Init` no longer repeats those checks on
  a bound config; a config passed with `WithConfig`, or the defaults with no
  provider, is checked against the same tags in `Init`. The rules that depend
  on the mode or on `WithConnector` stay in code and run in `Init` on both
  paths, with their messages unchanged: the driver, DSN and connector checks,
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

## [0.3.0] - 2026-07-02

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

[Unreleased]: https://github.com/gasmod/gas/database/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/gasmod/gas/database/releases/tag/v0.3.0

