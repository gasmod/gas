# github.com/gasmod/gas

```go
import "github.com/gasmod/gas"
```

Package gas is the core of the Gas framework: a Worker that runs
services with dependency injection and an event bus, and an App that
adds a router and an HTTP server. The modules of the framework, such as
gas/config, gas/log and gas/database, meet only through the interfaces
this package defines, and this package imports none of them.

## Usage

An App is built from options, registers its routes, and runs until
SIGINT or SIGTERM:

```go
type Greeter struct{ prefix string }

func (g *Greeter) Greet(name string) string { return g.prefix + name }

app := gas.NewApp(
	gas.WithSingletonService[*Greeter](func() *Greeter {
		return &Greeter{prefix: "hello, "}
	}),
)

app.Router().Get(nil, "/hello/{name}", func(c gas.Context, g *Greeter) error {
	return c.Text(http.StatusOK, g.Greet(c.Param("name")))
})

if err := app.Run(); err != nil {
	log.Fatal(err)
}
```

A GET of `/hello/gas` then answers 200 with the body `hello, gas`, and a
GET of `/nope` answers 404 with the RFC 9457 problem details
`{"title":"Not Found","status":404,"detail":"not found","code":"not_found"}`.

## Lifecycle

A Worker, and the App that embeds one, runs once: Start, then Shutdown,
or Stop for an App. Run does both around a wait for a signal.

`Worker.Start` checks the options, then builds every singleton in
dependency order, calling Init on each `Service` with Start's ctx under
the service's init deadline (see `ServiceInitTimeout`). Pre-registered
instances are initialized first, in registration order. An App then
binds and validates its Config (see Configuration), seals its router,
checks that every DI-aware handler's dependencies are registered, and
emits `AppConfigUpdated`. Start then emits
`SystemAllServicesInitialized`, runs the pending migrations of the
`MigrationManager`, when one is registered, with ctx, and runs the ready
hooks in registration order. A second Start returns `ErrAlreadyStarted`
and a Start once Shutdown has begun `ErrShutDown`. A Start that fails
closes, newest first, every service it had initialized, with ctx
stripped of its cancellation, and returns its error joined with any
Close error; every later Start returns that error again. A panic in an
Init or a constructor fails Start the same way, with an error wrapping
`ErrPanic`, and is panicked again once the cleanup is done. The cleanup
builds nothing: it logs through the Logger only when the Logger is
built already, and through slog's default logger otherwise.

`Worker.Shutdown` waits for a CloseService or RestartService in
progress, emits `SystemShuttingDown` and waits for its subscribers,
each wait no longer than its ctx allows, then closes every active
service in the reverse order of their Init, passing its ctx to each
Close. Each service turns closed just before its own Close, so a Close
may still resolve the services that close after it. A failing Close
does not stop the others; their errors, and a
wait cut short, are returned, joined. The Logger and the services it
depends on are not closed in that order: Shutdown logs `shutdown
complete`, flushes the Logger, and then closes the Logger, if it is a
service, and then its dependencies, newest first, last of all. Shutdown
runs once; a second call waits for the first and returns nil. A
Shutdown during Start waits for Start to return, or for its ctx to end,
when it returns the ctx's error and Start closes what it initialized
once it returns. A Shutdown before Start closes nothing and makes Start
return ErrShutDown.

`App.Stop` bounds the whole shutdown by the Config's ShutdownTimeout
plus up to three times ShutdownGracePeriod, and by its ctx, which every
wait honors. It emits `SystemServerShuttingDown` and waits for its
subscribers until the deadline, shuts the HTTP server down, and, at the
deadline, cancels the context of every request still running and waits
up to `ShutdownGracePeriod` for them to return. If some do not, it
closes their connections, which ends their blocked reads and writes,
and waits up to ShutdownGracePeriod again, logging how many are left
when it gives up. It then shuts the Worker down with a context whose
deadline is the shutdown deadline, or ShutdownGracePeriod from then,
whichever is later. So Stop returns within ShutdownTimeout plus three
ShutdownGracePeriods: two waits for requests and the least time left to
close services. A Close must return when its ctx ends, so a shutdown
always ends.

`Worker.Run` and `App.Run` start under a context that the first SIGINT
or SIGTERM cancels, so a signal during a slow startup fails Start, then
block until that signal, and then shut down within the shutdown timeout:
`WithShutdownTimeout` for a Worker, the Config's ShutdownTimeout for an
App. After the first signal they stop handling signals, so a second
one ends the process. App.Run also logs the route map and serves, and
stops when the server fails, returning that error joined with Stop's.

A queue consumer, or any work that uses a service, must stop before that
service closes. Run it in a service that depends on the services it
uses, so it closes first, or stop it from a `SystemShuttingDown`
subscriber, which runs before any Close.

## Services

A `Service` has a Name, an Init and a Close. The Worker tracks every
singleton service it initialized by instance, so two services with one
name are both closed. `Worker.ActiveServices` lists the names of the
active ones; `Worker.CheckHealth` and `Worker.CheckReady` run the
`HealthReporter` and `ReadyReporter` checks of the active services at
once and return each result by name, joining the results of two
services with one name. A check that panics reports an error wrapping
`ErrPanic`.

The kill switch takes a service out at runtime. `Worker.CloseService`
removes the routes, named middleware, NotFound handler and event
subscriptions the service registered, then calls its Close and returns
its error, wrapped; the service counts as closed either way. Its routes
then answer 503 in the unified error shape, its named middleware answers
503 wherever it is used, and its `Router.Use` middleware on every route,
so a route it guarded never runs unguarded. `Worker.RestartService` calls Init again on the same
instance, which registers its routes, middleware and subscriptions anew;
only what that Init registers is served, so a route the restarted
service no longer registers answers 404. A restart whose Init fails
removes what it had registered and leaves the service closed. Neither
holds a lock while the service's code runs, so a Close or Init may call
back into the Worker. Both find the service by its exact registration
type, and return `ErrNoBuiltInstance` when the Worker started none under
it, `ErrDuplicateServiceName` when another started service has its
name, since registrations are owned by name, `ErrNotStarted` before
Start succeeds and `ErrShutDown` once Shutdown begins. CloseService
returns `ErrServiceNotActive` for a service that is not active, and
RestartService `ErrServiceAlreadyActive` for one that is not closed.
They emit `SystemServiceClosed` and `SystemServiceInitialized`. A
service that is not active, closed or being closed or restarted, is not
handed out either: resolving it fails with an error wrapping
ErrServiceNotActive, and a handler that depends on it, or returns that
error, answers 503 in the unified error shape. The one exception is a
restart's own Init, on the goroutine running it, which may resolve the
service it restarts; a goroutine that Init starts may not. A restart
whose Init returns once Shutdown has begun closes the service and
returns an error wrapping ErrShutDown, as Shutdown closes no service
that is restarting.

## Dependency injection

A `ServiceContainer` holds constructors, registered with a lifetime,
and instances. A constructor takes its dependencies as parameters and
returns its value, or its value and an error. A singleton is built once,
a scoped service once per `Scope`, and a transient one on every
resolution; a transient type must not be a Service. Resolution is by
exact type: an interface is resolvable only when it is registered, or
is an alias of a registered type (`ServiceContainer.RegisterServiceAlias`).
A Service is initialized right after it is built, and a scoped Service
is closed with its Scope, newest first.

The container rejects, with errors wrapping its sentinels: a dependency
that is not registered (`ErrNotRegistered`); constructors that depend on
each other in a cycle, a constructor or Init that resolves its own type,
and builds on two goroutines that would wait on each other
(`ErrCircularDependency`); a singleton that depends on a scoped or
transient service (`ErrCaptiveDependency`); a scoped service resolved
from the container (`ErrScopeRequired`) or from a closed Scope
(`ErrScopeClosed`); and a type that declares Init or Close without being
a Service (`ErrInvalidServiceShape`). Wiring mistakes panic at
registration with an error wrapping a sentinel: a constructor the
container cannot call (`ErrInvalidConstructor`), a nil instance
(`ErrNilInstance`), a bad alias (`ErrInvalidAlias`), and a transient
Service (`ErrInvalidLifetime`).

An App registers its `*Router`, so a service can take it as a
dependency. It does not register its `Config`: a service binds the
settings it needs through its own `ConfigProvider` dependency. Every
Worker registers its `*EventBus`, and itself as `HealthProvider` and
`ReadyProvider`.

## Routing

A `Router` wraps chi. Every route, middleware and NotFound handler is
owned by a service, or by the router when the service is nil, which is
what the kill switch removes. `Router.Group` and `Router.Route` take the
owner once and pass a `RouteGroup`, whose registrations take none:
everything in the group, nested groups included, belongs to that
service. The router defers every registration until Start seals it, so
services register middleware and routes in any order; until then every
path answers 404. Once sealed, every change builds a new routing tree
and publishes it atomically, and requests in flight keep the tree they
started on.

`Router.Use` applies middleware to every route, registered before or
after it. When the kill switch removes its owner, it answers 503 on
every route until the owner calls Use again, as a restarted service
does: the new call takes the place of the old one, so the middleware
keeps its position and runs once per request. A restart drops the calls
its Init no longer makes.

A path no route matches answers 404 with code `CodeNotFound`, and a path
routed for other methods only answers 405 with code
`CodeMethodNotAllowed` and an Allow header listing them, both in the
unified error shape. A GET route also serves HEAD. `Router.Routes`
reports the routes by owner, with the names of the named middleware
that applies to each, the top-level ones first. A MiddlewareByName whose
name is not registered panics with `ErrUnknownMiddleware`, and a
NotFound handler set by a second service with `ErrNotFoundHandlerSet`.
A `RouteGroup` uses the router's validator, form decoder and JSON body
limit.

## Handlers and errors

A handler is an `http.HandlerFunc`, or a DI-aware func(Context,
Deps...) error whose dependencies are resolved from the request's Scope
on each call; another shape panics with `ErrInvalidHandler`. An error a
DI-aware handler returns goes to the `ErrorHandler`, which by default
logs it and writes it with `WriteError`: an `*Error` as a `Problem`,
and anything else as a 500 with code `CodeInternal`. A cause never
reaches the client. The response is an RFC 9457 problem details object,
`ProblemContentType`, unless the client asks for text/html without
application/json or application/problem+json, which gets the message as
plain text (`WantsJSON`). The problem's title is the HTTP status phrase,
its detail the message, and its extension members the code, the field
errors as `errors`, each with an RFC 6901 `pointer` to its field, and
the `Error.WithDetail` values as `details`. Its type is the error's code
under the base `Router.SetProblemTypeBase` or `WithProblemTypeBase`
sets, such as `[https://api.example.com/problems/not_found](https://api.example.com/problems/not_found)`, and is left
out, meaning `about:blank`, without one. A status outside 100 to 599 is
written as 500.

The Router recovers a panic anywhere in its middleware and handlers. It
logs the panic with its stack, through the request's logger, the one
RequestLogger derived even for a panic in middleware below it, or the
App's, or else writes the stack to stderr, and passes an error wrapping
`ErrPanic` to the ErrorHandler. `http.ErrAbortHandler` is panicked
again, so net/http aborts the request. Once a response has a status or
a body, WriteError writes nothing and returns `ErrResponseCommitted`,
and the default ErrorHandler logs the error instead, so an error never
corrupts a response already sent.

`Context.BindJSON` reads at most the Config's MaxJSONBodyBytes, 413 with
code `CodePayloadTooLarge` past it, 400 with code `CodeInvalidJSON` for
a malformed body, and 422 with code `CodeValidationFailed` and one
`FieldError` per violation when validation fails. `Context.BindForm`
reads at most the Config's MaxFormBodyBytes, and answers 413 past it or
past a `BodyLimit`, 400 with code `CodeInvalidForm`, and 422 the same
way. Every error response carries `Cache-Control: no-store`.
`Context.Principal` returns 401 with code `CodeUnauthorized` when the
request has no `Principal`.

## Middleware

`RequestLogger` derives a per-request logger, with `request_id` when
chi's RequestID middleware runs before it, puts it on the request
context, and logs one line per request, `request`, with the method,
path, status, bytes, duration and remote address: at info level below
400, warn below 500 and error from 500. The default ErrorHandler hands
it the errors it handles, which the line carries as `error`, so a failed
request logs one line; without RequestLogger, the ErrorHandler logs
`request error` itself, at warn level for a 4xx and error for a 5xx. A
5xx is logged even with no Logger registered, through the App's logger
over slog's default logger.

`SecurityHeaders` sets security headers with safe defaults.
`CacheControl` sets Cache-Control on responses with a 2xx or 3xx
status only, rounding durations up to whole seconds; a negative one
panics with `ErrInvalidOption`. `BodyLimit` caps request bodies,
answering 413 in the unified error shape. `Timeout` gives each request
a deadline, answering 503 with code `CodeTimeout` when it passes. It is
for bounded responses, not streams: it buffers the response, up to
`DefaultTimeoutMaxBuffer` or `WithTimeoutMaxBuffer`, and answers 503
past that, and a request ends only once its handler returns.
Cross-origin protection guards every App with
`http.CrossOriginProtection`, whose rejection is logged at warn level
with the method and path and answers 403 with code `CodeForbidden` in
the unified error shape, with the `SecurityHeaders` defaults. Core
ships no CORS.

## Events

An `EventBus` dispatches an event to each of its subscribers on its own
goroutine, one goroutine per subscriber per Emit; Emit returns a
WaitGroup that completes once they return. A
subscriber that panics is recovered and, on a Worker's bus, logged with
the event, its owner and the stack. A subscription made with
SubscribeWithOwner is removed when its owner is closed by the kill
switch.

## Configuration

An App's `Config` starts as `DefaultConfig`, or as the base config set
with `WithBaseConfig`. Once every service is built and initialized,
Start copies it, binds the `ConfigProvider`'s values over the copy,
when one is registered, and validates the copy: Env, from
the key `gasenv` where the config module's EnvExtension stores the
environment it resolved from `GAS_ENV`, must be one of the allowed
environments (`WithAllowedEnvironments`), which always include
`DefaultEnvironment`. ConfigProvider has no other way to report the
environment, so a provider that resolves it differently must store it
at `gasenv`. Host must be an IP address or an RFC 1123 hostname; Port
must be 0 to 65535; ReadTimeout, WriteTimeout and IdleTimeout must not
be negative, 0 being no limit, as in net/http, but IdleTimeout must be
positive when ReadTimeout is 0, as net/http then bounds an idle
connection by neither; ReadHeaderTimeout and ShutdownTimeout must be
positive, so no configuration leaves a connection open to a client that
trickles its headers; MaxJSONBodyBytes and MaxFormBodyBytes must be
positive. A Config that breaks a rule fails Start, closing the
services, with an error wrapping `ErrInvalidConfig` that names the key
and the rule, never the value. A failing Bind of the provider fails
Start the same way, with its error after `gas: invalid config: bind: `.
Start keeps the copy only once it is valid. `App.Config` returns a copy
of the base config until then, so a service's Init sees the base
config, and of the bound one after.

`App.Server` builds the `http.Server` from the Config, with its handler,
`App.Handler`. A server built before Start, such as by a service's Init,
takes the bound address and timeouts at Start for each of those fields
the caller has not changed, unless Serve has begun. Options that get a
value they cannot take, such as an origin that is not an absolute URL
or a negative init timeout, make Start fail with `ErrInvalidOption`
before anything is initialized.

## Concurrency

A Worker, an App, a ServiceContainer, a Scope, a Router and an EventBus
are safe for concurrent use. Goroutines that resolve one singleton, or
one scoped type of a Scope, share one build. No lock is held while a
service's Init or Close, a constructor, a subscriber or a check runs.
`App.Config` may be called concurrently with Start; `App.Server` may
not. Do not change the server's fields once it serves. A `LogEvent` is
used by one goroutine and sent once.

## Errors

The lifecycle, kill switch, configuration and HTTP errors wrap these
sentinels, to be tested with `errors.Is`:

- `ErrAlreadyStarted`, `ErrNotStarted`, `ErrShutDown`: as under
Lifecycle.
- `ErrInvalidOption`: an option got a value it cannot take.
- `ErrInvalidConfig`: the bound Config breaks a rule.
- `ErrNoBuiltInstance`, `ErrServiceNotActive`,
`ErrServiceAlreadyActive`, `ErrDuplicateServiceName`: as under
Services.
- `ErrNotRegistered`, `ErrCircularDependency`, `ErrCaptiveDependency`,
`ErrScopeRequired`, `ErrScopeClosed`, `ErrInvalidLifetime`,
`ErrInvalidServiceShape`, `ErrInvalidConstructor`, `ErrNilInstance`,
`ErrInvalidAlias`: as under Dependency injection.
- `ErrInvalidHandler`, `ErrUnknownMiddleware`,
`ErrNotFoundHandlerSet`: as under Routing.
- `ErrPanic`: a recovered panic.
- `ErrResponseCommitted`: an error written after the response was
committed.
- `ErrResponseTooLarge`: a write past the buffer cap of `Timeout`.

Errors name services, types, keys, methods and paths, never a
configuration value. Sample errors, as returned:

```go
gas: already started
gas: not started
gas: shut down
gas: invalid option: WithServiceInitTimeout must not be negative
gas: invalid option: WithShutdownTimeout must be positive
gas: invalid option: WithTrustedOrigin needs an absolute URL with a scheme and a host
gas: invalid option: WithAllowedEnvironments needs an environment
gas: invalid config: gasenv must be one of development, testing, staging, production
gas: invalid config: server.host must be an IP address or an RFC 1123 hostname
gas: invalid config: server.port must be 0 to 65535
gas: invalid config: server.readtimeout must not be negative
gas: invalid config: server.readheadertimeout must be positive
gas: invalid config: server.writetimeout must not be negative
gas: invalid config: server.idletimeout must not be negative
gas: invalid config: server.idletimeout must be positive when server.readtimeout is 0
gas: invalid config: server.maxjsonbodybytes must be positive
gas: invalid config: server.maxformbodybytes must be positive
gas: invalid config: server.shutdowntimeout must be positive
gas: run migrations: dirty migration
gas: ready hook: cache cold
gas: handler GET /x (service "gas/router"): dependency *bytes.Buffer is not registered
gas: close billing: flush failed
gas: restart billing: init *gas_test.Billing: dial failed
gas: restart billing: gas: shut down
gas: no built instance for *gas.Router
gas: duplicate service name "billing"
gas: service not active: "billing"
gas: service already active: "billing"
gas: server shutdown: context deadline exceeded
gas: stop: wait for SystemServerShuttingDown subscribers: context deadline exceeded
gas: shutdown: wait for SystemShuttingDown subscribers: context deadline exceeded
gas: shutdown: wait for the kill switch: context deadline exceeded
gas: shutdown: wait for Start: context deadline exceeded
gas: panic in Start: boom
no registration for *bytes.Buffer
gas: resolve *bytes.Buffer: no registration for *bytes.Buffer
gas: scope closed: resolving *bytes.Buffer
gas: panic in handler: boom
gas: panic in health check: boom
gas: response already committed
gas: response too large for the timeout buffer
```

A registration mistake panics with an error such as:

```go
gas: invalid option: an AppOption needs NewApp
gas: already started: ReadyFunc after the ready hooks ran
gas: nil instance: gas.ConfigProvider
gas: invalid constructor: constructor for *bytes.Buffer is nil, not a function
gas: invalid alias: alias *bytes.Buffer must be an interface
gas: invalid handler: func(http.ResponseWriter) does not take gas.Context first
gas: unknown middleware "auth" (route GET /x)
gas: not found handler already set by service "billing"
gas: invalid option: BodyLimit must be positive
gas: invalid option: Timeout must be positive
gas: invalid option: WithTimeoutMaxBuffer must be positive
gas: invalid option: WithCacheControlMaxAge must not be negative
```

## API

Run `go doc -all github.com/gasmod/gas` for every declaration, or `go doc github.com/gasmod/gas.<Name>` for one.
