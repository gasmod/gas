# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Service init deadline:** every singleton's Init (at startup and on
  `RestartService`) gets a deadline, 15 seconds by default. When it passes,
  Init's context is canceled and the container waits for Init to return,
  then fails with an error wrapping `context.DeadlineExceeded`; no Init is
  left running behind a failed start. An Init that ignores the cancellation
  and returns nil late is closed again. `gas.WithServiceInitTimeout` (a
  `WorkerOption`) and `gas.WithServiceContainerServiceTimeout` (a new
  `gas.ServiceContainerOption` for `NewServiceContainer`) change the
  default, and 0 disables it; both panic on a negative duration. A service
  implementing `gas.ServiceInitTimeout` sets its own; a negative value there
  fails Init without running it.
- **DI sentinel errors:** `gas.ErrNotRegistered`,
  `gas.ErrCircularDependency`, `gas.ErrCaptiveDependency`,
  `gas.ErrScopeRequired`, `gas.ErrNoConstructor`, `gas.ErrInvalidLifetime`,
  and `gas.ErrInvalidServiceShape`. Errors from `Resolve`, `BuildAll`, and
  startup wrap the matching sentinel, so callers can use `errors.Is` instead
  of matching message text. The messages are unchanged, except that the
  scoped-service error now says to use `container.NewScope(ctx)`.

### Changed

- **BREAKING: `gas.Service` takes a context.** `Init(ctx context.Context)`
  and `Close(ctx context.Context)` replace `Init()` and `Close()`; the
  container rejects a type with the old signatures and names the expected
  ones. A singleton's Init gets the caller's startup context plus its init
  deadline, and that context is canceled when Init returns, so background
  work must not derive from it. A scoped service's Init gets the scope's
  context; in an App that is the request context, and the request scope's
  Close gets that context without its cancellation.
- **BREAKING: lifecycle methods take the caller's context.**
  `Worker.Start(ctx)`, `Worker.InitServices(ctx)`, `Worker.Shutdown(ctx)`,
  `Worker.CloseService[T](ctx)`, `Worker.RestartService[T](ctx)`,
  `App.Start(ctx)`, `App.Stop(ctx)`, `ServiceContainer.BuildAll(ctx)`,
  `ServiceContainer.NewScope(ctx)`, and `Scope.Close(ctx)`. `Run` is
  unchanged and uses `context.Background()`. `App.Stop` bounds the HTTP
  server's shutdown by `ServerSettings.ShutdownTimeout` or its context,
  whichever ends first.
- **BREAKING: `Worker.Shutdown` runs once and returns Close errors.** It
  closes only services that are still active, so one already closed by
  `CloseService` is not closed again, and services it closes leave
  `ActiveServices`. Every Close error is returned, joined, instead of being
  logged; later calls return nil. Once Shutdown has begun, `CloseService`
  and `RestartService` return an error, so the kill switch cannot reopen a
  service after shutdown.
- **BREAKING: `Authenticator`, `Authorizer`, and `PrincipalRevoker` moved to
  the auth module.** They are now `auth.Authenticator`, `auth.Authorizer`, and
  `auth.PrincipalRevoker` in `github.com/gasmod/gas/auth`, the only module
  that implements them. Replace `gas.Authenticator` with `auth.Authenticator`
  (and likewise for the other two). `gas.Principal`, `gas.PrincipalMetadata`,
  `gas.BasePrincipalMetadata`, `gas.MetadataValue`, `gas.WithPrincipal`,
  `gas.PrincipalFromContext`, and `Context.Principal` stay in core, unchanged.

### Removed

- **`gas.Config.Validate`.** `ConfigProvider.Bind` already validates the
  struct it binds, so `App.Start` no longer calls a separate `Validate`, and
  the method is gone; the rule lives in `Server.Host`'s validate tag. An App
  started without a `ConfigProvider` runs on its Config unchecked.
- **BREAKING: `gas.StorageProvider` is removed from core**, together with
  `gas.StorageObject`, `gas.ObjectInfo`, `gas.StorageOption`, `gas.InBucket`,
  `gas.WithContentType`, `gas.WithMetadata`, and `gas.ApplyStorageOptions`.
  Object storage is now `storage.Provider` and `storage.Presigner` in the
  `gas/storage` module; register `s3.NewProvider()` and `s3.NewPresigner()`
  (or `dir.NewProvider(...)`) under them. See `storage/CHANGELOG.md`.
- **BREAKING: `gas.MustPrincipalFromContext`.** It panicked on any request
  that reached a handler without a principal. Use `ctx.Principal()` in a
  handler, which returns a 401 `*gas.Error` the handler can return, or
  `gas.PrincipalFromContext` and check for nil.
- **BREAKING: `Logger.SetBaseFields` and `gas.MutableLoggerContext`**, with
  `gas.NopMutableLoggerContext`. `SetBaseFields` changed a logger in place
  without synchronization, so calling it on the shared singleton (as
  `gas.RequestLogger` did) raced between requests and piled up fields for the
  life of the process. Derive a logger with `With()` and put it on the request
  context with `gas.WithLogger` instead; read it with `gas.LoggerFromContext`.
  `Logger` implementations no longer need the method.

### Fixed

- **Server config validation no longer does a DNS lookup.** `Config.Validate`
  checked `Server.Host` with `net.LookupHost`, so startup depended on the
  resolver: slow or offline DNS delayed or failed `Start`, and a well-formed
  name that only resolves later (such as a container hostname) was rejected.
  `Server.Host` now carries the validate tag
  `required,max=253,ip|hostname_rfc1123`, which `ConfigProvider.Bind` checks
  when `App.Start` binds the Config: an IP address or an RFC 1123 hostname
  (no trailing dot), not resolved, so a host that does not resolve fails when
  the server listens. The error is the validator's (`Field validation for
  'Host' failed on the 'ip|hostname_rfc1123' tag`) under
  `gas: config binding:` (part of #12).
- **`App.Run` stops the App when the server fails.** When `Serve` returned an
  error, such as a port already in use, `Run` returned it without calling
  `Stop`, leaving every service `Start` had initialized open. It now runs
  `Stop` and returns the server error joined with any `Stop` error. The
  server error is no longer wrapped a second time in `gas: server error:`
  (part of #12).
- **Plain handlers get panic recovery.** Only DI-aware handlers were wrapped
  in recovery, so a panic in a plain `http.HandlerFunc` route or `NotFound`
  handler bypassed the error handler: net/http recovered it, logged it to the
  server's error log, and dropped the connection with no response. Plain
  handlers now get the same recovery: the stack goes to stderr and to the
  request's `Logger`, and the error handler renders the error (500 by
  default). `http.ErrAbortHandler` still aborts. The default error handler
  also no longer panics on a request with no request scope, such as one
  served by a `Router` used without an App (part of #12).
- **Boot validation no longer accepts dependencies that fail on every
  request.** A DI-aware handler, or `CanResolve`, accepted an interface that
  was only implemented by a registered concrete type, but resolution looks up
  the exact type, so startup passed and each request answered 500. Validation
  now uses the same exact-type lookup as resolution: register the
  constructor under the interface (`WithService[MyInterface](...)`) to inject
  it as one. The captive-dependency check uses the same lookup. A DI-aware
  `NotFound` handler's dependencies, never validated before, are now checked
  at startup like a route handler's (#10).
- **A failed `Start` no longer leaves services running.** When a service's
  `Init` failed, the services initialized before it were never tracked, so
  neither the failed `Start` nor a later `Shutdown` closed them; a failing
  migration or ready hook left every service running until the caller called
  `Shutdown`; and a second `Start` got nil from the run-once `InitServices`
  and ran migrations and ready hooks against a half-built container. Now a
  failed `Start` (or `InitServices`) closes, in reverse order, every service
  it had initialized, with `context.WithoutCancel` of its context, and
  returns the startup error joined with any `Close` errors. Later calls
  return that same error, and a later `Shutdown` has nothing to close. A
  pre-registered instance whose `Init` failed is not closed. `App.Start` does
  the same when its config fails to bind or validate after the Worker
  started.
- **A dependency cycle through a captured container no longer crashes the
  process.** A constructor or `Init` that resolved the type being built
  through a captured `*ServiceContainer` or `Scope` recursed until `fatal
  error: stack overflow`, which `recover` cannot catch. Resolution now tracks
  the types each goroutine is building and returns `ErrCircularDependency`
  naming the cycle (`A -> B -> A`), for every lifetime. Constructors nested
  more than 256 deep also return `ErrCircularDependency`.
- **Concurrent resolution of a lazy singleton builds it once.** The container
  had no lock, so goroutines resolving an unbuilt singleton at the same time
  raced and each ran its constructor and `Init`. One goroutine now builds it
  while the others wait for that build and share its instance; two builds on
  different goroutines that would wait on each other return
  `ErrCircularDependency` instead of deadlocking. The container's instance
  maps are guarded by a mutex, and constructors and `Init` run without it
  held.
- **The Worker's logger no longer races with the kill switch.** `Worker`
  resolved its logger lazily and unsynchronized on first use and, with no
  `Logger` registered, registered a slog fallback in the container, so
  `CloseService` and `RestartService` running concurrently raced on the
  logger field and the container's instance map (intermittently `fatal
  error: concurrent map read and map write`). The logger is now resolved
  once under a `sync.Once`, and the fallback stays private to the Worker:
  resolving `gas.Logger` with none registered keeps returning
  `ErrNotRegistered` instead of the fallback.
- **`gas.RequestLogger` no longer changes the logger it resolves.** It added
  `request_id` with `SetBaseFields`, so with the singleton `gas.Logger`
  concurrent requests raced and every request added one more `request_id` to
  the shared logger. It now derives a per-request logger with `With()`, runs
  the builder on it, and stores it on the request context with
  `gas.WithLogger`. When `T` is registered scoped (the documented
  `RequestLogger` type built from the singleton with `With()`), the request
  logger also replaces `T`'s instance in the request scope, so handlers and
  services that take `T` get its `request_id`, as they did before. A
  singleton `T` is never shadowed, and a scoped `T` whose instance is a
  `Service` is not replaced, so the scope still closes it.
  `gas.RequestLogger[gas.Logger]()` with the singleton is now safe too, with
  the request logger on the context only.

### Security

- **`BindJSON` caps the request body.** It decoded straight off the body with
  no limit, so large JSON bodies could exhaust memory within the read
  timeout. It now reads at most `Server.MaxJSONBodyBytes` (new, 1 MiB by
  default; zero or negative turns the cap off) and returns a 413 `*Error`
  with the new code `gas.CodePayloadTooLarge`, wrapping the
  `*http.MaxBytesError`. A Context built with `NewContext` gets the same 1 MiB
  default, and the new `gas.WithMaxJSONBodyBytes` option changes it. Handlers
  that read the body themselves are not capped (#8).

## [0.5.0] - 2026-09-19

### Added

- **Unified error shape** — `gas.Error` with `Status`, `Code`, `Message`,
  `Fields`, and `Details`, plus constructors (`gas.NotFound`,
  `gas.BadRequest`, `gas.Unprocessable`, and the rest), builder methods
  (`WithCause`, `WithField`, `WithDetail`), and `gas.AsError`. Handlers
  return it and core renders it, replacing the per-application error struct.
- **`gas.WriteError`** — the single rendering entry point, usable from
  handlers, custom `ErrorHandler`s, and custom middleware. It never logs and
  never touches the request scope, so it is safe before the scope middleware
  runs. `gas.WantsJSON` exposes the same Accept negotiation.
- **`Context.Error` and `Context.ErrorJSON`** — write the unified response
  directly from a handler.
- **`gas.ErrorResponse`** — the `{"error": {...}}` envelope, exported so Go
  clients and tests can decode it.
- **`Worker.ReadyFunc`** — the imperative form of `WithReadyFunc`. Safe for
  concurrent use; panics if called after the ready hooks have run, since the
  func would never execute.
- **`Scope.Resolve[T]` and `Scope.MustResolve[T]`** — resolve directly from a
  scope.
- `*Worker`, `*EventBus`, and `*Router` implement `gas.Service`, so they can
  be passed as owners. They appear in `ActiveServices` as `gas/worker`,
  `gas/eventbus`, and `gas/router`.

### Changed

- **BREAKING: Go 1.27 is required.** The API now uses generic methods.
- **BREAKING: DI registration and resolution are generic methods.**
  `gas.RegisterCtor`, `gas.RegisterInstance`, `gas.Resolve`, `gas.MustResolve`,
  `gas.TypePtr`, and the reflection-based type-token methods are removed. Use
  `c.RegisterService[T](ctor, lifetime)`, `c.RegisterSingletonService[T](ctor)`
  (and the scoped and transient variants), `c.RegisterServiceInstance[T](val)`,
  `c.Resolve[T]()`, and `c.MustResolve[T]()` on `*ServiceContainer`; the
  registration methods are forwarded on `*Worker`. `CanResolve` is now
  `c.CanResolve[T]()`. The `With*Service` options are unchanged.
- **BREAKING: `RegisterServiceInstance` registers under the static type.** It
  previously used the value's dynamic type; it now registers under `T` at the
  call site, so an interface-typed variable is resolvable as that interface
  only.
- **BREAKING: events are types, not values.** Declare an event by embedding
  its payload, `type UserCreated struct{ gas.Event[UserCreatedPayload] }`, and
  use the generic methods `bus.Emit[UserCreated](payload)`,
  `bus.Subscribe[UserCreated](handler)`, and
  `bus.SubscribeWithOwner[UserCreated](service, handler)`. The package-level
  `gas.Emit`, `gas.Subscribe`, and `gas.SubscribeWithOwner`, the string-keyed
  `EventBus` methods, and `Event.Name` are removed. Subscriptions are keyed by
  the event type, so two events never collide even with identical payloads.
  The system events (`gas.SystemServiceClosed` and the rest) are now types.
- **BREAKING: owners are a `gas.Service`, not a name.** `Router.Handle`,
  `Router.Register`, `Router.NotFound`, `Router.RemoveByService`,
  `EventBus.SubscribeWithOwner`, `EventBus.RemoveByService`, and
  `MigrationManager.Register` / `RegisterSlice` / `RegisterFS` take the service
  itself; pass `s` instead of `s.Name()`. `Migration.Service` is a
  `gas.Service`. Ownership is still tracked by `Name()`. A `nil` owner on the
  router means no owning service and is attributed to the root router
  (`gas/router`), on sub-routers too.
- **BREAKING: the kill switch is addressed by type.** `CloseService(name)` and
  `RestartService(name)` become `CloseService[T]()` and `RestartService[T]()`,
  where `T` is the exact type the service was registered under. Neither
  constructs a service that was never built.
- **BREAKING: `NopLoggerCtor` is removed**; `NewNopLogger` returns a plain
  `func() *NopLogger`.
- **BREAKING (gas/auth): the `db` stores' `Init` takes the owning
  `gas.Service`** instead of its name.
- **`gas.Error.Status` is serialized into the JSON body** as `"status"`. An
  invalid status is normalized to 500 in both the status line and the body, so
  the two always agree.
- **Constructor signatures are validated at registration.** `RegisterService`
  and its lifetime variants now panic at the call site,
  naming the constructor and the type it was registered for, when the
  constructor is not a function, is variadic, returns other than one or two
  values, produces a first result that is neither assignable to nor an
  implementation of the registered type, or returns a non-error second value.
  These previously registered cleanly and surfaced as a bare `reflect` panic
  out of `BuildAll` with no registration site in the message — except the
  three-result case, which built the service and silently dropped the
  constructor's error.
- The default `ErrorHandler` now renders a `gas.Error` at its own status
  instead of collapsing everything to a plain-text 500. Clients that do not
  explicitly prefer `text/html` receive the JSON envelope.
- Handler errors below status 500 log at warn level instead of error.
- Routes belonging to a service torn down via `CloseService` now return
  the unified error shape (503, `service_unavailable`) instead of a
  plain-text body. The status is unchanged.
- `Context.BindJSON` and `Context.BindForm` return `*gas.Error`: 400 for a
  malformed body, 422 with per-field detail for a validation failure. The
  underlying error remains reachable through `errors.As`.
- Validation field names now follow the `json` tag the client sent rather than
  the Go struct field name.

### Fixed

- **`CloseService` and `RestartService` deadlocked a subscriber that called
  back into the Worker.** Both emitted their system event while holding the
  Worker lock; they now release it first.
- **Shutdown closed services in a random order.** `Worker.Shutdown` documents
  "reverse initialization order", but `InitServices` derived `serviceOrder` by
  ranging over the container's instance map, and Go randomizes map iteration.
  A service could be closed after a dependency it uses inside `Close()`, which
  surfaced as an intermittent, per-process shutdown failure. The container now
  records the order instances become available and `EachInstance` walks it.
- `Scope.Close` had the same defect on the per-request path and now tears
  scoped services down in reverse resolution order.

- **Breaking:** a `Service` registered with `WithServiceInstance` is now
  initialized by the container. It previously was not: `Init` never ran, yet
  the container still closed it at shutdown and still called `Init` if it was
  restarted through the kill switch, so a pre-built service was closed without
  ever having been initialized. Implementing `gas.Service` hands the lifecycle
  to the container regardless of how the value was registered. Callers that
  called `Init()` themselves before registering should stop, or it runs twice.

- **Breaking:** a registered type that declares `Init` or `Close` but does not
  fully implement `gas.Service` is now rejected at startup with an error naming
  the missing or mis-typed methods, instead of being silently skipped. Writing
  one lifecycle hook and forgetting the rest used to produce a service that
  registered cleanly and then did nothing: `Init` never ran, `Close` never ran
  (leaking whatever it held), and the kill switch could not see it. `Init` and
  `Close` are the managed lifecycle, so declaring either commits to the whole
  interface. Declaring only `Name()` does not trigger it — such types stay
  ordinary dependencies. A third-party `io.Closer` (`*sql.DB` and the like) can
  no longer be registered directly; wrap it in a service of your own.
- **Breaking:** `Router.RemoveByModule` and `EventBus.RemoveByModule` are
  renamed to `RemoveByService`. Both always took a service name; "module" was
  vestigial vocabulary from before services were named services.
- Killing a service now disables every named middleware it registered wherever
  that middleware is referenced, not just the routes the service owns. A route
  guarded by a killed service's middleware returns 503 instead of continuing to
  run the torn-down middleware, so a teardown can never drop an authorization
  check and leave the route open. Named middleware is now resolved on each
  build of the routing tree rather than captured at registration; re-registering
  the name (the `RestartService` path) re-arms it.

### Security

- Errors that are not a `gas.Error`, including handler panics and DI
  resolution failures, collapse to a generic 500. The original reaches the
  logger only and is never serialized into a response body.

## [0.3.0] - 2026-07-02

First open source release. Versions prior to 0.3.0 were developed in a private
repository; this entry summarizes the framework as published.

### Added

- **App lifecycle** — `gas.NewApp` with a single-call `Run()`, plus composable
  `Start()` / `Serve()` / `Stop()` for custom orchestration. Graceful shutdown
  closes services in reverse init order.
- **Worker lifecycle** — `gas.Worker` for non-HTTP environments (Lambda,
  background workers, CLI tools) with the same DI, events, and migration
  support without a router or HTTP server.
- **Dependency injection container** with singleton, scoped, and transient
  lifetimes, constructor-based registration, automatic topological sorting,
  and lifetime-rule validation.
- **Router** with per-service ownership tracking, surgical service teardown
  (kill-switch), route grouping, idempotent `Route()` registration, DI-aware
  handlers, and automatic `HEAD` handlers for `GET` routes.
- **EventBus** for decoupled service-to-service communication, with
  ownership-tracked subscriptions and system events.
- **Middleware** — request logging, recovery, CSRF protection, and
  `SecurityHeaders` with configurable CSP, HSTS, and cross-origin policies.
- **Request scopes** for per-request service resolution.
- **Migrations** — migration registration and lifecycle integration, run
  automatically on startup.
- **Context and error handling** — `gas.Context` request helpers and a
  pluggable `ErrorHandler`.
- **Provider interfaces** implemented by the other gasmod modules:
  `ConfigProvider`, `DatabaseProvider`, `CacheProvider`, `StorageProvider`,
  `EmailProvider`, `JobQueueProvider`, `TemplateProvider`, `UIProvider`, and
  `Logger` (with `slog` and no-op implementations built in).
- **Authentication and authorization interfaces** — `Authenticator`,
  `Authorizer`, `PrincipalRevoker`, and `Principal`.
- **Health and readiness** — `HealthReporter` / `ReadyReporter` interfaces and
  ready hooks for startup gating.

### Fixed

- Eliminated a data race between the router and the service kill-switch by
  switching route storage to copy-on-write.

[Unreleased]: https://github.com/gasmod/gas/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/gasmod/gas/releases/tag/v0.5.0
[0.3.0]: https://github.com/gasmod/gas/releases/tag/v0.3.0
