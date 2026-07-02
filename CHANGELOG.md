# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
