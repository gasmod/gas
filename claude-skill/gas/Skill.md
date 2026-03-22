---
name: gas
description: >
  Reference documentation for the Gas core Go package (github.com/gasmod/gas) —
  the foundational layer of the Gas ecosystem for rapid SaaS development. Use
  this skill when writing, reviewing, or debugging Go code that imports or
  extends the gas core package. Covers the App lifecycle, DI container, service
  registration and lifetimes, Router with ownership tracking, DI-aware handlers,
  Context, ErrorHandler, EventBus, middleware, migrations, request scopes,
  logging, provider interfaces, and system events. Make sure to use this skill
  whenever working with gas service constructors, route handlers, event
  subscriptions, middleware registration, or any code under a gasmod/gas import
  path, even if the user doesn't explicitly mention "gas".
---

# Gas Core Package Reference

Gas is a modular ecosystem for building micro-SaaS applications in Go. The core
package provides dependency injection, routing, middleware, events, migrations,
and service lifecycle management.

```
import "github.com/gasmod/gas"
```

For full provider interface signatures (Logger, DatabaseProvider, etc.),
supporting types (Email, Migration, etc.), and logging builder APIs, see
`references/providers.md`.

For built-in middleware option signatures (RequestLogger, SecurityHeaders,
CacheControl), see `references/middleware.md`.

## Architecture Principles

- **Infrastructure flows inward.** Services never import each other. They
  receive shared infrastructure (router, event bus, providers) through
  constructor injection and communicate via events and provider interfaces.
- **Ownership tracking.** Every route, middleware, and event subscription is
  tagged with its owning service, enabling surgical teardown at runtime.
- **Functional options** for App configuration (`AppOption`).
- **Interfaces defined where consumed**, not where implemented.

## Service Interface

Any type registered with the DI container that implements `Service` gets
automatic lifecycle management: `Init()` after construction, `Close()` at
shutdown (singletons) or scope end (scoped). Transient services **cannot**
implement `Service` — registration panics.

```go
type Service interface {
	Name() string // unique identifier, e.g. "gas-auth"
	Init() error  // register routes, middleware, subscriptions
	Close() error // cleanup internal resources
}
```

## Service Lifetimes

```go
const (
	ServiceLifetimeSingleton  ServiceLifetime = iota // created once, shared everywhere
	ServiceLifetimeScoped                            // created once per Scope (i.e. per HTTP request)
	ServiceLifetimeTransient                         // fresh on every resolution, CANNOT implement Service
)
```

**Choosing a lifetime:**
- **Singleton** — stateless services, routers, event subscriptions, anything
  that survives the full app lifetime. Most services are singletons.
- **Scoped** — per-request state like loggers with request-level fields, or
  database connections with request-scoped transactions.
- **Transient** — lightweight value objects that need fresh state on every
  injection. Cannot implement `Service` (no Init/Close lifecycle).

## App

`App` manages service lifecycle, the HTTP server, and graceful shutdown.

### Construction

```go
app := gas.NewApp(opts ...AppOption) *App
```

`NewApp` creates a `Router` and `EventBus` internally and registers them in
the container — services receive them via constructor injection. CSRF
protection (`net/http.CrossOriginProtection`) is enabled by default.

### AppOption functions

```go
// Service registration — pick the shorthand that matches the lifetime:
gas.WithSingletonService[T any](ctor any) AppOption
gas.WithScopedService[T any](ctor any) AppOption
gas.WithTransientService[T any](ctor any) AppOption

// Or specify the lifetime explicitly:
gas.WithService[T any](ctor any, lifetime ServiceLifetime) AppOption
gas.WithAppModule[T any](ctor any) AppOption     // alias for WithSingletonService
gas.WithServiceInstance[T any](val T) AppOption   // pre-built singleton instance

// Hooks and handlers
gas.WithErrorHandler(h ErrorHandler) AppOption
gas.WithReadyFunc(fn func(*ServiceContainer) error) AppOption

// CSRF protection (enabled by default)
gas.WithTrustedOrigin(origin string) AppOption               // panics if invalid URL
gas.WithCSRFInsecureBypassPattern(pattern string) AppOption  // for webhooks with own validation
gas.WithCSRFDenyHandler(h http.Handler) AppOption            // default: 403 Forbidden
```

`WithReadyFunc` runs after services init + migrations, before HTTP server
starts. Use for data seeding. Multiple hooks run in registration order; any
error aborts startup.

### App.Run()

**Startup sequence:** `InitServices` → `bindConfig` → migrations → ready hooks → route map log → HTTP server.

On shutdown (SIGINT/SIGTERM), services are closed in reverse init order.

### App methods

| Method             | Signature              | Description                                        |
|--------------------|------------------------|----------------------------------------------------|
| `Run`              | `() error`             | Full lifecycle: init → migrate → serve → shutdown  |
| `Config`           | `() *Config`           | Application configuration                          |
| `ConfigProvider`   | `() ConfigProvider`    | Resolved from DI, nil if unregistered              |
| `Router`           | `() *Router`           | The app's router                                   |
| `EventBus`         | `() *EventBus`         | The app's event bus                                |
| `MigrationManager` | `() MigrationManager`  | Resolved from DI, nil if unregistered              |
| `ServiceContainer` | `() *ServiceContainer` | The DI container                                   |
| `ActiveServices`   | `() []string`          | Names of currently active services                 |
| `CloseService`     | `(name string) error`  | Kill-switch: 503 routes, remove subs, call Close() |
| `RestartService`   | `(name string) error`  | Re-initialize a previously closed service          |

## DI Container

### Registration

```go
gas.RegisterCtor[T any](c *ServiceContainer, ctor any, lifetime ServiceLifetime)
gas.RegisterInstance[T any](c *ServiceContainer, val T)
```

Constructor signature: `func(DepA, DepB, ...) T` or `func(DepA, DepB, ...) (T, error)`

### Resolution

```go
gas.Resolve[T any](r Resolver) (T, error)
gas.MustResolve[T any](r Resolver) T // panics on failure
```

`Resolver` is implemented by `*ServiceContainer` and `*Scope`.

### Container methods

| Method                                 | Description                                                   |
|----------------------------------------|---------------------------------------------------------------|
| `NewServiceContainer()`                | Create new container                                          |
| `BuildAll() error`                     | Validate lifetimes, topo-sort, eagerly resolve all singletons |
| `NewScope() *Scope`                    | Create a scoped resolution context                            |
| `EachInstance(fn func(reflect.Value))` | Iterate all built singleton instances                         |
| `CanResolve(t reflect.Type) bool`      | Check if a type can be resolved                               |

**Captive dependency validation:** `BuildAll()` rejects singletons that depend
on scoped or transient services — this would "capture" a short-lived instance
inside a long-lived one. Error: `captive dependency: singleton X depends on scoped Y`.

## Request Scopes

The App installs middleware that creates a DI `Scope` per HTTP request. Scoped
services get a fresh instance per request — `Init()` on first resolution,
`Close()` when the request completes.

DI-aware handlers resolve scoped services automatically via their parameter
list. For classic `http.HandlerFunc` handlers:

```go
gas.ResolveFromRequestScope[T any](r *http.Request) (T, error)
gas.MustResolveFromRequestScope[T any](r *http.Request) T
```

For resolving multiple services, access the scope directly:

```go
scope := gas.RequestScope(r) // panics outside scope middleware
```

For non-HTTP contexts (background jobs, tests):

```go
scope := container.NewScope()
defer scope.Close()
ctx := gas.WithRequestScope(context.Background(), scope)
```

## Router

`Router` wraps Chi and tracks route/middleware ownership by service.

### Registering routes

```go
router.Handle(service, method, path string, handler any, middleware ...Middleware)
router.NotFound(service string, handler any)
```

The `handler` parameter accepts either `http.HandlerFunc` /
`func(http.ResponseWriter, *http.Request)` (passed through directly), or a
DI-aware function (see below).

### DI-Aware Handlers

Handlers declare dependencies as typed parameters. The router resolves each
from the per-request DI scope automatically.

```go
func(ctx gas.Context) error
func(ctx gas.Context, dep1 Dep1, dep2 Dep2, ...) error
```

Signature rules (panics at `Handle()` call time if violated):
- Must be a function
- First parameter must be `gas.Context`
- Must return exactly one value of type `error`

Boot-time validation ensures every handler dependency is registered in the
container — the app fails fast at startup, not at request time.

The adapter installs panic recovery around every DI-aware handler.
`http.ErrAbortHandler` is re-panicked; all other panics are logged and passed
to the `ErrorHandler` as `fmt.Errorf("gas: handler panic: %v", rec)`.

### Middleware

```go
gas.MiddlewareByName(name string) Middleware                                  // resolved from registry
gas.MiddlewareFunc(fn func(http.Handler) http.Handler) Middleware             // anonymous inline
gas.MiddlewareFuncWithName(name string, fn func(http.Handler) http.Handler) Middleware // named inline (appears in route map)

router.Register(service, name string, mw func(http.Handler) http.Handler)    // register named middleware
router.Use(middleware ...Middleware)                                           // apply globally
router.UseMiddlewareByName(name string)
router.UseMiddlewareFunc(fn func(http.Handler) http.Handler)
```

### Built-in Middleware

Gas provides three middleware constructors (see `references/middleware.md` for
full option signatures):

- **`RequestLogger[T Logger]`** — logs method, path, status, bytes, duration
  per request. Resolves a scoped Logger from DI. Status >= 400 → error level.
- **`SecurityHeaders`** — sets X-Content-Type-Options, X-Frame-Options, etc.
  with secure defaults.
- **`CacheControl`** — sets Cache-Control header based on path matching rules.

### Route grouping

```go
router.Group(fn func(sub *Router))              // inline group
router.Route(pattern string, fn func(sub *Router)) // pattern-scoped group
```

### Deferred registration

Top-level routers start **unsealed** — `Use`, `Handle`, `Group`, and `Route`
calls are deferred until `Seal()`. This lets services register in any order
during `Init()`. The App calls `Seal()` automatically after all services init.

### Other Router methods

| Method                                  | Description                                |
|-----------------------------------------|--------------------------------------------|
| `Mux() chi.Router`                      | Underlying Chi router                      |
| `Seal()`                                | Flush deferred middleware then routes      |
| `RemoveByModule(service string)`        | Replace routes with 503, remove middleware |
| `SetErrorHandler(h ErrorHandler)`       | Set error handler for DI-aware handlers    |
| `Routes() map[string][]RegisteredRoute` | Snapshot of registered routes by service   |
| `NamedMiddleware() map[string]string`   | Named middleware registry (name → service) |
| `ServeHTTP(w, req)`                     | Implements http.Handler                    |

## Context

`Context` is an **interface** that embeds `context.Context`. It is the first
parameter of every DI-aware handler. Because it satisfies `context.Context`, it
can be passed directly to database calls, gRPC, tracing, etc.

```go
gas.NewContext(parent context.Context, w http.ResponseWriter, r *http.Request, opts ...ContextOption) Context
```

Panics if any of parent, w, or r is nil. Options: `gas.WithValidate(v)`,
`gas.WithFormDecoder(d)`.

### Context methods

| Method           | Signature                      | Notes                   |
|------------------|--------------------------------|-------------------------|
| `ResponseWriter` | `() http.ResponseWriter`       |                         |
| `Request`        | `() *http.Request`             |                         |
| `JSON`           | `(status int, v any) error`    | application/json        |
| `XML`            | `(status int, v any) error`    | application/xml         |
| `RSS`            | `(status int, v any) error`    | application/rss+xml     |
| `HTML`           | `(status int, s string) error` | text/html               |
| `Text`           | `(status int, s string) error` | text/plain              |
| `NoContent`      | `() error`                     | 204                     |
| `Redirect`       | `(status int, url string)`     |                         |
| `Param`          | `(key string) string`          | chi.URLParam            |
| `Query`          | `(key string) string`          |                         |
| `Header`         | `(key string) string`          | request header          |
| `SetHeader`      | `(key, value string)`          | response header         |
| `BindJSON`       | `(dest any) error`             | decode + validate       |
| `BindForm`       | `(dest any) error`             | decode + validate       |
| `Validator`      | `() *validator.Validate`       | go-playground/validator |
| `FormDecoder`    | `() *schema.Decoder`           | gorilla/schema          |

`BindJSON` and `BindForm` both decode and then run struct validation via
`go-playground/validator`. The form decoder uses alias tag `"form"` and has
`IgnoreUnknownKeys(true)` enabled.

Because `Context` is an interface, tests can mock it via embedding:
```go
type mockContext struct { gas.Context }
```

## ErrorHandler

Converts a handler error into an HTTP response. The default logs the error
(if a Logger is in the container) and returns 500 with the standard status text.

```go
type ErrorHandler func(ctx Context, err error)
```

Custom error handlers should handle both normal errors and panic-originated
errors (which arrive as `fmt.Errorf("gas: handler panic: %v", rec)`).

Override via `gas.WithErrorHandler(h)` or `router.SetErrorHandler(h)`.

## EventBus

Typed publish/subscribe messaging between modules. Always prefer the generic
functions over the low-level string-based methods.

```go
// Define a typed event
var UserCreated = gas.Event[UserCreatedPayload]{Name: "user:created"}

// Emit (concurrent dispatch, returns *sync.WaitGroup)
gas.Emit[T](bus *EventBus, event Event[T], data T) *sync.WaitGroup

// Subscribe — always use SubscribeWithOwner from a service so that
// CloseService can clean up subscriptions. Bare Subscribe has no ownership
// tracking and should only be used outside of services.
gas.Subscribe[T](bus *EventBus, event Event[T], handler func(T))
gas.SubscribeWithOwner[T](bus *EventBus, service string, event Event[T], handler func(T))
```

### System Events

| Event                              | Payload                               | Fired When                        |
|------------------------------------|---------------------------------------|-----------------------------------|
| `gas.SystemServiceClosed`          | `{ServiceName string}`                | Service killed via `CloseService` |
| `gas.SystemServiceInitialized`     | `{ServiceName string}`                | Service finishes `Init`           |
| `gas.SystemAllServicesInitialized` | `struct{}`                            | All services initialized          |
| `gas.SystemServerShuttingDown`     | `struct{}`                            | Graceful shutdown begins          |
| `gas.AppConfigUpdated`             | `{Config Config}`                     | Config bound after init           |

## Provider Interfaces (summary)

Gas defines provider interfaces in the core package; implementations live in
separate modules. See `references/providers.md` for full signatures.

| Interface            | Purpose                    | Implementing module |
|----------------------|----------------------------|---------------------|
| `DatabaseProvider`   | SQL database access        | gas-database        |
| `CacheProvider`      | Key-value caching          | (custom)            |
| `JobQueueProvider`   | Async job/message queues   | (custom)            |
| `EmailProvider`      | Email sending              | (custom)            |
| `StorageProvider`    | File storage (S3, etc.)    | (custom)            |
| `ConfigProvider`     | Configuration management   | gas-config          |
| `TemplateProvider`   | Template storage/retrieval | gas-ui              |
| `UIProvider`         | Template rendering         | gas-ui              |
| `Logger`             | Structured logging         | gas-log             |
| `MigrationManager`   | Database migrations        | gas-migrate         |

### NopLogger

Built-in no-op logger for tests or when logging isn't needed:

```go
gas.WithScopedService[gas.Logger](gas.NewNopLogger())
```

## Config

```go
type Config struct {
	env.WithGasEnv
	Server ServerSettings
}

type ServerSettings struct {
	Host            string        // default: "0.0.0.0"
	Port            int           // default: 8080
	ReadTimeout     time.Duration // default: 5s
	WriteTimeout    time.Duration // default: 10s
	IdleTimeout     time.Duration // default: 2m
	ShutdownTimeout time.Duration // default: 30s
}

gas.DefaultConfig() *Config
config.Validate() error
```

## Writing a Service (Complete Example)

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

func New(router *gas.Router, bus *gas.EventBus) *Service {
	return &Service{router: router, bus: bus}
}

func (s *Service) Name() string { return "myservice" }

func (s *Service) Init() error {
	// DI-aware handler — db is resolved per-request from the scoped container.
	s.router.Handle(s.Name(), "GET", "/hello", s.handleHello)

	// Classic http.HandlerFunc works too.
	s.router.Handle(s.Name(), "GET", "/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Always use SubscribeWithOwner from a service (not bare Subscribe)
	// so CloseService can clean up this subscription.
	gas.SubscribeWithOwner(s.bus, s.Name(), gas.SystemServiceClosed,
		func(payload gas.SystemServiceClosedPayload) {
			// handle another service being closed
		})

	return nil
}

func (s *Service) handleHello(ctx gas.Context, db gas.DatabaseProvider) error {
	return ctx.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Service) Close() error { return nil }
```

```go
app := gas.NewApp(
	gas.WithSingletonService[*myservice.Service](myservice.New),
)
```
