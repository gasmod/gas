# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.9.0] - 2026-11-11

### Changed

- The internal modules are required at v0.9.0.

## [0.8.0] - 2026-11-11

### Changed

- **Go 1.27.0 or later is required**, down from 1.27.3, as for the core:
  build apps with Go 1.27.4 or later, for the `net/http` fix of
  [GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859).
- Dependencies updated, and the internal modules required at v0.8.0.

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

### Fixed

- **A call's connection is closed in gas/database's `ModePgx` too.**
  There `*sql.DB` borrows the pgxpool's connections, and closing one
  returned it to the pool, so a migration's session settings, such as
  `SET search_path`, reached the app's later queries, and an advisory
  lock whose release failed stayed held. The connection is now closed,
  as the package documentation says.
- **A read of the tracking table on SQLite waits for another instance's
  write lock.** It failed with `SQLITE_BUSY` when it ran while another
  instance committed and SQLite's busy timeout, as `busy_timeout(0)`
  sets it, ran out first, so two instances running together could fail.
  It now tries again within the lock timeout, as taking the write lock
  does, and a read that waits longer fails with an error wrapping
  `ErrLockTimeout`.

## [0.6.0] - 2026-11-08

v0.6.0 rewrites the module around the contract in its package
documentation (`doc.go`), which lists every error verbatim. Read it before
upgrading, and its section "Upgrading from v0.5" in particular.

### Added

- **A migration lock in the database.** `Init`, `RunPendingContext`,
  `DownContext` and `ClearDirty` take a lock on one connection held for
  the whole call: a session-level advisory lock on `PostgresLockKey` on
  PostgreSQL, a `GET_LOCK` user-level lock named `MySQLLockPrefix` and the
  MD5 of the database name on MySQL, and a `BEGIN IMMEDIATE` write
  transaction per write on SQLite. Several instances of an app can start
  at once against one database: one applies the pending migrations while
  the others wait. `WithLockTimeout` bounds the wait,
  `DefaultLockTimeout` (10 minutes) unless set, and a wait that runs out
  fails with `ErrLockTimeout`.
- **Checksums.** The tracking table records the SHA-256 of each
  migration's up SQL, with CRLF read as LF, in a new `checksum` column.
  `RunPending` checks every applied, registered version on each call,
  also when nothing is pending, and fails with `ErrChecksumMismatch`,
  naming the version, its owner and both checksums, when the up SQL was
  edited after it was applied; it applies nothing then. `Init` adds the
  column to an existing table, and the first `RunPending` records the
  checksums of rows written without one. `Down` and `CheckReady` do not
  check checksums.
- **`Applied`** returns the rows of the tracking table as
  `AppliedMigration` values (version, owner, description, dirty mark and
  checksum), in version order.
- **`RunPendingContext` and `DownContext`**, as `gas.MigrationManager`
  now requires; `RunPending` and `Down` call them with
  `context.Background()`.
- **Directives.** A migration's up or down SQL can open with
  `-- gas:no-transaction`, to run without a transaction on PostgreSQL
  and MySQL, such as for CREATE INDEX CONCURRENTLY, and
  `-- gas:no-split`, to send MySQL SQL as one statement, such as a stored
  routine. An unknown directive is `ErrUnknownDirective`;
  `no-transaction` on SQLite is `ErrUnsupportedDirective`. The name is
  all that follows `gas:`, so `-- gas: no-split`, with white space after
  the colon, is an unknown directive. **BREAKING** for a v0.5 migration
  whose up or down SQL opens with a plain comment starting `gas:`, such
  as `-- gas: create users`: `RunPending`, `Down` and `CheckReady` now
  fail with `ErrUnknownDirective`. Reword the comment, as
  `-- create users`, in the build that upgrades to v0.6.0: its first
  `RunPending` records the checksum of the reworded up SQL for a row
  v0.5 wrote, so there is no `ErrChecksumMismatch`. Only the leading
  blank and `--` lines are read, so such a comment after the first
  statement is plain SQL.
- **`ServiceName`, `TableName`, `PostgresLockKey`, `MySQLLockPrefix`**
  and `DefaultLockTimeout` constants, and the sentinel errors
  `ErrNilDatabase`, `ErrNilLogger`, `ErrNotInitialized`,
  `ErrAlreadyInitialized`, `ErrUnsupportedDriver`,
  `ErrInvalidLockTimeout`, `ErrNilService`, `ErrNilFS`,
  `ErrInvalidVersion`, `ErrUnknownDirective`, `ErrUnsupportedDirective`,
  `ErrInvalidMigrationFile`, `ErrPending`, `ErrUnregisteredVersion`,
  `ErrNoDownSQL`, `ErrMigrationFailed`, `ErrTrackingRowLost`,
  `ErrLockTimeout` and `ErrChecksumMismatch`.
- **Logs.** The service logs each migration applied or reverted, a
  migration marked dirty, a cleared dirty mark, a wait for the lock,
  recorded checksums and a checksum mismatch, through the `gas.Logger`
  it now takes. No log holds a migration's SQL.

### Changed

- **BREAKING: `New` takes options and a logger.** `New(opts ...Option)`
  returns `func(gas.DatabaseProvider, gas.Logger) (*Service, error)`,
  which fails with `ErrNilDatabase` or `ErrNilLogger` for a nil
  argument. Registering it with
  `gas.WithSingletonService[gas.MigrationManager]` is unchanged.
- **BREAKING: `ClearDirty` takes a context:**
  `ClearDirty(ctx, version)`. It takes the migration lock.
- **BREAKING: all-digit versions are ordered by value.** When every
  registered version is all ASCII digits, `9` comes before `10`; versions
  were compared as text. A set whose all-digit versions have one length,
  such as timestamps, keeps its order. All-digit versions registered
  beside other versions are `ErrInvalidVersion`, so such an app fails to
  start until its versions are renamed.
- **BREAKING: error texts.** The sentinel errors no longer start with
  `gas/migrate: `; every error returned starts with it once, then names
  what failed and the version and owner, such as
  `gas/migrate: apply 002 (owner gas/auth/session): migration failed: ...`.
  `ErrDuplicateVersion` reads `duplicate version` and `ErrNotDirty`
  `migration not dirty`.
- **BREAKING: `migratetest.MockMigrationManager`.** `InitFn` and
  `CloseFn` take the context, `Calls` is a method returning a copy of the
  recorded calls, and the mock implements `RunPendingContext` and
  `DownContext`, which return what `RunPendingFn` and `DownFn` return.
  An unset `NameFn` returns `migrate.ServiceName`. A call is recorded
  under the method called only, and gas's `Start` now calls
  `RunPendingContext`, so a test that starts an app with the mock and
  asserts `CallCount("RunPending")` gets 0: count `"RunPendingContext"`
  instead, and `"DownContext"` for a revert through `gas.MigrationManager`.
- **MySQL migrations are split into statements.** The up and down SQL
  are split at each semicolon outside quotes, backticks and comments, as
  MySQL reads them in the session's SQL mode, and sent one at a time, so
  the DSN needs no `multiStatements`. The tracking row is written dirty
  before the SQL runs and cleared after, so a failure, a canceled context
  or a crash leaves the migration dirty rather than applied in part with
  a clean row.
- **Registration problems are recorded.** `Register` and
  `RegisterSlice` cannot return an error, so a nil owner
  (`ErrNilService`), an empty version (`ErrInvalidVersion`), a duplicate
  version (`ErrDuplicateVersion`) or an unknown directive is recorded,
  and `RunPending`, `Down` and `CheckReady` return the first one;
  `Register(nil, m)` no longer panics. Registering the same migration
  again with the same owner does nothing, so a restarted service no
  longer adds a copy each time (KI-057).
- **`RegisterFS` checks the files.** It returns `ErrNilFS` for a nil
  `fs.FS`, `ErrNilService` for a nil owner, and an error wrapping
  `ErrInvalidMigrationFile` for an empty version, two up files of one
  version, two down files of one name, or an up or down file without the
  other, and then registers nothing. An empty down file means the
  migration has no down SQL.
- **`Down` checks before it reverts.** It returns
  `ErrUnregisteredVersion` for a version no service registers and
  `ErrNoDownSQL` for an empty down SQL before it reverts anything; an
  `n` of zero or less does nothing.
- **`CheckReady` reports why.** It returns `ErrNotInitialized`, a
  registration problem, `ErrDirty`, or `ErrPending` with the number of
  pending migrations. On PostgreSQL and MySQL, while another session
  holds the migration lock, dirty rows are taken as in flight, so other
  replicas stay ready during a rolling deploy.
- **`Init` runs once.** A second `Init` returns `ErrAlreadyInitialized`;
  `Close` makes the service uninitialized, and `Init` may run again.
  Until `Init` succeeds, `RunPending`, `Down`, `ClearDirty`, `CheckReady`
  and `Applied` return `ErrNotInitialized`. A driver it does not support
  is `ErrUnsupportedDriver`.
- **Driver errors pass through `SafeError`.** When the provider has a
  `SafeError(error) error` method, as gas/database's Service has, every
  driver error goes through it, so a connection-level failure names no
  user, host or database.
- **The connection is closed after a failed lock release** on PostgreSQL
  and MySQL, so no lock, open transaction or session setting a migration
  made reaches the app's later queries.
- **`migrate_version` and `module_version` are written empty.** They
  used to hold module versions read from the binary's build information;
  they stay in the table for earlier releases.

### Fixed

- `RunPending` and `Down` return an error wrapping `ErrNotInitialized`
  when called before `Init`, as `ClearDirty` and `CheckReady` already
  did. They used to panic with a nil pointer dereference.
- **MySQL no longer needs `parseTime=true` (KI-048).** The service never
  reads `applied_at`, so every read of the tracking table works whatever
  the DSN says.

### Removed

- **BREAKING:** the `db/postgres`, `db/mysql` and `db/sqlite` packages of
  sqlc-generated tracking queries.

### Security

- **Requires Go 1.27.3.** The `go` directive moves from 1.27 to 1.27.3,
  the first 1.27 release with the fix for GO-2026-6809 (CVE-2026-111943):
  `crypto/x509` now rejects intermediate certificates known to be
  compromised. A build with an older toolchain switches to 1.27.3 under
  `GOTOOLCHAIN=auto` and fails under `GOTOOLCHAIN=local`.

## [0.5.6] - 2026-10-19

The tags `migrate/v0.4.1` to `migrate/v0.5.6` shipped their changes under
`[Unreleased]`. This section holds them, and covers every change since
0.3.0, which was released from the separate
`github.com/gasmod/gas-migrate` repository.

### Changed

- **BREAKING: import path.** The module moved from
  `github.com/gasmod/gas-migrate` to `github.com/gasmod/gas/migrate`, in
  migrate/v0.4.1.
- **BREAKING: Go 1.27 is required.** The `go` directive moves from 1.26.1
  to 1.27, in migrate/v0.5.0, as core now uses generic methods.
- **BREAKING: migrations are registered under a `gas.Service`, not a
  name.** `Register`, `RegisterSlice` and `RegisterFS` take the owning
  service instead of its name, on `Service` and on
  `migratetest.MockMigrationManager`, whose `RegisterFn`, `RegisterSliceFn`
  and `RegisterFSFn` fields change to match, in migrate/v0.5.0. Pass `s`
  instead of `s.Name()`.
- **BREAKING: `migrate.New` returns an error.** It now returns
  `func(gas.DatabaseProvider) (*Service, error)`, the `CONVENTIONS.md`
  shape. It cannot fail yet, so it returns nil; the change is the shape.
  Registering it with `gas.WithSingletonService` is unchanged, since the
  container accepts a trailing error. Code that calls the returned function
  directly must take the second result (gas#106).
- **BREAKING:** `Service` and `migratetest.MockMigrationManager` implement the
  context-aware `gas.Service` (`Init(ctx context.Context) error`,
  `Close(ctx context.Context) error`). `Service.Init` creates the
  `__gas_migrations` tracking table under the Init context, so a canceled or
  expired Init context aborts it. The mock's `InitFn` and `CloseFn` fields keep
  their `func() error` type.
- `Down` refuses to run while any migration is dirty, returning an error
  wrapping `ErrDirty` that names the dirty versions. It used to count a
  dirty version as applied and run its down SQL, although that
  migration's up SQL had been rolled back, which could drop tables an
  earlier migration created.
- `Down` deletes each tracking row in the same transaction as the down SQL.
  If the delete fails, the down SQL rolls back and `Down` returns the error;
  it used to commit the down SQL first, so a failed delete left the schema
  reverted under a row that still marked the migration applied, and
  `RunPending` never re-applied it. On MySQL, DDL commits implicitly, so a
  migration containing DDL is still not atomic with its tracking row.
- The dirty-migration errors from `RunPending` and `CheckReady` now wrap
  `ErrDirty`; their messages are unchanged.
- A service that registers an identical migration more than once gets it
  applied once. Registering two different migrations under one version in
  one service is now an error wrapping `ErrDuplicateVersion`, returned by
  `RunPending`, `Down`, and `CheckReady` before anything runs; it used to
  apply the first, fail to record the second, and leave the version dirty.
- The cross-service duplicate-version error from `RunPending` and `Down`
  wraps `ErrDuplicateVersion`, and `CheckReady` now returns it too. `Down`'s
  message names the services rather than printing their values.
- `CheckReady` counts each pending version once.

### Added

- `Service.ClearDirty(version)` removes a dirty mark by deleting the version's
  tracking row, so the next `RunPending` applies that migration again. It
  returns an error wrapping the new `ErrNotDirty`, and changes nothing, for a
  version that is not dirty. Recovering from a failed migration used to need
  manual SQL against `__gas_migrations`.
- `ErrDirty`, wrapped by `RunPending`, `Down`, and `CheckReady` while any
  migration is dirty.
- `ErrDuplicateVersion`, wrapped by `RunPending`, `Down`, and `CheckReady`
  when two different migrations share a version.

## 0.3.0 - 2026-07-03

First open source release. Versions prior to 0.3.0 were developed in a private
repository; this entry summarizes the module as published.

### Added

- **Migration registration** — `Register` for a single migration,
  `RegisterSlice` for a batch, and `RegisterFS` for embedded `up`/`down` SQL
  file pairs (including nested directories), with the version and description
  parsed from the filename convention.
- **`RunPending`** — sorts all registered migrations globally by version
  across every service and applies any not yet recorded, each inside its own
  transaction.
- **`Down(n)`** — rolls back the last `n` applied migrations in reverse
  version order.
- **Dirty-state handling** — a failed migration is recorded as dirty and
  blocks further execution until manually resolved.
- **Version collision detection** — `RunPending` and `Down` return an error
  identifying the version and both service names when two services register
  migrations under the same version.
- **`__gas_migrations` tracking table**, created automatically on `Init()`.
- **sqlc-generated multi-dialect adapters** for PostgreSQL, MySQL, and SQLite,
  selected automatically from the underlying database driver.
- **`gas.ReadyReporter` implementation** — `CheckReady` gates readiness on
  initialization, no dirty migrations, and no pending registered migrations.
- **Build-info version tracking** for `gas/migrate` itself and for the
  registering services, read from `runtime/debug.ReadBuildInfo`.
- **`migratetest`** package with a mock `gas.MigrationManager` for testing
  services that register migrations.
- **DI wiring** with `gas/database` via `gas.WithSingletonService`.

### Fixed

- Applied a migration's DDL and its `__gas_migrations` tracking row in the
  same transaction, so a crash between the two no longer leaves the schema
  changed but unrecorded and the pipeline wedged as dirty.

[Unreleased]: https://github.com/gasmod/gas/compare/migrate/v0.8.0...HEAD
[0.8.0]: https://github.com/gasmod/gas/releases/tag/migrate/v0.8.0
[0.7.0]: https://github.com/gasmod/gas/releases/tag/migrate/v0.7.0
[0.6.3]: https://github.com/gasmod/gas/releases/tag/migrate/v0.6.3
[0.6.2]: https://github.com/gasmod/gas/releases/tag/migrate/v0.6.2
[0.6.1]: https://github.com/gasmod/gas/releases/tag/migrate/v0.6.1
[0.6.0]: https://github.com/gasmod/gas/releases/tag/migrate/v0.6.0
[0.5.6]: https://github.com/gasmod/gas/releases/tag/migrate/v0.5.6
