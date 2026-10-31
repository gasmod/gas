---
name: gas-ui
description: >
  Reference documentation for the gas/ui Go package
  (github.com/gasmod/gas/ui), template rendering, static file serving, and UI
  infrastructure for the Gas ecosystem. Use this skill when writing, reviewing,
  or debugging Go code involving HTML templates, layouts, partials, static
  assets, HTMX fragment rendering, template functions, gas.TemplateProvider
  integration, embedded templates/static via embed.FS, or ui.Provider
  integration. Covers the Service, template naming conventions, built-in
  funcmap, RegisterFuncs and WithFuncs for template function contribution,
  render options (WithStatus, WithBlock, WithLayout), Reload, StaticHandler,
  the uitest mock for ui.Provider, and DI wiring patterns with generic type
  parameters. Make sure to use this skill
  whenever working with templates, rendering, static files, or UI concerns in
  a Gas application, even if the user doesn't explicitly mention gas/ui; any
  code touching html/template, layouts, partials, ui.Provider, or embed.FS
  for UI assets should trigger this skill.
---

# Gas UI Package Reference

Template rendering, static file serving, and UI infrastructure for the Gas
ecosystem. Satisfies `ui.Provider` so other services can render HTML without
importing the service type. Templates come from a `gas.TemplateProvider`.

```
import ui "github.com/gasmod/gas/ui"

// template stores used in the examples below
import "github.com/gasmod/gas/template/memory"
import tmpldir "github.com/gasmod/gas/template/dir"
import tmplfs "github.com/gasmod/gas/template/fs"
import tmpldb "github.com/gasmod/gas/template/db"
```

## Constructor

```go
func New[T gas.TemplateProvider](opts ...Option) func(T, *gas.Router, gas.ConfigProvider, gas.Logger) (*Service, error)
```

`T` is the type the template store is registered as, usually
`gas.TemplateProvider`; use a concrete type such as `*tmpldb.Store` when the
store is registered under it. A nil logger is `ErrNilLogger`, a nil store
`ErrNilTemplateProvider`; a nil ConfigProvider is accepted (Init then needs
`WithConfig`); a nil router is accepted unless static files are served
(`ErrNilRouter`).

### Options

```go
func WithConfig(cfg *Config) Option          // use this config (a copy, same checks) instead of binding
func WithFuncs(funcs template.FuncMap) Option // add template functions over the defaults; checked in Init
func WithStaticFS(fsys fs.FS) Option          // serve static files from fsys instead of ui.staticdir
```

## Service

Implements `gas.Service`, `ui.Provider`, and `gas.ReadyReporter`.

```go
func (s *Service) Name() string                          // "gas/ui" (ui.ServiceName)
func (s *Service) Init(ctx context.Context) error        // resolves config, checks WithFuncs, opens static files, registers static routes; builds nothing
func (s *Service) Close(context.Context) error           // closes; renders in progress complete; static routes answer 503
func (s *Service) CheckReady(ctx context.Context) error  // runs the first build if none tried; fails only while no usable set exists
func (s *Service) Reload(ctx context.Context) error      // rebuild from the store now; returns the build's error
```

Lifecycle: `ErrNotInitialized` before the first successful Init (every method
but Init, Close, Name), `ErrAlreadyInitialized` from Init while open,
`ErrClosed` after Close until Init succeeds again. Init after Close works.

### Rendering (ui.Provider)

```go
type Provider interface {
    Render(ctx context.Context, w http.ResponseWriter, name string, data any, opts ...RenderOption) error
    RenderFragment(ctx context.Context, w http.ResponseWriter, name string, data any, opts ...RenderOption) error
    RegisterFuncs(funcs template.FuncMap) error
}
```

- The page executes into a buffer first: **on any error nothing is written**
  (no header, status or byte), so return the error and let the error handler
  write the response. A failed write is `ErrWrite`.
- `Content-Type: text/html; charset=utf-8` is set unless the handler set one.
  WriteHeader is called only with `WithStatus`.
- Render options: `WithStatus(code)` (200 to 599, not 204/304, else
  `ErrInvalidStatus`); `WithLayout(name)` (Render only); `WithBlock(name)`
  (RenderFragment only: execute any template of the page's set, else
  `ErrBlockNotFound`). The wrong method or a nil option is
  `ErrInvalidOption`. Take block and layout names from code, never from the
  request (such as HX-Target).
- `RenderFragment` executes the page's `ui.fragmentblock` (`content`) when the
  page defines it, else the page's top-level text, else `ErrNoFragment`. So
  one page serves the full document and the HTMX swap.
- `RegisterFuncs` adds functions from the next render on, checks each, and
  adds none when one fails (`ErrInvalidFunc`). Registered functions outlive
  Close and later Inits. Call it from a dependent's `Init` (the ui service
  is initialized first); before ui's Init it returns `ErrNotInitialized`.

## Names and layout

- Only store names ending in one of `ui.extensions` (default `.html`) are
  read. `layouts/...` are layouts, `partials/...` partials; any other name is
  a page when it starts with `ui.pagesprefix` (default empty).
- A page's name is its store name without the pages prefix and extension:
  `users.html` is `users`, `admin/users.html` is `admin/users`. A bad name is
  `ErrInvalidName`; a valid name that is no page (such as `users.html` or
  `layouts/base`) is `ErrPageNotFound`.
- Each page compiles with every layout and partial, then the page. A page
  overrides a layout's `{{block}}` by defining it.
- `Render` executes `ui.layoutname` (`base`). With no layouts in the store
  and no `WithLayout`, the page's top-level text renders whole; otherwise a
  missing layout is `ErrLayoutNotFound`.

```
templates/
  layouts/base.html     {{define "base"}}<main>{{block "content" .}}{{end}}</main>{{end}}
  partials/nav.html     {{define "nav"}} ... {{end}}
  home.html             {{define "content"}} ... {{end}}  -> Render(ctx, w, "home", data)
  dashboard/index.html  -> Render(ctx, w, "dashboard/index", data)
```

## Builds and Reload

Init builds nothing; the first render (or CheckReady) builds every page.
Outside reload mode the compiled set is kept until a function is registered
or `Reload` runs, so **store changes in production show only after Reload**.
With `ui.reload: true` (development) every render builds anew from the
store. A failed build keeps the previous set; a page that fails to compile
fails only its own renders (`ErrLoad`, `ErrCompile`, `ErrExecute`).

## Static File Serving

```go
func StaticHandler(prefix string, fsys fs.FS, opts ...StaticOption) (http.Handler, error)
// options: WithDotfiles(bool), WithMaxAge(time.Duration), WithErrorFunc(func(*http.Request, error))
```

`prefix` is empty or starts and ends with `/` (else `ErrInvalidPrefix`). It
serves regular files only: 404 for unclean paths, directories (no listing,
no redirect), dotfiles (unless `WithDotfiles(true)`), and files outside the
root; 405 with `Allow: GET, HEAD` for other methods. Every response carries
`X-Content-Type-Options: nosniff`. To serve a directory, pass an `os.Root`'s
FS.

In the service, static files are **off by default** (`ui.staticdir` is
empty). Set `ui.staticdir` (opened with `os.OpenRoot`; must exist) or use
`WithStaticFS`, and Init registers GET routes:

| Setting                         | Purpose                                 | Default      |
|---------------------------------|-----------------------------------------|--------------|
| `StaticDir` / `WithStaticFS`    | What to serve                           | none         |
| `StaticPath` / `StaticPaths`    | URL route patterns (`StaticPaths` wins) | `/static/*`  |
| `StaticStripPrefix`             | Prefix removed before lookup            | derived      |
| `StaticDotfiles`                | Serve dotfiles                          | false        |
| `StaticMaxAge`                  | `Cache-Control: public, max-age=N`      | 0 (none)     |

`StaticStripPrefix` empty derives from `StaticPath` up to the last `/` before
its first `*` or `{` (`/static/*` strips `/static/`); with `StaticPaths` set
and no `StaticStripPrefix`, nothing is stripped.

## Built-in Template Functions

| Function        | Description                                                   |
|-----------------|---------------------------------------------------------------|
| `upper`, `lower`, `trimSpace` | String case and whitespace                      |
| `contains`, `hasPrefix`, `hasSuffix` | String tests                             |
| `replace`       | `strings.ReplaceAll`                                          |
| `join`, `split` | Join / split strings                                          |
| `truncate n s`  | First n **runes** of s plus `...` when longer; error for negative n |
| `now`, `formatTime layout t`, `formatTimePtr layout t` | Time; `formatTimePtr` is empty for nil |
| `add`, `sub`    | Integer arithmetic                                            |
| `dict k v ...`  | Map of string keys; error for odd count or non-string key     |
| `list v ...`    | Slice                                                         |
| `json v`        | v as JSON; error when it cannot be encoded                    |
| `buildId`       | ID of the build: same within a build, new with each           |

There is no `title` and no `env` function. Register your own `title` (such as
one built on `golang.org/x/text/cases`). `WithFuncs` and `RegisterFuncs`
replace a function of the same name; a name must be a Go identifier and a
value a non-nil function returning one value or a value and an error. `html`,
`urlquery` and `_html_template_*` are refused. A function returning
`template.HTML` (or another trusted type) writes unescaped: never return user
data that way.

`dict` is key for passing data to partials:
```html
{{template "user-card" dict "Name" .UserName "Role" "admin"}}
```

## Config

Section `ui`; keys are the lowercased field names (`ui.layoutname`,
`ui.reload`, `ui.staticdir`, ...).

```go
type Config struct {
    UI Settings
}

type Settings struct {
    StaticDir         string        // default "" (no static files)
    StaticPath        string        // default "/static/*"
    StaticStripPrefix string        // empty = derived; else starts and ends with "/"
    PagesPrefix       string        // default ""; else a clean path ending in "/"
    LayoutName        string        `validate:"required"` // default "base"
    FragmentBlock     string        `validate:"required"` // default "content"
    Extensions        []string      // default [".html"]
    StaticPaths       []string      // overrides StaticPath when non-empty
    StaticMaxAge      time.Duration `validate:"min=0"`    // 0 or at least 1s
    StaticDotfiles    bool
    Reload            bool          // build on every render (development)
}

func DefaultConfig() *Config
```

There is no `gas.WithGasEnv` embed, no environment-driven dev mode, no
`FuncMap` config field (use `WithFuncs`) and no `Validate` method. Build a
`WithConfig` config from `DefaultConfig()`.

## DI Wiring Patterns

Register the service under `ui.Provider`, the type consumers depend on; the
container resolves by exact registered type. `New` also needs the `T`
registration, `*gas.Router`, a `gas.ConfigProvider` (unless `WithConfig`), and
a `gas.Logger`.

### Directory templates + static directory

```go
cfg := ui.DefaultConfig()
cfg.UI.StaticDir = "static"
cfg.UI.Reload = true // development

app := gas.NewApp(
    gas.WithSingletonService[gas.TemplateProvider](tmpldir.NewStore("templates")),
    gas.WithSingletonService[ui.Provider](
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

app := gas.NewApp(
    gas.WithSingletonService[gas.TemplateProvider](tmplfs.NewStore(sub)),
    gas.WithSingletonService[ui.Provider](
        ui.New[gas.TemplateProvider](ui.WithStaticFS(staticSub)),
    ),
)
```

### Custom template provider (DB-backed themes)

```go
gas.WithSingletonService[*tmpldb.Store](tmpldb.NewStore()),
gas.WithSingletonService[ui.Provider](ui.New[*tmpldb.Store]()),
```

Call `Reload` after editing rows so production renders see them.

### Consuming via ui.Provider

```go
type Service struct {
    ui ui.Provider
}

func (s *Service) handlePost(c gas.Context) error {
    return s.ui.Render(c, c.ResponseWriter(), "blog/post", map[string]any{"Title": "Hello"})
}

func (s *Service) handlePostFragment(c gas.Context) error {
    return s.ui.RenderFragment(c, c.ResponseWriter(), "blog/post", nil,
        ui.WithStatus(http.StatusAccepted))
}
```

### Service contributing templates and functions

Services register templates via `gas.TemplateProvider`, not via gas/ui. The
memory store suits this (the fs store is read-only):

```go
//go:embed templates
var blogTemplates embed.FS

type Blog struct {
    tp gas.TemplateProvider
    ui ui.Provider
}

func (b *Blog) Name() string                  { return "blog" }
func (b *Blog) Close(context.Context) error   { return nil }
func (b *Blog) Init(ctx context.Context) error {
    sub, err := fs.Sub(blogTemplates, "templates")
    if err != nil {
        return err
    }
    if err := b.tp.RegisterFS(ctx, sub); err != nil {
        return err
    }
    return b.ui.RegisterFuncs(template.FuncMap{
        "formatDate": func(t time.Time) string { return t.Format("January 2, 2006") },
    })
}
```

## Test Mock

```go
import "github.com/gasmod/gas/ui/uitest"

type MockUI struct {
    RenderFn         func(ctx context.Context, w http.ResponseWriter, name string, data any, opts ...ui.RenderOption) error
    RenderFragmentFn func(ctx context.Context, w http.ResponseWriter, name string, data any, opts ...ui.RenderOption) error
    RegisterFuncsFn  func(funcs template.FuncMap) error
}
```

With a field nil, the method writes nothing and returns nil. Each call is
recorded; render options are resolved with `ui.ApplyRenderOptions` into a
`ui.RenderOptions{Layout, Block, Status}`. The mock checks no rule; to test
the rules, build a `ui.Service` over a memory store.

| Method                  | Description                                  |
|-------------------------|----------------------------------------------|
| `Calls() []Call`        | Copy of the recorded calls, in order         |
| `CallCount(method) int` | Count calls by method name (e.g. `"Render"`) |
| `Reset()`               | Forget all recorded calls                    |

Methods may run from several goroutines; setting an `Fn` field while a
method runs is a data race.

```go
mock := &uitest.MockUI{
    RenderFn: func(_ context.Context, w http.ResponseWriter, name string, _ any, _ ...ui.RenderOption) error {
        _, err := io.WriteString(w, "<h1>"+name+"</h1>")
        return err
    },
}
// inject mock as ui.Provider; assert with mock.CallCount("Render")
```
