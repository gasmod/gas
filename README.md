# Gas

Gas is the core module of a modular Gas ecosystem for building micro-SaaS applications. It provides shared
infrastructure — routing, middleware, events, migrations, and module lifecycle management — so you can focus on business
logic instead of rebuilding the same plumbing for every project.

## Install

```bash
go get github.com/gasmod/gas
```

## Key Concepts

**Modules** are self-contained units of functionality (auth, billing, etc.) that implement a simple three-method
interface:

```go
type Module interface {
	Name() string // Unique identifier, e.g. "gas-auth"
	Init() error  // Register routes, middleware, subscriptions
	Close() error // Cleanup internal resources
}
```

**Infrastructure flows inward.** Modules never import each other. They receive shared infrastructure (router, event bus,
providers) through functional options and communicate via events and provider interfaces.

**Ownership tracking.** Every route, middleware, and event subscription is tagged with its owning module, enabling
surgical teardown of a single module at runtime.

## Usage

### App Lifecycle

```go
package main

import "github.com/gasmod/gas"

func main() {
	reg := gas.NewMiddlewareRegistry()
	router := gas.NewRouter(reg)
	bus := gas.NewEventBus()

	app := gas.NewApp(
		gas.WithRouter(router),
		gas.WithMiddlewareRegistry(reg),
		gas.WithEventBus(bus),
		gas.WithModule(auth.New(
			auth.WithRouter(router),
			auth.WithEventBus(bus),
		)),
		gas.WithModule(billing.New(
			billing.WithRouter(router),
			billing.WithEventBus(bus),
		)),
	)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
```

`Run()` initializes all modules, runs pending migrations, starts the HTTP server, and waits for a shutdown signal.

### Routing

```go
func (m *Module) Init() error {
	m.router.Handle(m.Name(), "GET", "/users", m.listUsers)
	m.router.Handle(m.Name(), "POST", "/users", m.createUser, "require-auth")
	return nil
}
```

Routes declare middleware by name. The router resolves them from the middleware registry at registration time.

### Middleware

```go
reg.Register("auth", "require-auth", func (next gas.Handler) gas.Handler {
	return gas.HandlerFunc(func (w http.ResponseWriter, r *http.Request) {
		// validate token...
		next.ServeHTTP(w, r)
	})
})
```

### Events

```go
// Subscribe
bus.SubscribeWithOwner("billing", "user:created", func (data gas.EventData) {
	email, _ := data.GetString("email")
	// provision billing account...
})

// Emit
bus.Emit("user:created", gas.NewEventData().Set("email", "user@example.com"))
```

`EventData` provides type-safe accessors: `GetString`, `GetInt`, `GetBool`, `GetFloat64`, `GetTime`, `GetStringSlice`,
and `Raw`.

### Kill-Switch

Disable a module at runtime without restarting the server:

```go
app.CloseModule("auth") // routes return 503, subscriptions removed, Close() called
app.RestartModule("auth") // re-initializes the module
```

Other modules can react to closures:

```go
bus.SubscribeWithOwner("billing", gas.SystemModuleClosed, func(data gas.EventData) {
	name, _ := data.GetString("module_name")
	// enter degraded mode if needed
})
```

### Provider Interfaces

Modules depend on interfaces, not implementations. Gas defines common providers that any module can accept:

| Interface          | Methods                                                         |
|--------------------|-----------------------------------------------------------------|
| `DatabaseProvider` | `Query`, `Exec`                                                 |
| `CacheProvider`    | `Get`, `Set`, `Delete`                                          |
| `EmailProvider`    | `Send`                                                          |
| `StorageProvider`  | `Upload`, `Download`, `Delete`                                  |
| `MigrationManager` | `Register`, `RegisterSlice`, `RegisterFS`, `RunPending`, `Down` |

### Writing a Module

```go
package mymodule

import "github.com/gasmod/gas"

type Module struct {
	router *gas.Router
	bus    *gas.EventBus
}

type Option func(*Module)

func WithRouter(r *gas.Router) Option     { return func(m *Module) { m.router = r } }
func WithEventBus(b *gas.EventBus) Option { return func(m *Module) { m.bus = b } }

func New(opts ...Option) *Module {
	m := &Module{}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (m *Module) Name() string { return "mymodule" }

func (m *Module) Init() error {
	m.router.Handle(m.Name(), "GET", "/hello", m.handleHello)
	m.bus.SubscribeWithOwner(m.Name(), gas.SystemModuleClosed, m.onModuleClosed)
	return nil
}

func (m *Module) Close() error { return nil }
```

## System Events

| Constant                       | Fired When                                   |
|--------------------------------|----------------------------------------------|
| `gas.SystemModuleClosed`       | A module is killed via `CloseModule`         |
| `gas.SystemModuleInitialized`  | A module finishes `Init` (including restart) |
| `gas.SystemServerShuttingDown` | Server begins graceful shutdown              |

## Configuration

```go
gas.NewApp(
	gas.WithConfig(&gas.Config{
		Addr:            ":3000", // default ":8080"
		ReadTimeout:     10 * time.Second, // default 5s
		WriteTimeout:    20 * time.Second, // default 10s
		IdleTimeout:     60 * time.Second, // default 120s
		ShutdownTimeout: 15 * time.Second, // default 30s
		Logger:          slog.Default(),
	}),
)
```
