---
title: Project layout
description: Where the entry point, the wiring, the modules and their migrations go in a Gas app.
---

Gas does not require a layout. An app is a `gas.App` with services
registered on it, and its code can live wherever Go allows: the app on the
[Installation](/start/installation/) page is a single file.

This page is the layout to move to once the app covers more than one
domain. It splits the app into modules of its own, one per domain, the way
Gas splits its own capabilities into modules, and wires them in one place.

## The layout

```text
myapp/
├── cmd/
│   └── main.go                loads the config, builds the app and runs it
├── internal/
│   ├── app/                   the composition root
│   │   ├── app.go             New registers every service and module
│   │   └── cfg.go             Config and LoadConfig
│   ├── contracts/             what the modules share with each other
│   ├── modules/               one package per domain
│   │   └── notes/
│   │       ├── doc.go
│   │       ├── module.go      the gas.Service: routes and migrations
│   │       ├── settings.go    the module's settings
│   │       ├── handlers.go
│   │       ├── service.go     the domain logic
│   │       ├── repo.go        the queries
│   │       ├── types.go       request and response types
│   │       ├── migrations.go  embeds migrations/
│   │       └── migrations/
│   │           ├── 20261110120000_create_notes.up.sql
│   │           └── 20261110120000_create_notes.down.sql
│   ├── httpx/                 HTTP helpers the modules share
│   └── dbx/                   database helpers the modules share
├── test/
│   ├── harness/               starts the whole app for a test
│   └── e2e/                   tests that call it over HTTP
├── example.env                every setting, committed
└── .env                       local settings, not committed
```

Everything but the entry point and the tests is under `internal/`, which
no other Go module can import.

## `cmd`: the entry point

`main` does as little as it can: it loads the configuration, builds the app
and runs it. Everything worth testing is in the packages it imports.

```go title="cmd/main.go"
package main

import (
	"context"
	"log"

	"example.com/myapp/internal/app"
)

func main() {
	cfg, err := app.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}

	if err := app.New(cfg).Run(context.Background()); err != nil {
		log.Fatal(err)
	}
}
```

## `internal/app`: the wiring

`internal/app` is the composition root: the one package that says what the
app is made of. It imports every module, and no module imports it.

`cfg.go` holds `Config`, which has one field per module, of the module's
own `Settings` type, and `LoadConfig`, which binds and validates all of it
once:

```go title="internal/app/cfg.go"
// Config is everything New needs to build the app.
type Config struct {
	// One field per module: its settings, bound from the section of the
	// same name.
	Notes notes.Settings

	// store is registered as gas.ConfigProvider, so the Gas modules bind
	// their own sections from it.
	store *config.Config
}

// LoadConfig reads the configuration from `.env`, when there is one, and
// from the environment, which overrides it.
func LoadConfig() (*Config, error) {
	return LoadConfigFrom(
		config.NewDotEnvProvider(config.WithDotEnvFileNotFoundPanic(false)),
		config.NewEnvProvider(config.WithEnvPrefix("APP_")),
	)
}

// LoadConfigFrom reads the configuration from the given providers, a later
// one overriding an earlier one, so a test can pass fixed values.
func LoadConfigFrom(providers ...config.Provider) (*Config, error) {
	if len(providers) == 0 {
		return nil, errors.New("app: LoadConfigFrom needs a provider")
	}

	// The extension resolves the environment from GAS_ENV.
	opts := []config.Option{config.WithExtension(config.NewEnvExtension())}
	for _, p := range providers {
		opts = append(opts, config.WithProvider(p))
	}

	store := config.New(opts...)
	if err := store.Load(); err != nil {
		return nil, err
	}

	cfg := &Config{Notes: notes.DefaultSettings(), store: store}
	if err := store.Bind(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}
```

A few things to know:

- **A bad setting stops the app before anything is built.** `Bind` checks
  the `validate` tags of every module's settings, so `LoadConfig` fails and
  `main` exits.
- **A module never reads the configuration itself.** It is handed its
  settings as a plain value, so what it depends on is in its constructor.
- **Gas's modules bind their own sections.** The loaded configuration is
  registered as `gas.ConfigProvider`, where the server, the database and
  the other Gas modules find sections such as `server` and `database`.
- **The environment overrides `.env`.** A later provider overrides an
  earlier one, and a missing `.env` loads nothing, so a deployment can set
  the environment alone. See [Settings files](#settings-files) for how the
  names differ.
- **`GAS_ENV` picks the environment.** The `EnvExtension` resolves it, as
  under
  [Resolving the environment](/reference/config/#resolving-the-environment),
  and stores it where the App reads it. Without the extension the App
  runs as `development` whatever `GAS_ENV` holds.
- **`LoadConfigFrom` needs a provider.** `config.New` with none falls back
  to an environment provider with no prefix, whose load fails with
  `environment prefix is required`, so `LoadConfigFrom` says what is
  missing itself.

`app.go` holds `New`. It registers in three layers: what every module
depends on, then each domain's repository and service, then the modules.

```go title="internal/app/app.go"
// New returns the app with everything registered and nothing started.
func New(cfg *Config) *gas.App {
	app := gas.NewApp()

	registerInfra(app, cfg)
	registerServices(app)
	registerModules(app, cfg)

	// Middleware for every route.
	app.Router().UseMiddlewareFunc(nil, gas.SecurityHeaders())

	return app
}

// registerInfra registers what every module depends on.
func registerInfra(app *gas.App, cfg *Config) {
	app.RegisterServiceInstance[gas.ConfigProvider](cfg.store)
	app.RegisterSingletonService[gas.Logger](gaslog.NewSlogLogger())
	app.RegisterSingletonService[gas.DatabaseProvider](database.New())
	app.RegisterSingletonService[gas.MigrationManager](migrate.New())
}

// registerServices registers each domain's repository and service, and a
// service under the contract other modules reach it by.
func registerServices(app *gas.App) {
	app.RegisterSingletonService[*notes.Repository](notes.NewRepository)
	app.RegisterSingletonService[*notes.Service](notes.NewService)
	app.RegisterServiceAlias[*notes.Service, contracts.NoteCounter]()
}

// registerModules registers the modules, each with its own settings.
func registerModules(app *gas.App, cfg *Config) {
	app.RegisterSingletonService[*notes.Module](notes.NewModule(cfg.Notes))
	app.RegisterSingletonService[*stats.Module](stats.NewModule())
}
```

`New` only registers. Gas builds the services, calls each `Init` and
applies the pending migrations when the app starts, which is what lets a
test build the same app and start it itself. Middleware for every route
goes in `New`, as above. So do ready hooks, registered with
`app.ReadyFunc`, for work that must wait until the migrations are applied.

## `internal/modules`: one package per domain

Each domain is a package under `internal/modules`, named for what it owns,
with the same files:

- `doc.go`: the package comment. It says what the module owns: its routes,
  its settings, its tables, and what other modules may ask of it.
- `module.go`: the `Module`, a `gas.Service`. Its `Init` registers the
  module's migrations and routes.
- `settings.go`: `Settings`, the module's section of the configuration, and
  its defaults.
- `handlers.go`: the HTTP handlers, methods of `Module`. Split it by area,
  as `handlers_<area>.go`, when it grows.
- `service.go`: `Service`, the domain logic, with no HTTP in it.
- `repo.go`: `Repository`, the queries.
- `types.go`: the request and response types.
- `migrations.go` and `migrations/`: the module's migration files, and the
  variable that embeds them.

```go title="internal/modules/notes/module.go"
// ModuleName is the module's service name.
const ModuleName = "myapp/notes"

// Module serves the notes routes and owns the notes migrations.
type Module struct {
	settings Settings
	router   *gas.Router
	migrate  gas.MigrationManager
	svc      *Service
}

// NewModule returns the Module's constructor, whose parameters the
// container resolves.
func NewModule(settings Settings) func(*gas.Router, gas.MigrationManager, *Service) *Module {
	return func(router *gas.Router, migrate gas.MigrationManager, svc *Service) *Module {
		return &Module{settings: settings, router: router, migrate: migrate, svc: svc}
	}
}

// Name implements gas.Service.
func (m *Module) Name() string { return ModuleName }

// Init implements gas.Service. It registers the module's migrations and
// routes, both owned by the module.
func (m *Module) Init(context.Context) error {
	if err := m.migrate.RegisterFS(m, migrations); err != nil {
		return fmt.Errorf("%s: register migrations: %w", ModuleName, err)
	}

	m.router.Route(m, "/notes", func(g *gas.RouteGroup) {
		g.Get("/", m.handleList)
		g.Post("/", m.handleCreate)
	})

	return nil
}

// Close implements gas.Service. There is nothing to release.
func (m *Module) Close(context.Context) error { return nil }
```

```go title="internal/modules/notes/migrations.go"
package notes

import "embed"

//go:embed migrations/*.sql
var migrations embed.FS
```

Passing the module as the owner is what ties its routes and migrations to
it. The route map Gas logs at startup lists each module's routes under its
name, each `migration applied` log line names its `owner`, and the
[kill switch](/reference/gas/#kill-switch) can take a module's routes out
at runtime.

The module that owns a table owns its migrations. A migration is a pair of
files, `<version>_<description>.up.sql` and `.down.sql`. Gas applies the
migrations of every module in one order, by version, so use a timestamp,
such as `20261110120000`, as the version. See
[migrate](/reference/migrate/#migration-files).

## `internal/contracts`: how modules meet

No module imports another. When one needs something another owns, both
depend on an interface in `internal/contracts`, which imports no module:

```go title="internal/contracts/notes.go"
// NoteCounter counts the stored notes. The notes module implements it.
type NoteCounter interface {
	CountNotes(ctx context.Context) (int, error)
}
```

The notes module's `Service` implements it, and `internal/app` registers
that service under the interface as well, with `RegisterServiceAlias` in
`registerServices` above. A module that needs the count takes a
`contracts.NoteCounter` in its constructor, and never learns which package
is behind it:

```go title="internal/modules/stats/module.go"
func NewModule() func(*gas.Router, contracts.NoteCounter) *Module {
	return func(router *gas.Router, notes contracts.NoteCounter) *Module {
		return &Module{router: router, notes: notes}
	}
}
```

What those interfaces exchange lives in `contracts` too: their types and
errors, and the names more than one module must agree on, such as the name
of a middleware. An interface that only one module uses stays in that
module.

## Shared packages

Code that more than one module needs, and that is not a contract, sits
beside the modules under `internal/`, in a package named for what it
holds: `internal/httpx` for HTTP helpers such as error mapping,
`internal/dbx` for database helpers such as transactions. A small helper
that knows nothing of the app gets a package of its own under
`internal/util`, such as `internal/util/slug`.

## Tests

Unit tests sit beside the code they test. Tests of the whole app go through
HTTP, as a client does: `test/harness` builds the app with `app.New`,
starts it, and serves its server from a listener the test owns, and
`test/e2e` holds the tests.

```go title="test/harness/harness.go"
cfg, err := app.LoadConfigFrom(configtest.NewStaticProvider(map[string]any{
	"gasenv":   "testing",
	"database": map[string]any{"dialect": "postgres", "dsn": dsn},
}))
if err != nil {
	t.Fatal(err)
}

a := app.New(cfg)

if err := a.Start(t.Context()); err != nil {
	t.Fatal(err)
}

l, err := net.Listen("tcp", "127.0.0.1:0")
if err != nil {
	t.Fatal(err)
}

srv := a.Server()
served := make(chan error, 1)

go func() { served <- srv.Serve(l) }()

baseURL := "http://" + l.Addr().String()

t.Cleanup(func() {
	if err := a.Stop(context.Background()); err != nil {
		t.Error(err)
	}

	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		t.Error(err)
	}
})
```

`a.Server()` is the `http.Server` the app builds from its `server`
settings, so the tests meet the timeouts the app runs with; a test server
around `a.Handler()` would have its own. The listener takes a free port, and
`baseURL` is where the tests send their requests. `Stop` shuts the server
down, and `Serve` then returns `http.ErrServerClosed`.

A test reaches a registered service through the started app's container,
under the type it was registered as, such as the contract the notes module
implements, with `a` from the harness:

```go
notes, err := a.ServiceContainer().Resolve[contracts.NoteCounter]()
if err != nil {
	t.Fatal(err)
}
```

`cfg` comes from `app.LoadConfigFrom`, with the values the test needs in a
[`configtest.StaticProvider`](/reference/config/configtest/#static-provider),
so neither `.env` nor the environment's settings reach the test; `dsn` is
the test database's. `Start` applies the migrations, so each test runs against the schema the
modules declare. When the tests need outside services, such as a database
in a container, keep them behind a build tag, such as `e2e`, so that
`go test ./...` runs the unit tests alone.

## Settings files

Settings files sit in the project root, and the app runs from there: the
`.env` and JSON providers read from the
[working directory](/reference/config/#file-access). Keep `.env` out of
version control, and commit an `example.env` that lists every setting.

`LoadConfig` reads `.env` when the file is there, and then the
environment. The same setting has two names, as a name in `.env` has no
prefix and one in the environment has the app's:

```sh title=".env"
SERVER__PORT=8080
```

```sh
export APP_SERVER__PORT=8080
```

Both become the key `server.port`. A name in `.env` that keeps the prefix,
such as `APP_SERVER__PORT`, becomes `app_server.port`, which nothing
reads.

`GAS_ENV` is the exception: the `EnvExtension` reads it from the process
environment under that name, with no prefix. In `.env`, set the
environment as `GASENV`, the key it is stored at. A `GAS_ENV` line there
becomes the key `gas_env`, which nothing reads.
