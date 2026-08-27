# gas/template

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/template.svg)](https://pkg.go.dev/github.com/gasmod/gas/template) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=template/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [All modules](../README.md#modules) · [Examples](../example/README.md)

Template storage service for the [Gas](../README.md) framework. Provides multiple `gas.TemplateProvider`
implementations — in-memory, filesystem, database, and composite — for storing and retrieving raw template content.

## Install

```bash
go get github.com/gasmod/gas/template
```

## Backends

| Backend    | Package                                    | Use case                                                 |
|------------|--------------------------------------------|----------------------------------------------------------|
| Memory     | `github.com/gasmod/gas/template/memory`    | Development, testing, ephemeral storage                  |
| Directory  | `github.com/gasmod/gas/template/dir`       | Static templates on disk with runtime overlay            |
| FS         | `github.com/gasmod/gas/template/fs`        | Read-only adapter for `fs.FS` (e.g. `embed.FS`)          |
| Database   | `github.com/gasmod/gas/template/db`        | Persistent, multi-instance deployments (Pg/MySQL/SQLite) |
| Composite  | `github.com/gasmod/gas/template/composite` | Chain multiple providers with fallback reads             |

Memory, directory, fs, and composite stores implement `gas.TemplateProvider`.
The database store also implements `gas.Service` (with DI, migrations, and lifecycle management).

The stores differ in how you construct them. `memory.NewStore()` and `composite.NewStore(...)` return a
`*Store` directly. `dir.NewStore(path)` and `fs.NewStore(fsys)` return a zero-argument DI constructor, so call
it yourself (`dir.NewStore("./templates")()`) when wiring by hand. `db.NewStore()` returns a DI constructor
that takes `gas.DatabaseProvider`, `gas.Logger`, and `gas.MigrationManager` (see
[gas/database](../database/README.md) and [gas/migrate](../migrate/README.md)).

## Usage

### Memory backend

```go
import "github.com/gasmod/gas/template/memory"

store := memory.NewStore()
if err := store.Register(ctx, "emails/welcome.html", []byte("<h1>Welcome</h1>")); err != nil {
    // handle error
}

content, err := store.Get(ctx, "emails/welcome.html")
```

### Directory backend

```go
import "github.com/gasmod/gas/template/dir"

// NewStore returns a DI-injectable constructor; call it to get a *Store.
store := dir.NewStore("./templates")()
defer store.Close()

// Reads from disk; overlay takes precedence.
content, err := store.Get(ctx, "home.html")

// Programmatic additions go to the in-memory overlay.
_ = store.Register(ctx, "dynamic.html", []byte("<p>Dynamic</p>"))
```

### FS backend

Read-only adapter for any `fs.FS` — most commonly an `embed.FS`. `Register`
and `RegisterFS` return `template.ErrReadOnly`; wrap in a composite store
with a writable provider for mutability.

```go
import (
    "embed"

    tmplfs "github.com/gasmod/gas/template/fs"
)

//go:embed templates/*.html
var templateFS embed.FS

// NewStore returns a DI-injectable constructor; call it to get a *Store.
store := tmplfs.NewStore(templateFS)()
content, err := store.Get(ctx, "templates/home.html")
```

### Database backend

```go
package main

import (
    "log"

    "github.com/gasmod/gas"
    "github.com/gasmod/gas/config"
    "github.com/gasmod/gas/config/providers"
    database "github.com/gasmod/gas/database"
    gaslog "github.com/gasmod/gas/log"
    migrate "github.com/gasmod/gas/migrate"
    tmpldb "github.com/gasmod/gas/template/db"
)

func main() {
    cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
    if err := cfg.Load(); err != nil {
        log.Fatal(err)
    }

    app := gas.NewApp(
        gas.WithServiceInstance[gas.ConfigProvider](cfg),
        gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

        gas.WithSingletonService[gas.DatabaseProvider](database.New()),
        gas.WithSingletonService[gas.MigrationManager](migrate.New()),
        gas.WithSingletonService[gas.TemplateProvider](tmpldb.NewStore()),
    )

    if err := app.Run(); err != nil {
        log.Fatal(err)
    }
}
```

With a custom namespace:

```go
tmpldb.NewStore(tmpldb.WithNamespace("emails"))
```

### Composite backend

Chain multiple providers — writes go to the first, reads fall back through all:

```go
import (
    "github.com/gasmod/gas/template/composite"
    "github.com/gasmod/gas/template/memory"
    "github.com/gasmod/gas/template/dir"
)

writable := memory.NewStore()
disk := dir.NewStore("./templates")()
defer disk.Close()

store := composite.NewStore(writable, disk)

// Get checks writable first, then disk.
content, err := store.Get(ctx, "page.html")

// Register goes to the writable provider only.
_ = store.Register(ctx, "override.html", []byte("<p>Override</p>"))
```

### Dependency injection

Services receive templates through `gas.TemplateProvider` via constructor injection:

```go
type Service struct {
    templates gas.TemplateProvider
}

func New(templates gas.TemplateProvider) *Service {
    return &Service{templates: templates}
}

func (s *Service) Init() error {
    content, err := s.templates.Get(context.Background(), "emails/welcome.html")
    if err != nil {
        return err
    }
    // use content...
    return nil
}
```

### Registering templates from embedded files

All writable stores (memory, dir, db) support loading templates from an
`fs.FS` via `RegisterFS`:

```go
import "embed"

//go:embed templates/*.html
var templateFS embed.FS

if err := store.RegisterFS(ctx, templateFS); err != nil {
    // handle error
}
```

Only `.html` files are registered; other extensions are skipped. For a
read-only view over an `fs.FS` without copying contents into another
store, use the `fs` backend instead.

## Database Backends

The `db` package supports three database dialects. The correct dialect is selected
automatically based on the configured database driver:

| Driver              | Dialect    |
|---------------------|------------|
| `postgres`, `pgx`   | PostgreSQL |
| `mysql`             | MySQL      |
| `sqlite`, `sqlite3` | SQLite     |

The templates table migration is registered automatically with `gas/migrate` during `Init()`.

### Namespaces

Multiple `db.Store` instances can share the same table by using different namespaces:

```go
gas.WithSingletonService[gas.TemplateProvider](tmpldb.NewStore(tmpldb.WithNamespace("emails")))
```

The default namespace is `"default"`.

### Extra methods

The database store exposes two additional methods beyond `gas.TemplateProvider`:

```go
store.Exists("page.html")  // (bool, error)
store.Delete("page.html")  // error — returns template.ErrTemplateNotFound if missing
```

## Testing

The `templatetest` package provides a mock implementation of `gas.TemplateProvider`:

```go
import "github.com/gasmod/gas/template/templatetest"

mock := &templatetest.MockTemplate{}
mock.GetFn = func(ctx context.Context, name string) ([]byte, error) {
    return []byte("<h1>Hello</h1>"), nil
}

// pass mock as gas.TemplateProvider
// assert calls:
if mock.CallCount("Get") != 1 {
    t.Error("expected one Get call")
}
```

## Sentinel Errors

The root `template` package defines a sentinel error used by all backends:

```go
template.ErrTemplateNotFound // returned by Get when the template does not exist
template.IsNotFound(err)     // helper to check if an error is ErrTemplateNotFound
```
