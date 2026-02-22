---
name: gas
description: >
  Reference documentation for the Gas core Go package (github.com/gasmod/gas) —
  the foundational layer of the Gas ecosystem for rapid SaaS development. Use
  this skill when writing, reviewing, or debugging Go code that imports or
  extends the gas core package. Covers the App lifecycle, DI container, service
  registration and lifetimes, Router with ownership tracking, EventBus,
  middleware, migrations, request scopes, logging, provider interfaces, and
  system events.
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
gas.WithServiceInstance[T any](val T) AppOption
```

### App.Run()

`Run()` initializes all services (via DI container), runs pending migrations,
starts the HTTP server, and blocks until a shutdown signal is received. On
shutdown, services are closed in reverse init order.

### App methods

| Method               | Signature                                          | Description                                        |
|----------------------|----------------------------------------------------|----------------------------------------------------|
| `Run`                | `() error`                                         | Full lifecycle: init → migrate → serve → shutdown  |
| `Config`             | `() *Config`                                       | Application configuration                          |
| `ConfigProvider`     | `() ConfigProvider`                                | Resolved from DI, nil if unregistered              |
| `Router`             | `() *Router`                                       | The app's router                                   |
| `EventBus`           | `() *EventBus`                                     | The app's event bus                                |
| `MigrationManager`   | `() MigrationManager`                              | Resolved from DI, nil if unregistered              |
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
gas.Resolve[T any](r Resolver) (T, bool)
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

```go
scope := gas.RequestScope(r *http.Request) *Scope // panics outside scope middleware
svc := gas.MustResolve[*MyScopedService](scope)
```

For non-HTTP contexts (background jobs, tests):

```go
scope := container.NewScope()
defer scope.Close()
svc, ok := gas.Resolve[*MyScopedService](scope)
```

## Router

`Router` wraps Chi and tracks route/middleware ownership by service. During
kill-switch, `RemoveByModule` replaces closed service routes with 503 handlers.

### Registering routes

```go
router.Handle(service, method, path string, handler http.HandlerFunc, middleware ...Middleware)
```

```go
router.NotFound(service string, handler handler http.HandlerFunc)
```

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

| Method                           | Description                                               |
|----------------------------------|-----------------------------------------------------------|
| `Mux() chi.Router`               | Underlying Chi router for global middleware / http.Server |
| `Seal()`                         | Flush deferred middleware then routes; idempotent         |
| `RemoveByModule(service string)` | Replace service routes with 503, remove middleware        |
| `ServeHTTP(w, req)`              | Implements http.Handler                                   |

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
	Query(ctx context.Context, query string, args ...any) (Rows, error)
	Exec(ctx context.Context, query string, args ...any) (Result, error)
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

type Logger interface {
	Trace(msg string) LogEvent
	Debug(msg string) LogEvent
	Info(msg string) LogEvent
	Warn(msg string) LogEvent
	Error(msg string) LogEvent
	With() LoggerContext
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
	Logger() Logger
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
	ServerHost             string
	ServerPort             int
	ServerReadTimeout      time.Duration
	ServerWriteTimeout     time.Duration
	ServerIdleTimeout      time.Duration
	ServerShutdownTimeout  time.Duration
}

gas.DefaultConfig() *Config
config.Validate() error
```

## Writing a Service (Complete Example)

```go
package myservice

import "github.com/gasmod/gas"

type Service struct {
	router *gas.Router
	bus    *gas.EventBus
}

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

```go
app := gas.NewApp(
	gas.WithService[*myservice.Service](myservice.New, gas.ServiceLifetimeSingleton),
)
```
