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
```

## Constructor

```go
func New[T gas.TemplateProvider](opts ...Option) func(T, *gas.Router, gas.ConfigProvider, gas.Logger) *Service
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
ui.New[*fs.Store]()
ui.New[*MyCustomStore]()
```

The service logs structured diagnostics: errors at each `Init` failure point, a
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
func (s *Service) Init() error                           // builds template engine, registers static route
func (s *Service) Close() error                          // marks closed, subsequent Render returns 503
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

`RenderFragment` renders the page template without the layout wrapper. Useful
for HTMX partial responses.

`RegisterFuncs` merges template functions into the engine's funcmap and
invalidates the cache. Safe to call during `Init()` from other services.

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

func (s *Service) Init() error {
    sub, _ := fs.Sub(blogTemplates, "templates")
    s.tp.RegisterFS(sub)
    return nil
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
- **Pages** override blocks defined in the layout. Pages without `{{define}}`
  blocks render standalone (no layout).

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
- `StaticStripPrefix` controls `http.StripPrefix`. When empty (default), no
  prefix is stripped — request URL maps directly to file paths in the FS/dir.
- Directory listing is blocked.

## Built-in Template Functions

| Function        | Signature                     | Description                                 |
|-----------------|-------------------------------|---------------------------------------------|
| `safe`          | `(string) HTML`               | Trusted HTML                                |
| `safeAttr`      | `(string) HTMLAttr`           | Trusted attribute                           |
| `safeURL`       | `(string) URL`                | Trusted URL                                 |
| `upper`         | `(string) string`             | Uppercase                                   |
| `lower`         | `(string) string`             | Lowercase                                   |
| `title`         | `(string) string`             | Title case                                  |
| `trimSpace`     | `(string) string`             | Trim whitespace                             |
| `contains`      | `(s, substr) bool`            | String contains                             |
| `hasPrefix`     | `(s, prefix) bool`            | Has prefix                                  |
| `hasSuffix`     | `(s, suffix) bool`            | Has suffix                                  |
| `replace`       | `(s, old, new) string`        | Replace all                                 |
| `join`          | `([]string, sep) string`      | Join                                        |
| `split`         | `(s, sep) []string`           | Split                                       |
| `truncate`      | `(n int, s string) string`    | Truncate to n chars with `...`              |
| `now`           | `() time.Time`                | Current time                                |
| `formatTime`    | `(layout, time) string`       | Format time                                 |
| `formatTimePtr` | `(layout, *time.Time) string` | Format pointer to time; `""` if nil         |
| `add`           | `(a, b int) int`              | Addition                                    |
| `sub`           | `(a, b int) int`              | Subtraction                                 |
| `dict`          | `(pairs ...any) map`          | Create map from k/v pairs                   |
| `list`          | `(items ...any) []any`        | Create slice                                |
| `json`          | `(any) json.RawMessage`       | Marshal to JSON                             |
| `buildId`       | `() string`                   | Stable build ID; fresh UUID per call in dev |

`DefaultFuncMap` accepts an `env.Environment` to enable environment-aware
behaviour (e.g. `buildId` returns a fresh UUID per call in dev mode for
cache-busting, vs a stable ID in production).

`dict` is key for passing data to partials:
```html
{{template "user-card" dict "Name" .UserName "Role" "admin"}}
```

## Config

```go
type Config struct {
    env.WithGasEnv              // dev mode = rebuild on every request
    FuncMap template.FuncMap   // additional template functions (merged on top of defaults)
    UI      Settings
}

type Settings struct {
    StaticDir       string    // root dir for static files (empty = disabled)
    StaticPath      string    // URL route pattern, default "/static/*"
    StaticPaths     []string  // multiple URL route patterns; overrides StaticPath when non-empty
    StaticStripPrefix string  // URL prefix stripped before file lookup (empty = no stripping)
    LayoutName      string    // entry-point template name, default "base"
}

func DefaultConfig() *Config
func (c *Config) Validate(hasStaticFS bool) error
```

## DI Wiring Patterns

### Basic filesystem setup

```go
app := gas.NewApp(
    gas.WithSingletonService[gas.TemplateProvider](tmpl.NewStore("templates")),
    gas.WithSingletonService[*ui.Service](
        ui.New[gas.TemplateProvider](ui.WithConfig(&ui.Config{
            UI: ui.Settings{
                StaticDir: "static",
            },
        })),
    ),
)
```

### Embedded templates + static (single binary)

```go
//go:embed templates
var templateFiles embed.FS

//go:embed static
var staticFiles embed.FS

sub, _ := fs.Sub(templateFiles, "templates")
staticSub, _ := fs.Sub(staticFiles, "static")

// Register a memory-backed provider and load the embedded FS into it
memStore := memory.NewStore()
memStore.RegisterFS(sub)

app := gas.NewApp(
    gas.WithServiceInstance[gas.TemplateProvider](memStore),
    gas.WithSingletonService[*ui.Service](
        ui.New[gas.TemplateProvider](ui.WithStaticFS(staticSub)),
    ),
)
```

### Custom template provider (DB-backed themes)

```go
gas.WithSingletonService[*db.Store](db.NewStore),
gas.WithSingletonService[*ui.Service](
    ui.New[*db.Store](ui.WithConfig(&ui.Config{
        UI: ui.Settings{StaticDir: "static", LayoutName: "base"},
    })),
),
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

func (s *Service) Init() error {
    sub, _ := fs.Sub(blogTemplates, "templates")
    s.tp.RegisterFS(sub)
    s.ui.RegisterFuncs(template.FuncMap{
        "formatDate":    func(t time.Time) string { return t.Format("January 2, 2006") },
        "markdownToHTML": s.renderMarkdown,
    })
    return nil
}
```
