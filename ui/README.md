# gas/ui

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/ui.svg)](https://pkg.go.dev/github.com/gasmod/gas/ui) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=ui/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Template rendering, static file serving, and UI infrastructure for the [Gas](https://github.com/gasmod/gas) ecosystem.
Implements `gas.UIProvider` on top of a pluggable `gas.TemplateProvider` backend — filesystem, database, memory, or
composite (see [gas/template](https://github.com/gasmod/gas/template)).

## What It Does

`gas/ui` is a Gas service that provides:

- **Template rendering** using Go's `html/template` with layout/partial support
- **Static file serving** with directory listing blocked
- **Pluggable template backends** via `gas.TemplateProvider` — filesystem, database, memory, or composite (see [gas/template](https://github.com/gasmod/gas/template))
- **Template function registration** — other services can contribute helpers via `RegisterFuncs`
- **`gas.UIProvider` interface** — other services accept this for rendering without importing gas/ui

## Installation

```bash
go get github.com/gasmod/gas/ui
```

## Quick Start

```go
package main

import (
	"github.com/gasmod/gas"
	tmpl "github.com/gasmod/gas/template/fs"
	ui "github.com/gasmod/gas/ui"
)

func main() {
	app := gas.NewApp(
		gas.WithSingletonService[gas.TemplateProvider](tmpl.NewStore("templates")),
		gas.WithSingletonService[*ui.Service](
			ui.New[gas.TemplateProvider](ui.WithConfig(&ui.Config{
				UI: ui.Settings{
					StaticDir:  "static",
					StaticPath: "/static/*",
					LayoutName: "base",
				},
			})),
		),
	)
	app.Run()
}
```

## Template Directory Convention

```
templates/
  layouts/
    base.html           <-- parsed into every render, defines the entry point
  partials/
    header.html         <-- parsed into every render, available via {{template}}
    footer.html
  home.html             <-- page template, rendered with Render("home", data)
  about.html
  dashboard/
    index.html          <-- nested pages work too: Render("dashboard/index", data)
```

- **`layouts/`** — Base layout templates. Define the HTML skeleton with `{{define "base"}}` and declare blocks with
  `{{block "content" .}}`.
- **`partials/`** — Reusable fragments. Define named templates with `{{define "nav"}}` and use them anywhere with
  `{{template "nav" .}}`.
- **Everything else** — Page templates. Override blocks defined in the layout.

## Template Examples

**`layouts/base.html`**

```html
{{define "base"}}
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>{{block "title" .}}My App{{end}}</title>
    <link rel="stylesheet" href="/static/css/style.css">
</head>
<body>
{{template "nav" .}}
<main>{{block "content" .}}{{end}}</main>
{{template "footer" .}}
</body>
</html>
{{end}}
```

**`partials/nav.html`**

```html
{{define "nav"}}
<nav>
    <a href="/">Home</a>
    <a href="/about">About</a>
</nav>
{{end}}
```

**`partials/footer.html`**

```html
{{define "footer"}}
<footer>&copy; 2026 {{.SiteName}}</footer>
{{end}}
```

**`home.html`**

```html
{{define "title"}}Home - My App{{end}}

{{define "content"}}
<h1>Welcome, {{.Name}}!</h1>
<p>{{.Message}}</p>
{{end}}
```

**Rendering in a handler:**

```go
func (s *Service) handleHome(w http.ResponseWriter, r *http.Request) {
	s.ui.Render(w, "home", map[string]any{
		"SiteName": "My App",
		"Name":     "Ahmed",
		"Message":  "Hello from Gas.",
	})
}
```

## Rendering Without a Layout

Page templates that don't use `{{define}}` blocks render standalone — no layout wrapping:

**`widget.html`**

```html

<div class="widget">{{.Label}}: {{.Value}}</div>
```

```go
uiSvc.Render(w, "widget", map[string]any{"Label": "Count", "Value": 42})
// Output: <div class="widget">Count: 42</div>
```

## HTMX Fragment Rendering

`RenderFragment` renders a page template without the layout wrapper. Useful for HTMX partial responses:

```go
// Full page render (with layout):
uiSvc.Render(w, "users/list", data)

// Fragment render (no layout, just the page content):
uiSvc.RenderFragment(w, "users/list", data)
```

## Passing gas/ui to Other Services

`gas/ui` satisfies the `gas.UIProvider` interface. Other services receive it through DI constructor injection without
importing gas/ui:

```go
// gas/blog receives gas.UIProvider via DI — no import of gas/ui needed
type Service struct {
	ui gas.UIProvider
}

func New(ui gas.UIProvider) *Service {
	return &Service{ui: ui}
}
```

If your service needs both standard rendering and fragment rendering, `gas.UIProvider` supports both:

```go
func (s *Service) handleHTMX(w http.ResponseWriter, r *http.Request) {
    if r.Header.Get("HX-Request") == "true" {
        s.ui.RenderFragment(w, "content", data)
        return
    }
    s.ui.Render(w, "content", data)
}
```

Registration:

```go
app := gas.NewApp(
	gas.WithSingletonService[gas.TemplateProvider](tmpl.NewStore("templates")),
	gas.WithSingletonService[*ui.Service](
		ui.New[gas.TemplateProvider](),
	),
	gas.WithSingletonService[*blog.Service](
		blog.New,
	),
)
```

Inside gas/blog:

```go
func (s *Service) handlePost(w http.ResponseWriter, r *http.Request) {
	post := s.getPost(r)
	s.ui.Render(w, "blog/post", map[string]any{
		"Title": post.Title,
		"Body":  post.Body,
	})
}
```

## Registering Template Functions from Other Services

Services can contribute template helpers via `RegisterFuncs`. Templates are built lazily on the first render, so funcs
registered during `Init()` are available.

```go
// gas/blog/service.go
func (s *Service) Init() error {
	s.ui.RegisterFuncs(template.FuncMap{
		"formatDate": func(t time.Time) string {
			return t.Format("January 2, 2006")
		},
		"markdownToHTML": s.renderMarkdown,
	})
	return nil
}
```

Templates can now use them:

```html
{{define "content"}}
<article>
    <time>{{formatDate .PublishedAt}}</time>
    {{safe (markdownToHTML .Body)}}
</article>
{{end}}
```

## Registering Templates from Other Services

Services can contribute their own templates — layouts, partials, and pages — by registering them with the
`gas.TemplateProvider`. Templates are classified by name prefix: `layouts/` for layouts, `partials/` for partials,
everything else is a page.

### Single template

```go
tp.Register("partials/blog-card.html", []byte(`{{define "blog-card"}}...{{end}}`))
tp.Register("blog/post.html", []byte(`{{define "content"}}...{{end}}`))
```

### Embedded fs.FS (recommended for services)

Services can embed their templates with `//go:embed` and register the entire tree via the template provider:

```go
// gas/blog/service.go
package blog

import (
	"embed"
	"io/fs"

	"github.com/gasmod/gas"
)

//go:embed templates
var blogTemplates embed.FS

type Service struct {
	tp gas.TemplateProvider
	ui gas.UIProvider
}

func (s *Service) Init() error {
	sub, _ := fs.Sub(blogTemplates, "templates")
	s.tp.RegisterFS(sub)
	return nil
}
```

With a directory like:

```
gas/blog/
  templates/
    partials/
      blog-card.html    <-- available as {{template "blog-card" .}} in all templates
    blog/
      post.html         <-- renderable with Render("blog/post", data)
      index.html
```

## Embedded Static Files

By default, static files are served from a directory on disk (`UI.StaticDir`). To serve embedded static files — for
example when shipping a single binary — use `WithStaticFS`:

```go
package main

import (
	"embed"
	"io/fs"

	"github.com/gasmod/gas"
	ui "github.com/gasmod/gas/ui"
)

//go:embed static
var staticFiles embed.FS

func main() {
	sub, _ := fs.Sub(staticFiles, "static")

	app := gas.NewApp(
		// ... template provider registration ...
		gas.WithSingletonService[*ui.Service](
			ui.New[gas.TemplateProvider](ui.WithStaticFS(sub)),
		),
	)
	// ...
}
```

When `WithStaticFS` is set, `UI.StaticDir` is ignored and its directory validation is skipped.

You can also use `StaticHandlerFS` directly if you need a standalone handler without the service:

```go
mux.Handle("/static/", ui.StaticHandlerFS("/static/", sub))
```

## Template Provider

gas/ui receives its template backend via DI as a `gas.TemplateProvider`. Template storage and retrieval is fully
decoupled from rendering. Implementations are provided by the [gas/template](https://github.com/gasmod/gas/template)
package:

- **`fs.Store`** — filesystem-backed, sandboxed via `os.Root`
- **`memory.Store`** — pure in-memory
- **`db.Store`** — database-backed (PostgreSQL, MySQL, SQLite)
- **`composite.Store`** — chains multiple providers

To use a custom template provider, implement `gas.TemplateProvider` and register it in the DI container. The type
parameter on `New[T]` ensures the DI container resolves the correct concrete type:

```go
gas.WithSingletonService[*MyCustomStore](NewMyCustomStore),
gas.WithSingletonService[*ui.Service](ui.New[*MyCustomStore]()),
```

## Built-in Template Functions

| Function        | Signature                     | Description                                   |
|-----------------|-------------------------------|-----------------------------------------------|
| `safe`          | `(string) HTML`               | Mark string as trusted HTML                   |
| `safeAttr`      | `(string) HTMLAttr`           | Mark string as trusted attribute              |
| `safeURL`       | `(string) URL`                | Mark string as trusted URL                    |
| `upper`         | `(string) string`             | Uppercase                                     |
| `lower`         | `(string) string`             | Lowercase                                     |
| `title`         | `(string) string`             | Title case                                    |
| `trimSpace`     | `(string) string`             | Trim whitespace                               |
| `contains`      | `(s, substr) bool`            | String contains                               |
| `hasPrefix`     | `(s, prefix) bool`            | String has prefix                             |
| `hasSuffix`     | `(s, suffix) bool`            | String has suffix                             |
| `replace`       | `(s, old, new) string`        | Replace all occurrences                       |
| `join`          | `([]string, sep) string`      | Join strings                                  |
| `split`         | `(s, sep) []string`           | Split string                                  |
| `truncate`      | `(n int, s string) string`    | Truncate to n chars with `...`                |
| `now`           | `() time.Time`                | Current time                                  |
| `formatTime`    | `(layout, time) string`       | Format a time value                           |
| `formatTimePtr` | `(layout, *time.Time) string` | Format a pointer to time; returns `""` if nil |
| `add`           | `(a, b int) int`              | Addition                                      |
| `sub`           | `(a, b int) int`              | Subtraction                                   |
| `dict`          | `(pairs ...any) map`          | Create a map from key-value pairs             |
| `list`          | `(items ...any) []any`        | Create a slice                                |
| `json`          | `(any) json.RawMessage`       | Marshal to JSON                               |
| `buildId`       | `() string`                   | Stable build ID; fresh UUID per call in dev   |

**`dict` is especially useful for passing data to partials:**

```html
{{template "user-card" dict "Name" .UserName "Role" "admin"}}
```

## Configuration

```go
ui.Config{
	FuncMap template.FuncMap  // Additional template functions
	UI      ui.Settings{
		StaticDir       string    // Root directory for static files (empty = disabled)
		StaticPath      string    // URL route pattern for static assets (default: "/static/*")
		StaticPaths     []string  // Multiple URL route patterns; overrides StaticPath when set
		StaticStripPrefix string  // URL prefix to strip before file lookup (empty = no stripping)
		LayoutName      string    // Entry-point template name (default: "base")
	}
}
```

### Static file serving configuration

Static file serving has three independent concerns:

| Setting                         | Purpose                                               | Example                              |
|---------------------------------|-------------------------------------------------------|--------------------------------------|
| `StaticDir` (or `WithStaticFS`) | **What** to serve — the source directory or FS        | `"static/"`                          |
| `StaticPath` / `StaticPaths`    | **Where** to serve — the URL route pattern(s)         | `"/static/*"`, `["/css/*", "/js/*"]` |
| `StaticStripPrefix`             | **What to strip** — prefix removed before file lookup | `"/static/"`                         |

- `StaticPath` registers a single route. `StaticPaths` registers multiple routes. When `StaticPaths` is set, `StaticPath`
  is ignored.
- If both `StaticPath` and `StaticPaths` are provided, only `StaticPaths` is used.
- `StaticStripPrefix` controls `http.StripPrefix`. When empty (the default), no prefix is stripped — the request URL
  path is used as-is to look up files in the static directory or FS.

**Examples:**

```go
// FS contains: css/main.css, js/app.js
// Serve at /css/* and /js/*, no stripping needed (FS mirrors URL structure)
ui.Settings{
    StaticPaths: []string{"/css/*", "/js/*"},
}

// FS contains: main.css, app.js (flat, no subdirectories)
// Serve at /static/*, strip "/static/" so "/static/main.css" looks up "main.css"
ui.Settings{
    StaticPath:        "/static/*",
    StaticStripPrefix: "/static/",
}

// FS contains: main.css, app.js (flat)
// Serve at /assets/css/* and /assets/js/*, strip "/assets/" so
// "/assets/css/main.css" looks up "css/main.css"
ui.Settings{
    StaticPaths:       []string{"/assets/css/*", "/assets/js/*"},
    StaticStripPrefix: "/assets/",
}
```

Dev mode is driven by the `GasEnv` embedded field (from `gas/config`). When `GasEnv` is set to a development-like
environment, templates are rebuilt on every request so changes are picked up immediately.

## Test Mock

The `uitest` package provides `MockUI`, a configurable mock of `gas.UIProvider` for unit tests. Each method delegates
to its `Fn` field if set, otherwise is a no-op that returns the zero value without touching the `ResponseWriter`. All
calls are recorded for assertions and the mock is safe for concurrent use.

```go
import "github.com/gasmod/gas/ui/uitest"

mock := &uitest.MockUI{}
mock.RenderFn = func(w http.ResponseWriter, name string, data any) error {
    w.Write([]byte("<h1>Hello</h1>"))
    return nil
}

// inject mock as gas.UIProvider in tests
svc := blog.New(mock)
svc.handlePost(rec, req)

mock.CallCount("Render") // 1
mock.Calls[0].Args       // ["blog/post", map[...]]
mock.Reset()             // clear recorded calls
```

## DI Registration

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

The `New[T]` function is generic over the template provider type and returns a curried DI constructor. The container
injects the template provider, `*gas.Router`, `gas.ConfigProvider`, and `gas.Logger` automatically.
Configuration options like `WithConfig` and `WithStaticFS` are captured in the outer closure.

The service logs structured diagnostics throughout its lifecycle: errors at each `Init` failure point, a `DEBUG`
message listing all registered template function names on successful init, and `WARN` messages when a function name
collision is detected via `RegisterFuncs` or when `RegisterFuncs` is called before the engine is ready.

## Readiness

`*Service` implements `gas.ReadyReporter`. `CheckReady` returns an error before `Init` completes (template engine not
built) or after `Close` is called (draining), and nil otherwise — suitable for a Kubernetes `readinessProbe`.
`gas.HealthReporter` is intentionally not implemented, since the service has no external state a process restart
would recover.
