# gas/migrate

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/migrate.svg)](https://pkg.go.dev/github.com/gasmod/gas/migrate) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=migrate/go.mod) [![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Migration manager for the [Gas](https://github.com/gasmod/gas) ecosystem. Tracks and applies database migrations across
all Gas services with dirty-state detection and rollback support.

## Install

```
go get github.com/gasmod/gas/migrate
```

## Usage

### Wiring in `main.go`

```go
package main

import (
	"github.com/gasmod/gas"
	database "github.com/gasmod/gas/database"
	migrate "github.com/gasmod/gas/migrate"
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

### Registering migrations

Services register their migrations during `Init()`. There are three ways to register.

#### Single migration

```go
func (s *Service) Init() error {
	s.migrationMgr.Register(s.Name(), gas.Migration{
		Version:     "20250216001",
		Description: "create users table",
		Up:          "CREATE TABLE users (id SERIAL PRIMARY KEY, email TEXT NOT NULL);",
		Down:        "DROP TABLE users;",
	})
	return nil
}
```

#### Slice of migrations

```go
func (s *Service) Init() error {
	s.migrationMgr.RegisterSlice(s.Name(), []gas.Migration{
		{
			Version:     "20250216001",
			Description: "create users table",
			Up:          "CREATE TABLE users (id SERIAL PRIMARY KEY, email TEXT NOT NULL);",
			Down:        "DROP TABLE users;",
		},
		{
			Version:     "20250216002",
			Description: "create sessions table",
			Up:          "CREATE TABLE sessions (id TEXT PRIMARY KEY, user_id INT REFERENCES users(id));",
			Down:        "DROP TABLE sessions;",
		},
	})
	return nil
}
```

#### Embedded SQL files

```go
import "embed"

//go:embed migrations/*.sql
var migrationsFS embed.FS

func (s *Service) Init() error {
	return s.migrationMgr.RegisterFS(s.Name(), migrationsFS)
}
```

Files must follow this naming convention:

```
migrations/
    20250216001_create_users.up.sql
    20250216001_create_users.down.sql
    20250216002_create_sessions.up.sql
    20250216002_create_sessions.down.sql
```

The version is the first underscore-delimited segment (e.g. `20250216001`), and the description is parsed from the
remaining segments (underscores become spaces).

### Running migrations

```go
// Apply all pending migrations in global version order.
err := migrationMgr.RunPending()

// Roll back the last 2 applied migrations.
err := migrationMgr.Down(2)
```

## How it works

- Migrations are tracked in a `__gas_migrations` table created automatically on `Init()`.
- `Init()` selects the correct sqlc-generated query adapter based on the database driver
  (PostgreSQL, MySQL, or SQLite). Unsupported drivers cause `Init()` to return an error.
- `RunPending()` sorts all registered migrations globally by version across all services and applies any that haven't
  been applied yet.
- Each migration runs in its own transaction. If a migration fails, it is marked **dirty** and all further execution is
  blocked until the dirty state is manually resolved.
- `Down(n)` reverses the last `n` applied migrations in reverse version order.
- **Version collision detection**: If two services register migrations with the same version, `RunPending()` and `Down()`
  return an error identifying the conflicting version and both service names.

## Readiness

The service implements `gas.ReadyReporter`. `CheckReady(ctx)` returns nil only when the service is initialized, not
closed, has no dirty migrations, and has no registered migrations pending application. This maps to a Kubernetes
readinessProbe — traffic is gated until migrations have applied successfully. `gas.HealthReporter` is intentionally
not implemented, since `gas/migrate` owns no live runtime state distinct from the underlying database connection
(which `gas/database` reports on).

## Dirty migrations

If a migration fails, it is recorded as dirty in the tracking table. Subsequent calls to `RunPending()` will return an
error listing the dirty versions. To resolve:

1. Fix the underlying issue (bad SQL, missing dependency, etc.).
2. Manually remove or update the dirty row in `__gas_migrations`.
3. Run `RunPending()` again.

## Testing

The `migratetest` package provides a mock implementation of `gas.MigrationManager`:

```go
import "github.com/gasmod/gas/migrate/migratetest"

mock := &migratetest.MockMigrationManager{}
mock.RunPendingFn = func() error {
	return nil
}

// pass mock as gas.MigrationManager
// assert calls:
if mock.CallCount("RegisterFS") != 1 {
	t.Error("expected one RegisterFS call")
}
```

Every method delegates to its `Fn` field when set and otherwise returns the zero value; `Name()` returns
`"gas/migrate"`. All calls are recorded in `Calls`, and `Reset()` clears them. The mock is safe for concurrent use.
