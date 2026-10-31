---
name: gas
description: >
  Reference documentation for the Gas core Go package (github.com/gasmod/gas),
  the foundational layer of the Gas ecosystem for rapid SaaS development. Use
  this skill when writing, reviewing, or debugging Go code that imports or
  extends the gas core package. Covers the one-shot App and Worker lifecycle,
  DI container, service registration and lifetimes, Router with ownership
  tracking, DI-aware handlers, Context, the unified Error and ErrorHandler,
  EventBus, middleware (RequestLogger, SecurityHeaders, CacheControl,
  BodyLimit, Timeout), migrations, request scopes, logging, provider
  interfaces, Config validation, system events, and the runtime kill switch
  (CloseService / RestartService / RemoveByService) that 503s a torn-down
  service's routes and its named middleware wherever that middleware is used.
  Make sure to use this skill whenever working with gas service constructors,
  route handlers, event subscriptions, middleware registration,
  authentication, authorization, or any code under a gasmod/gas import path,
  even if the user doesn't explicitly mention "gas".
---

# Gas Core Package Reference

Gas is a modular ecosystem for building micro-SaaS applications in Go. The core
package provides dependency injection, routing, middleware, events, migrations,
and service lifecycle management. It imports none of the gas modules; they meet
only through the interfaces core defines.

```
import "github.com/gasmod/gas"
```

For full provider interface signatures (Logger, DatabaseProvider, etc.),
supporting types (Email, Migration, etc.), and logging builder APIs, see
`references/providers.md`.

For built-in middleware option signatures (RequestLogger, SecurityHeaders,
CacheControl, BodyLimit, Timeout), see `references/middleware.md`.

## Architecture Principles

- **Infrastructure flows inward.** Services never import each other. They
  receive shared infrastructure (router, event bus, providers) through
  constructor injection and communicate via events and provider interfaces.
- **Ownership tracking.** Every route, named middleware, NotFound handler and
  owned event subscription is tagged with its owning service, by name,
  enabling surgical teardown at runtime.
- **Functional options**: `WorkerOption` for DI/lifecycle, `AppOption` for
  HTTP-specific concerns. Both satisfy a shared `Option` interface.
- **Interfaces defined where consumed**, not where implemented.
- **One-shot lifecycle.** A Worker or App starts once and shuts down once.
  Restart is per service, through the kill switch.

## Service Interface

Any type registered with the DI container that implements `Service` gets
automatic lifecycle management: `Init(ctx)` after construction, `Close(ctx)`
at shutdown (singletons) or scope end (scoped). Transient services **cannot**
implement `Service`: registration panics with an error wrapping
`gas.ErrInvalidLifetime`.

```go
type Service interface {
	Name() string                    // e.g. "gas/auth"; routes etc. are owned by name
	Init(ctx context.Context) error  // register routes, middleware, subscriptions
	Close(ctx context.Context) error // cleanup; must return when ctx ends
}
```

**Init's context.** A singleton's Init gets the caller's startup context
(`Start(ctx)` / `RestartService(ctx)`) plus an init deadline,
`gas.DefaultServiceInitTimeout` (15s) by default (`gas.WithServiceInitTimeout`;
0 disables it; a negative value makes `Start` fail with an error wrapping
`gas.ErrInvalidOption`). A service implementing `gas.ServiceInitTimeout`
(`InitTimeout() time.Duration`) sets its own; a negative value there fails Init
without running it. When the deadline passes the container cancels ctx,
**waits for Init to return**, and fails with an error wrapping
`context.DeadlineExceeded`; an Init that ignores ctx and returns nil late is
closed again. So:

- Do synchronous startup work (dial, ping, retry loops) with ctx.
- Never derive a background goroutine from Init's ctx: it is canceled as soon
  as Init returns. Give the goroutine its own context and cancel it in Close.
- A scoped service's Init gets the scope's context (the request's, in an App)
  with the same init deadline and timeout handling; a scoped Init that times
  out fails the resolution.

Close gets the caller's shutdown context (`Shutdown(ctx)` / `Stop(ctx)` /
`CloseService(ctx)`; a request scope passes the request context without its
cancellation). Close must return when ctx ends, since the shutdown deadline
relies on it.

**Declare `Init` or `Close` and you owe all three.** They are the managed
lifecycle, so a registered type declaring either one must implement the full
interface with the exact signatures above. Anything short of that makes
`Start` (via `BuildAll`) fail with an error wrapping
`gas.ErrInvalidServiceShape` that names what is missing or mis-typed:

```
gas: *app.Foo declares Close but does not implement gas.Service (missing Name()
string; missing Init(context.Context) error); Init and Close are the managed
lifecycle, so a type declaring either must implement all of gas.Service;
otherwise it is never initialized, never closed, and invisible to the kill switch
```

Watch for these, all of which are rejected:
- `func (s *Foo) Init() error`: the pre-context signature; the error says
  `Init has signature ..., want Init(context.Context) error`
- `func (s *Foo) Init(ctx context.Context)`: no `error` return
- any two of the three, or just one: `Init` alone, `Close` alone, `Name`+`Close`
- registering the value type (`WithSingletonService[Foo]`) when the methods are
  declared on `*Foo`: the error says to register it as `*Foo`
- a third-party `io.Closer` such as `*sql.DB`: you cannot add the remaining
  methods to another package's type, so wrap it in a service of your own

`Name` alone is **not** a trigger. A type declaring only `Name() string` is an
ordinary dependency and is unaffected, as is one declaring none of the three.
The same registration options carry both kinds: `gas.Logger`, `ConfigProvider`,
a scoped request-logger type, a transient value type are all registered with
`With*Service` and none of them implement `Service`.

Give every service a unique `Name()`. The kill switch refuses a service whose
name another started service shares (`gas.ErrDuplicateServiceName`).

## Service Lifetimes

```go
const (
	ServiceLifetimeSingleton  ServiceLifetime = iota // created once, shared everywhere
	ServiceLifetimeScoped                            // created once per Scope (i.e. per HTTP request)
	ServiceLifetimeTransient                         // fresh on every resolution, CANNOT implement Service
)
```

**Choosing a lifetime:**
- **Singleton**: stateless services, routers, event subscriptions, anything
  that survives the full app lifetime. Most services are singletons.
- **Scoped**: per-request state like a request logger type with
  request-level fields (derived from the singleton `gas.Logger`, which stays a
  singleton because services depend on it), or per-request units of work.
- **Transient**: lightweight value objects that need fresh state on every
  injection. Cannot implement `Service` (no Init/Close lifecycle).

## Option Types

Both `NewWorker` and `NewApp` accept `...Option`. The `Option` interface is
satisfied by two concrete types:

```go
type Option interface{ /* unexported */ }

type WorkerOption func(*Worker)   // DI registration, ready hooks, timeouts
type AppOption    func(*App)      // HTTP-specific: error handler, CSRF, origins, environments
```

### WorkerOption functions (work with both NewWorker and NewApp)

```go
gas.WithSingletonService[T any](ctor any) WorkerOption
gas.WithScopedService[T any](ctor any) WorkerOption
gas.WithTransientService[T any](ctor any) WorkerOption
gas.WithService[T any](ctor any, lifetime ServiceLifetime) WorkerOption
gas.WithServiceInstance[T any](val T) WorkerOption
gas.WithServiceAlias[Target, Alias any]() WorkerOption // Alias resolves Target
gas.WithReadyFunc(fn func(*ServiceContainer) error) WorkerOption
gas.WithServiceInitTimeout(timeout time.Duration) WorkerOption // default 15s; 0 = none
gas.WithShutdownTimeout(timeout time.Duration) WorkerOption    // Worker.Run only; default 30s
```

`WithServiceInstance` means pre-**constructed**, not pre-**initialized**. If the
value implements `Service`, the container calls `Init(ctx)` on it during
`Start` (before anything it constructs, in registration order, so
reverse-order shutdown stays correct) and `Close(ctx)` at shutdown, exactly as
for a constructor-registered service, with the same init deadline. Do not call
`Init` yourself before registering, or it runs twice. A nil value panics with
an error wrapping `gas.ErrNilInstance`.

`WithShutdownTimeout` bounds the Shutdown that `Worker.Run` starts after a
signal. In an App it has no effect: `Stop` and `App.Run` use the Config's
`ShutdownTimeout`. A non-positive value makes `Start` fail with
`ErrInvalidOption`.

### AppOption functions (only work with NewApp)

```go
gas.WithErrorHandler(h ErrorHandler) AppOption               // nil keeps the default
gas.WithTrustedOrigin(origin string) AppOption               // absolute URL, else Start fails (ErrInvalidOption)
gas.WithCSRFInsecureBypassPattern(pattern string) AppOption  // ServeMux pattern; for webhooks with own validation
gas.WithCSRFDenyHandler(h http.Handler) AppOption            // default: 403 *Error, code "forbidden"
gas.WithAllowedEnvironments(envs ...Environment) AppOption   // environments Start accepts in Config.Env
```

Passing an `AppOption` to `NewWorker` panics with an error wrapping
`gas.ErrInvalidOption`. A bypass pattern `http.CrossOriginProtection` rejects
panics. `WithAllowedEnvironments` defaults to development, testing, staging and
production; `gas.DefaultEnvironment` (development) is always allowed, and an
empty list makes `Start` fail with `ErrInvalidOption`. Options with a bad value
fail `Start` before anything is initialized.

## Worker

`Worker` manages service lifecycle, DI, events, and migrations without an
HTTP server. Use it for AWS Lambda, background workers, or CLI tools.

### Construction

```go
w := gas.NewWorker(opts ...Option) *Worker
```

`NewWorker` creates an `EventBus` and registers it in the container, and
registers the Worker itself as `gas.HealthProvider` and `gas.ReadyProvider`.

### Lifecycle

A Worker runs **once**: `Start`, then `Shutdown`. `Start` checks the options,
builds every singleton in dependency order calling `Init` on each `Service`
(pre-registered instances first), emits `SystemAllServicesInitialized`, runs
the pending migrations of the registered `MigrationManager` with its ctx, then
runs the ready hooks in registration order. It does not block.

- A second `Start` returns `gas.ErrAlreadyStarted`; a `Start` once Shutdown has
  begun returns `gas.ErrShutDown`.
- A failed `Start` closes, newest first, every service it had initialized
  (with ctx stripped of its cancellation) and returns its error joined with
  any Close error. **Every later `Start` returns that error again.** Build a
  new Worker to retry.
- A panic in an Init or constructor fails `Start` with an error wrapping
  `gas.ErrPanic`, and is panicked again once cleanup is done.

`Shutdown` waits for a Start, CloseService or RestartService in progress,
emits `SystemShuttingDown` and waits for its subscribers, then closes every
active service in reverse Init order, passing its ctx to each Close. A failing
Close does not stop the others; errors are returned joined. The `Logger`, and
the services it depends on, are closed last, after the final `shutdown
complete` line and a `Flush`. `Shutdown` runs once; a second call waits for
the first and returns nil. A `Shutdown` before `Start` closes nothing and makes
`Start` return `ErrShutDown`.

Work that uses a service (a queue consumer, a poller) must stop before that
service closes: run it in a service that depends on what it uses, so it closes
first, or stop it from a `SystemShuttingDown` subscriber.

### Worker methods

| Method             | Signature                | Description                                                                      |
|--------------------|--------------------------|----------------------------------------------------------------------------------|
| `Start`            | `(ctx) error`            | Init services, migrations, ready hooks (non-blocking); one-shot, see above      |
| `Shutdown`         | `(ctx) error`            | Emit SystemShuttingDown, close services in reverse order, Logger last           |
| `Run`              | `() error`               | Start under a signal-canceled ctx, block on SIGINT/SIGTERM, Shutdown within `WithShutdownTimeout` |
| `EventBus`         | `() *EventBus`           | The worker's event bus                                                           |
| `ServiceContainer` | `() *ServiceContainer`   | The DI container                                                                 |
| `MigrationManager` | `() MigrationManager`    | Resolved from DI as exactly `gas.MigrationManager`; nil if unregistered or it cannot be built |
| `ConfigProvider`   | `() ConfigProvider`      | Resolved from DI; nil if unregistered or it cannot be built                      |
| `RegisterService[T]` | `(ctor any, lifetime ServiceLifetime)` | Imperative form of `WithService`; forwards to the container             |
| `RegisterTransientService[T]` / `RegisterScopedService[T]` / `RegisterSingletonService[T]` | `(ctor any)` | Same, with the lifetime fixed |
| `RegisterServiceInstance[T]` | `(val T)`      | Registers a pre-built value under `T`, the static type at the call site          |
| `RegisterServiceAlias[Target, Alias]` | `()` | Imperative form of `WithServiceAlias`; forwards to the container |
| `ReadyFunc`        | `(fn func(*ServiceContainer) error)` | Imperative form of `WithReadyFunc`; panics with `ErrAlreadyStarted` after the ready hooks ran |
| `ActiveServices`   | `() []string`            | Names of active services in Init order, including `gas/eventbus` (and `gas/router` in an App) |
| `CloseService[T Service]`   | `(ctx) error`   | Kill switch: 503 the service's routes and middleware, remove subs, `Close(ctx)`, emit event |
| `RestartService[T Service]` | `(ctx) error`   | Re-run Init on a closed service under the init deadline; it registers anew |
| `CheckHealth`      | `(ctx) map[string]error` | Concurrently polls all active `HealthReporter`s; also satisfies `HealthProvider` |
| `CheckReady`       | `(ctx) map[string]error` | Concurrently polls all active `ReadyReporter`s; also satisfies `ReadyProvider`   |

### Worker usage (Lambda example)

```go
w := gas.NewWorker(
    // database.New needs a gas.ConfigProvider and a gas.Logger registered too.
    gas.WithSingletonService[gas.DatabaseProvider](database.New()),
    gas.WithSingletonService[*myservice.Service](myservice.New),
)
if err := w.Start(context.Background()); err != nil { log.Fatal(err) }
defer func() { _ = w.Shutdown(context.Background()) }() // runs once; returns Close errors joined

lambda.Start(func(ctx context.Context, event MyEvent) error {
    scope := w.ServiceContainer().NewScope(ctx)
    defer func() { _ = scope.Close(context.WithoutCancel(ctx)) }()
    svc := scope.MustResolve[*myservice.Service]()
    return svc.Handle(ctx, event)
})
```

## App

`App` embeds `*Worker` and adds routing, cross-origin protection, and an HTTP
server. All Worker methods are available on App, including `Start`.

### Construction

```go
app := gas.NewApp(opts ...Option) *App
```

`NewApp` creates a `Router` and `EventBus` and registers them in the container;
at `Start` it also registers the bound `*gas.Config`, so services receive any
of them via constructor injection (registering `*gas.Config` yourself fails
`Start` with `ErrInvalidOption`). Cross-origin protection
(`net/http.CrossOriginProtection`) guards every App; a rejection answers 403
with code `forbidden` in the unified error shape. Core ships no CORS and no
health endpoints.

### Startup and shutdown

**`Start` on an App**: the Worker sequence above, with the App's part after the
services are built: `*Config` is bound from the `ConfigProvider` and validated
(see **Config**), the router is sealed, every DI-aware handler's dependencies
are checked (a missing one fails `Start` with `ErrNotRegistered`, naming the
route), and `AppConfigUpdated` is emitted. Then `SystemAllServicesInitialized`,
migrations, ready hooks.

**`Run`**: `Start` under a context the first SIGINT/SIGTERM cancels, log the
route map, `Serve`, block until a signal or a server error, then `Stop` with
`context.Background()`. A second signal ends the process. A server error is
returned joined with Stop's error.

**`Stop`** is bounded by the Config's `ShutdownTimeout` plus up to three times
`gas.ShutdownGracePeriod` (1s), and by its ctx: it emits
`SystemServerShuttingDown` and waits for its subscribers, shuts the HTTP server
down, at the deadline cancels the context of every request still running and
waits up to `ShutdownGracePeriod`, then closes their connections and waits once
more, then runs `Worker.Shutdown`. Every error is returned joined.

`Run` is the convenience path. For embedding scenarios (tests, custom
listeners, Lambda with HTTP, etc.), compose `Start`, `Serve`, `Stop` yourself,
or serve `Handler()` directly.

### App-specific methods

| Method    | Signature         | Description                                                           |
|-----------|-------------------|-----------------------------------------------------------------------|
| `Run`     | `() error`        | Full lifecycle: Start, Serve, block on signal, Stop                   |
| `Serve`   | `() error`        | Serve and block; `ErrNotStarted` before Start, `ErrShutDown` once Stop began, nil when closed by Stop |
| `Stop`    | `(ctx) error`     | Bounded shutdown: server, in-flight requests, then `Worker.Shutdown`  |
| `Server`  | `() *http.Server` | Built once from the Config as it stands; see below                    |
| `Handler` | `() http.Handler` | Router behind cross-origin protection, counting in-flight requests; for `httptest.NewServer` |
| `Config`  | `() *Config`      | The App's Config; change it only before Start, from one goroutine     |
| `Router`  | `() *Router`      | The app's router                                                      |

Before `Start`, while the router is not sealed, every path answers 404.

A `Server()` built before `Start` (say, from a service's `Init`) takes the
bound address and timeouts at `Start` for each field the caller has not
changed, unless `Serve` has begun; a field the caller set, such as
`ReadTimeout = 0` to stream, is kept. `AppConfigUpdated` subscribers run after
that, so a field they set stays. Do not call `Server()` concurrently with
`Start`, and do not change the server's fields once it serves.

## DI Container

### Registration

Registration and resolution are generic methods (Go 1.27) on
`*ServiceContainer`, forwarded on `*Worker`. There are no package-level
`RegisterCtor` / `RegisterInstance` / `Resolve` functions.

```go
c.RegisterService[T any](ctor any, lifetime ServiceLifetime)
c.RegisterSingletonService[T any](ctor any)
c.RegisterScopedService[T any](ctor any)
c.RegisterTransientService[T any](ctor any)
c.RegisterServiceInstance[T any](val T)
c.RegisterServiceAlias[Target, Alias any]()
```

```go
c.RegisterSingletonService[*myservice.Service](myservice.New)
```

`RegisterServiceInstance` registers under `T`, the static type at the call
site, not the dynamic type of `val`. Passing an interface-typed variable makes
the value resolvable as that interface only; name `T` explicitly to control it.
Registering a type twice replaces the earlier registration, and an instance
wins over a constructor for the same type whichever came last.

`RegisterServiceAlias[Target, Alias]()` makes the registration under `Target`
resolvable as the interface `Alias` too: the same instance, `Target`'s
lifetime, one `Init` and one `Close`. Use it when consumers ask for one
service under several interfaces, instead of a second constructor, which
builds a second instance. Either order works; a missing target fails
`BuildAll` with `ErrNotRegistered`, and the captive-dependency check looks
through the alias. It panics with an error wrapping `gas.ErrInvalidAlias` if
`Alias` is not an interface, `Target` does not implement it, they are the same
type, `Alias` is already registered, `Target` is itself an alias, or `Alias` is
the target of another alias; registering a service under an existing alias
panics too. Registering the same alias again replaces its target.

Constructor signature: `func(DepA, DepB, ...) T` or `func(DepA, DepB, ...) (T, error)`

The signature is validated at registration. Registration panics with an error
wrapping `gas.ErrInvalidConstructor`, naming the constructor and the type, if
`ctor` is not a function, is variadic, returns other than one or two values,
returns a first result that is neither assignable to `T` nor an implementation
of `T` when `T` is an interface, or returns a second result that is not
`error`. Parameters are not checked here: they are the dependencies, and
whether they resolve is only knowable at `BuildAll` time.

Registering a constructor returning a concrete `*Impl` under an interface `T`
is the normal case: it implements `T` without being assignable to it.

### Resolution

```go
c.Resolve[T any]() (T, error)      // *ServiceContainer
c.MustResolve[T any]() T           // panics on failure
scope.Resolve[T any]() (T, error)  // *Scope
scope.MustResolve[T any]() T
```

Resolution is by exact type: an interface is resolvable only when it is
registered, or is an alias of a registered type. Scoped registrations cannot
be resolved from the container; resolve them from a `Scope`
(`c.NewScope(ctx)`), which also serves singletons and transients.
`MustResolve` panics with an error, `gas: resolve T: ...`, wrapping the
resolution error.

```go
svc := c.MustResolve[*myservice.Service]()
```

A service that is not active (closed by the kill switch, or being closed or
restarted) is not handed out: resolving it fails with an error wrapping
`gas.ErrServiceNotActive`, and a handler that depends on it answers 503.

### Container methods

| Method                                 | Description                                                   |
|----------------------------------------|---------------------------------------------------------------|
| `NewServiceContainer(opts ...ServiceContainerOption)` | Create new container (`WithServiceContainerServiceTimeout` sets the init deadline; negative fails `BuildAll` with `ErrInvalidOption`) |
| `BuildAll(ctx) error`                  | Init pre-registered instances, validate, eagerly build all singletons in dependency order |
| `NewScope(ctx) *Scope`                 | Create a scoped resolution context; ctx goes to scoped Inits  |
| `EachInstance(fn func(reflect.Value))` | Iterate all built singleton instances in build order          |
| `CanResolve[T any]() bool`             | Whether exactly `T` (or an alias of a registered type) is registered; builds nothing |

**Captive dependency validation:** `BuildAll(ctx)` rejects singletons that depend
on scoped or transient services, which would "capture" a short-lived instance
inside a long-lived one (`gas.ErrCaptiveDependency`).

### DI errors

Resolution and startup errors wrap a sentinel; match them with `errors.Is`
(the message text still names the types):

| Sentinel | Returned when |
|---|---|
| `gas.ErrNotRegistered` | A type, or a constructor's dependency, has no registration or instance |
| `gas.ErrCircularDependency` | Constructors depend on each other in a cycle, a constructor or `Init` resolves its own type, two goroutines' builds would wait on each other, or constructors nest deeper than 256 |
| `gas.ErrCaptiveDependency` | A singleton depends on a scoped or transient service |
| `gas.ErrScopeRequired` | A scoped service is resolved from the container instead of a `Scope` |
| `gas.ErrScopeClosed` | A scoped service is resolved from a `Scope` after its `Close` |
| `gas.ErrInvalidServiceShape` | A registered type declares `Init` or `Close` but does not implement `Service` |
| `gas.ErrServiceNotActive` | The resolved service is closed, or being closed or restarted |

Wiring mistakes **panic** at registration with an error wrapping a sentinel:
`gas.ErrInvalidConstructor` (a constructor the container cannot call),
`gas.ErrNilInstance` (a nil instance), `gas.ErrInvalidAlias` (a bad alias) and
`gas.ErrInvalidLifetime` (a transient `Service`). A registration with an
unknown `ServiceLifetime` value fails its build or resolution with an error
wrapping `gas.ErrInvalidLifetime` instead.

## Request Scopes

The App creates a DI `Scope` per HTTP request. Scoped services get a fresh
instance per request: `Init(ctx)` with the request context on first
resolution, `Close(ctx)` when the request completes (with the request context
minus its cancellation, so cleanup runs after a disconnect), newest first.
A `Scope` is safe for concurrent use; goroutines resolving one scoped type
share one build.

DI-aware handlers resolve scoped services automatically via their parameter
list. For classic `http.HandlerFunc` handlers:

```go
gas.ResolveFromRequestScope[T any](r *http.Request) (T, error)
gas.MustResolveFromRequestScope[T any](r *http.Request) T
```

For resolving multiple services, access the scope directly:

```go
scope := gas.RequestScope(r) // panics when r has no scope (not served by an App's router)
```

For non-HTTP contexts (background jobs, tests):

```go
scope := container.NewScope(ctx)
defer func() { _ = scope.Close(context.WithoutCancel(ctx)) }()
ctx = gas.WithRequestScope(ctx, scope)
```

`Scope.Close` closes the scoped services it built in reverse resolution order
and returns their errors joined; a second `Close` closes nothing.

## Router

`Router` wraps chi and tracks route, named middleware and NotFound ownership by
service. It is safe for concurrent use. Once sealed, every change builds a new
routing tree and publishes it atomically; requests in flight keep the tree
they started on.

### Registering routes

```go
router.Handle(service Service, method, path string, handler any, middleware ...Middleware)
router.NotFound(service Service, handler any)
```

`Get`, `Post`, `Put`, `Patch`, `Delete`, and `Head` take
`(service Service, path string, handler any, middleware ...Middleware)` and call
`Handle` with that method. A GET route also serves HEAD. Route middleware
applies in order, outermost first.

The owner is the `gas.Service` itself, not its name: pass `s` from inside a
service. Ownership is tracked by `service.Name()`. `nil` means "no owning
service"; such registrations are owned by the root router (`gas/router`), on
sub-routers too.

The `handler` parameter accepts an `http.HandlerFunc`, a
`func(http.ResponseWriter, *http.Request)`, or a DI-aware function (see
below). Any other shape panics with an error wrapping `gas.ErrInvalidHandler`.

A path no route matches answers 404 with code `not_found`; a path routed for
other methods only answers 405 with code `method_not_allowed` and an `Allow`
header. Both use the unified error shape. `NotFound` replaces the 404; a second
service setting it panics with `gas.ErrNotFoundHandlerSet` (its owner may set
it again, such as on restart).

### DI-Aware Handlers

Handlers declare dependencies as typed parameters. The router resolves each
from the per-request DI scope on each call.

```go
func(ctx gas.Context) error
func(ctx gas.Context, dep1 Dep1, dep2 Dep2, ...) error
```

Signature rules (panics at `Handle()` call time if violated): a function whose
first parameter is `gas.Context` and that returns exactly one `error`.

`Start` checks that every handler dependency is registered, so the app fails
fast at startup, not at request time.

The router recovers a panic anywhere in its middleware and handlers, including
`NotFound` handlers. It logs the panic with its stack and passes an error
wrapping `gas.ErrPanic` (`gas: panic in handler: <value>`) to the
`ErrorHandler`. `http.ErrAbortHandler` is panicked again, so net/http aborts
the request.

### Middleware

```go
gas.MiddlewareByName(name string) Middleware                                  // resolved from registry
gas.MiddlewareFunc(fn func(http.Handler) http.Handler) Middleware             // anonymous inline
gas.MiddlewareFuncWithName(name string, fn func(http.Handler) http.Handler) Middleware // named inline (appears in route map)

router.Register(service Service, name string, mw func(http.Handler) http.Handler) // register named middleware
router.Use(middleware ...Middleware)                                           // apply to every route
router.UseMiddlewareByName(name string)
router.UseMiddlewareFunc(fn func(http.Handler) http.Handler)
```

A `MiddlewareByName` whose name is not registered panics with
`gas.ErrUnknownMiddleware`. A top-level `Use` applies to every route,
registered before or after it. **`Use` is not owned by a service**, so a
service that calls it in `Init` applies it again when it restarts; call it
from `main` or from a service that is never restarted.

### Built-in Middleware

See `references/middleware.md` for full option signatures.

- **`RequestLogger[T Logger]`**: derives a per-request logger from T (resolved
  from the request scope) carrying `request_id`, puts it on the request context
  for `gas.LoggerFromContext`, and logs one `request` line with method, path,
  status, bytes, duration, remote address and the errors the default
  ErrorHandler handled: info below 400, warn below 500, error from 500. Make T
  a scoped type derived from the singleton `gas.Logger`
  (`RequestLogger interface{ gas.Logger }`, built with `l.With().Logger()`):
  the request logger then replaces T's instance in the scope, so handlers
  taking T get `request_id`. A singleton T is never shadowed or changed.
- **`SecurityHeaders`**: sets X-Content-Type-Options, X-Frame-Options,
  Referrer-Policy and Permissions-Policy with safe defaults.
- **`CacheControl`**: sets Cache-Control on 2xx/3xx responses for matching
  paths; never on error responses.
- **`BodyLimit(n)`**: caps request bodies; 413 `payload_too_large`.
- **`Timeout(d)`**: per-request deadline; 503 `request_timeout`. Buffers the
  response (8 MiB cap by default), so it is for bounded responses, not streams.

### Route grouping

```go
router.Group(fn func(sub *Router))              // inline group
router.Route(pattern string, fn func(sub *Router)) // pattern-scoped group
```

`Route()` may be called with the **same pattern more than once**: later calls
attach to the mount the first one created, which is what lets several
services share a prefix like `/api`. Each call's body runs in a group of its
own, so `Use()` inside one block applies only to handlers registered in that
block. Sub-routers share the top-level router's validator, form decoder and
JSON body limit.

### Kill switch

`Worker.CloseService[T](ctx)` tears down the service registered under `T` at
runtime; the App wires it to `Router.RemoveByService(service)`. `T` must be the
exact type the service was registered under (no interface matching), and the
service must have been started. Before its `Close` runs:

- Its **routes** answer 503 `service_unavailable` in the unified error shape.
- Its **named middleware** answers 503 *wherever it is referenced*, including
  on routes owned by services that are still running, and inside
  `Group`/`Route` blocks.
- Its **NotFound handler** and owned **event subscriptions** are removed.

Then `Close(ctx)` runs; a Close error is returned wrapped, and the service
counts as closed either way. `SystemServiceClosed` is emitted.

So a route guarded by a killed service's middleware stops serving:

```go
router.Register(authSvc, "require-auth", requireAuth)  // owned by authSvc

router.Route("/api", func(sub *gas.Router) {
    sub.Use(gas.MiddlewareByName("require-auth"))
    sub.Get(billingSvc, "/invoices", handler) // owned by billingSvc
})

err := worker.CloseService[*AuthService](ctx) // the type authSvc was registered under
// GET /api/invoices -> 503, even though billing is still running.
```

The middleware is **disabled, never skipped**: a teardown can never drop an
authorization check and leave the route open. Design middleware ownership with
that blast radius in mind. Ownership is only tracked for names passed to
`Register`; an inline `MiddlewareFunc` (and `MiddlewareFuncWithName`) has no
owner and survives any teardown.

`RestartService[T](ctx)` runs `Init` again on the same instance, under the init
deadline, which registers its routes, middleware and subscriptions anew. Only
what that Init registers is served: a route it no longer registers answers 404.
A failed restart removes what it had registered and leaves the service closed.
`SystemServiceInitialized` is emitted on success.

Both return, wrapped: `gas.ErrNotStarted` before `Start` succeeded,
`gas.ErrShutDown` once Shutdown began, `gas.ErrNoBuiltInstance` when no started
service is registered under `T`, `gas.ErrDuplicateServiceName` when another
started service shares its name; `CloseService` returns
`gas.ErrServiceNotActive` for a service that is not active, `RestartService`
`gas.ErrServiceAlreadyActive` for one that is not closed. Neither holds a lock
while the service's code runs.

### Deferred registration

Top-level routers start **unsealed**: `Use`, `Handle`, `Group`, `Route` and
`Register` are deferred until `Seal()`. This lets services register in any
order during `Init`. The App seals the router at `Start`, after all services
are built.

### Other Router methods

| Method                                  | Description                                                        |
|-----------------------------------------|--------------------------------------------------------------------|
| `Seal()`                                | Build and publish the tree: top-level `Use` first, then routes     |
| `RemoveByService(service Service)`      | Kill switch: 503 for the service's routes **and for its named middleware everywhere it is used**; removes its NotFound |
| `SetErrorHandler(h ErrorHandler)`       | Set error handler for every route; nil restores the default        |
| `Routes() map[string][]RegisteredRoute` | Snapshot of registered routes by owner, with named middleware      |
| `NamedMiddleware() map[string]string`   | Named middleware registry (name to owning service)                 |
| `ServeHTTP(w, req)`                     | Implements http.Handler                                            |

There is no `Mux()`: the chi router is not exposed.

## Context

`Context` is an **interface** that embeds `context.Context`. It is the first
parameter of every DI-aware handler. Because it satisfies `context.Context`, it
can be passed directly to database calls, gRPC, tracing, etc.

```go
gas.NewContext(parent context.Context, w http.ResponseWriter, r *http.Request, opts ...ContextOption) Context
```

Panics if any of parent, w, or r is nil. Options: `gas.WithValidate(v)`,
`gas.WithFormDecoder(d)`, `gas.WithMaxJSONBodyBytes(n)`,
`gas.WithMaxFormBodyBytes(n)`.

### Context methods

| Method           | Signature                      | Notes                    |
|------------------|--------------------------------|--------------------------|
| `ResponseWriter` | `() http.ResponseWriter`       |                          |
| `Request`        | `() *http.Request`             |                          |
| `JSON`           | `(status int, v any) error`    | application/json         |
| `XML`            | `(status int, v any) error`    | application/xml          |
| `HTML`           | `(status int, s string) error` | text/html                |
| `Text`           | `(status int, s string) error` | text/plain               |
| `Blob`           | `(status int, contentType string, b []byte) error` | given type; sniffed if empty |
| `NoContent`      | `() error`                     | 204                      |
| `WriteError`     | `(err error) error`            | unified error response   |
| `WriteErrorJSON` | `(err error) error`            | forces the JSON envelope |
| `Redirect`       | `(status int, url string)`     |                          |
| `Param`          | `(key string) string`          | chi.URLParam             |
| `Query`          | `(key string) string`          |                          |
| `Header`         | `(key string) string`          | request header           |
| `SetHeader`      | `(key, value string)`          | response header          |
| `BindJSON`       | `(dest any) error`             | decode + validate        |
| `BindForm`       | `(dest any) error`             | decode + validate        |
| `Validator`      | `() *validator.Validate`       | go-playground/validator  |
| `FormDecoder`    | `() *schema.Decoder`           | gorilla/schema           |
| `Principal`      | `() (Principal, error)`        | 401 `*gas.Error` if none |

**Context has no error constructors.** Build errors with the package functions
(`gas.NotFound(msg)`, `gas.BadRequest(msg)`, ...) and return them; the builders
chain off those:

```go
return gas.NotFound("user not found").WithCause(sql.ErrNoRows)
return gas.NotFound("user not found").WithDetail("id", id)
```

`BindJSON` and `BindForm` both decode and then run struct validation via
`go-playground/validator`, returning a `*gas.Error`: 400 (`invalid_json` /
`invalid_form`) for a malformed body, 413 (`payload_too_large`) when the body
is over `Server.MaxJSONBodyBytes` / `Server.MaxFormBodyBytes` (1 MiB each by
default) or a `BodyLimit`, 422 (`validation_failed`) with one `FieldError` per
violation, named by its `json` tag, else its `form` tag, else its Go name. The
underlying error stays reachable through `errors.As`. The form decoder uses
alias tag `"form"` and ignores unknown keys.

Because `Context` is an interface, tests can mock it via embedding:
```go
type mockContext struct { gas.Context }
```

## Errors and ErrorHandler

`gas.Error` is the unified error shape handlers return. Applications should not
define their own error struct.

```go
type Error struct {
	Status  int            `json:"status"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Fields  []FieldError   `json:"fields,omitempty"`
	Details map[string]any `json:"details,omitempty"`
	// unexported cause: logged, never serialized
}

type FieldError struct {
	Field   string `json:"field"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
}
```

Constructors, each returning `*Error` with the matching status and code:

| Constructor                   | Status | Code                  |
|-------------------------------|--------|-----------------------|
| `BadRequest(msg)`             | 400    | `bad_request`         |
| `Unauthorized(msg)`           | 401    | `unauthorized`        |
| `Forbidden(msg)`              | 403    | `forbidden`           |
| `NotFound(msg)`               | 404    | `not_found`           |
| `Conflict(msg)`               | 409    | `conflict`            |
| `Unprocessable(msg)`          | 422    | `validation_failed`   |
| `TooManyRequests(msg)`        | 429    | `rate_limited`        |
| `Internal(msg)`               | 500    | `internal_error`      |
| `ServiceUnavailable(msg)`     | 503    | `service_unavailable` |
| `NewError(status, code, msg)` | custom | custom                |

Codes are exported as constants: `CodeBadRequest`, `CodeInvalidJSON`,
`CodeInvalidForm`, `CodeUnauthorized`, `CodeForbidden`, `CodeNotFound`,
`CodeConflict`, `CodeValidationFailed`, `CodeRateLimited`, `CodeInternal`,
`CodeUnavailable`, `CodePayloadTooLarge`, `CodeMethodNotAllowed` and
`CodeTimeout` (`request_timeout`). Builders: `WithCause(err)`,
`WithField(field, rule, message)`, `WithDetail(key, val)`. Classify with
`gas.AsError(err) (*Error, bool)`.

These are functions, not sentinel vars: the builders mutate the receiver, so a
shared `var` would be corruptible.

Wire format is `gas.ErrorResponse`, the `{"error": {...}}` envelope, with
`Cache-Control: no-store` on every error response:

```json
{"error":{"status":404,"code":"not_found","message":"not found"}}
```

`ErrorHandler` is `func(ctx Context, err error)`. The default logs the error
and writes it with `WriteError`: an `*Error` at its own status, anything else
(raw errors, recovered panics, DI resolution failures) as a generic 500 with
code `internal_error`; a cause never reaches the client, and a status outside
100 to 599 is written as 500. With `RequestLogger` installed, the error rides
on the one `request` line; without it, the handler logs `request error` at
warn for a 4xx and error for a 5xx. An error that wraps
`ErrServiceNotActive` answers 503.

Rendering is one exported function, safe to call from middleware because it
neither logs nor touches the request scope:

```go
gas.WriteError(w http.ResponseWriter, r *http.Request, err error) error
gas.WantsJSON(r *http.Request) bool
```

A request that explicitly asks for `text/html` without `application/json` gets
plain text; everything else gets JSON. Once the response has a status or a
body, `WriteError` writes nothing and returns `gas.ErrResponseCommitted`, and
the default ErrorHandler logs the error instead, so an error never corrupts a
response already sent. Inside a handler, `ctx.WriteError(err)` writes the
negotiated response and `ctx.WriteErrorJSON(err)` forces the envelope.
Returning the error and letting the `ErrorHandler` render it is the default
path.

Override the handler via `gas.WithErrorHandler(h)` or `router.SetErrorHandler(h)`.

Other Gas modules (gas/auth, gas/database, and so on) keep their own sentinel
errors; map them at the application boundary:

```go
if errors.Is(err, auth.ErrCredentialsExpired) {
	return gas.Unauthorized("session expired").WithCause(err)
}
```

## EventBus

Typed publish/subscribe messaging between services. An event is a **type**,
not a value: embed `gas.Event[Payload]` to bind the event to its payload.
`Emit`, `Subscribe` and `SubscribeWithOwner` are generic methods on `*EventBus`
that take the event type as their only explicit type argument and infer the
payload, so a mismatched payload or handler does not compile. Subscriptions are
keyed by the event type, so two events never collide even with the same payload.

```go
// Define a typed event
type UserCreated struct{ gas.Event[UserCreatedPayload] }

// Emit (concurrent dispatch, returns *sync.WaitGroup; never nil)
bus.Emit[UserCreated](UserCreatedPayload{ID: id}).Wait()

// Subscribe: always use SubscribeWithOwner from a service so that
// CloseService can clean up subscriptions (via EventBus.RemoveByService).
// Bare Subscribe has no ownership tracking; use it only outside services.
bus.Subscribe[UserCreated](func(p UserCreatedPayload) { ... })
bus.SubscribeWithOwner[UserCreated](s, func(p UserCreatedPayload) { ... }) // s is the gas.Service
bus.RemoveByService(s)
```

Handlers run concurrently, one goroutine per subscriber per Emit; a handler
may subscribe or emit in turn. A panicking handler is recovered: the others
still run and `Wait` still returns. On a Worker's bus the panic is logged as
`event subscriber panic` with the event type, owning service and stack; on a
bus of its own the stack goes to stderr.

### System Events

| Event (type)                       | Payload                               | Fired When                                      |
|------------------------------------|---------------------------------------|-------------------------------------------------|
| `gas.SystemServiceClosed`          | `{ServiceName string}`                | Service closed via `CloseService`               |
| `gas.SystemServiceInitialized`     | `{ServiceName string}`                | Service re-initialized via `RestartService`     |
| `gas.SystemAllServicesInitialized` | `struct{}`                            | All services initialized, before migrations     |
| `gas.SystemShuttingDown`           | `struct{}`                            | Worker or App begins `Shutdown`, before any Close |
| `gas.SystemServerShuttingDown`     | `struct{}`                            | `App.Stop` begins (App only)                    |
| `gas.AppConfigUpdated`             | `{Config Config}`                     | Config bound and validated at Start (App only)  |

`Shutdown` and `Stop` wait for these subscribers, bounded by their ctx.

## Provider Interfaces (summary)

Gas defines provider interfaces in the core package; implementations live in
separate modules. See `references/providers.md` for full signatures.

| Interface          | Purpose                                | Implementing module     |
|--------------------|----------------------------------------|-------------------------|
| `DatabaseProvider` | SQL database access                    | gas/database            |
| `CacheProvider`    | Key-value caching                      | gas/cache               |
| `JobQueueProvider` | Async job/message queues               | gas/queue               |
| `EmailProvider`    | Email sending                          | gas/email               |
| `ConfigProvider`   | Configuration management               | gas/config              |
| `TemplateProvider` | Template storage/retrieval             | gas/template            |
| `Logger`           | Structured logging                     | gas/log                 |
| `MigrationManager` | Database migrations                    | gas/migrate             |
| `HealthProvider`   | Aggregate health of active services    | core (Worker satisfies) |
| `ReadyProvider`    | Aggregate readiness of active services | core (Worker satisfies) |

HTML rendering is not a core interface: `ui.Provider` lives in the gas/ui
module (see the gas-ui skill). Object storage is not either: `storage.Provider`
and `storage.Presigner` live in the gas/storage module (see the gas-storage
skill).

Services opt into health/readiness reporting by implementing `HealthReporter`
(`CheckHealth(ctx) error`) and/or `ReadyReporter` (`CheckReady(ctx) error`).
The `Worker` aggregates them concurrently with panic recovery (a panic reports
an error wrapping `ErrPanic`); resolve `HealthProvider` / `ReadyProvider` from
the container to expose a `/healthz` or `/readyz` endpoint without coupling
handlers to `*Worker`. Core registers no such endpoints.

### NopLogger

Built-in no-op logger for tests or when logging isn't needed. Its zero value
is ready to use; there is no constructor function:

```go
gas.WithServiceInstance[gas.Logger](&gas.NopLogger{})
logger := &gas.NopLogger{} // a value, e.g. in a test
```

## Config (App only)

```go
type Config struct {
	Env    Environment `json:"gasenv"` // bound from key "gasenv"; default "development"
	Server ServerSettings              // bound from key "server"
}

type ServerSettings struct {
	Host              string        // default "0.0.0.0"; IP or RFC 1123 hostname
	Port              int           // default 8080; 0 to 65535, 0 picks a free port
	ReadTimeout       time.Duration // default 5s; >= 0, 0 = no limit (streaming)
	ReadHeaderTimeout time.Duration // default 5s; must be positive
	WriteTimeout      time.Duration // default 10s; >= 0, 0 = no limit (streaming)
	IdleTimeout       time.Duration // default 2m; >= 0, but positive when ReadTimeout is 0
	ShutdownTimeout   time.Duration // default 30s; must be positive
	MaxJSONBodyBytes  int64         // default 1 MiB; BindJSON's cap, must be positive
	MaxFormBodyBytes  int64         // default 1 MiB; BindForm's cap, must be positive
}

gas.DefaultConfig() *Config
```

`gas.Environment` has the constants `EnvDevelopment`, `EnvTesting`,
`EnvStaging`, `EnvProduction` and the helpers `IsDevelopment`,
`IsDevelopmentLike` (development or testing), `IsStaging`, `IsTesting`,
`IsProduction`, `IsProductionLike` (production or staging). There is no
`WithGasEnv` embed: `Env` is a plain field.

At `Start` the App binds its Config from the registered `ConfigProvider`, when
there is one, and **always validates it**. `Env` comes from the key `gasenv`,
where the config module's `EnvExtension` stores what it resolved from
`GAS_ENV`; it must be one of the allowed environments
(`WithAllowedEnvironments`). A broken rule fails `Start` with an error wrapping
`gas.ErrInvalidConfig` that names the key and the rule, never the value, such
as `building *gas.Config: gas: invalid config: server.port must be 0 to 65535`.
There is no `Config.Validate`. Change `App.Config()` only before `Start`.

Worker does not have a `Config` struct. Individual services bind their own
configuration from `gas.ConfigProvider` during `Init`; an App's services may
also take the bound `*gas.Config` as a dependency.

## Writing a Service (Complete Example)

```go
package myservice

import (
	"context"
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

func (s *Service) Init(context.Context) error {
	// DI-aware handler: db is resolved per request from the request scope.
	s.router.Get(s, "/hello", s.handleHello)

	// A classic http.HandlerFunc works too.
	s.router.Get(s, "/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Subscribe with an owner, so CloseService removes the subscription.
	s.bus.SubscribeWithOwner[gas.SystemServiceClosed](s,
		func(payload gas.SystemServiceClosedPayload) {
			// react to another service being closed
		})

	return nil
}

func (s *Service) handleHello(ctx gas.Context, db gas.DatabaseProvider) error {
	if err := db.Ping(ctx); err != nil {
		return gas.ServiceUnavailable("database unavailable").WithCause(err)
	}
	return ctx.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Service) Close(context.Context) error { return nil }
```

```go
app := gas.NewApp(
	gas.WithSingletonService[*myservice.Service](myservice.New),
)
if err := app.Run(); err != nil {
	log.Fatal(err)
}
```
