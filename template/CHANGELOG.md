# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] - 2026-07-03

First open source release. Versions prior to 0.3.0 were developed in a private
repository; this entry summarizes the module as published.

### Added

- **`gas.TemplateProvider` implementation** across four backends — `memory`,
  `dir`, `db`, and `fs` — plus a `composite` store that chains them.
- **`memory.Store`** — an in-memory, concurrency-safe store for templates
  registered programmatically.
- **`dir.Store`** — reads `.html` templates from a directory on disk,
  sandboxed to the root via `os.Root`, with an in-memory overlay so
  `Register`/`RegisterFS` calls can add or override templates without
  touching disk.
- **`fs.Store`** — a read-only store backed by any `fs.FS` (e.g.
  `embed.FS`); `Register` and `RegisterFS` return `template.ErrReadOnly`,
  meant to be wrapped in a `composite.Store` with a writable provider for
  mutability.
- **`composite.Store`** — checks a writable provider first, then each
  reader in order, for both `Get` and a merged, deduplicated `List`;
  `Register`/`RegisterFS` delegate to the writable provider.
- **`db.Store`** — a database-backed store with `Get`, `List`, `Register`,
  `RegisterFS`, plus `Exists` and `Delete`, scoped by a configurable
  `WithNamespace` so multiple stores can share the `__gas_templates` table.
- **sqlc-generated multi-dialect adapters** for PostgreSQL, MySQL, and
  SQLite, selected automatically from the underlying database driver.
- **Migration registration** with `gas/migrate` — `db.Store.Init` registers
  the `__gas_templates` table migration for the resolved dialect.
- **`RegisterFS`** on every backend — recursively walks an `fs.FS` and
  registers every `.html` file found, keyed by its slash-separated path.
- **Sentinel errors** — `ErrTemplateNotFound` (with an `IsNotFound` helper)
  and `ErrReadOnly`.
- **`templatetest.MockTemplate`** — a configurable mock of
  `gas.TemplateProvider` with per-method function fields and a recorded
  `Calls` slice for assertions.
- **DI wiring** — `dir.NewStore`, `fs.NewStore`, and `db.NewStore` all
  return DI-injectable constructors for use with `gas.WithSingletonService`.

[Unreleased]: https://github.com/gasmod/gas/template/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/gasmod/gas/template/releases/tag/v0.3.0

