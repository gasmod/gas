---
name: gas-database
description: >
  Reference documentation for the gas/database Go package
  (github.com/gasmod/gas/database) — the database connection service for the
  Gas ecosystem. Use this skill when writing, reviewing, or debugging Go code
  that uses gas/database for database access, transactions, connection pooling,
  sqlc integration, or PostgreSQL/SQLite connectivity. Covers ModeSQL and
  ModePgx backends, DI wiring via gas.DatabaseProvider, transaction helpers
  (BeginTx, WithTx, BeginPgxTx, WithPgxTx), pgxpool native access via Pool
  and PoolFrom, connector injection, connection retry with exponential
  backoff, and configuration binding. Make sure to use this skill whenever
  working with database connections in the Gas ecosystem, even if the user
  doesn't explicitly mention gas/database.
---

# Gas Database Package Reference

Database connection service for the Gas ecosystem. Wraps `database/sql` and
native `pgxpool` to provide connection management, transaction helpers, and
sqlc compatibility.

```
import database "github.com/gasmod/gas/database"
```

## Choosing a Mode

| Mode                                  | Backend        | Use case                                                          |
|---------------------------------------|----------------|-------------------------------------------------------------------|
| `database.ModeSQL` (`"sql"`, default) | `database/sql` | Any driver: PostgreSQL, SQLite, MySQL                             |
| `database.ModePgx` (`"pgx"`)         | `pgxpool.Pool` | Native pgx for PostgreSQL (better perf, pgx types, batch queries) |

In both modes, `DB()` returns `*sql.DB` so sqlc `database/sql` mode always
works. In pgx mode, `Pool()` additionally returns `*pgxpool.Pool` for sqlc pgx
mode.

## Constructor

```go
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) *Service
```

`New` captures options and returns a DI-injectable constructor. The returned
func receives `gas.ConfigProvider` and `gas.Logger` from the DI container.

### Options

| Option                              | Description                                                                 |
|-------------------------------------|-----------------------------------------------------------------------------|
| `WithConfig(cfg *Config)`           | Set database configuration explicitly (skips config binding from DI)        |
| `WithConnector(c driver.Connector)` | Provide a `driver.Connector` for ModeSQL; uses `sql.OpenDB(connector)`      |

- `WithConnector` — `Database.Driver` and `Database.DSN` are not required when
  a connector is set. ModeSQL only: the pgx pool is built from the DSN alone,
  so a connector in ModePgx is rejected at `Init` rather than silently ignored.
- If `WithConfig` is not provided, the service automatically binds
  configuration from the `gas.ConfigProvider` injected via DI.

## Service

`Service` implements `gas.Service`, `gas.DatabaseProvider`, `gas.HealthReporter`, and `gas.ReadyReporter`.

### Lifecycle (gas.Service)

| Method    | Signature        | Description                              |
|-----------|------------------|------------------------------------------|
| `Name`    | `() string`      | Returns `"gas/database"`                 |
| `Init`    | `() error`       | Opens connection, configures pool, pings |
| `Close`   | `() error`       | Closes underlying connections            |

### Database Access

| Method     | Signature                                                 | Description                                   |
|------------|-----------------------------------------------------------|-----------------------------------------------|
| `DB`       | `() *sql.DB`                                              | Always works in both modes                    |
| `Pool`     | `() *pgxpool.Pool`                                        | Native pool; nil in ModeSQL                   |
| `PoolFrom` | `(provider gas.DatabaseProvider) (*pgxpool.Pool, bool)`   | Package-level; unwraps a provider to its pool |
| `Query`    | `(ctx, query string, args ...any) (gas.Rows, error)`      | Implements `gas.DatabaseProvider`             |
| `Exec`     | `(ctx, query string, args ...any) (gas.Result, error)`    | Implements `gas.DatabaseProvider`             |
| `Ping`     | `(ctx context.Context) error`                             | Verify connectivity                           |
| `Driver`   | `() string`                                               | Returns driver name based on mode/settings    |

`Query` and `Exec` return an error if the service has been closed.

### Probes (gas.HealthReporter, gas.ReadyReporter)

| Method        | Signature                       | Description                                                                    |
|---------------|---------------------------------|--------------------------------------------------------------------------------|
| `CheckHealth` | `(ctx context.Context) error`   | Liveness. Fails only when uninitialized or closed; does not ping.              |
| `CheckReady`  | `(ctx context.Context) error`   | Readiness. Fails when closed or when `Ping(ctx)` fails.                        |

Auto-discovered by gas core — no extra wiring needed. Transient connectivity
issues surface through readiness (drain traffic) rather than liveness (restart),
since `database/sql` and `pgxpool` both auto-reconnect.

### Transactions

```go
// Manual — caller commits/rolls back
func (s *Service) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)

// Automatic — commits if fn returns nil, rolls back on error or panic
func (s *Service) WithTx(ctx context.Context, opts *sql.TxOptions, fn func(*sql.Tx) error) error

// Native pgx equivalents; both error out when not running in ModePgx.
// Pass nil opts for pgx defaults.
func (s *Service) BeginPgxTx(ctx context.Context, opts *pgx.TxOptions) (pgx.Tx, error)
func (s *Service) WithPgxTx(ctx context.Context, opts *pgx.TxOptions, fn func(pgx.Tx) error) error
```

Error handling in `WithTx` / `WithPgxTx`:

- `fn` returns nil — the transaction is committed; a commit failure is returned wrapped.
- `fn` returns an error — the transaction is rolled back and `fn`'s error is
  returned. A rollback that itself fails is logged and joined onto that error
  with `errors.Join`; `sql.ErrTxDone` / `pgx.ErrTxClosed` are treated as
  success, so `fn` may commit or roll back itself.
- `fn` panics — the transaction is rolled back and the panic propagates
  unchanged; a failed rollback is logged rather than returned.

`WithPgxTx` rolls back on a context detached from `ctx`'s cancellation, so
cleanup still runs when `ctx` is already done. The commit uses `ctx` unchanged,
so an already-canceled `ctx` fails the commit rather than persisting work the
caller abandoned.

## sqlc

sqlc generates its own `DBTX` interface in the target package — satisfied by
`*sql.DB` and `*sql.Tx` in `database/sql` mode, and by `*pgxpool.Pool` and
`pgx.Tx` in pgx mode. Pass `DB()`, `Pool()`, or a transaction from the helpers
above into the generated constructor. This package does not declare a `DBTX`
of its own.

## Config

```go
type Config struct {
    Database Settings
}

type Settings struct {
    Mode              string        // "sql" (default) or "pgx"
    Driver            string        // database/sql driver name, default "postgres" (ModeSQL only)
    DSN               string        // connection string (required, unless ModeSQL with WithConnector)
    MaxOpenConns      int32         // default 25
    MaxIdleConns      int           // default 5 (ModeSQL only, pgx manages internally)
    ConnMaxLifetime   time.Duration // default 30m
    ConnMaxIdleTime   time.Duration // default 5m
    ConnRetries       int           // default 0 (no retries); number of retry attempts on connect failure
    ConnRetryInterval time.Duration // default 2s; base interval, doubles each attempt (exponential backoff)
}

func DefaultConfig() *Config
func (c *Config) Validate() error
```

### Connection Retry

When `ConnRetries > 0`, the service retries the initial connection with
exponential backoff. The interval starts at `ConnRetryInterval` (default 2s)
and doubles after each failed attempt. If all retries are exhausted, `Init`
returns an error.

## Driver Constants

```go
const (
    DriverPostgres = "postgres"
    DriverPgx      = "pgx"
    DriverSQLite   = "sqlite"
)
```

## DI Wiring Patterns

### Basic registration

```go
app := gas.NewApp(
    gas.WithService[*database.Service](
        database.New(database.WithConfig(&database.Config{
            Database: database.Settings{
                DSN:    "postgres://user:pass@localhost:5432/mydb?sslmode=disable",
                Driver: "pgx",
            },
        })),
        gas.ServiceLifetimeSingleton,
    ),
)
```

### Native pgx mode

```go
database.New(database.WithConfig(&database.Config{
    Database: database.Settings{
        Mode: database.ModePgx,
        DSN:  "postgres://user:pass@localhost:5432/mydb?sslmode=disable",
    },
}))
// After Init(): svc.DB() -> *sql.DB, svc.Pool() -> *pgxpool.Pool
```

### SQLite

```go
import _ "modernc.org/sqlite"

database.New(database.WithConfig(&database.Config{
    Database: database.Settings{
        Driver: "sqlite",
        DSN:    "./app.db",
    },
}))
```

### Custom connector

```go
import "github.com/jackc/pgx/v5/stdlib"

connConfig, _ := pgx.ParseConfig("postgres://user:pass@localhost:5432/mydb")
connector := stdlib.GetConnector(*connConfig)
database.New(database.WithConnector(connector))
```

### With connection retry

```go
database.New(database.WithConfig(&database.Config{
    Database: database.Settings{
        DSN:               "postgres://user:pass@localhost:5432/mydb?sslmode=disable",
        ConnRetries:       3,                // retry up to 3 times
        ConnRetryInterval: 2 * time.Second,  // 2s, 4s, 8s backoff
    },
}))
```

### Consuming via gas.DatabaseProvider

Services receive the database through the provider interface, never importing
gas/database directly:

```go
type Service struct {
    db gas.DatabaseProvider
}

func New(db gas.DatabaseProvider) *Service {
    return &Service{db: db}
}

func (s *Service) Init() error {
    s.queries = authdb.New(s.db.DB()) // sqlc database/sql mode
    return nil
}
```

### Native pgx access via PoolFrom

`PoolFrom` unwraps a `gas.DatabaseProvider` to its native pool, reporting
`false` when the provider is not pgx-backed or is running in `ModeSQL`:

```go
func (s *Service) Init() error {
    if pool, ok := database.PoolFrom(s.db); ok {
        s.queries = authdb.New(pool) // sqlc pgx mode
    } else {
        s.queries = authdb.New(s.db.DB()) // fallback
    }
    return nil
}
```

To avoid importing gas/database in the consuming service, declare a local
interface and type-assert — what `PoolFrom` does internally:

```go
// Define locally where consumed
type PgxProvider interface {
    Pool() *pgxpool.Pool
}

if pp, ok := s.db.(PgxProvider); ok && pp.Pool() != nil {
    s.queries = authdb.New(pp.Pool())
}
```

### Transaction patterns with sqlc

```go
// Manual
tx, err := dbSvc.BeginTx(ctx, nil)
qtx := queries.WithTx(tx)
// ... use qtx ...
tx.Commit()

// Automatic
dbSvc.WithTx(ctx, nil, func(tx *sql.Tx) error {
    qtx := queries.WithTx(tx)
    if err := qtx.CreateUser(ctx, params); err != nil {
        return err // rollback
    }
    return qtx.CreateProfile(ctx, params) // commit if nil
})

// Automatic, native pgx (ModePgx only)
dbSvc.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
    qtx := queries.WithTx(tx)
    return qtx.CreateUser(ctx, params)
})
```

## Complete Example

```go
package main

import (
    "time"

    _ "github.com/jackc/pgx/v5/stdlib"

    "github.com/gasmod/gas"
    database "github.com/gasmod/gas/database"
)

func main() {
    app := gas.NewApp(
        // Register database as a singleton service
        gas.WithService[*database.Service](
            database.New(database.WithConfig(&database.Config{
                Database: database.Settings{
                    DSN:               "postgres://user:pass@localhost:5432/mydb?sslmode=disable",
                    Driver:            "pgx",
                    MaxOpenConns:      25,
                    MaxIdleConns:      5,
                    ConnMaxLifetime:   30 * time.Minute,
                    ConnMaxIdleTime:   5 * time.Minute,
                    ConnRetries:       3,               // retry on startup failure
                    ConnRetryInterval: 2 * time.Second, // exponential backoff: 2s, 4s, 8s
                },
            })),
            gas.ServiceLifetimeSingleton, // shared across all services
        ),
        // Other services receive *database.Service as gas.DatabaseProvider via DI
    )

    app.Run()
}
```
