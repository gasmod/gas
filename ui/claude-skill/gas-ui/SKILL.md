---
name: gas-ui
description: >
  Reference documentation for the gas/ui Go package
  (github.com/gasmod/gas/ui) — template rendering, static file serving, and UI
  infrastructure for the Gas ecosystem. Use this skill when writing, reviewing,
  or debugging Go code involving HTML templates, layouts, partials, static
  assets, HTMX fragment rendering, template functions, gas.TemplateProvider
  integration, embedded templates/static via embed.FS, or gas.UIProvider
  integration. Covers the Engine, template directory conventions, built-in
  funcmap, RegisterFuncs for cross-service template function contribution,
  the uitest mock for gas.UIProvider, and DI wiring patterns with generic type
  parameters. Make sure to use this skill
  whenever working with templates, rendering, static files, or UI concerns in
  a Gas application, even if the user doesn't explicitly mention gas/ui — any
  code touching html/template, layouts, partials, gas.UIProvider, or embed.FS
  for UI assets should trigger this skill.
---

# Gas UI Package Reference

Template rendering, static file serving, and UI infrastructure for the Gas
ecosystem. Satisfies `gas.UIProvider` so other services can render HTML without
importing gas/ui directly.

```
import ui "github.com/gasmod/gas/ui"

// template stores used in the examples below
import "github.com/gasmod/gas/template/memory"
import tmpldir "github.com/gasmod/gas/template/dir"
import "github.com/gasmod/gas/template/composite"
import tmplfs "github.com/gasmod/gas/template/fs"
import tmpldb "github.com/gasmod/gas/template/db"
```

## Constructor

```go
func New[T gas.TemplateProvider](opts ...Option) func(T, *gas.Router, gas.ConfigProvider, gas.Logger) (*Service, error)
```

Generic over the template provider type. Captures options, returns a
DI-injectable constructor. The container injects `T` (the template provider),
`*gas.Router`, `gas.ConfigProvider`, and `gas.Logger`
automatically.

The type parameter `T` allows users to provide a custom `TemplateProvider`
implementation so the DI container resolves the correct concrete type:

```go
// Using the standard gas.TemplateProvider interface:
ui.New[gas.TemplateProvider]()

// Using a concrete type for DI resolution:
ui.New[*tmplfs.Store]()
ui.New[*MyCustomStore]()
```

`Init` returns its config binding, validation (`gas/ui: invalid configuration:
<cause>`), and static route errors without logging them. The service logs a
`DEBUG` listing all registered template function names on successful init, and
`WARN` on function-name collisions (`AddFuncs`) or when `RegisterFuncs` is
called before the engine is initialised.

### Options

```go
func WithConfig(cfg *Config) Option           // explicit config, skips ConfigProvider binding
func WithStaticFS(fsys fs.FS) Option          // embedded static files via embed.FS, ignores UI.StaticDir
```

## Service

Implements `gas.Service`, `gas.UIProvider`, and `gas.ReadyReporter`.

### Lifecycle

```go
func (s *Service) Name() string                          // "gas/ui"
func (s *Service) Init(context.Context) error          // binds and validates config, builds template engine, registers static route
func (s *Service) Close(context.Context) error         // marks closed: Render* write 503 and return an error; the static route keeps serving
func (s *Service) CheckReady(ctx context.Context) error  // readiness probe: not-ready pre-Init or post-Close
```

`CheckReady` returns an error before `Init` completes (engine not built) or
after `Close` is called (draining). Returns nil otherwise. Maps to a Kubernetes
`readinessProbe`. `HealthReporter` is intentionally not implemented — the
service has no external state a process restart would recover.

### Rendering (gas.UIProvider)

```go
func (s *Service) Render(w http.ResponseWriter, name string, data any) error
func (s *Service) RenderWithStatus(w http.ResponseWriter, status int, name string, data any) error
func (s *Service) RenderFragment(w http.ResponseWriter, name string, data any) error
func (s *Service) RegisterFuncs(funcs template.FuncMap)
```

`RenderFragment` renders the page's top-level content (the text outside any
`{{define}}`) without the layout. Useful for HTMX partial responses. A page
whose content sits only inside `{{define "content"}}` renders an empty
fragment; to serve one page both ways, put `{{template "content" .}}` at its
top level next to the `{{define "content"}}` block.

`RegisterFuncs` merges template functions into the engine's funcmap and
invalidates the cache. Safe to call during `Init(ctx)` from other services
that depend on `gas.UIProvider`, so the ui service has initialized first;
called before the ui service's `Init`, it logs a warning and drops the
functions.

### Engine access

```go
func (s *Service) Engine() *Engine
```

## Template Provider

gas/ui receives its template backend via DI as a `gas.TemplateProvider`.
Template storage and retrieval is fully decoupled from rendering.
Implementations are provided by the gas/template package (`fs.Store`,
`memory.Store`, `db.Store`, `composite.Store`) or any custom implementation.

Services that need to register templates (pages, partials, layouts) should
depend on `gas.TemplateProvider` and call `Register` or `RegisterFS` directly:

```go
type Service struct {
    tp gas.TemplateProvider
}

// Init is part of gas.Service: the type also needs Name and Close, or the
// container rejects it.
func (s *Service) Init(ctx context.Context) error {
    sub, err := fs.Sub(blogTemplates, "templates")
    if err != nil {
        return err
    }
    return s.tp.RegisterFS(ctx, sub)
}
```

## Template Directory Convention

```
templates/
  layouts/       — base layouts, parsed into every render
    base.html        {{define "base"}} ... {{block "content" .}} ... {{end}}
  partials/      — reusable fragments, parsed into every render
    nav.html         {{define "nav"}} ... {{end}}
    footer.html
  home.html      — page template: Render("home", data)
  dashboard/
    index.html   — nested: Render("dashboard/index", data)
```

- **Layouts** define the HTML skeleton with `{{define "base"}}` and declare
  blocks with `{{block "content" .}}`.
- **Partials** define named templates, usable anywhere with `{{template "nav" .}}`.
- **Pages** override blocks defined in the layout. When the provider holds
  any `layouts/` template, `Render` always executes the layout (`LayoutName`),
  so a page's top-level content outside `{{define}}` blocks is not shown;
  only with no layouts at all do pages render standalone.

## Engine

```go
func NewEngine(provider gas.TemplateProvider, funcMap template.FuncMap, layout string, devMode bool, logger gas.Logger) *Engine

func (e *Engine) Build() error
func (e *Engine) Render(w http.ResponseWriter, name string, data any) error
func (e *Engine) RenderWithStatus(w http.ResponseWriter, status int, name string, data any) error
func (e *Engine) RenderFragment(w http.ResponseWriter, name string, data any) error
func (e *Engine) AddFuncs(funcs map[string]any)
```

In dev mode, templates rebuild on every request for hot reload.

## Static File Serving

```go
// Standalone handlers (no service needed)
// prefix is stripped from request URL before file lookup; pass "" to skip stripping
func StaticHandler(prefix, dir string) http.HandlerFunc
func StaticHandlerFS(prefix string, fsys fs.FS) http.HandlerFunc
```

When using the service, static serving is configured via three independent
settings:

| Setting                      | Purpose                                 | Example                     |
|------------------------------|-----------------------------------------|-----------------------------|
| `StaticDir` / `WithStaticFS` | **What** to serve                       | `"static/"`                 |
| `StaticPath` / `StaticPaths` | **Where** to serve (URL route patterns) | `"/static/*"`, `["/css/*"]` |
| `StaticStripPrefix`          | **What to strip** before file lookup    | `"/static/"`                |

- `StaticPath` registers a single route. `StaticPaths` registers multiple.
  When `StaticPaths` is set, `StaticPath` is ignored.
- If both `StaticPath` and `StaticPaths` are provided, only `StaticPaths` is used.
- `StaticStripPrefix` controls `http.StripPrefix`. When empty (default) it is
  derived from `StaticPath`'s literal part up to the first wildcard
  (`"/static/*"` strips `"/static/"`), so `/static/app.css` serves `app.css`
  from the static root. With `StaticPaths` set and no `StaticStripPrefix`,
  nothing is stripped. Set it to `"/"` to strip nothing, when the FS mirrors
  the URLs (`static/app.css` served at `/static/app.css`).
- Directory listing is blocked.

## Built-in Template Functions

| Function        | Signature                     | Description                                 |
|-----------------|-------------------------------|---------------------------------------------|
| `upper`         | `(string) string`             | Uppercase                                   |
| `lower`         | `(string) string`             | Lowercase                                   |
| `title`         | `(string) string`             | `strings.ToTitle`: every letter, not just the first (`"hello"` gives `"HELLO"`) |
| `trimSpace`     | `(string) string`             | Trim whitespace                             |
| `contains`      | `(s, substr) bool`            | String contains                             |
| `hasPrefix`     | `(s, prefix) bool`            | Has prefix                                  |
| `hasSuffix`     | `(s, suffix) bool`            | Has suffix                                  |
| `replace`       | `(s, old, new) string`        | Replace all                                 |
| `join`          | `([]string, sep) string`      | Join                                        |
| `split`         | `(s, sep) []string`           | Split                                       |
| `truncate`      | `(n int, s string) string`    | Truncate to n bytes (not runes) with `...`  |
| `now`           | `() time.Time`                | Current time                                |
| `formatTime`    | `(layout, time) string`       | Format time                                 |
| `formatTimePtr` | `(layout, *time.Time) string` | Format pointer to time; `""` if nil         |
| `add`           | `(a, b int) int`              | Addition                                    |
| `sub`           | `(a, b int) int`              | Subtraction                                 |
| `dict`          | `(pairs ...any) map`          | Create map from k/v pairs                   |
| `list`          | `(items ...any) []any`        | Create slice                                |
| `json`          | `(any) json.RawMessage`       | Marshal to JSON; nil on a marshal error     |
| `env`           | `() gas.Environment`       | Environment passed to `DefaultFuncMap`      |
| `buildId`       | `() string`                   | Stable build ID; fresh UUID per call in dev |

`DefaultFuncMap` accepts a `gas.Environment` to enable environment-aware
behaviour (e.g. `buildId` returns a fresh UUID per call in dev mode for
cache-busting, vs a stable ID in production).

`dict` is key for passing data to partials:
```html
{{template "user-card" dict "Name" .UserName "Role" "admin"}}
```

## Config

```go
type Config struct {
    gas.WithGasEnv              // dev mode = rebuild on every request
    FuncMap template.FuncMap   // additional template functions (merged on top of defaults)
    UI      Settings
}

type Settings struct {
    StaticDir       string    // root dir for static files, default "static/" (must exist under the working dir unless WithStaticFS); empty = disabled
    StaticPath      string    // URL route pattern, default "/static/*"
    StaticPaths     []string  // multiple URL route patterns; overrides StaticPath when non-empty
    StaticStripPrefix string  // URL prefix stripped before file lookup (empty = StaticPath's literal prefix; "/" = no stripping)
    LayoutName      string    // entry-point template name, default "base"
}

func DefaultConfig() *Config
func (c *Config) Validate(hasStaticFS bool) error
```

## DI Wiring Patterns

Register the service under `gas.UIProvider`, the type consumers depend on;
the container resolves a dependency by its exact registered type. `New` also
needs the `T` registration, a `gas.ConfigProvider`, and a `gas.Logger`.
`WithConfig` uses the config as given, with no defaults filled in (an empty
`LayoutName` fails validation), so build it from `DefaultConfig()`.

### Basic filesystem setup

The container refuses a `*tmpldir.Store` registered directly, by constructor
or as an instance (`gas.ErrInvalidServiceShape`: it declares `Close() error`
but is not a `gas.Service`; KI-033). Build it yourself, register it wrapped
in a `composite.Store`, and close it when the app is done:

```go
dirStore, err := tmpldir.NewStore("templates")()
if err != nil {
    return err
}
defer dirStore.Close()

cfg := ui.DefaultConfig()
cfg.UI.StaticDir = "static"

app := gas.NewApp(
    gas.WithServiceInstance[gas.TemplateProvider](composite.NewStore(dirStore)),
    gas.WithSingletonService[gas.UIProvider](
        ui.New[gas.TemplateProvider](ui.WithConfig(cfg)),
    ),
)
```

### Embedded templates + static (single binary)

```go
//go:embed templates
var templateFiles embed.FS

//go:embed static
var staticFiles embed.FS

sub, err := fs.Sub(templateFiles, "templates")
if err != nil {
    return err
}
staticSub, err := fs.Sub(staticFiles, "static")
if err != nil {
    return err
}

// Register a memory-backed provider and load the embedded FS into it
memStore := memory.NewStore()
if err := memStore.RegisterFS(context.Background(), sub); err != nil {
    return err
}

app := gas.NewApp(
    gas.WithServiceInstance[gas.TemplateProvider](memStore),
    gas.WithSingletonService[gas.UIProvider](
        ui.New[gas.TemplateProvider](ui.WithStaticFS(staticSub)),
    ),
)
```

### Custom template provider (DB-backed themes)

```go
gas.WithSingletonService[*tmpldb.Store](tmpldb.NewStore()),
gas.WithSingletonService[gas.UIProvider](ui.New[*tmpldb.Store]()),
```

### Consuming via gas.UIProvider

```go
type Service struct {
    ui gas.UIProvider
}

func New(ui gas.UIProvider) *Service {
    return &Service{ui: ui}
}

func (s *Service) handlePost(w http.ResponseWriter, r *http.Request) {
    s.ui.Render(w, "blog/post", map[string]any{"Title": post.Title, "Body": post.Body})
}
```

## Test Mock

The `uitest` package provides `MockUI`, a configurable mock of
`gas.UIProvider` for use in unit tests.

```go
import "github.com/gasmod/gas/ui/uitest"
```

### MockUI

```go
type MockUI struct {
    RenderFn           func(w http.ResponseWriter, name string, data any) error
    RenderFragmentFn   func(w http.ResponseWriter, name string, data any) error
    RenderWithStatusFn func(w http.ResponseWriter, status int, name string, data any) error
    RegisterFuncsFn    func(funcs template.FuncMap)
    Calls              []Call
}
```

Each method delegates to its `Fn` field if set, otherwise returns the zero
value without writing to the `ResponseWriter`. All calls are recorded in
`Calls` for assertions. Thread-safe.

| Method                  | Description                                    |
|-------------------------|------------------------------------------------|
| `Reset()`               | Clear all recorded calls                       |
| `CallCount(method) int` | Count calls by method name (e.g. `"Render"`)   |

### Testing with MockUI

```go
mock := &uitest.MockUI{}
mock.RenderFn = func(w http.ResponseWriter, name string, data any) error {
    w.Write([]byte("<h1>Hello</h1>"))
    return nil
}

// inject mock as gas.UIProvider in tests
```

### Service contributing embedded templates

Services register templates via `gas.TemplateProvider`, not via gas/ui:

```go
//go:embed templates
var blogTemplates embed.FS

type Service struct {
    tp gas.TemplateProvider
    ui gas.UIProvider
}

// Init is part of gas.Service: the type also needs Name and Close.
func (s *Service) Init(ctx context.Context) error {
    sub, err := fs.Sub(blogTemplates, "templates")
    if err != nil {
        return err
    }
    if err := s.tp.RegisterFS(ctx, sub); err != nil {
        return err
    }
    s.ui.RegisterFuncs(template.FuncMap{
        "formatDate":    func(t time.Time) string { return t.Format("January 2, 2006") },
        "markdownToHTML": s.renderMarkdown,
    })
    return nil
}
```
