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

Convenience shorthands that infer the lifetime from the function name:

```go
gas.WithSingletonService[*auth.Service](auth.New)   // equivalent to WithService(ctor, ServiceLifetimeSingleton)
gas.WithScopedService[*RequestLog](NewRequestLog)    // equivalent to WithService(ctor, ServiceLifetimeScoped)
gas.WithTransientService[*Nonce](NewNonce)           // equivalent to WithService(ctor, ServiceLifetimeTransient)
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

When a DI-aware handler returns a non-nil error, it is passed to the `ErrorHandler`. The default writes a
500 Internal Server Error with the default `http.StatusText(http.StatusInternalServerError)` body,
and logs the error if a logger is registered in the service container.

**Panic recovery:** DI-aware handlers automatically recover from panics. When a handler panics, the stack trace is
written to stderr, the error is logged via the request-scoped `Logger` (if available), and the panic is routed
through the `ErrorHandler` as a `gas: handler panic: <value>` error. `http.ErrAbortHandler` is re-panicked to
preserve `net/http`'s connection-teardown behavior.

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
disabled (by passing an empty string):

| Header                 | Default                                    |
|------------------------|--------------------------------------------|
| X-Content-Type-Options | `nosniff`                                  |
| X-Frame-Options        | `DENY`                                     |
| X-XSS-Protection       | `1; mode=block`                            |
| Referrer-Policy        | `strict-origin-when-cross-origin`          |
| Permissions-Policy     | `camera=(), microphone=(), geolocation=()` |

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
| `StorageProvider`  | `Upload`, `Download`, `Delete`, `PresignURL`                                |
| `ConfigProvider`   | `SetDefault`, `SetDefaults`, `Set`, `Bind`, `Get`, `Find`, `Values`         |
| `TemplateProvider` | `Get`, `List`, `Register`, `RegisterFS`                                     |
| `UIProvider`       | `Render`, `RenderWithStatus`, `RenderFragment`, `RegisterFuncs`             |
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

`Config` embeds `env.WithGasEnv` (from gas-env) for environment detection, and holds a `Server ServerSettings` sub-struct.

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
| `w.Shutdown()`                     | `error`                     | Emit shutdown event, close services in reverse order   |
| `w.Run()`                          | `error`                     | Start + block on signal + Shutdown                    |
| `w.EventBus()`                     | `*EventBus`                 |                                                       |
| `w.ServiceContainer()`             | `*ServiceContainer`         |                                                       |
| `w.MigrationManager()`             | `MigrationManager` (or nil) |                                                       |
| `w.ConfigProvider()`               | `ConfigProvider` (or nil)   |                                                       |
| `w.ActiveServices()`               | `[]string`                  |                                                       |
| `w.CloseService(name)`             | `error`                     | Kill-switch for a single service                      |
| `w.RestartService(name)`           | `error`                     | Re-initialize a previously closed service             |

## App Methods

`App` embeds `Worker`, so all Worker methods are available. Additionally:

| Method                       | Returns                     |
|------------------------------|-----------------------------|
| `app.Router()`               | `*Router`                   |
| `app.Config()`               | `*Config`                   |
