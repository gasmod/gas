# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] - 2026-07-03

First open source release. Versions prior to 0.3.0 were developed in a private
repository; this entry summarizes the module as published.

### Added

- **Migration registration** — `Register` for a single migration,
  `RegisterSlice` for a batch, and `RegisterFS` for embedded `up`/`down` SQL
  file pairs (including nested directories), with the version and description
  parsed from the filename convention.
- **`RunPending`** — sorts all registered migrations globally by version
  across every service and applies any not yet recorded, each inside its own
  transaction.
- **`Down(n)`** — rolls back the last `n` applied migrations in reverse
  version order.
- **Dirty-state handling** — a failed migration is recorded as dirty and
  blocks further execution until manually resolved.
- **Version collision detection** — `RunPending` and `Down` return an error
  identifying the version and both service names when two services register
  migrations under the same version.
- **`__gas_migrations` tracking table**, created automatically on `Init()`.
- **sqlc-generated multi-dialect adapters** for PostgreSQL, MySQL, and SQLite,
  selected automatically from the underlying database driver.
- **`gas.ReadyReporter` implementation** — `CheckReady` gates readiness on
  initialization, no dirty migrations, and no pending registered migrations.
- **Build-info version tracking** for `gas/migrate` itself and for the
  registering services, read from `runtime/debug.ReadBuildInfo`.
- **`migratetest`** package with a mock `gas.MigrationManager` for testing
  services that register migrations.
- **DI wiring** with `gas/database` via `gas.WithSingletonService`.

### Fixed

- Applied a migration's DDL and its `__gas_migrations` tracking row in the
  same transaction, so a crash between the two no longer leaves the schema
  changed but unrecorded and the pipeline wedged as dirty.

[Unreleased]: https://github.com/gasmod/gas/migrate/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/gasmod/gas/migrate/releases/tag/v0.3.0

