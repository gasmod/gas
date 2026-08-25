# gas/database

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/database.svg)](https://pkg.go.dev/github.com/gasmod/gas/database) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=database/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Database connection service for the [Gas](https://github.com/gasmod/gas) ecosystem. Wraps `database/sql` and native
`pgxpool` to provide connection management, transaction helpers, and sqlc compatibility.

## Install

```bash
go get github.com/gasmod/gas/database
```

## Modes

| Mode                         | Backend        | Use case                                                                 |
|------------------------------|----------------|--------------------------------------------------------------------------|
| `database.ModeSQL` (default) | `database/sql` | Any driver: PostgreSQL, SQLite, MySQL, etc.                              |
| `database.ModePgx`           | `pgxpool.Pool` | Native pgx for PostgreSQL (better performance, pgx types, batch queries) |

In both modes, `DB()` returns a `*sql.DB` so sqlc `database/sql` mode always works. In pgx mode, `Pool()` additionally
returns the native `*pgxpool.Pool` for sqlc pgx mode.

## Usage

### Basic setup (database/sql)

```go
package main

import (
	_ "github.com/jackc/pgx/v5/stdlib" // register pgx as database/sql driver

	"github.com/gasmod/gas"
	database "github.com/gasmod/gas/database"
)

func main() {
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
		// ...
	)

	app.Run()
}
```

### Native pgx mode

```go
database.New(database.WithConfig(&database.Config{
	Database: database.Settings{
		Mode: database.ModePgx,
		DSN:  "postgres://user:pass@localhost:5432/mydb?sslmode=disable",
	},
}))

// After Init(), both are available:
// svc.DB()   -> *sql.DB (via stdlib adapter)
// svc.Pool() -> *pgxpool.Pool
```

### Using a connector (sql.OpenDB)

When you need full control over connection setup (e.g., custom TLS, auth tokens), pass a `driver.Connector` directly:

```go
import "github.com/jackc/pgx/v5/stdlib"

connConfig, _ := pgx.ParseConfig("postgres://user:pass@localhost:5432/mydb")
connector := stdlib.GetConnector(*connConfig)

database.New(database.WithConnector(connector))
```

When a connector is provided, `Database.Driver` and `Database.DSN` are not
required. A connector is ModeSQL only: the pgx pool is built from the DSN
alone, so pairing one with `Database.Mode: "pgx"` is rejected at `Init` rather
than silently ignored.

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

### Dependency injection

Services receive the database through `gas.DatabaseProvider` via constructor injection:

```go
// gas/auth/service.go
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

For services that want native pgx access, `PoolFrom` unwraps the provider. It reports `false` when the provider is not
pgx-backed or is running in `ModeSQL`, so the caller can fall back to `DB()`:

```go
// gas/auth/service.go
func (s *Service) Init() error {
	if pool, ok := database.PoolFrom(s.db); ok {
		s.queries = authdb.New(pool) // sqlc pgx mode
	} else {
		s.queries = authdb.New(s.db.DB()) // fallback to database/sql
	}
	return nil
}
```

To avoid importing gas/database in the consuming service, declare a local interface matching `Pool()` and
type-assert the provider yourself, which is what `PoolFrom` does internally:

```go
// gas/auth/providers.go
type PgxProvider interface {
	Pool() *pgxpool.Pool
}
```

### Transactions

Manual transaction management:

```go
tx, err := dbSvc.BeginTx(ctx, nil)
if err != nil {
	return err
}
// use tx with sqlc: queries.WithTx(tx)
err = tx.Commit()
```

Automatic commit/rollback with `WithTx`:

```go
err := dbSvc.WithTx(ctx, nil, func(tx *sql.Tx) error {
	qtx := queries.WithTx(tx)
	if err := qtx.CreateUser(ctx, params); err != nil {
		return err // triggers rollback
	}
	return qtx.CreateProfile(ctx, params) // commits if nil
})
```

If `fn` returns an error, `WithTx` rolls back and returns `fn`'s error; a rollback that itself fails is logged and
joined onto that error with `errors.Join`. A commit failure is returned wrapped. `WithTx` also rolls back on panic, in
which case a failed rollback is logged rather than returned so the panic propagates unchanged.

### Native pgx transactions

In `ModePgx`, `BeginPgxTx` and `WithPgxTx` mirror the pair above against `pgx.Tx`, for sqlc's pgx mode and pgx-only
features. Both return an error when the service is not running in `ModePgx`:

```go
err := dbSvc.WithPgxTx(ctx, nil, func(tx pgx.Tx) error {
	qtx := queries.WithTx(tx)
	if err := qtx.CreateUser(ctx, params); err != nil {
		return err // triggers rollback
	}
	return qtx.CreateProfile(ctx, params) // commits if nil
})
```

`WithPgxTx` rolls back on a context detached from `ctx`'s cancellation, so cleanup still runs when `ctx` is already
done. The commit uses `ctx` unchanged, so an already-canceled `ctx` fails the commit rather than persisting work the
caller abandoned.

## Health and readiness probes

`Service` implements `gas.HealthReporter` and `gas.ReadyReporter`, auto-discovered
by gas core:

- `CheckHealth` (liveness) — fails only when the service is uninitialized or
  closed. It does not ping the database, because `database/sql` and `pgxpool`
  both auto-reconnect; a transient outage should not trigger a pod restart.
- `CheckReady` (readiness) — pings the database. A failure signals that traffic
  should drain off this instance until the dependency recovers.

## Config

If `WithConfig` is not provided, the service automatically binds configuration from the `gas.ConfigProvider` injected
via DI. This lets you drive database settings from environment variables or a config file without any explicit wiring.

| Field                        | Default      | Description                                               |
|------------------------------|--------------|-----------------------------------------------------------|
| `Database.Mode`              | `"sql"`      | Backend mode: `"sql"` or `"pgx"`                          |
| `Database.Driver`            | `"postgres"` | `database/sql` driver name (ModeSQL only)                 |
| `Database.DSN`               |              | Connection string (required, unless ModeSQL with `WithConnector`) |
| `Database.MaxOpenConns`      | `25`         | Max open connections                                      |
| `Database.MaxIdleConns`      | `5`          | Max idle connections (ModeSQL only)                       |
| `Database.ConnMaxLifetime`   | `30m`        | Max connection reuse time                                 |
| `Database.ConnMaxIdleTime`   | `5m`         | Max connection idle time                                  |
| `Database.ConnRetries`       | `0`          | Number of connection retry attempts (0 = no retries)      |
| `Database.ConnRetryInterval` | `2s`         | Base retry interval; doubles each attempt (exp. backoff)  |

## sqlc

sqlc generates its own `DBTX` interface in the target package, satisfied by `*sql.DB` and `*sql.Tx` in
`database/sql` mode and by `*pgxpool.Pool` and `pgx.Tx` in pgx mode. Pass `DB()`, `Pool()`, or a transaction from the
helpers above straight into the generated constructor; this package does not declare a `DBTX` of its own.
