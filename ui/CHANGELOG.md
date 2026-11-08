# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

v0.6.0 rewrites the module around the `ui.Provider` contract, which the
package's `doc.go` documents and a conformance suite checks over the
memory, fs and dir stores of gas/template. `doc.go` is the behavioral
contract and lists the errors verbatim; read it before upgrading.

### Changed

- **BREAKING: `ui.Provider` replaces `gas.UIProvider`**, which core no
  longer defines. Register the service as `ui.Provider`,
  `gas.WithSingletonService[ui.Provider](ui.New[gas.TemplateProvider]())`,
  and have the services that render pages take a `ui.Provider`.
- **BREAKING: `Render` and `RenderFragment` take a context and options.**
  They are `Render(ctx, w, name, data, opts...)` and
  `RenderFragment(ctx, w, name, data, opts...)`; the context bounds what
  they read from the store. `RenderWithStatus` is removed: pass
  `ui.WithStatus(status)`. `ui.WithLayout` picks another layout for
  `Render`, and `ui.WithBlock` another template for `RenderFragment`.
- **BREAKING: a render writes nothing on error and sets only what it is
  asked to.** It calls `WriteHeader` only with the status of
  `WithStatus`, so a status the handler wrote stands, where it used to
  write 200; it sets `Content-Type: text/html; charset=utf-8` only when
  the handler set no Content-Type; and on any error it writes nothing.
  On a closed service it returns an error wrapping `ui.ErrClosed`
  instead of writing a 503 response itself.
- **BREAKING: `RegisterFuncs` returns an error.** It checks every
  function first and adds none when one fails, with an error wrapping
  `ui.ErrInvalidFunc`: a name that is not a Go identifier, a nil value
  or one that is not a function, a function without one result or a
  result and an error, and the names `html`, `urlquery` and those
  starting with `_html_template_`, through which html/template escapes.
  Replacing a function is logged at warn level. The functions belong to
  the service and outlive `Close` and every later `Init`. Before `Init`
  it returns an error wrapping `ui.ErrNotInitialized`, where it used to
  log a warning and drop the functions.
- **BREAKING: `Config.FuncMap` is removed**; pass `ui.WithFuncs`, whose
  functions `Init` checks as `RegisterFuncs` does.
- **BREAKING: no environment in ui.** `Config` no longer embeds
  `gas.WithGasEnv`. Rebuilding the templates on every render is the
  `ui.reload` setting, off by default, where it used to follow a
  development-like environment. The `env` template function is removed,
  and `buildId` returns one ID per template build, where it returned a
  new ID on every call in a development-like environment.
- **BREAKING: default functions.** `title` is removed, as
  `strings.ToTitle` upper-cased every letter; register a function built
  on golang.org/x/text/cases instead. `truncate` counts runes, not
  bytes, and fails for a negative length; `dict` fails for an odd count
  or a key that is not a string, which it used to drop; `json` fails
  when the value cannot be encoded, where it wrote nothing.
- **BREAKING: `StaticHandler(prefix, fsys, opts...)`** returns
  `(http.Handler, error)` and serves an `fs.FS`; it replaces
  `StaticHandler(prefix, dir)` and `StaticHandlerFS`. To serve a
  directory, pass the FS of an `os.Root`. A bad prefix is an error
  wrapping `ui.ErrInvalidPrefix`, and a nil `fs.FS` `ui.ErrNilFS`. Its
  options are `WithDotfiles`, `WithMaxAge` and `WithErrorFunc`.
- **BREAKING: static files are off unless configured.**
  `DefaultStaticDir` is empty, where it was `static/`. `Init` opens
  `ui.staticdir` with `os.OpenRoot`, wherever it is: a directory that
  is missing or is not one fails `Init`, where it used to have to sit
  under the working directory, a check skipped under tests.
  `WithStaticFS` with a nil file system fails `Init` with `ui.ErrNilFS`,
  and static files with a nil router fail it with `ui.ErrNilRouter`,
  where no route was registered.
- **BREAKING: the static handler refuses more.** It answers 404 for a
  path that is not clean, holds a NUL byte or a backslash, or is not
  valid UTF-8, for a directory, a file that is not regular, a file
  reached through a symlink out of the root or one it may not read, and,
  unless dotfiles are on, a path with an element starting with `.`. It
  answers 405 with `Allow: GET, HEAD` for another method and 503 once
  the root is closed. Every response carries
  `X-Content-Type-Options: nosniff`; files without a mod time get an
  ETag, and `ui.staticmaxage` or `WithMaxAge` adds
  `Cache-Control: public, max-age=N`.
- **BREAKING: configuration.** `Init` resolves the configuration each
  time it runs. With `WithConfig` it takes a copy, slices included, and
  never changes the caller's struct, and checks its `validate` tags; a
  nil Config is `ui.ErrNilConfig`. Without it, a nil ConfigProvider is
  `ui.ErrNoConfigProvider`, where the defaults were used silently. New
  settings: `ui.extensions` (`.html`), `ui.pagesprefix`,
  `ui.fragmentblock` (`content`), `ui.reload`, `ui.staticdotfiles` and
  `ui.staticmaxage`. `ui.staticpath` routes and `ui.staticpaths` must
  start with `/`, hold `*` only last and pair `{` with `}`. A failure
  wraps `ui.ErrInvalidConfig`, reads `gas/ui: init: invalid config:`
  and the cause, where it read `gas/ui: invalid configuration:` or
  `gas/ui: config binding:`, and names the setting, never its value.
- **BREAKING: error texts.** Every error starts with `gas/ui: ` and the
  operation, and, for a render of a valid name, the quoted page name,
  such as `gas/ui: render "users": page not found`. The package exports
  a sentinel for each failure, listed under Added.
- **BREAKING: lifecycle.** `Init` on an initialized service returns
  `ui.ErrAlreadyInitialized`; before the first successful `Init` every
  method but `Init`, `Close`, `Name` and `Config` returns an error
  wrapping `ui.ErrNotInitialized`, and after `Close`, until `Init`
  succeeds again, `ui.ErrClosed`. `Init` after `Close` starts the
  service again. `Init` and `Close` log `service initialized` and
  `service closed` at info level. `New`'s constructor returns
  `ui.ErrNilLogger` for a nil logger and `ui.ErrNilTemplateProvider` for
  a nil store.
- **Page names and templates.** Only templates whose names end in one of
  `ui.extensions` are read, so a store shared with gas/email keeps its
  `.txt` templates out. A page name must be a clean slash path, or the
  render fails with `ui.ErrInvalidName`; a name that is no page is
  `ui.ErrPageNotFound`. Two store names that make one page name, a
  store name that makes none, and a template that defines a name ending
  in an extension fail to compile with `ui.ErrCompile`.
- **Fragments.** `RenderFragment` executes the page's `ui.fragmentblock`
  block when the page defines it, else the page's top-level text, else
  fails with `ui.ErrNoFragment`; a layout's or a partial's top-level
  text is never a page's fragment (KI-032).
- **Builds.** `Init` builds nothing; the first render does, and
  `CheckReady` runs the first build when none has been tried. A page
  that cannot be read or compiled fails its own renders only (KI-053);
  outside reload mode a failed build keeps the set of the build before,
  and a store change is seen on `Service.Reload`. Builds run one at a
  time, and no lock is held while the store is read.
- **Each template a render executes runs in a copy of the page's set of
  its own**, so its escaping never depends on which templates ran
  before it, and a template that fails to escape fails each time
  without breaking the page. A panic while a page executes is returned
  as an error wrapping `ui.ErrExecute`.
- **`uitest.MockUI` follows `ui.Provider`.** **BREAKING:** `RenderFn`
  and `RenderFragmentFn` take the context and the options,
  `RegisterFuncsFn` returns an error, and `RenderWithStatusFn` is
  removed. `Calls` is a method that returns copies of the recorded
  calls, in place of an exported field, and a call records its options
  resolved into a `ui.RenderOptions`. With a field nil, a method writes
  nothing and returns nil.

### Added

- `Service.Config`, which returns a copy, slices included, of the
  Config the last successful `Init` resolved, defaults included, or the
  zero Config before one; a failed `Init` and `Close` leave it as it
  was.
- `Service.Reload`, which builds the templates anew from the store and
  returns the build's error.
- `ui.Provider`, `ui.WithFuncs`, `ui.WithStatus`, `ui.WithLayout`,
  `ui.WithBlock`, `ui.RenderOptions`, `ui.RenderOption` and
  `ui.ApplyRenderOptions`.
- `ui.WithDotfiles`, `ui.WithMaxAge`, `ui.WithErrorFunc` and
  `ui.StaticOption`.
- `ui.ServiceName`, `ui.DefaultStaticDir`, `ui.DefaultStaticPath`,
  `ui.DefaultLayoutName`, `ui.DefaultFragmentBlock`,
  `ui.DefaultExtension`, `ui.LayoutsPrefix` and `ui.PartialsPrefix`.
- The sentinels `ErrNotInitialized`, `ErrAlreadyInitialized`,
  `ErrClosed`, `ErrNilLogger`, `ErrNilTemplateProvider`,
  `ErrNilRouter`, `ErrNilFS`, `ErrNilConfig`, `ErrNoConfigProvider`,
  `ErrInvalidConfig`, `ErrInvalidFunc`, `ErrInvalidName`,
  `ErrPageNotFound`, `ErrLayoutNotFound`, `ErrBlockNotFound`,
  `ErrNoFragment`, `ErrInvalidOption`, `ErrInvalidStatus`,
  `ErrInvalidPrefix`, `ErrLoad`, `ErrCompile`, `ErrExecute` and
  `ErrWrite`.

### Removed

- **BREAKING:** the public `Engine`, `NewEngine` and `Service.Engine`.
- **BREAKING:** `DefaultFuncMap`, `Service.RenderWithStatus` and
  `StaticHandlerFS`.
- The `claude-skill/gas-ui` skill file.

### Fixed

- `RenderFragment` of a page that only defines `content` renders that
  block instead of nothing, and stray text in a partial no longer
  becomes a page's fragment (KI-032).
- One page that fails to compile, or a failed rebuild, no longer makes
  every render fail; the other pages, and the previous build, keep
  serving (KI-053).
- A template whose escaping failed once no longer makes later renders
  of its page panic, and a template's output no longer depends on the
  order of renders.

### Security

- **The static handler no longer lists the static root.** A GET of the
  strip prefix itself, such as `/static/`, or `/static` when the prefix
  was given as `/static`, reached `http.FileServer` and answered with a
  listing of the static root's contents. It now gets 404, as any
  directory does; a root that holds an `index.html` gets 404 too.
- **Static files stay inside their root.** The service serves
  `ui.staticdir` through an `os.Root`, so no path or symlink reaches a
  file outside it, where `http.Dir` followed symlinks out of it; give
  `StaticHandler` an `os.Root`'s FS for the same. Dotfiles such as `.env` and `.git/config` are not
  served unless asked for, and `nosniff` keeps a browser from running a
  file as another type.
- **Requires Go 1.27.3.** The `go` directive moves from 1.27 to 1.27.3,
  the first 1.27 release with the fix for GO-2026-6809 (CVE-2026-111943):
  `crypto/x509` now rejects intermediate certificates known to be
  compromised. A build with an older toolchain switches to 1.27.3 under
  `GOTOOLCHAIN=auto` and fails under `GOTOOLCHAIN=local`.

## [0.5.6] - 2026-10-19

The tags `ui/v0.4.1` to `ui/v0.5.6` shipped their changes under
`[Unreleased]`. This section holds them, and covers every change since
0.3.0, which was released from the separate
`github.com/gasmod/gas-ui` repository.

### Changed

- **BREAKING: import path.** The module moved from
  `github.com/gasmod/gas-ui` to `github.com/gasmod/gas/ui`, in
  ui/v0.4.1.
- **BREAKING: Go 1.27 is required.** The `go` directive moves from 1.26.1
  to 1.27, in ui/v0.5.0, as core now uses generic methods.
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

## 0.3.0 - 2026-07-02

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

[Unreleased]: https://github.com/gasmod/gas/compare/ui/v0.5.6...HEAD
[0.5.6]: https://github.com/gasmod/gas/releases/tag/ui/v0.5.6
