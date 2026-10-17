package ui

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"maps"
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
//
// mu guards funcMap, cache, hasLayouts, and built. Build reads the provider
// and compiles into local values without holding mu, then swaps the result
// in under it, so renders, AddFuncs, and concurrent rebuilds never see a
// half-built set.
type Engine struct {
	provider   gas.TemplateProvider
	funcMap    template.FuncMap
	logger     gas.Logger
	cache      map[string]*template.Template
	layout     string
	mu         sync.RWMutex
	devMode    bool
	built      bool
	hasLayouts bool
}

// templateSet is the classified content of one Build.
type templateSet struct {
	layouts  []namedContent
	partials []namedContent
	pages    []string
}

type namedContent struct {
	name    string
	content []byte
}

// NewEngine creates a template engine backed by a [gas.TemplateProvider].
func NewEngine(
	provider gas.TemplateProvider,
	funcMap template.FuncMap,
	layout string,
	devMode bool,
	logger gas.Logger,
) *Engine {
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
// page templates into cached *template.Template instances. It is safe to call
// concurrently with renders, AddFuncs, and other Builds. If it fails, the
// previously built templates stay in place.
func (e *Engine) Build() error {
	ctx := context.Background()

	names, err := e.provider.List(ctx)
	if err != nil {
		return fmt.Errorf("ui: listing templates: %w", err)
	}

	var set templateSet
	for _, name := range names {
		content, getErr := e.provider.Get(ctx, name)
		if getErr != nil {
			return fmt.Errorf("ui: loading template %q: %w", name, getErr)
		}
		set.classify(namedContent{name: name, content: content})
	}

	// Compile against a snapshot of the funcmap, so AddFuncs can run while
	// this Build compiles.
	e.mu.RLock()
	funcs := make(template.FuncMap, len(e.funcMap))
	maps.Copy(funcs, e.funcMap)
	e.mu.RUnlock()

	cache := make(map[string]*template.Template, len(set.pages))
	for _, page := range set.pages {
		t, compileErr := e.compile(&set, funcs, page)
		if compileErr != nil {
			return compileErr
		}
		cache[e.pageKey(page)] = t
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.cache = cache
	e.hasLayouts = len(set.layouts) > 0
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
	t, hasLayouts, err := e.lookup(name)
	if err != nil {
		return err
	}

	// Render into a buffer so a template error doesn't produce partial output.
	var buf bytes.Buffer
	entry := e.layout
	if !hasLayouts {
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
	t, _, err := e.lookup(name)
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

// lookup returns the compiled template for the given page name, and whether
// the build that produced it had layouts. On the first call it triggers
// Build() lazily so that template funcs registered by other services during
// Init are available. In dev mode templates are rebuilt on every call.
func (e *Engine) lookup(name string) (*template.Template, bool, error) {
	e.mu.RLock()
	needsBuild := !e.built || e.devMode
	e.mu.RUnlock()

	if needsBuild {
		if err := e.Build(); err != nil {
			return nil, false, err
		}
	}

	e.mu.RLock()
	t, ok := e.cache[name]
	hasLayouts := e.hasLayouts
	e.mu.RUnlock()
	if !ok {
		return nil, false, fmt.Errorf("ui: template %q not found", name)
	}
	return t, hasLayouts, nil
}

// classify sorts a template into layouts, partials, or pages based on its
// name prefix.
func (s *templateSet) classify(nc namedContent) {
	switch {
	case strings.HasPrefix(nc.name, "layouts/"):
		s.layouts = append(s.layouts, nc)
	case strings.HasPrefix(nc.name, "partials/"):
		s.partials = append(s.partials, nc)
	default:
		s.pages = append(s.pages, nc.name)
	}
}

// compile builds a single *template.Template for a page by combining the
// set's layouts + partials + the page itself, with funcs as its funcmap.
func (e *Engine) compile(
	set *templateSet,
	funcs template.FuncMap,
	page string,
) (*template.Template, error) {
	t := template.New("__page__").Funcs(funcs)

	for _, l := range set.layouts {
		if _, err := t.Parse(string(l.content)); err != nil {
			return nil, fmt.Errorf("ui: parsing layout %q: %w", l.name, err)
		}
	}
	for _, p := range set.partials {
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
