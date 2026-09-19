# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
