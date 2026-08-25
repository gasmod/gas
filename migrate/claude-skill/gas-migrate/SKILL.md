---
name: gas-migrate
description: >
  Reference documentation for the gas/migrate Go package
  (github.com/gasmod/gas/migrate) — the database migration manager for the Gas
  ecosystem. Use this skill when writing, reviewing, or debugging Go code that
  involves database migrations in Gas services. Covers the Service constructor,
  migration registration (single, slice, embedded FS), RunPending, Down/rollback,
  dirty-state handling, version naming conventions, the __gas_migrations tracking
  table, sqlc multi-dialect adapters (PostgreSQL, MySQL, SQLite), build-info
  version tracking, the migratetest mock, and DI wiring with gas/database. Make
  sure to use this skill whenever working with database migrations, schema
  changes, or any code under a gasmod/gas/migrate import path, even if the user
  doesn't explicitly mention "migrate".
---

# Gas Migrate Package Reference

Database migration manager for the Gas ecosystem. Tracks and applies SQL
migrations across all Gas services with dirty-state detection, rollback support,
and sqlc-generated multi-dialect adapters for PostgreSQL, MySQL, and SQLite.

```
import migrate "github.com/gasmod/gas/migrate"
```

## Architecture Overview

- **Global version ordering** — migrations from all services are sorted by
  version and applied in a single global sequence.
- **Per-migration transactions** — each migration runs in its own `BEGIN`/`COMMIT`
  block; a failure marks that migration dirty and halts further execution.
- **Ownership tracking** — every migration is tagged with its owning service name,
  enabling per-service registration while maintaining global ordering.
- **Multi-driver support** — uses sqlc code generation with dialect-specific
  adapters for PostgreSQL, MySQL, and SQLite. Each adapter wraps sqlc-generated
  queries and normalises types behind an unexported `querier` interface.
- **Build-info tracking** — each applied migration records the gas/migrate
  version and the owning module's version from Go build info.

## Constructor

```go
func New() func(gas.DatabaseProvider) *Service
```

Returns a curried DI-injectable constructor. The inner function receives
`gas.DatabaseProvider` from the container — requires `gas/database` to be
registered.

```go
// Usage: the outer call returns the constructor, DI calls the inner function.
migrate.New()  // → func(gas.DatabaseProvider) *Service
```

## Service

Implements `gas.Service`, `gas.MigrationManager`, and `gas.ReadyReporter`.

### Lifecycle

| Method       | Signature                       | Description                                            |
|--------------|---------------------------------|--------------------------------------------------------|
| `Name`       | `() string`                     | Returns `"gas/migrate"`                                |
| `Init`       | `() error`                      | Validates DatabaseProvider, creates `__gas_migrations` |
| `Close`      | `() error`                      | Marks service as closed; further operations error      |
| `CheckReady` | `(ctx context.Context) error`   | Reports ready when no dirty or pending migrations      |

### Readiness

`CheckReady` implements `gas.ReadyReporter` (Kubernetes readinessProbe). It returns a non-nil error when:

- The service is closed or not yet initialized.
- Any tracked migration is **dirty**.
- Any registered migration has not been applied yet.

`gas.HealthReporter` is intentionally not implemented — `gas/migrate` owns no live runtime state distinct from
the underlying DB, which `gas/database` reports on. Implementing both would duplicate or conflict with that signal.

### Registering Migrations

Services register their migrations during `Init()`. Three approaches:

```go
// Single migration
func (s *Service) Register(service string, migration gas.Migration)

// Batch of migrations
func (s *Service) RegisterSlice(service string, migrations []gas.Migration)

// Embedded SQL files (see "Embedded SQL Files" section)
func (s *Service) RegisterFS(service string, fsys fs.FS) error
```

All three set `migration.Service` automatically — callers don't need to fill it.
Registration is thread-safe (mutex-protected).

### Executing Migrations

```go
// Apply all unapplied migrations in global version order.
// Blocks if any migration is marked dirty.
func (s *Service) RunPending() error

// Reverse the last n applied migrations in reverse version order.
// If n > applied count, rolls back all applied migrations.
func (s *Service) Down(n int) error
```

Both methods return an error if the service is closed.

## Migration Struct (defined in gas core)

```go
type Migration struct {
    Version     string // e.g. "20250216001"
    Service     string // owning service name (set automatically by Register*)
    Description string // human-readable
    Up          string // apply SQL
    Down        string // rollback SQL
}
```

## Version Naming Convention

Format: `YYYYMMDDNNN` — date prefix + three-digit sequence number as a single
segment (no underscore separator).

```
20250216001  — first migration on Feb 16, 2025
20250216002  — second migration same day
20250301001  — first migration on Mar 1, 2025
```

Migrations are sorted globally by version string across all services. **Versions
must be unique across all services.** If two services register the same version,
`RunPending()` and `Down()` return an error identifying the conflicting version
and both service names.

## Embedded SQL Files (RegisterFS)

File naming: `{version}_{description}.up.sql` / `{version}_{description}.down.sql`

```
migrations/
    20250216001_create_users.up.sql
    20250216001_create_users.down.sql
    20250216002_create_sessions.up.sql
    20250216002_create_sessions.down.sql
```

- Version is the first underscore-delimited segment (e.g. `20250216001`).
- Description is parsed from remaining segments (underscores become spaces).
- Every `.up.sql` **must** have a matching `.down.sql` — `RegisterFS` returns
  an error otherwise.
- Lone `.down.sql` files without a matching `.up.sql` are silently ignored.

Usage with `embed`:

```go
import "embed"

//go:embed migrations/*.sql
var migrationsFS embed.FS

func (s *MyService) Init() error {
    return s.migrationMgr.RegisterFS(s.Name(), migrationsFS)
}
```

## How It Works

1. `Init()` creates the `__gas_migrations` tracking table if it doesn't exist.
2. `RunPending()` checks for dirty migrations first — if any exist, execution is
   blocked with an error listing the dirty versions.
3. All registered migrations are collected, checked for duplicate versions across
   services, and sorted globally by version.
4. Each unapplied migration runs in its own transaction (`BeginTx` → `Exec` →
   `Commit`). On failure, the migration is marked dirty and an error is returned.
5. On success, a tracking row is inserted with the version, service, description,
   gas/migrate version, and the owning module's version.

### Tracking Table Schema

The `__gas_migrations` table DDL is dialect-specific, embedded from
`db/{postgres,mysql,sqlite}/create_tracking_table.sql`. Key differences:

| Column       | PostgreSQL    | MySQL             | SQLite    |
|--------------|---------------|-------------------|-----------|
| `version`    | `TEXT PK`     | `VARCHAR(255) PK` | `TEXT PK` |
| `applied_at` | `TIMESTAMPTZ` | `DATETIME`        | `TEXT`    |
| `dirty`      | `BOOLEAN`     | `BOOLEAN`         | `INTEGER` |

Common columns: `service`, `description`, `migrate_version`, `module_version` (all `TEXT NOT NULL`).

## Dirty State Resolution

When a migration fails, it is marked dirty in the tracking table. All subsequent
`RunPending()` calls are blocked until resolved:

1. Fix the underlying issue (bad SQL, missing dependency).
2. Manually remove or update the dirty row in `__gas_migrations`.
3. Call `RunPending()` again.

## Multi-Dialect Support (sqlc)

Internal queries are generated by sqlc into three dialect-specific packages
(`db/postgres`, `db/mysql`, `db/sqlite`). An unexported `querier` interface
abstracts the differences, and three adapter structs normalise type mismatches
(e.g. SQLite's `string` timestamps → `time.Time`, `int64` dirty → `bool`).

`Init()` selects the correct adapter based on `DatabaseProvider.Driver()`:

| Driver              | Adapter           |
|---------------------|-------------------|
| `postgres`, `pgx`   | `postgresAdapter` |
| `mysql`             | `mysqlAdapter`    |
| `sqlite`, `sqlite3` | `sqliteAdapter`   |

Unsupported drivers cause `Init()` to return an error.

## Test Mock

The `migratetest` package provides `MockMigrationManager`, a configurable mock
of `gas.MigrationManager` for use in unit tests.

```go
import "github.com/gasmod/gas/migrate/migratetest"
```

### MockMigrationManager

```go
type MockMigrationManager struct {
    NameFn          func() string
    InitFn          func() error
    CloseFn         func() error
    RegisterFn      func(service string, m gas.Migration)
    RegisterSliceFn func(service string, migrations []gas.Migration)
    RegisterFSFn    func(service string, fsys fs.FS) error
    RunPendingFn    func() error
    DownFn          func(n int) error
    Calls           []Call
}
```

Implements `gas.MigrationManager`. Each method delegates to its `Fn` field if
set, otherwise returns the zero value (`Name()` returns `"gas/migrate"`). All
calls are recorded in `Calls` for assertions. Thread-safe.

| Method                  | Description                                       |
|-------------------------|---------------------------------------------------|
| `Reset()`               | Clear all recorded calls                          |
| `CallCount(method) int` | Count calls by method name (e.g. `"RunPending"`)  |

```go
mock := &migratetest.MockMigrationManager{}
mock.RunPendingFn = func() error { return nil }

svc := myservice.New(mock)
// assert registration happened:
if mock.CallCount("RegisterFS") != 1 {
    t.Error("expected one RegisterFS call")
}
```

## DI Wiring

```go
app := gas.NewApp(
    // Database provider is required — gas/migrate depends on it.
    gas.WithSingletonService[*database.Service](
        database.New(),
    ),
    // Register gas/migrate as a singleton service.
    gas.WithSingletonService[*migrate.Service](
        migrate.New(),
    ),
    // Your services that use migrations.
    gas.WithSingletonService[*auth.Service](auth.New),
)
```

The DI container resolves `gas.DatabaseProvider` automatically when constructing
the migrate service. The App calls `RunPending()` during its startup sequence
(after `InitServices` and config binding, before ready hooks and HTTP server).

## Complete Example: Service with Migrations

```go
package auth

import (
    "embed"

    "github.com/gasmod/gas"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Service struct {
    router       *gas.Router
    bus          *gas.EventBus
    migrationMgr gas.MigrationManager // interface, not concrete type
}

// New is the DI constructor — receives dependencies from the container.
func New(
    router *gas.Router,
    bus *gas.EventBus,
    mgr gas.MigrationManager,
) *Service {
    return &Service{
        router:       router,
        bus:          bus,
        migrationMgr: mgr,
    }
}

func (s *Service) Name() string { return "gas/auth" }

func (s *Service) Init() error {
    // Register migrations from embedded SQL files.
    if err := s.migrationMgr.RegisterFS(s.Name(), migrationsFS); err != nil {
        return err
    }

    // Or register inline migrations:
    // s.migrationMgr.RegisterSlice(s.Name(), []gas.Migration{
    //     {
    //         Version:     "20250216001",
    //         Description: "create users table",
    //         Up:          "CREATE TABLE users (id SERIAL PRIMARY KEY, email TEXT NOT NULL);",
    //         Down:        "DROP TABLE users;",
    //     },
    // })

    s.router.Handle(s.Name(), "GET", "/users", s.handleListUsers)
    return nil
}

func (s *Service) handleListUsers(ctx gas.Context, db gas.DatabaseProvider) error {
    // db is resolved per-request from the scoped container
    rows, err := db.Query(ctx, "SELECT id, email FROM users")
    if err != nil {
        return err
    }
    defer rows.Close()
    // ... process rows
    return ctx.JSON(200, nil)
}

func (s *Service) Close() error { return nil }
```

### Wiring in main.go

```go
package main

import (
    "github.com/gasmod/gas"
    database "github.com/gasmod/gas/database"
    migrate "github.com/gasmod/gas/migrate"

    "myapp/auth"
)

func main() {
    app := gas.NewApp(
        gas.WithSingletonService[*database.Service](database.New()),
        gas.WithSingletonService[*migrate.Service](migrate.New()),
        gas.WithSingletonService[*auth.Service](auth.New),
    )
    app.Run()
}
```

## Choosing a Registration Method

| Method          | Best For                                                 |
|-----------------|----------------------------------------------------------|
| `Register`      | One-off migrations or dynamically generated SQL          |
| `RegisterSlice` | Small number of inline migrations defined in Go code     |
| `RegisterFS`    | Production services with SQL files under version control |

`RegisterFS` is the recommended approach for production services — it keeps SQL
separate from Go code, makes migrations reviewable in PRs, and works naturally
with `go:embed`.
