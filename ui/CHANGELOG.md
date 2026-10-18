# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **BREAKING: `ui.New` returns an error.** It now returns
  `func(T, *gas.Router, gas.ConfigProvider, gas.Logger) (*Service, error)`,
  the `CONVENTIONS.md` shape. It cannot fail yet, so it returns nil; the
  change is the shape. Registering it with `gas.WithSingletonService` is
  unchanged, since the container accepts a trailing error. Code that calls
  the returned function directly must take the second result (gas#106).
- `Service.Init` no longer logs a config binding, validation, or static route
  error it also returns; the caller logs it once. A validation error is
  returned as `gas/ui: invalid configuration: <cause>`.
- **BREAKING:** `Service` implements the context-aware `gas.Service`:
  `Init(ctx)` and `Close(ctx)`. Both do only in-memory work and ignore ctx.

### Fixed

- **`StaticStripPrefix` now applies its documented default.** The config
  comment said an empty value defaults to `StaticPath`, but nothing was
  stripped, so with the defaults `/static/app.css` was looked up as
  `static/app.css` inside the static root and a file at `./static/app.css`
  was reachable at no URL. An empty `StaticStripPrefix` now strips
  `StaticPath`'s literal part up to its first wildcard (`/static/` for
  `/static/*`); with `StaticPaths` set it still strips nothing. Apps whose
  static FS mirrors the URLs (`static/app.css` served at `/static/app.css`)
  must set `StaticStripPrefix: "/"` to keep that layout.
- Concurrent renders in a development-like environment, where every render
  rebuilds the templates, no longer race on the engine's template lists, and
  `AddFuncs` no longer races with a rebuild. `Engine.Build` now compiles into
  fresh values and swaps them in under the lock; if it fails, the previously
  built templates stay in place instead of an emptied cache.

## [0.3.0] - 2026-07-02

First open source release. Versions prior to 0.3.0 were developed in a private
repository; this entry summarizes the module as published.

### Added

- **`Service`** — a Gas service implementing `gas.UIProvider`, providing
  HTML template rendering and static file serving. Constructed via
  `ui.New[T]`, generic over the `gas.TemplateProvider` implementation so the
  DI container resolves the correct concrete type.
- **Template engine** (`Engine`) built on `html/template`, with a
  `layouts/` + `partials/` + page-template directory convention. Pages are
  compiled by combining layouts and partials with the page content, and
  cached; templates rebuild automatically in development mode.
- **Rendering** — `Render`, `RenderWithStatus`, and `RenderFragment` (layout
  bypass for HTMX/partial responses).
- **Static file serving** via `StaticHandler` (directory-backed) and
  `StaticHandlerFS` (`fs.FS`-backed, e.g. `embed.FS`), with directory
  listing blocked. Supports a single `StaticPath`/`StaticDir` or multiple
  `StaticPaths`, and an independent `StaticStripPrefix`.
- **Template function registration** — `RegisterFuncs` lets other services
  contribute template helpers at `Init()` time, merged into the engine's
  funcmap.
- **Built-in funcmap** — markup safety (`safe`, `safeAttr`, `safeURL`),
  string helpers, time formatting, arithmetic, `dict`/`list` collection
  helpers, `json`, and an environment-aware `buildId`.
- **`gas.UIProvider` interface implementation**, so other services can
  depend on rendering without importing gas/ui directly, and
  **`gas.ReadyReporter`** via `CheckReady`, gating startup until the
  template engine is initialized.
- **Configuration** — `Config`/`Settings`, bindable via `gas.ConfigProvider`
  or set explicitly with `WithConfig`, with validation of static
  directories and route patterns.
- **`uitest`** — a mock implementation of `gas.UIProvider` for use in
  dependents' tests, recording calls and allowing per-method behavior via
  function fields.

[Unreleased]: https://github.com/gasmod/gas/ui/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/gasmod/gas/ui/releases/tag/v0.3.0
