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

Shorthand for singleton registration (`WithService` with `ServiceLifetimeSingleton`):

```go
gas.WithAppModule[*auth.Service](auth.New)
```

Register pre-built instances (treated as singletons):

```go
gas.WithServiceInstance[*MyService](myInstance)
```

### Routing

`Handle` accepts both classic `http.HandlerFunc` handlers and DI-aware typed handlers:

```go
func (s *Service) Init() error {
	// Classic http.HandlerFunc — still works, no wrapping.
	s.router.Handle(s.Name(), "GET", "/users", s.listUsers)

	// DI-aware handler — dependencies are auto-resolved from the request scope.
	s.router.Handle(s.Name(), "POST", "/users", s.createUser, gas.MiddlewareByName("require-auth"))
	return nil
}
```

Routes declare middleware using `MiddlewareByName()` (resolved from the router's registry) or `MiddlewareFunc()` (inline).

### DI-Aware Handlers

Handlers can declare dependencies as typed function parameters. The router resolves each dependency from the
per-request DI scope automatically — no manual `RequestScope` / `MustResolve` calls needed.

**Handler contract:** `gas.Context` first, dependencies in between, `error` return.

```go
func (s *Service) createUser(ctx gas.Context, db gas.DatabaseProvider, mailer gas.EmailProvider) error {
	var req CreateUserRequest
	if err := ctx.BindJSON(&req); err != nil {
		return err
	}
	// db and mailer are resolved from the request-scoped DI container
	return ctx.JSON(http.StatusCreated, user)
}
```

At startup, `InitServices()` validates that every handler dependency is registered in the container. If a type is
missing, initialization fails immediately — no runtime surprises.

### Context

`gas.Context` wraps `http.ResponseWriter` and `*http.Request` with convenience methods:

| Method                                 | Description                        |
|----------------------------------------|------------------------------------|
| `ResponseWriter() http.ResponseWriter` | Underlying response writer         |
| `Request() *http.Request`              | Underlying request                 |
| `JSON(status int, v any) error`        | Write JSON response                |
| `Text(status int, s string) error`     | Write plain-text response          |
| `NoContent() error`                    | Write 204 No Content               |
| `Redirect(status int, url string)`     | Send HTTP redirect                 |
| `Param(key string) string`             | URL path parameter (chi.URLParam)  |
| `Query(key string) string`             | Query string parameter             |
| `Header(key string) string`            | Request header value               |
| `SetHeader(key, value string)`         | Set response header                |
| `BindJSON(dest any) error`             | Decode JSON request body into dest |

### Error Handling

When a DI-aware handler returns a non-nil error, it is passed to the `ErrorHandler`. The default writes a
500 Internal Server Error with the default `http.StatusText(http.StatusInternalServerError)` body,
and logs the error if a logger is registered in the service container.

Override it with `WithErrorHandler`:

```go
app := gas.NewApp(
	gas.WithErrorHandler(func(ctx gas.Context, err error) {
		ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}),
)
```

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

DI-aware handlers resolve scoped services automatically — just declare the dependency as a parameter. For classic
`http.HandlerFunc` handlers, use the request-scope convenience helpers:

```go
func (s *Service) handleOrder(w http.ResponseWriter, r *http.Request) {
	txLog := gas.MustResolveFromRequestScope[*TransactionLog](r)
	txLog.Record("order created")
	// txLog.Close() is called automatically when the request ends
}
```

Or the two-value form to handle missing registrations without panicking:

```go
txLog, ok := gas.ResolveFromRequestScope[*TransactionLog](r)
```

Both helpers are thin wrappers around `gas.RequestScope(r)` + `gas.Resolve`/`gas.MustResolve`. For full scope
access (e.g. resolving multiple services), use `gas.RequestScope(r)` directly:

```go
scope := gas.RequestScope(r)
txLog := gas.MustResolve[*TransactionLog](scope)
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
| `Logger`           | `Trace`, `Debug`, `Info`, `Warn`, `Error`, `With`, `SetBaseFields`, `Flush` |

Logger context helpers:

```go
// Store a logger in a context (e.g. in middleware)
ctx = gas.WithLogger(ctx, logger)

// Retrieve it downstream (returns nil if absent)
l := gas.LoggerFromContext(ctx)
```

`With()` branches into a new Logger instance. For request-scoped middleware that shares one Logger instance across the
whole request, use `SetBaseFields()` instead — it mutates the receiver in place so every subsequent log call carries
the attached fields automatically:

```go
func authMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        logger := gas.MustResolveFromRequestScope[gas.Logger](r)
        logger.SetBaseFields().Str("user_id", userID).Str("trace_id", traceID).Apply()
        // All subsequent log calls within this request — including in the handler — carry user_id and trace_id.
        next.ServeHTTP(w, r)
    })
}
```

| `MigrationManager` | `Register`, `RegisterSlice`, `RegisterFS`, `RunPending`, `Down` |

### Writing a Service

```go
package myservice

import (
	"net/http"

	"github.com/gasmod/gas"
)

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
	// DI-aware handler — db is resolved per-request from the scoped container.
	s.router.Handle(s.Name(), "GET", "/hello", s.handleHello)

	// Classic http.HandlerFunc still works.
	s.router.Handle(s.Name(), "GET", "/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	gas.SubscribeWithOwner(s.bus, s.Name(), gas.SystemServiceClosed, s.onServiceClosed)
	return nil
}

func (s *Service) handleHello(ctx gas.Context, db gas.DatabaseProvider) error {
	return ctx.JSON(http.StatusOK, map[string]string{"status": "ok"})
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
