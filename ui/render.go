package ui

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"

	"github.com/gasmod/gas"
)

// Engine compiles and caches templates from a [gas.TemplateProvider].
//
// Template directory convention:
//
//	layouts/   — base layout templates (parsed into every page render)
//	partials/  — reusable fragments (parsed into every page render)
//	*          — page templates (each one combined with layouts + partials)
//
// A layout file typically defines an entry point template (default name "base")
// using {{define "base"}} and declares blocks with {{block "content" .}}.
// Page templates override those blocks with {{define "content"}}.
type Engine struct {
	provider gas.TemplateProvider
	funcMap  template.FuncMap
	logger   gas.Logger
	cache    map[string]*template.Template
	layout   string
	layouts  []namedContent
	partials []namedContent
	pages    []string
	mu       sync.RWMutex
	devMode  bool
	built    bool
}

type namedContent struct {
	name    string
	content []byte
}

// NewEngine creates a template engine backed by a [gas.TemplateProvider].
func NewEngine(provider gas.TemplateProvider, funcMap template.FuncMap, layout string, devMode bool, logger gas.Logger) *Engine {
	if layout == "" {
		layout = "base"
	}
	return &Engine{
		provider: provider,
		funcMap:  funcMap,
		layout:   layout,
		devMode:  devMode,
		cache:    make(map[string]*template.Template),
		logger:   logger,
	}
}

// AddFuncs merges additional template functions into the engine's funcmap
// and invalidates the cache so the next Render triggers a rebuild.
func (e *Engine) AddFuncs(funcs map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, v := range funcs {
		if _, ok := e.funcMap[k]; ok {
			e.logger.Warn("template function already exists, overwriting").Str("function", k).Send()
		}
		e.funcMap[k] = v
	}
	e.built = false
}

// Build loads all templates from the provider, classifies them, and compiles
// page templates into cached *template.Template instances.
func (e *Engine) Build() error {
	ctx := context.Background()

	names, err := e.provider.List(ctx)
	if err != nil {
		return fmt.Errorf("ui: listing templates: %w", err)
	}

	e.layouts = e.layouts[:0]
	e.partials = e.partials[:0]
	e.pages = e.pages[:0]

	for _, name := range names {
		content, getErr := e.provider.Get(ctx, name)
		if getErr != nil {
			return fmt.Errorf("ui: loading template %q: %w", name, getErr)
		}
		e.classify(namedContent{name: name, content: content})
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Clear existing cache.
	e.cache = make(map[string]*template.Template, len(e.pages))

	for _, page := range e.pages {
		t, compileErr := e.compile(page)
		if compileErr != nil {
			return compileErr
		}
		key := e.pageKey(page)
		e.cache[key] = t
	}
	e.built = true
	return nil
}

// Render executes the named page template, writing the result to w with a
// 200 status code. The name is the page path without extension
// (e.g. "home", "dashboard/index").
func (e *Engine) Render(w http.ResponseWriter, name string, data any) error {
	return e.RenderWithStatus(w, http.StatusOK, name, data)
}

// RenderWithStatus is like Render but sets the HTTP status code first.
func (e *Engine) RenderWithStatus(w http.ResponseWriter, status int, name string, data any) error {
	t, err := e.lookup(name)
	if err != nil {
		return err
	}

	// Render into a buffer so a template error doesn't produce partial output.
	var buf bytes.Buffer
	entry := e.layout
	if len(e.layouts) == 0 {
		entry = "__page__"
	}
	if execErr := t.ExecuteTemplate(&buf, entry, data); execErr != nil {
		return fmt.Errorf("ui: executing template %q: %w", name, execErr)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, writeErr := buf.WriteTo(w); writeErr != nil {
		return fmt.Errorf("ui: writing response for %q: %w", name, writeErr)
	}
	return nil
}

// RenderFragment renders the page template's own content, bypassing the
// layout. Useful for HTMX partial responses.
func (e *Engine) RenderFragment(w http.ResponseWriter, name string, data any) error {
	t, err := e.lookup(name)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	if execErr := t.ExecuteTemplate(&buf, "__page__", data); execErr != nil {
		return fmt.Errorf("ui: executing fragment %q: %w", name, execErr)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, writeErr := buf.WriteTo(w); writeErr != nil {
		return fmt.Errorf("ui: writing fragment response for %q: %w", name, writeErr)
	}
	return nil
}

// lookup returns the compiled template for the given page name. On the first
// call it triggers Build() lazily so that template funcs registered by other
// services during Init() are available. In dev mode templates are rebuilt on
// every call.
func (e *Engine) lookup(name string) (*template.Template, error) {
	e.mu.RLock()
	needsBuild := !e.built || e.devMode
	e.mu.RUnlock()

	if needsBuild {
		if err := e.Build(); err != nil {
			return nil, err
		}
	}

	e.mu.RLock()
	t, ok := e.cache[name]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("ui: template %q not found", name)
	}
	return t, nil
}

// classify sorts a template into layouts, partials, or pages based on its
// name prefix.
func (e *Engine) classify(nc namedContent) {
	switch {
	case strings.HasPrefix(nc.name, "layouts/"):
		e.layouts = append(e.layouts, nc)
	case strings.HasPrefix(nc.name, "partials/"):
		e.partials = append(e.partials, nc)
	default:
		e.pages = append(e.pages, nc.name)
	}
}

// compile builds a single *template.Template for a page by combining
// layouts + partials + the page itself.
func (e *Engine) compile(page string) (*template.Template, error) {
	t := template.New("__page__").Funcs(e.funcMap)

	for _, l := range e.layouts {
		if _, err := t.Parse(string(l.content)); err != nil {
			return nil, fmt.Errorf("ui: parsing layout %q: %w", l.name, err)
		}
	}
	for _, p := range e.partials {
		if _, err := t.Parse(string(p.content)); err != nil {
			return nil, fmt.Errorf("ui: parsing partial %q: %w", p.name, err)
		}
	}

	content, err := e.provider.Get(context.Background(), page)
	if err != nil {
		return nil, fmt.Errorf("ui: loading page %q: %w", page, err)
	}
	if _, err := t.Parse(string(content)); err != nil {
		return nil, fmt.Errorf("ui: parsing page %q: %w", page, err)
	}
	return t, nil
}

// pageKey strips the .html extension to produce the key callers use in
// Render (e.g. "home.html" → "home", "dashboard/index.html" → "dashboard/index").
func (e *Engine) pageKey(name string) string {
	if strings.HasSuffix(name, ".html") {
		return name[:len(name)-5]
	}
	return name
}
