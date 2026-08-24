# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

### Changed

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

[Unreleased]: https://github.com/gasmod/gas/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/gasmod/gas/releases/tag/v0.3.0
