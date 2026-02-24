---
name: gas
description: >
  Reference documentation for the Gas core Go package (github.com/gasmod/gas) —
  the foundational layer of the Gas ecosystem for rapid SaaS development. Use
  this skill when writing, reviewing, or debugging Go code that imports or
  extends the gas core package. Covers the App lifecycle, DI container, service
  registration and lifetimes, Router with ownership tracking, DI-aware handlers,
  Context, ErrorHandler, EventBus, middleware, migrations, request scopes,
  logging, provider interfaces, and system events.
---

# Gas Core Package Reference

Gas is a modular ecosystem for building micro-SaaS applications in Go. The core
package provides shared infrastructure — dependency injection, routing,
middleware, events, migrations, and service lifecycle management.

```
import "github.com/gasmod/gas"
```

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
	ServiceLifetimeScoped                            // created once per Scope
	ServiceLifetimeTransient                         // fresh on every resolution, CANNOT implement Service
)
```

## App

`App` manages service lifecycle, the HTTP server, and graceful shutdown.

### Construction

```go
app := gas.NewApp(opts ...AppOption) *App
```

`NewApp` creates a `Router` and `EventBus` internally and registers them in the
container. Services receive them via constructor injection.

### AppOption functions

```go
gas.WithService[T any](ctor any, lifetime ServiceLifetime) AppOption
gas.WithAppModule[T any](ctor any) AppOption  // shorthand: WithService(ctor, ServiceLifetimeSingleton)
gas.WithServiceInstance[T any](val T) AppOption
gas.WithErrorHandler(h ErrorHandler) AppOption
gas.WithReadyFunc(fn func(*ServiceContainer) error) AppOption
```

`WithReadyFunc` registers a hook that runs after all services are initialized
and migrations have run, but before the HTTP server starts accepting traffic.
Multiple hooks are called in registration order; any error aborts startup.
Intended for data seeding and other pre-traffic startup tasks.

```go
app := gas.NewApp(
    gas.WithSingletonService[*DB](NewDB),
    gas.WithReadyFunc(func(sc *gas.ServiceContainer) error {
        db := gas.MustResolve[*DB](sc)
        return seed.Run(db)
    }),
)
```

### App.Run()

`Run()` initializes all services (via DI container), runs pending migrations,
executes ready hooks, starts the HTTP server, and blocks until a shutdown signal
is received. On shutdown, services are closed in reverse init order.

**Startup sequence:** `InitServices` → `bindConfig` → migrations → ready hooks → HTTP server.

### App methods

| Method               | Signature                                          | Description                                        |
|----------------------|----------------------------------------------------|----------------------------------------------------|
| `Run`                | `() error`                                         | Full lifecycle: init → migrate → serve → shutdown  |
| `Config`             | `() *Config`                                       | Application configuration                          |
| `ConfigProvider`     | `() ConfigProvider`                                | Resolved from DI, nil if unregistered              |
| `Router`             | `() *Router`                                       | The app's router                                   |
| `EventBus`           | `() *EventBus`                                     | The app's event bus                                |
| `MigrationManager`   | `() MigrationManager`                              | Resolved from DI, nil if unregistered              |
| `ServiceContainer`   | `() *ServiceContainer`                             | The application's DI container                     |
| `ActiveServices`     | `() []string`                                      | Names of currently active services                 |
| `CloseService`       | `(name string) error`                              | Kill-switch: 503 routes, remove subs, call Close() |
| `RestartService`     | `(name string) error`                              | Re-initialize a previously closed service          |

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

| Method                                 | Description                                        |
|----------------------------------------|----------------------------------------------------|
| `NewServiceContainer()`                | Create new container                               |
| `BuildAll() error`                     | Eagerly resolve all singletons in dependency order |
| `NewScope() *Scope`                    | Create a scoped resolution context                 |
| `EachInstance(fn func(reflect.Value))` | Iterate all built singleton instances              |

## Request Scopes

The App installs middleware that creates a DI `Scope` per HTTP request. Scoped
services get a fresh instance per request — `Init()` on first resolution,
`Close()` when the request completes.

DI-aware handlers resolve scoped services automatically via their parameter
list. For classic `http.HandlerFunc` handlers, use the request-scope
convenience helpers:

```go
// Returns (T, error) — safe, no panic on missing registration.
gas.ResolveFromRequestScope[T any](r *http.Request) (T, error)

// Panics if T cannot be resolved.
gas.MustResolveFromRequestScope[T any](r *http.Request) T
```

Both are thin wrappers around `gas.RequestScope(r)` + `gas.Resolve`/
`gas.MustResolve`. For full scope access (resolving multiple services),
use `gas.RequestScope` directly:

```go
scope := gas.RequestScope(r) // *Scope — panics outside scope middleware
svc := gas.MustResolve[*MyScopedService](scope)
```

For non-HTTP contexts (background jobs, tests):

```go
scope := container.NewScope()
defer scope.Close()
svc, err := gas.Resolve[*MyScopedService](scope)
```

## Router

`Router` wraps Chi and tracks route/middleware ownership by service. During
kill-switch, `RemoveByModule` replaces closed service routes with 503 handlers.

### Registering routes

```go
router.Handle(service, method, path string, handler any, middleware ...Middleware)
```

```go
router.NotFound(service string, handler any)
```

The `handler` parameter accepts:

- `http.HandlerFunc` or `func(http.ResponseWriter, *http.Request)` — passed
  through directly to Chi with no wrapping.
- A DI-aware function — validated and wrapped via reflection. See
  [DI-Aware Handlers](#di-aware-handlers) below.

### DI-Aware Handlers

Handlers can declare dependencies as typed function parameters. The router
resolves each dependency from the per-request DI scope automatically.

**Handler contract:** `gas.Context` first, dependencies in between, `error` return.

```go
func(ctx gas.Context) error
func(ctx gas.Context, dep1 Dep1, dep2 Dep2, ...) error
```

**Signature validation** (panics at `Handle()` call time):
- Must be a function
- First parameter must be `gas.Context`
- Must return exactly one value of type `error`

**Boot-time validation:** `InitServices()` verifies (after `Seal()`) that every
handler dependency type is registered in the container. Returns an error on the
first unresolvable type — the app fails fast at startup.

**Runtime flow:** For each request, the adapter constructs a `Context`, resolves
each dependency from the request-scoped container via `Scope.resolveType()`,
calls the handler via reflection, and passes any returned error to the
`ErrorHandler`.

### Middleware

```go
// Named (resolved from registry at apply time)
gas.MiddlewareByName(name string) Middleware

// Inline
gas.MiddlewareFunc(fn func (http.Handler) http.Handler) Middleware

// Register named middleware
router.Register(service, name string, mw func (http.Handler) http.Handler)

// Apply globally (panics if named middleware not registered)
router.Use(middleware ...Middleware)
router.UseMiddlewareByName(name string)
router.UseMiddlewareFunc(fn func (http.Handler) http.Handler)
```

### Route grouping

```go
// Inline group (shares parent registry + tracking)
router.Group(fn func (sub *Router))

// Pattern-scoped group
router.Route(pattern string, fn func (sub *Router))
```

### Deferred registration

Top-level routers (created via `NewRouter()`) start **unsealed**: `Use`, `Handle`,
`Group`, and `Route` calls are deferred until `Seal()` is called. This lets
services register middleware and routes in any order during `Init()`. `Seal()`
flushes all pending middleware first, then replays route operations —
guaranteeing the middleware-before-routes ordering that Chi requires.

Sub-routers (created inside `Group`/`Route` callbacks) are always sealed.

The `App` calls `Seal()` automatically after all services are initialized.

### Other Router methods

| Method                            | Description                                               |
|-----------------------------------|-----------------------------------------------------------|
| `Mux() chi.Router`                | Underlying Chi router for global middleware / http.Server |
| `Seal()`                          | Flush deferred middleware then routes; idempotent         |
| `RemoveByModule(service string)`  | Replace service routes with 503, remove middleware        |
| `SetErrorHandler(h ErrorHandler)` | Set the error handler for DI-aware handlers               |
| `ServeHTTP(w, req)`               | Implements http.Handler                                   |

## Context

`Context` is the first parameter of every DI-aware handler. It wraps
`http.ResponseWriter` and `*http.Request` with convenience methods.

```go
type Context struct { /* unexported fields */ }

gas.NewContext(w http.ResponseWriter, r *http.Request) Context
```

### Context methods

| Method                                 | Description                         |
|----------------------------------------|-------------------------------------|
| `ResponseWriter() http.ResponseWriter` | Underlying response writer          |
| `Request() *http.Request`              | Underlying request                  |
| `RequestContext() context.Context`     | Context of the current HTTP request |
| `JSON(status int, v any) error`        | Write JSON response                 |
| `Text(status int, s string) error`     | Write plain-text response           |
| `NoContent() error`                    | Write 204 No Content                |
| `Redirect(status int, url string)`     | Send HTTP redirect                  |
| `Param(key string) string`             | URL path parameter (chi.URLParam)   |
| `Query(key string) string`             | Query string parameter              |
| `Header(key string) string`            | Request header value                |
| `SetHeader(key, value string)`         | Set response header                 |
| `BindJSON(dest any) error`             | Decode JSON request body            |

## ErrorHandler

`ErrorHandler` converts a handler error into an HTTP response. The default writes a
500 Internal Server Error with the default `http.StatusText(http.StatusInternalServerError)` body,
and logs the error if a logger is registered in the service container.

```go
type ErrorHandler func(ctx Context, err error)
```

Override at the App level:

```go
gas.WithErrorHandler(func(ctx gas.Context, err error) {
	ctx.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
})
```

Or on the Router directly:

```go
router.SetErrorHandler(h ErrorHandler)
```

## EventBus

Typed publish/subscribe messaging between modules using `Event[T]` definitions.

### Defining events

```go
var UserCreated = gas.Event[UserCreatedPayload]{Name: "user:created"}

type UserCreatedPayload struct {
	Email string
}
```

### Top-level generic functions

```go
// Emit dispatches a typed event concurrently; returns *sync.WaitGroup.
gas.Emit[T](bus *EventBus, event Event[T], data T) *sync.WaitGroup

// Subscribe registers a typed handler without service ownership.
gas.Subscribe[T](bus *EventBus, event Event[T], handler func(T))

// SubscribeWithOwner registers a typed handler with service ownership tracking.
gas.SubscribeWithOwner[T](bus *EventBus, service string, event Event[T], handler func(T))
```

### Low-level EventBus methods

```go
bus := gas.NewEventBus()

bus.Emit(event string, data any) *sync.WaitGroup
bus.Subscribe(event string, handler func(any))
bus.SubscribeWithOwner(service, event string, handler func(any))
bus.RemoveByModule(service string)
```

## System Events

| Event                              | Payload Type                          | Fired When                                  |
|------------------------------------|---------------------------------------|---------------------------------------------|
| `gas.SystemServiceClosed`          | `SystemServiceClosedPayload`          | Service killed via `CloseService`           |
| `gas.SystemServiceInitialized`     | `SystemServiceInitializedPayload`     | Service finishes `Init` (including restart) |
| `gas.SystemAllServicesInitialized` | `SystemAllServicesInitializedPayload` | All services initialized                    |
| `gas.SystemServerShuttingDown`     | `SystemServerShuttingDownPayload`     | Server begins graceful shutdown             |
| `gas.AppConfigUpdated`             | `AppConfigUpdatedPayload`             | App config updated after binding            |

Payload structs with fields:
- `SystemServiceClosedPayload{ServiceName string}`
- `SystemServiceInitializedPayload{ServiceName string}`
- `AppConfigUpdatedPayload{Config Config}`

## Provider Interfaces

Services depend on interfaces, not implementations. Gas defines these in the
core package; implementations live in separate modules.

```go
type DatabaseProvider interface {
	DB() *sql.DB
	Ping(ctx context.Context) error
	Query(ctx context.Context, query string, args ...any) (Rows, error)
	Exec(ctx context.Context, query string, args ...any) (Result, error)
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
	WithTx(ctx context.Context, opts *sql.TxOptions, fn func(*sql.Tx) error) (err error)
}

type CacheProvider interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

type EmailProvider interface {
	Send(ctx context.Context, msg Email) error
}

type StorageProvider interface {
	Upload(ctx context.Context, key string, data io.Reader) error
	Download(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

type ConfigProvider interface {
	SetDefault(key string, value any)
	SetDefaults(values any) error
	Set(key string, value any)
	Bind(dest any, options ...config.BindOption) error
	Get(key string) any
	Find(key string) (value any, exist bool)
	Values() map[string]any
}

type UIProvider interface {
	Render(w http.ResponseWriter, name string, data any) error
	RenderWithStatus(w http.ResponseWriter, status int, name string, data any) error
	RegisterTemplate(name string, content []byte)
	RegisterTemplatesFS(fsys fs.FS) error
	RegisterFuncs(funcs template.FuncMap)
}

// Context helpers — store / retrieve a Logger in context.Context.
gas.WithLogger(ctx context.Context, l Logger) context.Context
gas.LoggerFromContext(ctx context.Context) Logger // returns nil if absent

// With() vs SetBaseFields()
// With() always branches — it returns a LoggerContext whose Logger() produces a NEW Logger instance.
// SetBaseFields() mutates the receiver in place via Apply(). Use it in request-scoped middleware
// where one Logger instance is shared across the whole request and you want every subsequent log
// call (in other middleware and in the handler) to carry fields like request_id, user_id, trace_id
// automatically — without threading a new Logger reference around.
//
// Typical middleware pattern:
//   logger := gas.MustResolveFromRequestScope[gas.Logger](r)
//   logger.SetBaseFields().Str("request_id", reqID).Str("user_id", userID).Apply()
//   // All log calls for the rest of this request carry request_id and user_id.

type Logger interface {
	Trace(msg string) LogEvent
	Debug(msg string) LogEvent
	Info(msg string) LogEvent
	Warn(msg string) LogEvent
	Error(msg string) LogEvent
	With() LoggerContext
	SetBaseFields() MutableLoggerContext
	Flush()
}

type LoggerContext interface {
	Str(key, val string) LoggerContext
	Int(key string, val int) LoggerContext
	Int64(key string, val int64) LoggerContext
	Float64(key string, val float64) LoggerContext
	Bool(key string, val bool) LoggerContext
	Err(key string, val error) LoggerContext
	Duration(key string, val time.Duration) LoggerContext
	Any(key string, val any) LoggerContext
	Logger() Logger // returns a new branched Logger
}

// MutableLoggerContext mutates the originating Logger in place.
// Use SetBaseFields() (not With()) when you need a shared Logger instance
// to carry persistent fields for the rest of the request without threading
// a new Logger reference through every caller.
type MutableLoggerContext interface {
	Str(key, val string) MutableLoggerContext
	Int(key string, val int) MutableLoggerContext
	Int64(key string, val int64) MutableLoggerContext
	Float64(key string, val float64) MutableLoggerContext
	Bool(key string, val bool) MutableLoggerContext
	Err(key string, val error) MutableLoggerContext
	Duration(key string, val time.Duration) MutableLoggerContext
	Any(key string, val any) MutableLoggerContext
	Apply() // mutates the originating Logger in place
}

type LogEvent interface {
	Str(key, val string) LogEvent
	Int(key string, val int) LogEvent
	Int64(key string, val int64) LogEvent
	Float64(key string, val float64) LogEvent
	Bool(key string, val bool) LogEvent
	Err(key string, val error) LogEvent
	Duration(key string, val time.Duration) LogEvent
	Any(key string, val any) LogEvent
	Send()
}

type MigrationManager interface {
	Service
	Register(service string, m Migration)
	RegisterSlice(service string, migrations []Migration)
	RegisterFS(service string, fsys fs.FS) error
	RunPending() error
	Down(n int) error
}
```

### Supporting types

```go
type Email struct {
	To, From, Subject, Body, HTML string
}

type Migration struct {
	Version, Service, Description, Up, Down string
}

type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Close() error
	Err() error
}

type Result interface {
	RowsAffected() (int64, error)
	LastInsertId() (int64, error)
}
```

## Config

```go
type Config struct {
	env.WithGasEnv
	Server ServerSettings
}

type ServerSettings struct {
	Host            string
	Port            int
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
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

```go
app := gas.NewApp(
	gas.WithService[*myservice.Service](myservice.New, gas.ServiceLifetimeSingleton),
)
```
