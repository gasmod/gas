# Gas

Gas is the core of a modular Gas ecosystem for building micro-SaaS applications. It provides shared
infrastructure — dependency injection, routing, middleware, events, migrations, and service lifecycle management — so you
can focus on business logic instead of rebuilding the same plumbing for every project.

## Install

```bash
go get github.com/gasmod/gas
```

## Key Concepts

**Services** are self-contained units of functionality (auth, billing, etc.) that implement a simple three-method
interface:

```go
type Service interface {
	Name() string // Unique identifier, e.g. "gas-auth"
	Init() error  // Register routes, middleware, subscriptions
	Close() error // Cleanup internal resources
}
```

**Dependency injection.** Services are registered with the DI container via constructors. The container resolves
dependencies automatically, performs topological sorting, validates lifetime rules, and calls `Init()` on every
`Service` after construction.

**Three lifetimes:**
- **Singleton** — created once, shared everywhere. `Init()` is called during `BuildAll()`.
- **Scoped** — created once per `Scope`. `Init()` is called on first resolution within the scope.
- **Transient** — created fresh on every resolution. **Cannot implement `Service`** (registration panics).

**Infrastructure flows inward.** Services never import each other. They receive shared infrastructure (router, event bus,
providers) through constructor injection and communicate via events and provider interfaces.

**Ownership tracking.** Every route, middleware, and event subscription is tagged with its owning service, enabling
surgical teardown of a single service at runtime.

## Usage

### App Lifecycle

```go
package main

import "github.com/gasmod/gas"

func main() {
	app := gas.NewApp(
		gas.WithService[*auth.Service](auth.New, gas.ServiceLifetimeSingleton),
		gas.WithService[*billing.Service](billing.New, gas.ServiceLifetimeSingleton),
	)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
```

The `App` creates a `Router` and `EventBus` internally and registers them in the DI container. Services receive them
via constructor injection:

```go
func New(router *gas.Router, bus *gas.EventBus) *Service {
	return &Service{router: router, bus: bus}
}
```

`Run()` initializes all services (via the DI container), runs pending migrations, starts the HTTP server, and waits for
a shutdown signal. On shutdown, services are closed in reverse init order.

### Registering Services

Register constructor-based services with a lifetime:

```go
gas.WithService[*auth.Service](auth.New, gas.ServiceLifetimeSingleton)
```

Register pre-built instances (treated as singletons):

```go
gas.WithServiceInstance[*MyService](myInstance)
```

### Routing

```go
func (s *Service) Init() error {
	s.router.Handle(s.Name(), "GET", "/users", s.listUsers)
	s.router.Handle(s.Name(), "POST", "/users", s.createUser, gas.MiddlewareByName("require-auth"))
	return nil
}
```

Routes declare middleware using `MiddlewareByName()` (resolved from the router's registry) or `MiddlewareFunc()` (inline).

### Middleware

Register named middleware on the router:

```go
router.Register("auth", "require-auth", func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// validate token...
		next.ServeHTTP(w, r)
	})
})
```

Apply middleware globally:

```go
router.UseMiddlewareFunc(func(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// logging, CORS, etc.
		next.ServeHTTP(w, r)
	})
})
```

Or by name (panics if not registered):

```go
router.UseMiddlewareByName("require-auth")
```

### Grouping Routes

Use `Group()` for inline middleware scoping:

```go
router.Group(func(sub *gas.Router) {
	sub.UseMiddlewareByName("require-auth")
	sub.Handle("admin", "GET", "/admin/dashboard", s.dashboard)
	sub.Handle("admin", "GET", "/admin/settings", s.settings)
})
```

Use `Route()` for pattern-scoped groups:

```go
router.Route("/api", func(sub *gas.Router) {
	sub.Handle("api", "GET", "/users", s.listUsers)
	sub.Handle("api", "GET", "/items", s.listItems)
})
```

### Events

Events use typed `Event[T]` definitions for compile-time safety:

```go
// Define a typed event
var UserCreated = gas.Event[UserCreatedPayload]{Name: "user:created"}

type UserCreatedPayload struct {
	Email string
}

// Subscribe with ownership tracking
gas.SubscribeWithOwner(bus, s.Name(), UserCreated, func(data UserCreatedPayload) {
	// provision billing account for data.Email
})

// Emit (returns *sync.WaitGroup for concurrent handlers)
gas.Emit(bus, UserCreated, UserCreatedPayload{Email: "user@example.com"}).Wait()
```

### Kill-Switch

Disable a service at runtime without restarting the server:

```go
app.CloseService("auth") // routes return 503, middleware + subscriptions removed, Close() called
app.RestartService("auth") // re-initializes the service
```

Other services can react to closures:

```go
gas.SubscribeWithOwner(bus, s.Name(), gas.SystemServiceClosed, func(data gas.SystemServiceClosedPayload) {
	// enter degraded mode if data.ServiceName was a dependency
})
```

### Request Scopes

The App automatically installs middleware that creates a DI `Scope` per HTTP request. Scoped services get a fresh
instance for each request — `Init()` is called on first resolution and `Close()` is called when the request completes.

Resolve scoped services in any handler using `gas.RequestScope(r)`:

```go
func (s *Service) handleOrder(w http.ResponseWriter, r *http.Request) {
	scope := gas.RequestScope(r)
	txLog := gas.MustResolve[*TransactionLog](scope)
	txLog.Record("order created")
	// txLog.Close() is called automatically when the request ends
}
```

Register scoped services with `ServiceLifetimeScoped`:

```go
app := gas.NewApp(
	gas.WithService[*TransactionLog](NewTransactionLog, gas.ServiceLifetimeScoped),
)
```

For non-HTTP use cases (background jobs, tests), create scopes manually:

```go
scope := container.NewScope()
defer scope.Close() // calls Close() on all scoped Service instances

svc, ok := gas.Resolve[*MyScopedService](scope)
```

### Provider Interfaces

Services depend on interfaces, not implementations. Gas defines common providers that any service can accept:

| Interface          | Methods                                                         |
|--------------------|-----------------------------------------------------------------|
| `DatabaseProvider` | `Query`, `Exec`, `DB`                                           |
| `CacheProvider`    | `Get`, `Set`, `Delete`                                          |
| `EmailProvider`    | `Send`                                                          |
| `StorageProvider`  | `Upload`, `Download`, `Delete`                                  |
| `ConfigProvider`   | `SetDefault`, `Set`, `Bind`, `Get`, `Find`, `Values`            |
| `MigrationManager` | `Register`, `RegisterSlice`, `RegisterFS`, `RunPending`, `Down` |

### Writing a Service

```go
package myservice

import "github.com/gasmod/gas"

type Service struct {
	router *gas.Router
	bus    *gas.EventBus
}

// New is the constructor — dependencies are injected by the DI container.
func New(router *gas.Router, bus *gas.EventBus) *Service {
	return &Service{router: router, bus: bus}
}

func (s *Service) Name() string { return "myservice" }

func (s *Service) Init() error {
	s.router.Handle(s.Name(), "GET", "/hello", s.handleHello)
	gas.SubscribeWithOwner(s.bus, s.Name(), gas.SystemServiceClosed, s.onServiceClosed)
	return nil
}

func (s *Service) Close() error { return nil }
```

Register it in the App:

```go
app := gas.NewApp(
	gas.WithService[*myservice.Service](myservice.New, gas.ServiceLifetimeSingleton),
)
```

## System Events

| Event                              | Payload Type                             | Fired When                                      |
|------------------------------------|------------------------------------------|-------------------------------------------------|
| `gas.SystemServiceClosed`          | `SystemServiceClosedPayload`             | A service is killed via `CloseService`          |
| `gas.SystemServiceInitialized`     | `SystemServiceInitializedPayload`        | A service finishes `Init` (including restart)   |
| `gas.SystemAllServicesInitialized` | `SystemAllServicesInitializedPayload`    | All services have been successfully initialized |
| `gas.SystemServerShuttingDown`     | `SystemServerShuttingDownPayload`        | Server begins graceful shutdown                 |
| `gas.AppConfigUpdated`             | `AppConfigUpdatedPayload`                | App config is updated after binding             |

## App Accessors

| Method                   | Returns                     |
|--------------------------|-----------------------------|
| `app.Router()`           | `*Router`                   |
| `app.EventBus()`         | `*EventBus`                 |
| `app.Config()`           | `*Config`                   |
| `app.MigrationManager()` | `MigrationManager` (or nil) |
| `app.ConfigProvider()`   | `ConfigProvider` (or nil)   |
