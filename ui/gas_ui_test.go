package ui_test

import (
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/extensions/gasenv"
	"github.com/gasmod/gas/ui"
)

// ---------------------------------------------------------------------------
// test TemplateProvider
// ---------------------------------------------------------------------------

// testProvider is a minimal gas.TemplateProvider backed by an in-memory map.
type testProvider struct {
	templates map[string][]byte
	mu        sync.RWMutex
}

var _ gas.TemplateProvider = (*testProvider)(nil)

func newTestProvider(files map[string]string) *testProvider {
	tp := &testProvider{templates: make(map[string][]byte, len(files))}
	for name, content := range files {
		tp.templates[name] = []byte(content)
	}
	return tp
}

func (tp *testProvider) Get(_ context.Context, name string) ([]byte, error) {
	tp.mu.RLock()
	defer tp.mu.RUnlock()
	data, ok := tp.templates[name]
	if !ok {
		return nil, fmt.Errorf("template %q not found", name)
	}
	return data, nil
}

func (tp *testProvider) List(_ context.Context) ([]string, error) {
	tp.mu.RLock()
	defer tp.mu.RUnlock()
	names := make([]string, 0, len(tp.templates))
	for name := range tp.templates {
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

func (tp *testProvider) Register(_ context.Context, name string, content []byte) error {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.templates[name] = content
	return nil
}

func (tp *testProvider) RegisterFS(ctx context.Context, fsys fs.FS) error {
	return fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".html" {
			return nil
		}
		data, readErr := fs.ReadFile(fsys, path)
		if readErr != nil {
			return readErr
		}
		return tp.Register(ctx, path, data)
	})
}

// ---------------------------------------------------------------------------
// Engine — no layout
// ---------------------------------------------------------------------------

func TestEngine_RenderNoLayout(t *testing.T) {
	tp := newTestProvider(map[string]string{
		"hello.html": "<h1>Hello, {{.Name}}!</h1>",
	})

	e := ui.NewEngine(
		tp,
		ui.DefaultFuncMap(gasenv.Development),
		"base",
		false,
		&gas.NopLogger{},
	)
	if err := e.Build(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	if err := e.Render(w, "hello", map[string]any{"Name": "World"}); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Hello, World!") {
		t.Fatalf("unexpected body: %s", body)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("got status %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Engine — with layout
// ---------------------------------------------------------------------------

func TestEngine_RenderWithLayout(t *testing.T) {
	tp := newTestProvider(map[string]string{
		"layouts/base.html": `{{define "base"}}<!DOCTYPE html><html><body>{{block "content" .}}default{{end}}</body></html>{{end}}`,
		"home.html":         `{{define "content"}}<h1>Home: {{.Title}}</h1>{{end}}`,
	})

	e := ui.NewEngine(
		tp,
		ui.DefaultFuncMap(gasenv.Development),
		"base",
		false,
		&gas.NopLogger{},
	)
	if err := e.Build(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	if err := e.Render(w, "home", map[string]any{"Title": "Welcome"}); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatalf("missing layout wrapper: %s", body)
	}
	if !strings.Contains(body, "Home: Welcome") {
		t.Fatalf("missing page content: %s", body)
	}
}

// ---------------------------------------------------------------------------
// Engine — with partials
// ---------------------------------------------------------------------------

func TestEngine_RenderWithPartials(t *testing.T) {
	tp := newTestProvider(map[string]string{
		"layouts/base.html": `{{define "base"}}<html>{{template "nav" .}}{{block "content" .}}{{end}}</html>{{end}}`,
		"partials/nav.html": `{{define "nav"}}<nav>{{.SiteName}}</nav>{{end}}`,
		"home.html":         `{{define "content"}}<main>{{.Body}}</main>{{end}}`,
	})

	e := ui.NewEngine(
		tp,
		ui.DefaultFuncMap(gasenv.Development),
		"base",
		false,
		&gas.NopLogger{},
	)
	if err := e.Build(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	err := e.Render(w, "home", map[string]any{"SiteName": "MySaaS", "Body": "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<nav>MySaaS</nav>") {
		t.Fatalf("missing partial: %s", body)
	}
	if !strings.Contains(body, "<main>Hello</main>") {
		t.Fatalf("missing page content: %s", body)
	}
}

// ---------------------------------------------------------------------------
// Engine — RenderWithStatus
// ---------------------------------------------------------------------------

func TestEngine_RenderWithStatus(t *testing.T) {
	tp := newTestProvider(map[string]string{
		"error.html": "<p>Not Found</p>",
	})

	e := ui.NewEngine(
		tp,
		ui.DefaultFuncMap(gasenv.Development),
		"base",
		false,
		&gas.NopLogger{},
	)
	if err := e.Build(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	if err := e.RenderWithStatus(w, http.StatusNotFound, "error", nil); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("got status %d, want %d", w.Code, http.StatusNotFound)
	}
}

// ---------------------------------------------------------------------------
// Engine — RenderFragment (no layout wrapping)
// ---------------------------------------------------------------------------

func TestEngine_RenderFragment(t *testing.T) {
	tp := newTestProvider(map[string]string{
		"layouts/base.html": `{{define "base"}}<!DOCTYPE html>{{block "content" .}}{{end}}{{end}}`,
		"card.html":         `<div class="card">{{.Label}}</div>`,
	})

	e := ui.NewEngine(
		tp,
		ui.DefaultFuncMap(gasenv.Development),
		"base",
		false,
		&gas.NopLogger{},
	)
	if err := e.Build(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	if err := e.RenderFragment(w, "card", map[string]any{"Label": "Item"}); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatalf("fragment should not include layout: %s", body)
	}
	if !strings.Contains(body, `<div class="card">Item</div>`) {
		t.Fatalf("unexpected body: %s", body)
	}
}

// ---------------------------------------------------------------------------
// Engine — template not found
// ---------------------------------------------------------------------------

func TestEngine_RenderNotFound(t *testing.T) {
	tp := newTestProvider(map[string]string{
		"home.html": "ok",
	})

	e := ui.NewEngine(
		tp,
		ui.DefaultFuncMap(gasenv.Development),
		"base",
		false,
		&gas.NopLogger{},
	)
	if err := e.Build(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	err := e.Render(w, "missing", nil)
	if err == nil {
		t.Fatal("expected error for missing template")
	}
}

// ---------------------------------------------------------------------------
// Engine — dev mode rebuilds on every render
// ---------------------------------------------------------------------------

func TestEngine_DevModeRebuilds(t *testing.T) {
	tp := newTestProvider(map[string]string{
		"page.html": "<p>v1</p>",
	})

	e := ui.NewEngine(tp, ui.DefaultFuncMap(gasenv.Development), "base", true, &gas.NopLogger{})
	if err := e.Build(); err != nil {
		t.Fatal(err)
	}

	// Render v1.
	w := httptest.NewRecorder()
	if err := e.Render(w, "page", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.Body.String(), "v1") {
		t.Fatalf("expected v1: %s", w.Body.String())
	}

	// Update the template via the provider.
	_ = tp.Register(context.TODO(), "page.html", []byte("<p>v2</p>"))

	// Render again — dev mode should pick up the change.
	w = httptest.NewRecorder()
	if err := e.Render(w, "page", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.Body.String(), "v2") {
		t.Fatalf("expected v2 after rebuild: %s", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Engine — subdirectory page templates
// ---------------------------------------------------------------------------

func TestEngine_SubdirectoryPages(t *testing.T) {
	tp := newTestProvider(map[string]string{
		"layouts/base.html":       `{{define "base"}}[{{block "content" .}}{{end}}]{{end}}`,
		"dashboard/index.html":    `{{define "content"}}dashboard{{end}}`,
		"dashboard/settings.html": `{{define "content"}}settings{{end}}`,
	})

	e := ui.NewEngine(
		tp,
		ui.DefaultFuncMap(gasenv.Development),
		"base",
		false,
		&gas.NopLogger{},
	)
	if err := e.Build(); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		want string
	}{
		{"dashboard/index", "dashboard"},
		{"dashboard/settings", "settings"},
	} {
		w := httptest.NewRecorder()
		if err := e.Render(w, tc.name, nil); err != nil {
			t.Fatalf("render %q: %v", tc.name, err)
		}
		if !strings.Contains(w.Body.String(), tc.want) {
			t.Fatalf("render %q: got %q, want %q", tc.name, w.Body.String(), tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// FuncMap
// ---------------------------------------------------------------------------

func TestFuncMap_Dict(t *testing.T) {
	tp := newTestProvider(map[string]string{
		"test.html": `{{$d := dict "a" "1" "b" "2"}}{{$d.a}}-{{$d.b}}`,
	})

	e := ui.NewEngine(
		tp,
		ui.DefaultFuncMap(gasenv.Development),
		"base",
		false,
		&gas.NopLogger{},
	)
	if err := e.Build(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	if err := e.Render(w, "test", nil); err != nil {
		t.Fatal(err)
	}
	if w.Body.String() != "1-2" {
		t.Fatalf("got %q", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Static handler
// ---------------------------------------------------------------------------

func TestStaticHandler(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "style.css"), []byte("body{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := ui.StaticHandler("/static/", dir)

	req := httptest.NewRequest("GET", "/static/style.css", nil)
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got status %d", w.Code)
	}
	if w.Body.String() != "body{}" {
		t.Fatalf("got body %q", w.Body.String())
	}
}

func TestStaticHandler_DirectoryListingBlocked(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}

	h := ui.StaticHandler("/static/", dir)

	req := httptest.NewRequest("GET", "/static/sub/", nil)
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for directory listing, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Service lifecycle
// ---------------------------------------------------------------------------

func TestService_InitAndRender(t *testing.T) {
	tp := newTestProvider(map[string]string{
		"layouts/base.html": `{{define "base"}}<!DOCTYPE html>{{block "content" .}}{{end}}{{end}}`,
		"index.html":        `{{define "content"}}<h1>{{.Greeting}}</h1>{{end}}`,
	})

	svc := ui.New[*testProvider](
		ui.WithConfig(ui.DefaultConfig()),
	)(
		tp,
		nil,
		nil,
		&gas.NopLogger{},
	)
	if err := svc.Init(t.Context()); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	if err := svc.Render(w, "index", map[string]any{"Greeting": "Hi"}); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<h1>Hi</h1>") {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestService_ClosedReturns503(t *testing.T) {
	tp := newTestProvider(map[string]string{
		"home.html": "ok",
	})

	svc := ui.New[*testProvider](
		ui.WithConfig(ui.DefaultConfig()),
	)(
		tp,
		nil,
		nil,
		&gas.NopLogger{},
	)
	if err := svc.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := svc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	err := svc.Render(w, "home", nil)
	if err == nil {
		t.Fatal("expected error after close")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got status %d, want 503", w.Code)
	}
}

func TestService_RegisterFuncs(t *testing.T) {
	tp := newTestProvider(map[string]string{
		"page.html": `{{greet "World"}}`,
	})

	svc := ui.New[*testProvider](
		ui.WithConfig(ui.DefaultConfig()),
	)(
		tp,
		nil,
		nil,
		&gas.NopLogger{},
	)
	if err := svc.Init(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Register a new func after Init — templates rebuild lazily on next Render.
	svc.RegisterFuncs(template.FuncMap{
		"greet": func(name string) string { return "Hi, " + name + "!" },
	})

	w := httptest.NewRecorder()
	if err := svc.Render(w, "page", nil); err != nil {
		t.Fatal(err)
	}
	if w.Body.String() != "Hi, World!" {
		t.Fatalf("got %q", w.Body.String())
	}
}

func TestService_CheckReady(t *testing.T) {
	tp := newTestProvider(map[string]string{"home.html": "ok"})

	svc := ui.New[*testProvider](
		ui.WithConfig(ui.DefaultConfig()),
	)(
		tp,
		nil,
		nil,
		&gas.NopLogger{},
	)

	// Not ready before Init — engine not yet built.
	if err := svc.CheckReady(context.Background()); err == nil {
		t.Fatal("expected not-ready before Init")
	}

	if err := svc.Init(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Ready after Init.
	if err := svc.CheckReady(context.Background()); err != nil {
		t.Fatalf("expected ready after Init, got: %v", err)
	}

	// Not ready after Close (draining).
	if err := svc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckReady(context.Background()); err == nil {
		t.Fatal("expected not-ready after Close")
	}
}

func TestService_Name(t *testing.T) {
	tp := newTestProvider(nil)
	svc := ui.New[*testProvider]()(tp, nil, nil, &gas.NopLogger{})
	if svc.Name() != "gas/ui" {
		t.Fatalf("got %q", svc.Name())
	}
}

// ---------------------------------------------------------------------------
// Engine: concurrency
// ---------------------------------------------------------------------------

// In dev mode every render rebuilds the templates, so concurrent renders
// rebuild concurrently. Each must still render its own page through the
// layout and partial, without a data race.
func TestEngine_ConcurrentDevModeRenders(t *testing.T) {
	t.Parallel()

	tp := newTestProvider(map[string]string{
		"layouts/base.html":  `{{define "base"}}<main>{{block "content" .}}{{end}}</main>{{end}}`,
		"partials/nav.html":  `{{define "nav"}}<nav>nav</nav>{{end}}`,
		"home.html":          `{{define "content"}}{{template "nav"}}<p>home</p>{{end}}`,
		"dashboard/top.html": `{{define "content"}}{{template "nav"}}<p>top</p>{{end}}`,
	})
	e := ui.NewEngine(tp, ui.DefaultFuncMap(gasenv.Development), "base", true, &gas.NopLogger{})

	pages := map[string]string{"home": "<p>home</p>", "dashboard/top": "<p>top</p>"}
	var wg sync.WaitGroup
	errs := make(chan error, 160)
	for i := range 8 {
		wg.Go(func() {
			for j := range 20 {
				name := "home"
				if (i+j)%2 == 1 {
					name = "dashboard/top"
				}
				w := httptest.NewRecorder()
				if err := e.Render(w, name, nil); err != nil {
					errs <- fmt.Errorf("Render(%q): %w", name, err)
					continue
				}
				body := w.Body.String()
				if !strings.Contains(body, "<main>") || !strings.Contains(body, "<nav>nav</nav>") ||
					!strings.Contains(body, pages[name]) {
					errs <- fmt.Errorf("Render(%q) body = %q", name, body)
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// AddFuncs while pages render must not race with the rebuild it triggers.
func TestEngine_AddFuncsDuringRenders(t *testing.T) {
	t.Parallel()

	tp := newTestProvider(map[string]string{"page.html": `<p>{{greet}}</p>`})
	funcs := ui.DefaultFuncMap(gasenv.Production)
	funcs["greet"] = func() string { return "hi" }
	e := ui.NewEngine(tp, funcs, "base", false, &gas.NopLogger{})

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 50 {
				w := httptest.NewRecorder()
				if err := e.Render(w, "page", nil); err != nil {
					t.Errorf("Render: %v", err)
					return
				}
			}
		})
	}
	wg.Go(func() {
		for i := range 50 {
			e.AddFuncs(map[string]any{fmt.Sprintf("extra%d", i): func() string { return "" }})
		}
	})
	wg.Wait()
}

// A configuration that fails validation is returned with the service name,
// so the error is identifiable without a log line.
func TestService_InvalidConfigNamesService(t *testing.T) {
	cfg := ui.DefaultConfig()
	cfg.UI.LayoutName = ""
	svc := ui.New[*testProvider](ui.WithConfig(cfg))(
		newTestProvider(map[string]string{"home.html": "ok"}), nil, nil, &gas.NopLogger{},
	)
	err := svc.Init(t.Context())
	if err == nil || !strings.HasPrefix(err.Error(), "gas/ui: invalid configuration: ") {
		t.Errorf("Init() error = %v, want it prefixed with the service name", err)
	}
}

// newStaticService starts a ui service over fsys with cfg and returns the
// router it registered its static route on.
func newStaticService(t *testing.T, cfg *ui.Config, fsys fs.FS) *gas.Router {
	t.Helper()
	router := gas.NewRouter()
	svc := ui.New[*testProvider](ui.WithConfig(cfg), ui.WithStaticFS(fsys))(
		newTestProvider(map[string]string{"home.html": "ok"}),
		router,
		nil,
		&gas.NopLogger{},
	)
	if err := svc.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	router.Seal()
	return router
}

func getBody(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr.Code, rr.Body.String()
}

// With the defaults, StaticStripPrefix defaults to StaticPath's literal
// prefix, so /static/app.css serves app.css from the root of the static FS.
func TestService_StaticDefaultStripsStaticPath(t *testing.T) {
	fsys := fstest.MapFS{
		"app.css":        &fstest.MapFile{Data: []byte("root")},
		"static/app.css": &fstest.MapFile{Data: []byte("nested")},
	}
	router := newStaticService(t, ui.DefaultConfig(), fsys)

	code, body := getBody(t, router, "/static/app.css")
	if code != http.StatusOK || body != "root" {
		t.Errorf("GET /static/app.css = %d %q, want 200 %q", code, body, "root")
	}
}

// An explicit StaticStripPrefix still wins over the default.
func TestService_StaticExplicitStripPrefix(t *testing.T) {
	fsys := fstest.MapFS{
		"assets/app.css": &fstest.MapFile{Data: []byte("assets")},
	}
	cfg := ui.DefaultConfig()
	cfg.UI.StaticPath = "/assets/*"
	cfg.UI.StaticStripPrefix = "/"
	router := newStaticService(t, cfg, fsys)

	code, body := getBody(t, router, "/assets/app.css")
	if code != http.StatusOK || body != "assets" {
		t.Errorf("GET /assets/app.css = %d %q, want 200 %q", code, body, "assets")
	}
}

// With StaticPaths set and no StaticStripPrefix, nothing is stripped, so
// each path maps to the same path in the static FS.
func TestService_StaticPathsKeepFSLayout(t *testing.T) {
	fsys := fstest.MapFS{
		"css/app.css": &fstest.MapFile{Data: []byte("css")},
	}
	cfg := ui.DefaultConfig()
	cfg.UI.StaticPaths = []string{"/css/*"}
	router := newStaticService(t, cfg, fsys)

	code, body := getBody(t, router, "/css/app.css")
	if code != http.StatusOK || body != "css" {
		t.Errorf("GET /css/app.css = %d %q, want 200 %q", code, body, "css")
	}
}
