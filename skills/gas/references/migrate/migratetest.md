# github.com/gasmod/gas/migrate/migratetest

```go
import "github.com/gasmod/gas/migrate/migratetest"
```

Package migratetest holds a mock of gas.MigrationManager,
`MockMigrationManager`, for tests of services that register migrations.

## Usage

The mock records each call, and returns what the function in the field
named after the method returns:

```go
m := &migratetest.MockMigrationManager{
	RunPendingFn: func() error { return errBoom },
}
err := m.RunPending()
// err is errBoom, and m.CallCount("RunPending") is 1
```

## Defaults

With a field nil, its method succeeds and does nothing:

- Name returns migrate.ServiceName.
- Init, Close, RegisterFS, RunPending, RunPendingContext, Down and
DownContext return nil.
- Register and RegisterSlice return nothing.

RunPendingContext returns what RunPendingFn returns, and DownContext
what DownFn returns.

## Calls

Each method appends a `Call` with its name and arguments, the context
of Init and Close included, before it calls its field. To read them:

- Calls returns a copy of them in the order they were made.
- CallCount counts those of one method.
- Reset forgets them all.

## Concurrency

The methods may be called from several goroutines at once, as the calls
are kept under a lock, which Calls, CallCount and Reset take too.
Setting a field while a method runs is a data race.

## API

Run `go doc -all github.com/gasmod/gas/migrate/migratetest` for every declaration, or `go doc github.com/gasmod/gas/migrate/migratetest.<Name>` for one.
