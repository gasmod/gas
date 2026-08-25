# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
