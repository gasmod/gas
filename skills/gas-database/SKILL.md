---
name: gas-database
description: >
  Reference documentation for the gas/database Go package
  (github.com/gasmod/gas/database), the database connection service for the
  Gas ecosystem. Use this skill when writing, reviewing, or debugging Go code
  that uses gas/database for database access, transactions, connection pooling,
  sqlc integration, or PostgreSQL/MySQL/SQLite connectivity. Covers ModeSQL and
  ModePgx backends, dialects, DI wiring via gas.DatabaseProvider, transaction
  helpers (BeginTx, WithTx, BeginPgxTx, WithPgxTx), pgxpool native access via
  Pool and PoolFrom, connector injection, connection retry with exponential
  backoff, SafeError, sentinel errors, and configuration binding. Make sure to
  use this skill whenever working with database connections in the Gas
  ecosystem, even if the user doesn't explicitly mention gas/database.
---

# Gas Database Package Reference

Database connection service for the Gas ecosystem. Connects to PostgreSQL,
MySQL or SQLite through `database/sql` or native `pgxpool`, with connection
management, transaction helpers, and sqlc compatibility.

```
import database "github.com/gasmod/gas/database"
```

## Choosing a Mode and Dialect

| Mode                                  | Backend        | Use case                                                           |
|---------------------------------------|----------------|--------------------------------------------------------------------|
| `database.ModeSQL` (`"sql"`, default) | `database/sql` | Any dialect, or a connector from `WithConnector`                   |
| `database.ModePgx` (`"pgx"`)          | `pgxpool.Pool` | Native pgx for PostgreSQL only (pgx types, batching); no connector |

`Mode` and `Dialect` are string types. The dialect is required (no default)
unless a connector is given, and is what `Driver()` returns:

| Dialect                                   | Driver in ModeSQL                                                  |
|-------------------------------------------|--------------------------------------------------------------------|
| `database.DialectPostgres` (`"postgres"`) | pgx's database/sql adapter; imported by the module                 |
| `database.DialectMySQL` (`"mysql"`)       | `github.com/go-sql-driver/mysql`; imported by the module           |
| `database.DialectSQLite` (`"sqlite"`)     | not imported: the app imports `modernc.org/sqlite` (or another driver registered as `sqlite`) |

- MySQL: `ParseTime` is always on; `multiStatements` stays off unless the DSN
  sets it. The driver's stderr log goes to the service logger as a warn event
  `mysql driver reported an error`, without the driver's text.
- SQLite: when the DSN sets no busy timeout (`_busy_timeout`, `_timeout`, or a
  `_pragma=busy_timeout(...)`), the service appends
  `_pragma=busy_timeout(5000)` (`DefaultSQLiteBusyTimeout`, 5s), so concurrent
  writers wait instead of failing with SQLITE_BUSY. `_pragma=busy_timeout(0)`
  turns the wait off. No registered `sqlite` driver fails `Init` with
  `ErrDriverNotRegistered`.

In both modes `DB()` returns `*sql.DB`, so sqlc `database/sql` mode always
works. In ModePgx, `DB()` is derived from the pool (`stdlib.OpenDBFromPool`)
and `Pool()` returns `*pgxpool.Pool` for sqlc pgx mode.

## Constructor

```go
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Service, error)
```

`New` captures options and returns a DI-injectable constructor. A nil logger
fails it with `ErrNilLogger`; a nil `gas.ConfigProvider` is accepted, and
`Init` then requires `WithConfig` (else `ErrNoConfigProvider`). In a container
both types must be registered, since the constructor's parameters are resolved
by type.

Register the constructor under `gas.DatabaseProvider`, the type other services
and gas/migrate inject. The container resolves by exact type: a registration
under `*database.Service` leaves `gas.DatabaseProvider` unregistered (consumers
fail with `gas.ErrNotRegistered`) unless it is aliased with
`gas.WithServiceAlias[*database.Service, gas.DatabaseProvider]()`.

### Options

| Option                                         | Description                                                               |
|------------------------------------------------|---------------------------------------------------------------------------|
| `WithConfig(cfg *Config)`                      | Use this config instead of binding from the `gas.ConfigProvider`          |
| `WithConnector(d Dialect, c driver.Connector)` | ModeSQL opens through `c` instead of a DSN; `d` is the dialect `c` speaks |

- `WithConfig` replaces the whole `Config`, so start from `DefaultConfig()`
  and change fields. `Init` takes a copy (never mutating the caller's struct)
  and checks the same `validate` tags as binding; a nil config is
  `ErrNilConfig`.
- Without `WithConfig`, every `Init` binds `DefaultConfig()` from the
  provider's `database` section.
- `WithConnector`: `Mode` must be ModeSQL, `Dialect` empty or `d`, `DSN`
  empty, `c` non-nil. For custom TLS, IAM auth and the like. The connector is
  reused by every connect attempt and never closed by the service; the caller
  owns it.

## Service

`Service` implements `gas.Service`, `gas.DatabaseProvider`, `gas.HealthReporter`, and `gas.ReadyReporter`. It is safe for concurrent use.

### Lifecycle (gas.Service)

| Method  | Signature                 | Description                                                             |
|---------|---------------------------|-------------------------------------------------------------------------|
| `Name`  | `() string`               | Returns `database.ServiceName`, `"gas/database"`                        |
| `Init`  | `(context.Context) error` | Resolves config, connects (open + ping, with retries); `ErrAlreadyInitialized` if already initialized |
| `Close` | `(context.Context) error` | Closes the handle and, in ModePgx, the pool; nil when not initialized   |

A failed `Init` closes what it opened and leaves the service uninitialized.
If `Close`'s context ends first, it returns an error wrapping
`ErrCloseTimeout` and the context error while closing continues in the
background. After `Close`, `Init` may run again with a fresh config and a new
handle; services that captured `DB()` in their own `Init` (migrate, auth
stores) keep the old one and must be restarted too.

### Database Access

| Method      | Signature                                               | Description                                         |
|-------------|---------------------------------------------------------|-----------------------------------------------------|
| `DB`        | `() *sql.DB`                                            | Both modes; nil until initialized                   |
| `Pool`      | `() *pgxpool.Pool`                                      | Native pool; nil in ModeSQL or uninitialized        |
| `PoolFrom`  | `(provider gas.DatabaseProvider) (*pgxpool.Pool, bool)` | Package-level; unwraps a provider to its pool       |
| `Query`     | `(ctx, query string, args ...any) (gas.Rows, error)`    | Implements `gas.DatabaseProvider`                   |
| `Exec`      | `(ctx, query string, args ...any) (gas.Result, error)`  | Implements `gas.DatabaseProvider`                   |
| `Ping`      | `(ctx context.Context) error`                           | Pings within ctx and `ConnectTimeout`               |
| `Driver`    | `() string`                                             | The dialect (`"postgres"`, `"mysql"`, `"sqlite"`); `""` until initialized |
| `SafeError` | `(err error) error`                                     | Replaces a connection-level error with a safe one   |

Before a successful `Init`, after a failed one, and after `Close`, `Ping`,
`Query`, `Exec`, the transaction methods and both probes return an error
wrapping `ErrNotInitialized`.

Connection-level failures (a pgx `*pgconn.ConnectError`, a `net.Error`,
`driver.ErrBadConn`, `mysql.ErrInvalidConn`, `pgconn.ErrConnClosed`) from
`Query`, `Exec`, the transaction methods and `Close` are replaced by
`<op>: <reason>` wrapping `ErrConnect` (and the context error when the
context ended), never the driver error, because pgx and MySQL connect errors
can carry the user, host and password. Statement errors pass through,
wrapped after a prefix such as `gas/database: query: `. Calls made directly
on `DB()` or `Pool()` get raw driver errors; run them through `SafeError`
before logging or returning them (migrate does this):

```go
if _, err := svc.DB().ExecContext(ctx, stmt); err != nil {
    return svc.SafeError(err) // "gas/database: connection: <reason>" or err unchanged
}
```

### Probes (gas.HealthReporter, gas.ReadyReporter)

| Method        | Signature                     | Description                                                    |
|---------------|-------------------------------|----------------------------------------------------------------|
| `CheckHealth` | `(ctx context.Context) error` | Liveness. No I/O; fails only when not initialized.             |
| `CheckReady`  | `(ctx context.Context) error` | Readiness. Pings; a failure wraps `ErrConnect` after `ping: `. |

Run by gas core's `Worker.CheckHealth` / `CheckReady` for active services; no
extra wiring. Transient outages surface through readiness (drain traffic)
rather than liveness (restart), since the handle and pool reconnect on their
own.

### Transactions

```go
// Manual: caller commits/rolls back
func (s *Service) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)

// Automatic: commits if fn returns nil, rolls back on error, panic or Goexit
func (s *Service) WithTx(ctx context.Context, opts *sql.TxOptions, fn func(*sql.Tx) error) error

// Native pgx equivalents; both return ErrModeNotPgx outside ModePgx.
// Pass nil opts for pgx defaults.
func (s *Service) BeginPgxTx(ctx context.Context, opts *pgx.TxOptions) (pgx.Tx, error)
func (s *Service) WithPgxTx(ctx context.Context, opts *pgx.TxOptions, fn func(pgx.Tx) error) error
```

Behavior of `WithTx` / `WithPgxTx`:

- A nil `fn` is `ErrNilFunc`, checked before the transaction begins.
- `fn` returns nil: the transaction is committed; a commit failure is
  returned after `gas/database: failed to commit transaction: `. An `fn` that
  commits or rolls back itself and returns nil gets an error wrapping
  `sql.ErrTxDone` / `pgx.ErrTxClosed`.
- `fn` returns an error: rolled back, and `fn`'s error is returned unchanged
  (not wrapped). A failed rollback is joined onto it; a rollback that finds
  the transaction already ended is not a failure.
- `fn` panics or calls `runtime.Goexit` (as `t.FailNow` does): rolled back,
  the panic or Goexit continues, and a failed rollback is logged at warn as
  `transaction rollback failed` with a fixed `reason`.

`WithPgxTx` commits with `ctx`, so an already-canceled `ctx` fails the commit
rather than persisting abandoned work. It rolls back on `ctx` without its
cancellation, bounded by `ConnectTimeout`. `WithTx` cannot bound its rollback
(`database/sql` takes no context there); `database/sql` rolls back itself when
`ctx` ends.

## sqlc

sqlc generates its own `DBTX` interface in the target package, satisfied by
`*sql.DB` and `*sql.Tx` in `database/sql` mode, and by `*pgxpool.Pool` and
`pgx.Tx` in pgx mode. Pass `DB()`, `Pool()`, or a transaction from the helpers
above into the generated constructor. This package declares no `DBTX`.

## Config

```go
type Config struct {
    Database Settings
}

type Settings struct {
    Mode              Mode          // "sql" (default) or "pgx"
    Dialect           Dialect       // "postgres", "mysql" or "sqlite"; no default
    DSN               gas.Secret    // dialect's format, printed as [redacted]; required unless a connector is given
    ConnMaxLifetime   time.Duration // default 30m; 1s..2h
    ConnMaxIdleTime   time.Duration // default 5m; 1s..1h, <= ConnMaxLifetime
    ConnectTimeout    time.Duration // default 5s; 1s..1m; bounds each connect attempt, ping, pgx rollback
    ConnRetryInterval time.Duration // default 2s; 100ms..30s, checked only when ConnRetries > 0
    MaxIdleConns      int           // default 5; 0..MaxOpenConns (ModeSQL applies it)
    ConnRetries       int           // default 0; 0..10
    MaxOpenConns      int32         // default 25; 1..1000 (pgx MaxConns in ModePgx)
}

func DefaultConfig() *Config
```

Defaults are exported as `DefaultMode`, `DefaultMaxOpenConns`,
`DefaultMaxIdleConns`, `DefaultConnMaxLifetime`, `DefaultConnMaxIdleTime`,
`DefaultConnectTimeout`, `DefaultConnRetries`, `DefaultConnRetryInterval`,
plus `MaxConnRetryInterval` (30s). Config keys are lowercase under
`database`: `database.mode`, `database.dialect`, `database.dsn`,
`database.maxopenconns`, `database.maxidleconns`, `database.connmaxlifetime`,
`database.connmaxidletime`, `database.connecttimeout`, `database.connretries`,
`database.connretryinterval`. With gas/config's default EnvProvider, `__`
separates levels: `DATABASE__DIALECT`, `DATABASE__DSN`.

No zero means "driver default": both modes apply every limit as configured. In
ModePgx the settings replace the DSN's matching `pool_*` parameters. There is
no `Validate` method. After the tags, `Init` checks, in order: the connector
rules; without a connector, dialect set, DSN set, and ModePgx only with
`postgres`; `MaxIdleConns <= MaxOpenConns`; the retry interval bounds. A
failure wraps `ErrInvalidConfig` and names the setting, never its value.

### Connecting and Retry

`Init` first parses the DSN (`ErrInvalidDSN`) and finds the driver
(`ErrDriverNotRegistered`); neither is retried. Each connect attempt opens and
pings within `Init`'s context and `ConnectTimeout`. A failed attempt is
retried up to `ConnRetries` times, logging `database connection failed,
retrying` at warn with `attempt`, `retries`, `wait`, `reason`. The wait starts
at `ConnRetryInterval` and doubles, capped at `MaxConnRetryInterval`; it ends
early when `Init`'s context ends. Exhausted attempts fail with `connect: ` and
a fixed reason (`authentication failed`, `access denied`, `database does not
exist`, `timed out`, `connection refused`, `unreachable`, `failed`) wrapping
`ErrConnect`, plus the context error if it ended. The driver error is not kept.

## Errors

Sentinels, for `errors.Is`: `ErrNilLogger`, `ErrNilConfig`,
`ErrNoConfigProvider`, `ErrInvalidConfig`, `ErrAlreadyInitialized`,
`ErrDriverNotRegistered`, `ErrInvalidDSN`, `ErrConnect`, `ErrNotInitialized`,
`ErrModeNotPgx`, `ErrNilFunc`, `ErrCloseTimeout`. Every error the package
makes starts with `gas/database: `. No error or log includes the DSN,
password, user, host, port, database name or TLS material.

## DI Wiring Patterns

### Basic registration (config from gas.ConfigProvider)

```go
app := gas.NewApp(
    gas.WithServiceInstance[gas.ConfigProvider](cfg), // a loaded *config.Config
    gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
    gas.WithSingletonService[gas.DatabaseProvider](database.New()),
)
// Environment: DATABASE__DIALECT=postgres DATABASE__DSN=postgres://...
```

### Native pgx mode

```go
cfg := database.DefaultConfig()
cfg.Database.Mode = database.ModePgx
cfg.Database.Dialect = database.DialectPostgres // required for ModePgx
cfg.Database.DSN = "postgres://user:pass@localhost:5432/mydb?sslmode=disable"
database.New(database.WithConfig(cfg))
// After Init(ctx): svc.DB() -> *sql.DB, svc.Pool() -> *pgxpool.Pool
```

### SQLite

```go
import _ "modernc.org/sqlite"

cfg := database.DefaultConfig()
cfg.Database.Dialect = database.DialectSQLite
cfg.Database.DSN = "file:app.db" // busy_timeout(5000) is appended
database.New(database.WithConfig(cfg))
```

### Custom connector

```go
import "github.com/jackc/pgx/v5/stdlib"

connConfig, err := pgx.ParseConfig("postgres://user:pass@localhost:5432/mydb")
if err != nil {
    return err
}
connector := stdlib.GetConnector(*connConfig)
// Leave Dialect and DSN empty; Mode must stay ModeSQL.
database.New(database.WithConnector(database.DialectPostgres, connector))
```

### With connection retry

```go
cfg := database.DefaultConfig()
cfg.Database.Dialect = database.DialectPostgres
cfg.Database.DSN = "postgres://user:pass@localhost:5432/mydb?sslmode=disable"
cfg.Database.ConnRetries = 3                     // retry up to 3 times
cfg.Database.ConnRetryInterval = 2 * time.Second // waits 2s, 4s, 8s
database.New(database.WithConfig(cfg))
```

### Consuming via gas.DatabaseProvider

Services receive the database through the provider interface, never importing
gas/database directly:

```go
type Service struct {
    db      gas.DatabaseProvider
    queries *authdb.Queries
}

func New(db gas.DatabaseProvider) *Service {
    return &Service{db: db}
}

func (s *Service) Init(ctx context.Context) error {
    s.queries = authdb.New(s.db.DB()) // sqlc database/sql mode
    return nil
}
```

Pick SQL per dialect with `s.db.Driver()`. To sanitize errors from direct
`DB()` use without importing gas/database, assert a local interface with
`SafeError(error) error`, as migrate does.

### Native pgx access via PoolFrom

`PoolFrom` returns the pool and true when the provider has a `Pool` method
returning a non-nil pool; nil and false otherwise (ModeSQL, uninitialized, or
another provider):

```go
func (s *Service) Init(ctx context.Context) error {
    if pool, ok := database.PoolFrom(s.db); ok {
        s.queries = authdb.New(pool) // sqlc pgx mode
    } else {
        s.queries = authdb.New(s.db.DB()) // fallback
    }
    return nil
}
```

To avoid importing gas/database, declare a local interface and type-assert:

```go
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
if err != nil {
    return err
}
defer tx.Rollback() // no-op after Commit
if err := queries.WithTx(tx).CreateUser(ctx, params); err != nil {
    return err
}
return tx.Commit()

// Automatic
err := dbSvc.WithTx(ctx, nil, func(tx *sql.Tx) error {
    qtx := queries.WithTx(tx)
    if err := qtx.CreateUser(ctx, params); err != nil {
        return err // rollback; returned unchanged
    }
    return qtx.CreateProfile(ctx, params) // commit if nil
})

// Automatic, native pgx (ModePgx only, else ErrModeNotPgx)
err = dbSvc.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
    return pgxQueries.WithTx(tx).CreateUser(ctx, params)
})
```

## Complete Example

```go
package main

import (
    "log"

    "github.com/gasmod/gas"
    config "github.com/gasmod/gas/config"
    database "github.com/gasmod/gas/database"
    gaslog "github.com/gasmod/gas/log"
)

func main() {
    // Reads DATABASE__DIALECT, DATABASE__DSN, DATABASE__CONNRETRIES, ...
    cfg := config.New()
    if err := cfg.Load(); err != nil {
        log.Fatal(err)
    }

    app := gas.NewApp(
        // The database constructor injects both.
        gas.WithServiceInstance[gas.ConfigProvider](cfg),
        gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
        // Register under the interface consumers inject.
        gas.WithSingletonService[gas.DatabaseProvider](database.New()),
        // Other services receive it as gas.DatabaseProvider via DI.
    )

    if err := app.Run(); err != nil {
        log.Fatal(err)
    }
}
```
