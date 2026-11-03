---
name: gas-migrate
description: >
  Reference documentation for the gas/migrate Go package
  (github.com/gasmod/gas/migrate), the database migration manager for the Gas
  ecosystem. Use this skill when writing, reviewing, or debugging Go code that
  involves database migrations in Gas services. Covers the Service constructor
  and WithLockTimeout, migration registration (single, slice, embedded FS),
  RunPending/RunPendingContext, Down/DownContext rollback, the migration lock
  for concurrent instances, SQL directives (no-transaction, no-split),
  dirty-state handling and ClearDirty, up SQL checksums and edited
  migrations, version ordering, the __gas_migrations tracking table, PostgreSQL/MySQL/SQLite behavior, readiness, sentinel
  errors, the migratetest mock, and DI wiring with gas/database. Make sure to
  use this skill whenever working with database migrations, schema changes, or
  any code under a gasmod/gas/migrate import path, even if the user doesn't
  explicitly mention "migrate".
---

# Gas Migrate Package Reference

Database migration manager for the Gas ecosystem. Applies the migrations that
an app's services register in one global version order, tracks them in a
table, holds a lock in the database while it changes the schema (so several
instances can start at once), and supports dirty-state detection and rollback
on PostgreSQL, MySQL and SQLite.

```go
import migrate "github.com/gasmod/gas/migrate"
```

## Architecture Overview

- **Global version ordering**: migrations from all services are sorted by
  version and applied in a single global sequence.
- **Migration lock**: `Init`, `RunPendingContext`, `DownContext` and
  `ClearDirty` take a lock in the database, so of several instances starting
  at once, one migrates while the others wait and then find the work done.
- **Per-dialect application**: on PostgreSQL and SQLite each migration runs in
  one transaction with its tracking row. On MySQL the row is written `dirty`
  first, then the statements run in a transaction and the mark is cleared,
  because MySQL DDL commits implicitly.
- **Ownership tracking**: every migration is tagged with its owning service's
  name, enabling per-service registration while keeping global ordering.
- **No sqlc, no build info**: the service runs plain SQL per dialect. The
  `migrate_version` and `module_version` columns are written empty and stay
  only because tables from earlier releases have them.

## Constructor

```go
func New(opts ...Option) func(gas.DatabaseProvider, gas.Logger) (*Service, error)
```

Returns a DI-injectable constructor. The container injects
`gas.DatabaseProvider` (for example gas/database's Service) and `gas.Logger`,
so both must be registered. The constructor returns `ErrNilDatabase` for a nil
provider and `ErrNilLogger` for a nil logger.

```go
migrate.New()                                     // default lock timeout
migrate.New(migrate.WithLockTimeout(time.Minute)) // custom lock timeout
```

| Option                           | Default                                   |
|----------------------------------|-------------------------------------------|
| `WithLockTimeout(time.Duration)` | `DefaultLockTimeout` (`10 * time.Minute`) |

Options only record values; `Init` returns `ErrInvalidLockTimeout` unless the
timeout is positive.

Register it under `gas.MigrationManager`. The Worker looks up
`gas.MigrationManager` to run migrations, and services such as gas/auth's
inject it. Registered only as `*migrate.Service`, nothing runs the migrations
and those services fail at startup with `gas.ErrNotRegistered`.

Constants: `ServiceName = "gas/migrate"`, `TableName = "__gas_migrations"`,
`PostgresLockKey`, `MySQLLockPrefix = "gas/migrate/"`, `DefaultLockTimeout`.

## Service

Implements `gas.Service`, `gas.MigrationManager`, and `gas.ReadyReporter`.
Safe for concurrent use.

### Lifecycle

| Method       | Signature                                       | Description                                                   |
|--------------|-------------------------------------------------|---------------------------------------------------------------|
| `Name`       | `() string`                                     | Returns `ServiceName` (`"gas/migrate"`)                       |
| `Init`       | `(context.Context) error`                       | Picks the dialect, takes the provider's DB, creates the table (or adds its `checksum` column) under the lock |
| `Close`      | `(context.Context) error`                       | Makes the service uninitialized; registrations stay           |
| `CheckReady` | `(context.Context) error`                       | nil when every registered migration has a clean row           |
| `Applied`    | `(context.Context) ([]AppliedMigration, error)` | Tracking rows in version order                                |

`Init` on an initialized service returns `ErrAlreadyInitialized`. After
`Close`, `Init` may run again and takes the provider's DB afresh. Until `Init`
succeeds (and after `Close`), `RunPending`, `Down`, `ClearDirty`, `CheckReady`
and `Applied` return `ErrNotInitialized`.

`AppliedMigration` has `Version`, `Service` (owner's name), `Description`,
`Checksum` (empty for a row written without one) and `Dirty`.

### Readiness

`CheckReady` takes no lock and returns, in this order of checks:
`ErrNotInitialized`, the first registration problem, a failure to read the
tracking table, `ErrDirty`, or `ErrPending` with the pending count. A migration
registered after `RunPending` makes the service unready until the next start.

On PostgreSQL and MySQL, while another session holds the migration lock,
dirty rows are taken as in flight (logged at debug as `dirty migrations in
flight`), so replicas stay ready during a rolling deploy. Once the lock is
free, dirty rows are `ErrDirty` again. On SQLite a dirty row is always
`ErrDirty`.

`gas.HealthReporter` is not implemented: gas/migrate owns no live runtime
state distinct from the database, which gas/database reports on.

### Registering Migrations

Services register their migrations during `Init(ctx)`:

```go
func (s *Service) Register(service gas.Service, m gas.Migration)
func (s *Service) RegisterSlice(service gas.Service, migrations []gas.Migration)
func (s *Service) RegisterFS(service gas.Service, fsys fs.FS) error
```

The owner's name is recorded with each migration; the migration's own
`Service` field is replaced by the owner, so callers don't need to fill it.

`Register` and `RegisterSlice` cannot return an error, so a problem is
**recorded** instead, the migration is not registered, and `RunPending`,
`Down` and `CheckReady` return the first recorded problem until the process
ends:

- nil owner service: `ErrNilService` (no panic).
- empty version: `ErrInvalidVersion`.
- a version already registered by another service, or by the same service
  with a different description, up SQL or down SQL: `ErrDuplicateVersion`.
  The first registration stays.
- an unknown `-- gas:` directive: `ErrUnknownDirective`.

Checked when migrations are read (by `RunPending`, `Down`, `CheckReady`):
all-digit versions mixed with other versions (`ErrInvalidVersion`), and
`-- gas:no-transaction` on SQLite (`ErrUnsupportedDirective`).

Registering the identical migration again (same owner, version, description
and SQL) does nothing, so a restarted service can re-register in `Init`.
Registrations outlive `Close`.

### Executing Migrations

```go
func (s *Service) RunPending() error // RunPendingContext(context.Background())
func (s *Service) RunPendingContext(ctx context.Context) error
func (s *Service) Down(n int) error  // DownContext(context.Background(), n)
func (s *Service) DownContext(ctx context.Context, n int) error
```

`RunPendingContext` takes the lock and then, on every call, also when nothing
is pending:

1. reads the tracking table; while any row is dirty it applies nothing and
   returns `ErrDirty` (dirty takes precedence over checksums);
2. records the registered checksum into each row of a registered version
   whose checksum is empty (conditional `UPDATE`, logged as `checksums
   recorded` with `count`), then re-reads the table and checks dirty again;
3. compares each registered row's stored checksum with the registered up
   SQL's; any difference logs `checksum mismatch` (warn, both hashes) and
   returns `ErrChecksumMismatch` with nothing applied;
4. applies each registered migration whose version has no row, in version
   order.

Rows of versions no service registers are left alone. The first failing
migration stops the run; those before it stay applied. A migration registered after a higher
version was applied is still applied by the next run.

`DownContext` reverts the **n highest versions in the tracking table**,
highest first in version order (not necessarily the order applied). `n <= 0`
does nothing; `n` larger than the row count reverts all. Before reverting
anything it checks each version: one no service registers is
`ErrUnregisteredVersion`, one with empty or whitespace down SQL is
`ErrNoDownSQL`. A failed revert stops the call. On PostgreSQL and SQLite a
failed revert rolls back and marks nothing dirty; on MySQL (and with
`-- gas:no-transaction` on PostgreSQL) the row is marked dirty first, so a
failed revert leaves it dirty. `Down` and `CheckReady` neither check nor
record checksums.

The core `gas.MigrationManager` interface has only `RunPendingContext` and
`DownContext` for execution; `RunPending`, `Down`, `ClearDirty` and `Applied`
exist on `*migrate.Service`.

## Migration Struct (defined in gas core)

```go
type Migration struct {
    Version     string      // non-empty, e.g. "20250216001"
    Service     gas.Service // owner; replaced by the Register* owner argument
    Description string      // human-readable
    Up          string      // apply SQL
    Down        string      // rollback SQL; empty means Down cannot revert it
}
```

## Versions

A version is any non-empty text. When **every** registered version is all
ASCII digits, versions are ordered by numeric value (`9` before `10`, `0010`
after `9`; equal values such as `01` and `1` by text). Otherwise every version
is ordered byte by byte, and all-digit versions beside non-digit ones are
`ErrInvalidVersion`. Recommended format: `YYYYMMDDNNN`, all digits, fixed
length.

```
20250216001  first migration on Feb 16, 2025
20250216002  second migration same day
20250301001  first migration on Mar 1, 2025
```

**Versions must be unique across all services.** On MySQL the `version` column
uses the database collation, which by default ignores case, so `v1` and `V1`
collide there.

Upgrading from v0.5: all-digit versions were ordered by text before, so sets
with mixed digit lengths apply and `Down(n)` in a different order now, and
mixing digit and non-digit versions now fails startup until renamed. Existing
tracking tables get a `checksum` column from `Init` (see Checksums); v0.5
instances keep working against the upgraded table and write empty checksums.

## Directives

The up or down SQL can open with comment lines `-- gas:<name>` (whitespace
after `--`, among the leading blank or `--` lines). A directive applies to the
SQL it opens.

- `-- gas:no-transaction`: on PostgreSQL and MySQL, the SQL runs without a
  transaction; its row is written dirty first and cleaned (or deleted, for a
  revert) after it succeeds. Use it for `CREATE INDEX CONCURRENTLY`, which
  must then be the only statement of its migration. Not supported on SQLite
  (`ErrUnsupportedDirective`).
- `-- gas:no-split`: on MySQL, send the SQL as one statement instead of
  splitting it at semicolons (for stored routines or triggers). No effect on
  other dialects.

On MySQL the service splits SQL at semicolons outside quotes, backticks and
comments, honoring the session's `NO_BACKSLASH_ESCAPES` and `ANSI_QUOTES`
modes, because gas/database leaves `multiStatements` off. The client
`DELIMITER` command is not understood.

## Embedded SQL Files (RegisterFS)

File naming: `{version}_{description}.up.sql` / `{version}_{description}.down.sql`

```
migrations/
    20250216001_create_users.up.sql
    20250216001_create_users.down.sql
    20250216002_create_sessions.up.sql
    20250216002_create_sessions.down.sql
```

- The fs.FS is walked from its root, subdirectories included; other files are
  ignored. The down file may be in any directory.
- Version is up to the first underscore (the whole name when there is none);
  in the description each underscore becomes a space.
- `RegisterFS` returns an error and registers nothing for a nil service
  (`ErrNilService`), an unreadable file, or a file problem wrapping
  `ErrInvalidMigrationFile`: an empty version, two up files of one version,
  two down files of one base name, an up file without its down file, or a
  down file without its up file.
- A nil fs.FS is `ErrNilFS`, returned and also recorded.
- An empty down file is allowed: the migration has no down SQL.

Usage with `embed`:

```go
//go:embed migrations/*.sql
var migrationsFS embed.FS

func (s *MyService) Init(ctx context.Context) error {
    return s.migrationMgr.RegisterFS(s, migrationsFS)
}
```

## How It Works per Dialect

| Dialect    | Driver names        | Lock                                       | Apply                                                   |
|------------|---------------------|--------------------------------------------|---------------------------------------------------------|
| PostgreSQL | `postgres`, `pgx`   | session advisory lock on `PostgresLockKey` | one transaction: up SQL as one query + insert row       |
| MySQL      | `mysql`             | `GET_LOCK`, `MySQLLockPrefix` + MD5 of the database name | insert dirty row, then transaction: split statements + clear mark |
| SQLite     | `sqlite`, `sqlite3` | none per call; `BEGIN IMMEDIATE` per write | one transaction per migration, table re-read inside     |

Any other driver is `ErrUnsupportedDriver` from `Init`; a provider whose `DB()`
is nil is `ErrNilDatabase`.

When the lock is held elsewhere the service logs `waiting for the migration
lock` and retries until the call's context ends (error wraps the context's
error) or the lock timeout passes (`ErrLockTimeout`). The lock is session
scoped: PgBouncer in transaction or statement mode breaks it; point migrate at
the server or a session-mode pool. A migration must not change `USE` /
`search_path` to another database or schema, and on SQLite must not change a
session PRAGMA the app relies on (the connection returns to the pool).

### Tracking Table Schema

`__gas_migrations` is created with `CREATE TABLE IF NOT EXISTS`; on an
existing table without the `checksum` column, `Init` adds it:

| Column       | PostgreSQL    | MySQL             | SQLite    |
|--------------|---------------|-------------------|-----------|
| `version`    | `TEXT PK`     | `VARCHAR(255) PK` | `TEXT PK` |
| `applied_at` | `TIMESTAMPTZ` | `DATETIME`        | `TEXT`    |
| `dirty`      | `BOOLEAN`     | `BOOLEAN`         | `INTEGER` |
| `checksum`   | `TEXT`        | `VARCHAR(64)`     | `TEXT`    |

Other columns: `service`, `description`, `migrate_version`, `module_version`
(all `TEXT NOT NULL`; the last two written empty). `checksum` is `NOT NULL
DEFAULT ''`.

## Checksums

Each row records `checksum = lowercase hex(sha256(up))`, with every `\r\n` in
the up SQL replaced by `\n` first (nothing else normalized; a lone `\r`
counts). It covers the up SQL only, directives included; editing the down SQL
or description is allowed. Every row the service inserts, clean or dirty,
carries it.

Upgrade: `Init` adds the column under the lock (`ALTER TABLE ... ADD COLUMN`,
logged as `checksum column added`), so on an existing table it needs ALTER
rights (PostgreSQL: table ownership). On PostgreSQL and MySQL the ALTER's
table-lock wait is bounded to `min(lockTimeout, 5s)` via `lock_timeout` /
`lock_wait_timeout`; exceeding it fails `Init` with `gas/migrate: add checksum
column: <driver error>` (not `ErrLockTimeout`). The first `RunPending` records
checksums of existing rows from what is registered then, so an edit made
before it is not caught. A failed record is `gas/migrate: record checksum
<version> (owner <owner>): <cause>`.

When an applied migration was edited (`ErrChecksumMismatch`, message
`gas/migrate: checksum mismatch: 003 (owner notes) stored <64 hex> registered
<64 hex>, ...`), there is no in-code accept:

1. Never edit an applied migration; write a new one. Gas modules never edit a
   shipped migration either.
2. In development, for the latest migration: `Down(1)`, then `RunPending`.
3. Last resort, by hand: `UPDATE __gas_migrations SET checksum =
   '<registered>' WHERE version = '<version>'`, before rolling out the edited
   build. Previous-build instances then fail `RunPending` until rolled
   forward; rolling back needs the `UPDATE` reverted to the stored value.

## Dirty State Resolution

A dirty row blocks `RunPending` and `Down` (error wraps `ErrDirty` naming the
versions) and fails `CheckReady`, until cleared:

1. Fix the underlying issue (bad SQL, missing dependency). On PostgreSQL and
   SQLite a failed migration was rolled back. On MySQL, or for a
   `-- gas:no-transaction` migration, part of it may have run: repair the
   schema by hand to its state before the migration (after it, for a revert).
2. Call `ClearDirty(ctx, version)` on the `*migrate.Service`, for example from
   a maintenance command. It takes the lock and deletes the dirty row. A
   version that is not dirty, or has no row, is `ErrNotDirty` and nothing
   changes.
3. Run `RunPending` again; the migration is applied from scratch.

```go
func (s *Service) ClearDirty(ctx context.Context, version string) error
```

`ErrTrackingRowLost` means a tracking row was changed from outside the service
while a migration ran; check the schema by hand before the next run.

## Errors

Test with `errors.Is`. Every error starts with `gas/migrate: ` and names the
version and owner when there is one, e.g.
`gas/migrate: apply 002 (owner gas/auth/session): migration failed: <driver error>`.
Errors never hold migration SQL beyond what the driver quotes.

| Sentinel | Meaning |
|----------|---------|
| `ErrNilDatabase`, `ErrNilLogger` | nil dependency in New's constructor (or nil DB at Init) |
| `ErrNotInitialized`, `ErrAlreadyInitialized` | lifecycle misuse |
| `ErrUnsupportedDriver`, `ErrInvalidLockTimeout` | Init could not start |
| `ErrNilService`, `ErrNilFS`, `ErrInvalidVersion`, `ErrDuplicateVersion`, `ErrUnknownDirective`, `ErrUnsupportedDirective`, `ErrInvalidMigrationFile` | registration problem |
| `ErrDirty`, `ErrNotDirty`, `ErrTrackingRowLost` | dirty-state handling |
| `ErrPending` | CheckReady found migrations to apply |
| `ErrUnregisteredVersion`, `ErrNoDownSQL` | Down cannot revert a version |
| `ErrMigrationFailed` | a migration's SQL or row write failed once started; wraps the driver error |
| `ErrLockTimeout` | lock not taken within the lock timeout |
| `ErrChecksumMismatch` | an applied migration's up SQL was edited; RunPending applies nothing |

## Test Mock

The `migratetest` package provides `MockMigrationManager`, a recording mock of
`gas.MigrationManager` for unit tests.

```go
import "github.com/gasmod/gas/migrate/migratetest"
```

```go
type MockMigrationManager struct {
    NameFn          func() string
    InitFn          func(ctx context.Context) error
    CloseFn         func(ctx context.Context) error
    RegisterFn      func(service gas.Service, m gas.Migration)
    RegisterSliceFn func(service gas.Service, migrations []gas.Migration)
    RegisterFSFn    func(service gas.Service, fsys fs.FS) error
    RunPendingFn    func() error
    DownFn          func(n int) error
    // unexported: recorded calls, mutex
}

type Call struct {
    Method string
    Args   []any
}
```

With a field nil, its method succeeds and does nothing (`Name()` returns
`migrate.ServiceName`). `RunPendingContext` returns what `RunPendingFn`
returns, and `DownContext` what `DownFn` returns. Each method records a `Call`
before calling its field. Safe for concurrent calls; setting a field while a
method runs is a data race.

| Method                  | Description                                       |
|-------------------------|---------------------------------------------------|
| `Calls() []Call`        | Copy of the recorded calls, in order              |
| `CallCount(method) int` | Count calls by method name (e.g. `"RegisterFS"`)  |
| `Reset()`               | Forget all recorded calls                         |

```go
mock := &migratetest.MockMigrationManager{}
mock.RunPendingFn = func() error { return nil }

svc := myservice.New(mock)
if err := svc.Init(ctx); err != nil {
    t.Fatal(err)
}
if mock.CallCount("RegisterFS") != 1 {
    t.Error("expected one RegisterFS call")
}
```

## DI Wiring

```go
app := gas.NewApp(
    // gas/database also injects gas.ConfigProvider (see the gas-database skill).
    gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
    gas.WithSingletonService[gas.DatabaseProvider](database.New()),
    // Register gas/migrate under the interface the Worker uses.
    gas.WithSingletonService[gas.MigrationManager](
        migrate.New(migrate.WithLockTimeout(2 * time.Minute)),
    ),
    gas.WithSingletonService[*auth.Service](auth.New),
)
```

`Worker.Start` (and so `App.Start` and `Run`) builds and initializes every
singleton, emits `gas.SystemAllServicesInitialized`, calls
`RunPendingContext(ctx)` on the registered `gas.MigrationManager` with Start's
ctx, and then runs the ready hooks. A migration failure fails Start.

## Complete Example: Service with Migrations

```go
package auth

import (
    "context"
    "embed"

    "github.com/gasmod/gas"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Service struct {
    router       *gas.Router
    migrationMgr gas.MigrationManager // interface, not concrete type
}

// New is the DI constructor; it receives dependencies from the container.
func New(router *gas.Router, mgr gas.MigrationManager) *Service {
    return &Service{router: router, migrationMgr: mgr}
}

func (s *Service) Name() string { return "gas/auth" }

func (s *Service) Init(ctx context.Context) error {
    if err := s.migrationMgr.RegisterFS(s, migrationsFS); err != nil {
        return err
    }

    // Or inline:
    // s.migrationMgr.RegisterSlice(s, []gas.Migration{{
    //     Version:     "20250216001",
    //     Description: "create users table",
    //     Up:          "CREATE TABLE users (id SERIAL PRIMARY KEY, email TEXT NOT NULL);",
    //     Down:        "DROP TABLE users;",
    // }})

    s.router.Handle(s, "GET", "/users", s.handleListUsers)
    return nil
}

func (s *Service) handleListUsers(ctx gas.Context, db gas.DatabaseProvider) error {
    rows, err := db.Query(ctx, "SELECT id, email FROM users")
    if err != nil {
        return err
    }
    defer rows.Close()
    // ... process rows
    return ctx.JSON(200, nil)
}

func (s *Service) Close(ctx context.Context) error { return nil }
```

### Maintenance: clearing a dirty migration by hand

```go
func clearDirty(ctx context.Context, db gas.DatabaseProvider, logger gas.Logger, version string) error {
    svc, err := migrate.New()(db, logger)
    if err != nil {
        return err
    }
    if err := svc.Init(ctx); err != nil {
        return err
    }
    defer svc.Close(ctx)

    if err := svc.ClearDirty(ctx, version); errors.Is(err, migrate.ErrNotDirty) {
        return fmt.Errorf("version %s is not dirty", version)
    } else if err != nil {
        return err
    }
    return nil
}
```

## Choosing a Registration Method

| Method          | Best For                                                 |
|-----------------|----------------------------------------------------------|
| `Register`      | One-off migrations or dynamically generated SQL          |
| `RegisterSlice` | Small number of inline migrations defined in Go code     |
| `RegisterFS`    | Production services with SQL files under version control |

`RegisterFS` is the recommended approach for production services: it keeps SQL
separate from Go code, makes migrations reviewable in PRs, works naturally with
`go:embed`, and is the only method that returns registration errors directly.
