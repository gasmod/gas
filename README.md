# Gas

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas.svg)](https://pkg.go.dev/github.com/gasmod/gas) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

**Gas is a modular Go framework for building micro-SaaS applications.** It provides the plumbing every product
needs (dependency injection, routing, middleware, events, migrations, and service lifecycle management) so you
can spend your time on business logic instead of rebuilding the same wiring for each project.

This repository is a monorepo. `github.com/gasmod/gas` is the core package documented below, and each official
module lives beside it in its own directory as a separate Go module. Take only the pieces you need: an app that
just serves HTTP depends on core alone.

## Quick start

```bash
go get github.com/gasmod/gas
```

```go
package main

import (
	"log"
	"net/http"

	"github.com/gasmod/gas"
)

func main() {
	app := gas.NewApp()

	app.Router().Handle("", http.MethodGet, "/", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, "Hello, World!")
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
```

```bash
go run .
# listening on 0.0.0.0:8080
```

That is a complete Gas app. From here you grow it by writing [services](#writing-a-service) and registering
modules the same way, letting the DI container wire them together.

## Modules

Each module is its own Go module, so `go get` pulls in only what you actually use.

| Module                             | Import path                      | What it gives you                                                                       |
|------------------------------------|----------------------------------|-----------------------------------------------------------------------------------------|
| **core** (this directory)          | `github.com/gasmod/gas`          | App and Worker lifecycle, DI container, router, middleware, events, provider interfaces |
| [**auth**](auth/README.md)         | `github.com/gasmod/gas/auth`     | JWT, server-side sessions, API keys, and single-use tokens                              |
| [**cache**](cache/README.md)       | `github.com/gasmod/gas/cache`    | Key-value caching, in-memory or Valkey (Redis-compatible)                               |
| [**config**](config/README.md)     | `github.com/gasmod/gas/config`   | Config from env vars, JSON, `.env`, and AWS Secrets Manager, bound to structs           |
| [**database**](database/README.md) | `github.com/gasmod/gas/database` | `database/sql` and native pgx pools, transaction helpers, sqlc-friendly                 |
| [**email**](email/README.md)       | `github.com/gasmod/gas/email`    | Transactional email over AWS SES, including templated sends                             |
| [**log**](log/README.md)           | `github.com/gasmod/gas/log`      | Structured logging (zerolog or slog) and HTTP/OTLP log shipping                         |
| [**migrate**](migrate/README.md)   | `github.com/gasmod/gas/migrate`  | Service-owned database migrations with dirty-state detection and rollback               |
| [**queue**](queue/README.md)       | `github.com/gasmod/gas/queue`    | Job queues backed by AWS SQS                                                            |
| [**storage**](storage/README.md)   | `github.com/gasmod/gas/storage`  | Object storage on S3 and S3-compatible services                                         |
| [**template**](template/README.md) | `github.com/gasmod/gas/template` | Template storage: memory, directory, embedded FS, database, or composite                |
| [**ui**](ui/README.md)             | `github.com/gasmod/gas/ui`       | HTML rendering with layouts and partials, static files, HTMX fragments                  |

No module imports another module's service package. They meet through the [provider
interfaces](#provider-interfaces) defined in core, so any of them can be replaced by your own implementation or
a test mock without the others noticing. The one shared dependency is `gas/config`: core and most modules embed
its `gasenv` extension for environment detection, and bind their settings through `gas.ConfigProvider`.

**Wiring them up.** A module's `New` returns a *constructor*, not a service: the DI container calls it with the
dependencies it declares. Most modules ask for `gas.ConfigProvider` and `gas.Logger`, and the database-backed
ones also ask for `gas.DatabaseProvider` and `gas.MigrationManager`. Every dependency must be registered or
startup fails with `no registration for <type>`, which is deliberate: a typo in your wiring should not surface
as a nil pointer on the first request. Each module README lists exactly what its constructors need.

```go
cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
if err := cfg.Load(); err != nil {
	log.Fatal(err)
}

app := gas.NewApp(
	// Infrastructure every module draws on.
	gas.WithServiceInstance[gas.ConfigProvider](cfg),
	gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

	// Modules, wired by interface so your services depend on the contract.
	gas.WithSingletonService[gas.DatabaseProvider](database.New()),
	gas.WithSingletonService[gas.MigrationManager](migrate.New()),
	gas.WithSingletonService[gas.CacheProvider](cachemem.New()),

	// Your own services.
	gas.WithSingletonService[*billing.Service](billing.New),
)
```

### Versioning

All modules share a single version and are released together. A release tags the root module `vX.Y.Z` and each
submodule `<dir>/vX.Y.Z`, which is what `go get github.com/gasmod/gas/auth@vX.Y.Z` resolves against. Mixing
versions across modules is not supported, so upgrade them together.

Gas is pre-1.0: minor versions may contain breaking changes. See the [CHANGELOG](CHANGELOG.md).

## Examples

Five runnable applications live in [`example/`](example/README.md), from a bare hello world up to a full API
server using auth, database, storage, queue, email, and cache together.

```bash
cd example/hello-world && go run ./cmd
```

## Documentation

- **Core reference** (below): [Key Concepts](#key-concepts) · [App and Worker](#app-lifecycle-http) ·
  [Services and DI](#registering-services) · [Routing](#routing) · [Handlers](#di-aware-handlers) ·
  [Context](#context) · [Errors](#error-handling) · [Middleware](#middleware) · [Events](#events) ·
  [Kill-Switch](#kill-switch) · [CSRF](#csrf-protection) · [Request Scopes](#request-scopes) ·
  [Providers](#provider-interfaces) · [Auth](#authentication--authorization) · [Configuration](#configuration)
- **Module reference**: each module's README, linked in the table above.
- **API reference**: [pkg.go.dev/github.com/gasmod/gas](https://pkg.go.dev/github.com/gasmod/gas)

## Key Concepts

**Services** are self-contained units of functionality (auth, billing, etc.) that implement a simple three-method
interface:

```go
type Service interface {
	Name() string // Unique identifier, e.g. "gas/auth"
	Init() error  // Register routes, middleware, subscriptions
	Close() error // Cleanup internal resources
}
```

`Init` and `Close` are the managed lifecycle, so a registered type that declares either one must implement all three.
Anything short of that is rejected at startup with an error naming the missing or mis-typed methods, rather than
registering cleanly and then never being initialized or closed. A type declaring none of them (or only `Name()`) is an
ordinary dependency and is unaffected.

**Dependency injection.** Services are registered with the DI container via constructors. The container resolves
dependencies automatically, performs topological sorting, validates lifetime rules, and calls `Init()` on every
`Service` it holds — including pre-built instances passed to `WithServiceInstance`, which are initialized before
anything the container constructs.

**Three lifetimes:**
- **Singleton** — created once, shared everywhere. `Init()` is called during `BuildAll()`.
- **Scoped** — created once per `Scope`. `Init()` is called on first resolution within the scope.
- **Transient** — created fresh on every resolution. **Cannot implement `Service`** (registration panics).

**Infrastructure flows inward.** Services never import each other. They receive shared infrastructure (router, event bus,
providers) through constructor injection and communicate via events and provider interfaces.

**Ownership tracking.** Every route, middleware, and event subscription is tagged with its owning service, enabling
teardown of a single service at runtime. Teardown follows ownership rather than route boundaries: killing a service
also disables the named middleware it registered wherever that middleware is used, so routes owned by other services
can be affected. See [Kill-Switch](#kill-switch).

## Usage

### App Lifecycle (HTTP)

```go
package main

import "github.com/gasmod/gas"

func main() {
	app := gas.NewApp(
		gas.WithSingletonService[*auth.Service](auth.New),
		gas.WithSingletonService[*billing.Service](billing.New),
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

`Run()` initializes all services (via the DI container), runs pending migrations, executes any registered ready hooks,
starts the HTTP server, and waits for a shutdown signal. On shutdown, services are closed in reverse init order.

### Worker Lifecycle (non-HTTP)

For non-HTTP environments (AWS Lambda, background workers, CLI tools), use `Worker` instead of `App`. It provides
the same DI container, service lifecycle, events, and migration support without routing or an HTTP server.

```go
w := gas.NewWorker(
	gas.WithSingletonService[*database.Service](database.New()),
	gas.WithSingletonService[*myservice.Service](myservice.New),
)

// Start initializes services, runs migrations, and executes ready hooks.
if err := w.Start(); err != nil {
	log.Fatal(err)
}
defer w.Shutdown()

// Use the DI container directly — e.g. in a Lambda handler.
lambda.Start(func(ctx context.Context, event MyEvent) error {
	scope := w.ServiceContainer().NewScope()
	defer scope.Close()
	svc := gas.MustResolve[*myservice.Service](scope)
	return svc.Handle(ctx, event)
})
```

For long-running worker processes that should block until a shutdown signal:

```go
w := gas.NewWorker(
	gas.WithSingletonService[*myservice.Service](myservice.New),
)
if err := w.Run(); err != nil { // Start + block on SIGINT/SIGTERM + Shutdown
	log.Fatal(err)
}
```

`App` embeds `Worker` — all DI registration options (`WithSingletonService`, `WithService`, `WithReadyFunc`, etc.)
work with both `NewApp` and `NewWorker`. HTTP-specific options (`WithErrorHandler`, `WithTrustedOrigin`, etc.) only
work with `NewApp`.

### Registering Services

Register constructor-based services with a lifetime:

```go
gas.WithService[*auth.Service](auth.New, gas.ServiceLifetimeSingleton)
```

Register pre-built instances (treated as singletons):

```go
gas.WithServiceInstance[*MyService](myInstance)
```

Pre-built means pre-constructed, not pre-initialized. If the value implements
`gas.Service` the container still calls `Init()` on it at startup and `Close()`
at shutdown, so don't call `Init()` yourself before registering.

Convenience shorthands that infer the lifetime from the function name:

```go
gas.WithSingletonService[*auth.Service](auth.New)   // equivalent to WithService(ctor, ServiceLifetimeSingleton)
gas.WithScopedService[*RequestLog](NewRequestLog)    // equivalent to WithService(ctor, ServiceLifetimeScoped)
gas.WithTransientService[*Nonce](NewNonce)           // equivalent to WithService(ctor, ServiceLifetimeTransient)
```

#### Register under the type your consumers ask for

The type parameter is the key the container stores the service under, and lookups are by **exact type**. The
container deliberately does not search its registrations for something that happens to satisfy an interface,
because that search is ambiguous the moment two services implement the same interface. So a service registered
under `*database.Service` does not resolve a dependency declared as `gas.DatabaseProvider`:

```go
// Does NOT work: registered as *database.Service, but migrate asks for gas.DatabaseProvider.
gas.WithSingletonService[*database.Service](database.New())
gas.WithSingletonService[*migrate.Service](migrate.New())
// building *migrate.Service: resolving dep gas.DatabaseProvider for *migrate.Service:
//   no registration for gas.DatabaseProvider
```

```go
// Works: registered under the interface its consumers declare.
gas.WithSingletonService[gas.DatabaseProvider](database.New())
gas.WithSingletonService[*migrate.Service](migrate.New())
```

The mismatch is caught at startup, not on the first request. As a rule, register infrastructure under its
[provider interface](#provider-interfaces) and your own services under their concrete type. To reach a backend
feature the interface does not expose, type-assert the provider you were injected rather than registering the
concrete type as well; each module README shows the assertion for its backend.

#### Registering by type token

When the type isn't known at compile time, register against the container (or
the Worker, which forwards to it) using a type token built with `gas.TypePtr`:

```go
c := w.ServiceContainer()
c.RegisterSingletonService(gas.TypePtr[*auth.Service](), auth.New)
c.RegisterScopedService(gas.TypePtr[*RequestLog](), NewRequestLog)
c.RegisterTransientService(gas.TypePtr[*Nonce](), NewNonce)
c.RegisterService(gas.TypePtr[*auth.Service](), auth.New, gas.ServiceLifetimeSingleton)
```

The token is dereferenced once, so `TypePtr[*T]()` registers under `*T`.
`RegisterServiceInstance` is the exception — it uses the value's dynamic type,
so pass the value itself:

```go
c.RegisterServiceInstance(myInstance)
```

Resolve the same way, getting back an `any` you assert:

```go
svc := c.MustResolve(gas.TypePtr[*auth.Service]()).(*auth.Service)
svc, err := c.Resolve(gas.TypePtr[*auth.Service]())
```

These share registrations with the generic `gas.Resolve[T]` / `gas.MustResolve[T]`
helpers, so both forms return the same instances.

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

`gas.Context` is an interface that embeds `context.Context` and wraps `http.ResponseWriter` and `*http.Request` with
convenience methods. Because it satisfies `context.Context`, you can pass it directly to database calls, gRPC clients,
tracing libraries, and any other API that accepts a `context.Context` — no unwrapping needed.

Create one with `NewContext`:

```go
ctx := gas.NewContext(parent, w, r, opts ...gas.ContextOption) // parent is a context.Context
```

| Method                                 | Description                                          |
|----------------------------------------|------------------------------------------------------|
| `ResponseWriter() http.ResponseWriter` | Underlying response writer                           |
| `Request() *http.Request`              | Underlying request                                   |
| `JSON(status int, v any) error`        | Write JSON response (`application/json`)             |
| `XML(status int, v any) error`         | Write XML response (`application/xml`)               |
| `RSS(status int, v any) error`         | Write RSS XML response (`application/rss+xml`)       |
| `HTML(status int, s string) error`     | Write HTML response (`text/html`)                    |
| `Text(status int, s string) error`     | Write plain-text response (`text/plain`)             |
| `NoContent() error`                    | Write 204 No Content                                 |
| `Error(err error) error`               | Write the unified error response (negotiated)        |
| `ErrorJSON(err error) error`           | Write the unified error response, always JSON        |
| `Redirect(status int, url string)`     | Send HTTP redirect                                   |
| `Param(key string) string`             | URL path parameter (chi.URLParam)                    |
| `Query(key string) string`             | Query string parameter                               |
| `Header(key string) string`            | Request header value                                 |
| `SetHeader(key, value string)`         | Set response header                                  |
| `BindJSON(dest any) error`             | Decode JSON request body into dest and auto-validate |
| `BindForm(dest any) error`             | Decode form body into dest and auto-validate         |
| `Validator() *validator.Validate`      | Access the validator instance                        |
| `FormDecoder() *schema.Decoder`        | Access the form decoder instance                     |

`BindForm` uses the `"form"` struct tag for field mapping and has `IgnoreUnknownKeys` enabled.
Both `BindJSON` and `BindForm` automatically validate the decoded struct using
[go-playground/validator](https://github.com/go-playground/validator).

Since `gas.Context` is an interface, you can mock it in tests without an HTTP server:

```go
type mockContext struct {
	gas.Context // embed for default implementations
	// override only the methods your test needs
}
```

### Error Handling

Handlers return `error`. Return a `gas.Error` and core renders it at the right status with a stable JSON shape,
so applications do not each define their own error struct:

```go
func (s *Service) handleGetUser(ctx gas.Context) error {
	user, err := s.repo.Find(ctx, ctx.Param("id"))
	if errors.Is(err, sql.ErrNoRows) {
		return gas.NotFound("user not found").WithCause(err)
	}
	if err != nil {
		return err // renders as a generic 500; the real error goes to the log
	}
	return ctx.JSON(http.StatusOK, user)
}
```

```json
{"error":{"code":"not_found","message":"user not found"}}
```

| Constructor                       | Status | Code                |
|-----------------------------------|--------|---------------------|
| `gas.BadRequest(msg)`             | 400    | `bad_request`       |
| `gas.Unauthorized(msg)`           | 401    | `unauthorized`      |
| `gas.Forbidden(msg)`              | 403    | `forbidden`         |
| `gas.NotFound(msg)`               | 404    | `not_found`         |
| `gas.Conflict(msg)`               | 409    | `conflict`          |
| `gas.Unprocessable(msg)`          | 422    | `validation_failed` |
| `gas.TooManyRequests(msg)`        | 429    | `rate_limited`      |
| `gas.Internal(msg)`               | 500    | `internal_error`    |
| `gas.ServiceUnavailable(msg)`     | 503    | `service_unavailable` |
| `gas.NewError(status, code, msg)` | custom | custom              |

Enrich with `WithCause(err)`, `WithField(field, rule, message)`, and `WithDetail(key, val)`. Classify an
incoming error with `gas.AsError(err) (*gas.Error, bool)`. A wrapped cause is logged and stays reachable
through `errors.Is` and `errors.As`, but is **never** serialized into a response.

Binding produces the same shape for free — `return err` straight out of `ctx.BindJSON` or `ctx.BindForm` yields a
400 (`invalid_json` / `invalid_form`) when the body itself is malformed, or a 422 (`validation_failed`) with
per-field detail, named by the JSON tag the client sent:

```json
{"error":{"code":"validation_failed","message":"request validation failed",
  "fields":[{"field":"email","rule":"email","message":"must be a valid email address"}]}}
```

Anything that is not a `gas.Error` — a raw error, a handler panic, a DI resolution failure — collapses to a
generic 500 with the original logged only. Errors at status 500 and above log at error level; the rest log at warn.

Requests that explicitly prefer `text/html` without also accepting JSON get a plain-text body instead, so
server-rendered apps stay readable without configuring anything.

**From middleware:** `gas.WriteError(w, r, err)` writes the same response from any `http.Handler`. It does not
log and does not touch the request scope, so it is safe in middleware that runs before the scope exists (a
`WithCSRFDenyHandler`, for example). `gas.WantsJSON(r)` exposes the same negotiation.

**Panic recovery:** DI-aware handlers automatically recover from panics. When a handler panics, the stack trace is
written to stderr, the error is logged via the request-scoped `Logger` (if available), and the panic is routed
through the `ErrorHandler` as a `gas: handler panic: <value>` error. `http.ErrAbortHandler` is re-panicked to
preserve `net/http`'s connection-teardown behavior.

Override the handler with `WithErrorHandler` — for example, to render HTML for browsers and the envelope for
API clients:

```go
app := gas.NewApp(
	gas.WithErrorHandler(func(ctx gas.Context, err error) {
		if gas.WantsJSON(ctx.Request()) {
			_ = ctx.Error(err)
			return
		}
		_ = ctx.HTML(http.StatusInternalServerError, renderErrorPage(err))
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

#### Built-in Middleware

Gas ships with ready-to-use middleware for common concerns:

**RequestLogger** — logs every HTTP request/response with method, path, status, bytes, duration, and remote address.
Responses with status >= 400 are logged at error level. Requires a scoped `Logger` in the DI container. If chi's
`RequestID` middleware is mounted upstream, the request ID is automatically attached to the logger's base fields.

```go
router.UseMiddlewareFunc(gas.RequestLogger[*mylogger.Logger]())

// Disable automatic request ID attachment:
router.UseMiddlewareFunc(gas.RequestLogger[*mylogger.Logger](
	gas.WithRequestLoggerAppendRequestID(false),
))
```

**SecurityHeaders** — sets common security response headers with secure defaults. Each header can be overridden or
disabled (by passing an empty string). Headers with no default are only emitted when explicitly configured:

| Header                       | Default                                    |
|------------------------------|--------------------------------------------|
| X-Content-Type-Options       | `nosniff`                                  |
| X-Frame-Options              | `DENY`                                     |
| Referrer-Policy              | `strict-origin-when-cross-origin`          |
| Permissions-Policy           | `camera=(), microphone=(), geolocation=()` |
| Content-Security-Policy      | _(none — application-specific)_            |
| Strict-Transport-Security    | _(none — enable once fully on HTTPS)_      |
| Cross-Origin-Opener-Policy   | _(none)_                                   |
| Cross-Origin-Resource-Policy | _(none)_                                   |

```go
// Secure defaults — no options needed:
router.UseMiddlewareFunc(gas.SecurityHeaders())

// Override a specific header:
router.UseMiddlewareFunc(gas.SecurityHeaders(
	gas.WithSecurityHeadersFrameOptions("SAMEORIGIN"),
))
```

**CacheControl** — sets the `Cache-Control` response header based on path matching rules and configured directives.
If no path filters are specified, the header applies to all requests. If no directives are specified, the middleware
passes through without setting any header.

```go
// Cache static assets for 1 year:
router.UseMiddlewareFunc(gas.CacheControl(
	gas.WithCacheControlPathPrefix("/static/"),
	gas.WithCacheControlPublic(),
	gas.WithCacheControlMaxAge(365 * 24 * time.Hour),
	gas.WithCacheControlImmutable(),
))

// Disable caching for API responses:
router.UseMiddlewareFunc(gas.CacheControl(
	gas.WithCacheControlPathPrefix("/api/"),
	gas.WithCacheControlNoStore(),
))
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

Several services can call `Route()` with the same pattern; the later calls attach to the mount created by the first
instead of panicking. Each call's body runs in its own group, so middleware added with `Use()` inside one block
applies only to the handlers registered in that block:

```go
// in auth.Service.Init()
router.Route("/api", func(sub *gas.Router) {
	sub.Use(gas.MiddlewareByName("require-auth"))
	sub.Handle("auth", "GET", "/me", s.me) // guarded
})

// in billing.Service.Init() — same "/api" mount, unaffected by auth's Use()
router.Route("/api", func(sub *gas.Router) {
	sub.Handle("billing", "GET", "/plans", s.plans) // not guarded
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
app.CloseService("auth")   // everything "auth" registered returns 503; subscriptions removed; Close() called
app.RestartService("auth") // re-initializes the service and re-arms what it registered
```

Everything the service registered is replaced with a static 503 `service_unavailable` response in the unified error
shape. That means its routes, and also **every named middleware it registered, wherever that middleware is
referenced** — including on routes owned by services that are still running, and including references nested inside
`Group`/`Route` blocks:

```go
router.Register("auth", "require-auth", requireAuth) // owned by "auth"

router.Route("/api", func(sub *gas.Router) {
	sub.Use(gas.MiddlewareByName("require-auth"))
	sub.Handle("billing", "GET", "/invoices", handler) // owned by "billing"
})

app.CloseService("auth")
// GET /api/invoices -> 503, even though "billing" is still running.
```

The middleware is disabled, never skipped, so a teardown can never drop an authorization check and leave the route
open. Design middleware ownership with that blast radius in mind: registering a widely used middleware under a service
that gets killed takes every consumer down with it. Ownership is only tracked for names passed to `Register` — an
inline `gas.MiddlewareFunc` has no owner and survives any teardown.

Other services can react to closures:

```go
gas.SubscribeWithOwner(bus, s.Name(), gas.SystemServiceClosed, func(data gas.SystemServiceClosedPayload) {
	// enter degraded mode if data.ServiceName was a dependency
})
```

### Ready Hooks

Register functions that run after all services are initialized and migrations have completed, but before the HTTP
server starts accepting traffic (App) or before `Start` returns (Worker). Use this for data seeding or any other
startup work that requires a live DI container:

```go
app := gas.NewApp(
	gas.WithSingletonService[*DB](NewDB),
	gas.WithReadyFunc(func(sc *gas.ServiceContainer) error {
		db := gas.MustResolve[*DB](sc)
		return seed.Run(db)
	}),
)
```

Multiple hooks are called in registration order. Any error aborts startup before the server starts.

### CSRF Protection

Gas enables cross-origin request protection by default using Go's
[`net/http.CrossOriginProtection`](https://pkg.go.dev/net/http#CrossOriginProtection). It rejects non-safe
cross-origin browser requests (POST, PUT, PATCH, DELETE, etc.) that originate from untrusted origins.
Safe methods (GET, HEAD, OPTIONS) are always allowed. Requests without `Sec-Fetch-Site` or `Origin` headers
(e.g. non-browser clients, curl) are also allowed.

No configuration is required for same-origin apps. For apps that receive cross-origin requests from known
front-ends, add trusted origins:

```go
app := gas.NewApp(
	gas.WithTrustedOrigin("https://app.example.com"),
	gas.WithTrustedOrigin("https://admin.example.com"),
)
```

To bypass protection for specific paths (e.g. webhook receivers that validate their own signatures):

```go
app := gas.NewApp(
	gas.WithCSRFInsecureBypassPattern("/webhooks/stripe"),
)
```

To customize the response returned for rejected requests (default: 403 Forbidden):

```go
app := gas.NewApp(
	gas.WithCSRFDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "cross-origin request denied", http.StatusForbidden)
	})),
)
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
txLog, err := gas.ResolveFromRequestScope[*TransactionLog](r)
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

svc, err := gas.Resolve[*MyScopedService](scope)
```

To inject a scope into a `context.Context` (useful in tests or background jobs that call code expecting a request
scope):

```go
scope := container.NewScope()
defer scope.Close()

ctx := gas.WithRequestScope(context.Background(), scope)
// code that calls gas.RequestScope(r) on a request built from ctx will find this scope
```

### Provider Interfaces

Services depend on interfaces, not implementations. Gas defines common providers that any service can accept:

| Interface          | Methods                                                                     |
|--------------------|-----------------------------------------------------------------------------|
| `DatabaseProvider` | `DB`, `Driver`, `Ping`, `Query`, `Exec`, `BeginTx`, `WithTx`                |
| `CacheProvider`    | `Get`, `Set`, `Delete`, `Exists`                                            |
| `JobQueueProvider` | `Enqueue`, `Dequeue`, `Ack`, `Nack`                                         |
| `EmailProvider`    | `Send`, `SendFromTemplate`                                                  |
| `StorageProvider`  | `Upload`, `Download`, `Delete`, `Head`, `PresignDownloadURL`, `PresignUploadURL` (all accept `...StorageOption`) |
| `ConfigProvider`   | `SetDefault`, `SetDefaults`, `Set`, `Bind`, `Get`, `Find`, `Values`         |
| `TemplateProvider` | `Get`, `List`, `Register`, `RegisterFS`                                     |
| `UIProvider`       | `Render`, `RenderWithStatus`, `RenderFragment`, `RegisterFuncs`             |
| `HealthProvider`   | `CheckHealth(ctx) map[string]error` — Worker satisfies it                   |
| `ReadyProvider`    | `CheckReady(ctx) map[string]error` — Worker satisfies it                    |
| `HealthReporter`   | `CheckHealth(ctx) error` — opt-in per service                               |
| `ReadyReporter`    | `CheckReady(ctx) error` — opt-in per service                                |
| `Logger`           | `Trace`, `Debug`, `Info`, `Warn`, `Error`, `With`, `SetBaseFields`, `Flush` |
| `MigrationManager` | `Register`, `RegisterSlice`, `RegisterFS`, `RunPending`, `Down`             |
| `Authenticator`    | `Authenticate`                                                              |
| `Authorizer`       | `Authorize`                                                                 |
| `PrincipalRevoker` | `Revoke`, `RevokeAll`, `RevokeAllByScheme`                                  |

### Authentication & Authorization

Gas defines three separate interfaces for auth concerns — each can be implemented independently:

- **`Authenticator`** — extracts a `Principal` from an `*http.Request` (JWT, session, API key, etc.)
- **`Authorizer`** — checks whether a `Principal` can perform an action on a resource
- **`PrincipalRevoker`** — invalidates credentials (single, all for a subject, or all by scheme)

A `Principal` represents an authenticated identity:

```go
type Principal interface {
	Subject() string        // stable user identifier
	Scheme() string         // auth method: "jwt", "session", "apikey", etc.
	CredentialID() string   // specific credential: session ID, JWT jti, API key ID
	Metadata() PrincipalMetadata
}
```

Store and retrieve a `Principal` in context:

```go
ctx = gas.WithPrincipal(ctx, principal)
p := gas.PrincipalFromContext(ctx)    // returns nil if absent
p := gas.MustPrincipalFromContext(ctx) // panics if absent
```

Use `MetadataValue` for type-safe metadata access:

```go
if role, ok := gas.MetadataValue[string](p.Metadata(), "role"); ok {
	// ...
}
```

`BasePrincipalMetadata` is a ready-to-use `map[string]any` implementation of `PrincipalMetadata`.

#### Logger context helpers

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

	gas.SubscribeWithOwner(s.bus, s.Name(), gas.SystemServiceClosed,
		func(payload gas.SystemServiceClosedPayload) {
			// react to another service being closed, e.g. enter degraded mode
		})

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
	gas.WithSingletonService[*myservice.Service](myservice.New),
)
```

## System Events

| Event                              | Payload Type                             | Fired When                                          |
|------------------------------------|------------------------------------------|-----------------------------------------------------|
| `gas.SystemServiceClosed`          | `SystemServiceClosedPayload`             | A service is killed via `CloseService`              |
| `gas.SystemServiceInitialized`     | `SystemServiceInitializedPayload`        | A service finishes `Init` (including restart)       |
| `gas.SystemAllServicesInitialized` | `SystemAllServicesInitializedPayload`    | All services have been successfully initialized     |
| `gas.SystemShuttingDown`           | `SystemShuttingDownPayload`              | Worker or App begins shutdown (always fires)        |
| `gas.SystemServerShuttingDown`     | `SystemServerShuttingDownPayload`        | HTTP server begins graceful shutdown (App only)     |
| `gas.AppConfigUpdated`             | `AppConfigUpdatedPayload`                | App config is updated after binding (App only)      |

## Configuration

`gas.DefaultConfig()` returns a `*Config` with sensible defaults. Pass a custom config via `WithServiceInstance`:

```go
cfg := gas.DefaultConfig()
cfg.Server.Port = 9090

app := gas.NewApp(
	gas.WithServiceInstance[*gas.Config](cfg),
)
```

### Config fields

`Config` embeds `env.WithGasEnv` (from gasenv) for environment detection, and holds a `Server ServerSettings` sub-struct.

| Field                    | Default    | Description                                              |
|--------------------------|------------|----------------------------------------------------------|
| `Server.Host`            | `0.0.0.0`  | Hostname or IP address to bind                           |
| `Server.Port`            | `8080`     | TCP port to listen on                                    |
| `Server.ReadTimeout`     | `5s`       | Maximum duration for reading the entire request          |
| `Server.WriteTimeout`    | `10s`      | Maximum duration before timing out response writes       |
| `Server.IdleTimeout`     | `2m`       | Maximum idle time between keep-alive requests            |
| `Server.ShutdownTimeout` | `30s`      | How long to wait for in-flight requests during shutdown  |

`Config.Validate()` checks that `Server.Host` is a valid IP or resolvable hostname.

## Worker Methods

| Method                             | Returns                     | Description                                           |
|------------------------------------|-----------------------------|-------------------------------------------------------|
| `w.Start()`                        | `error`                     | InitServices → migrations → ready hooks (non-blocking)|
| `w.InitServices()`                 | `error`                     | Build singletons, collect services, emit init event   |
| `w.Shutdown()`                     | `error`                     | Emit shutdown event, close services in reverse order   |
| `w.Run()`                          | `error`                     | Start + block on signal + Shutdown                    |
| `w.EventBus()`                     | `*EventBus`                 |                                                       |
| `w.ServiceContainer()`             | `*ServiceContainer`         |                                                       |
| `w.MigrationManager()`             | `MigrationManager` (or nil) |                                                       |
| `w.ConfigProvider()`               | `ConfigProvider` (or nil)   |                                                       |
| `w.RegisterService(i, ctor, lifetime)` | —                       | Register by type token (see [Registering Services](#registering-services)) |
| `w.RegisterSingletonService(i, ctor)` | —                        | Same, lifetime fixed to singleton                     |
| `w.RegisterScopedService(i, ctor)` | —                           | Same, lifetime fixed to scoped                        |
| `w.RegisterTransientService(i, ctor)` | —                        | Same, lifetime fixed to transient                     |
| `w.RegisterServiceInstance(val)`   | —                           | Register a pre-built value under its dynamic type     |
| `w.ActiveServices()`               | `[]string`                  |                                                       |
| `w.CloseService(name)`             | `error`                     | Kill-switch for a single service                      |
| `w.RestartService(name)`           | `error`                     | Re-initialize a previously closed service             |
| `w.CheckHealth(ctx)`               | `map[string]error`          | Concurrently polls all active `HealthReporter` services; satisfies `HealthProvider` |
| `w.CheckReady(ctx)`                | `map[string]error`          | Concurrently polls all active `ReadyReporter` services; satisfies `ReadyProvider` |

## App Methods

`App` embeds `Worker`, so all Worker methods are available. `App` overrides `Start` to also bind configuration,
and adds:

| Method           | Returns         | Description                                                                     |
|------------------|-----------------|---------------------------------------------------------------------------------|
| `app.Run()`      | `error`         | Start + Serve + block on SIGINT/SIGTERM + Stop. The usual entry point.           |
| `app.Start()`    | `error`         | Worker.Start (services, migrations, ready hooks) plus config binding. Non-blocking. |
| `app.Serve()`    | `error`         | Start the HTTP server and block. A clean shutdown returns nil.                   |
| `app.Stop()`     | `error`         | Graceful HTTP shutdown within `ShutdownTimeout`, then Worker.Shutdown.           |
| `app.Router()`   | `*Router`       | The App's router.                                                                |
| `app.Config()`   | `*Config`       | The App's config.                                                                |
| `app.Server()`   | `*http.Server`  | Built on first call from the current Config, then cached.                        |
| `app.Handler()`  | `http.Handler`  | The router behind CSRF protection. Use it with `httptest` or your own listener.  |

Splitting `Run` into `Start` / `Serve` / `Stop` is what lets tests drive an App without binding a port:

```go
app := myapp.New()
if err := app.Start(); err != nil {
	t.Fatal(err)
}
defer app.Stop()

srv := httptest.NewServer(app.Handler())
defer srv.Close()
```

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the workflow: conventional
commits, signed-off commits (DCO), tests with every change, and `make lint`.

Working on the repo itself:

```bash
make test-all   # go test -race in every module
make lint-all   # golangci-lint in every module
make build-all  # go build in every module
```

A `go.work` file at the root ties the modules together, so local changes to one module are picked up by the
others without a `replace` directive of your own.

## Community and policies

- [Code of Conduct](CODE_OF_CONDUCT.md)
- [Security policy](SECURITY.md): please report vulnerabilities privately, never in a public issue.
- [Changelog](CHANGELOG.md)

## License

[MIT](LICENSE) © Ahmed Kamal
