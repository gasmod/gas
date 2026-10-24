package modulesui_test

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/ui"
)

func render(t *testing.T, p gas.UIProvider, name string, data any) (*httptest.ResponseRecorder, error) {
	t.Helper()
	rr := httptest.NewRecorder()
	return rr, p.Render(rr, name, data)
}

// Docs: modules/ui.mdx#what-it-is-for
// "It reads template source through `gas.TemplateProvider`, so where templates live is the business of gas/template, and it implements `gas.UIProvider`, the core interface the app's services render through."
func TestServiceIsUIProviderOverTemplateProvider(t *testing.T) {
	t.Parallel()
	var _ gas.UIProvider = (*ui.Service)(nil)
	store := layoutStore(t)
	a := startUI(t, store, appOpts{})
	rr, err := render(t, a.ui, "home", nil)
	if err != nil || rr.Body.String() != "<html>home <nav>N</nav></html>" {
		t.Fatalf("Render = %q, %v", rr.Body.String(), err)
	}
	if store.reads() == 0 {
		t.Fatal("template source was not read through the TemplateProvider")
	}
}

// Docs: modules/ui.mdx#wiring
// "`ui.New[T]()` returns a constructor that resolves `T`, `*gas.Router`, `gas.ConfigProvider`, and `gas.Logger` from the container." and "The constructor never fails; its error result is always nil."
func TestNewConstructorShape(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeOf(ui.New[gas.TemplateProvider]())
	want := []reflect.Type{reflect.TypeFor[gas.TemplateProvider](), reflect.TypeFor[*gas.Router](), reflect.TypeFor[gas.ConfigProvider](), reflect.TypeFor[gas.Logger]()}
	if typ.NumIn() != len(want) {
		t.Fatalf("constructor %v, want parameters %v", typ, want)
	}
	for i, w := range want {
		if typ.In(i) != w {
			t.Errorf("parameter %d = %v, want %v", i, typ.In(i), w)
		}
	}
	svc, err := ui.New[gas.TemplateProvider](ui.WithStaticFS(fstest.MapFS{}), ui.WithConfig(nil))(nil, nil, nil, &gas.NopLogger{})
	if err != nil || svc == nil {
		t.Fatalf("constructor with nil dependencies = %v, %v; want a service and no error", svc, err)
	}
}

// customStore is a template provider registered under its concrete type.
type customStore struct{ *countingStore }

// Docs: modules/ui.mdx#wiring
// "`T` is the type the template store is registered under, so `ui.New[gas.TemplateProvider]()` takes whatever store the app registered for that interface."
func TestNewResolvesT(t *testing.T) {
	t.Parallel()
	store := &customStore{newStore(t, map[string]string{"home.html": "custom"})}
	cfg, err := configtest.NewMockConfigWithValues(nil)
	if err != nil {
		t.Fatal(err)
	}
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[*customStore](store),
		gas.WithSingletonService[gas.UIProvider](ui.New[*customStore](ui.WithStaticFS(fstest.MapFS{}))),
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	rr, err := render(t, app.ServiceContainer().MustResolve[gas.UIProvider](), "home", nil)
	if err != nil || rr.Body.String() != "custom" {
		t.Fatalf("Render = %q, %v; want the store registered as *customStore", rr.Body.String(), err)
	}
}

// Docs: modules/ui.mdx#options-and-startup
// "`ui.WithConfig(cfg)` uses `cfg` as the whole configuration and skips the `ConfigProvider`. Start from `ui.DefaultConfig()`. Since nothing is bound, `GasEnv` stays empty unless you set it, which turns development mode off."
func TestWithConfig(t *testing.T) {
	t.Parallel()
	store := newStore(t, map[string]string{"home.html": "v1", "other.html": "x"})
	cfg := ui.DefaultConfig()
	var uiBinds int
	mock := &configtest.MockConfig{BindFn: func(dest any, _ ...config.BindOption) error {
		if _, ok := dest.(*ui.Config); ok {
			uiBinds++
		}
		return nil
	}}
	a := startUI(t, store, appOpts{cfg: mock, ui: []ui.Option{ui.WithStaticFS(fstest.MapFS{}), ui.WithConfig(cfg)}})
	first, _ := render(t, a.ui, "home", nil)
	store.put(t, "home.html", "v2")
	second, _ := render(t, a.ui, "home", nil)
	if first.Body.String() != "v1" || second.Body.String() != "v1" {
		t.Fatalf("renders %q then %q, want the cached v1 (development mode off)", first.Body.String(), second.Body.String())
	}
	if uiBinds != 0 {
		t.Fatalf("the ui config was bound %d times with WithConfig, want 0", uiBinds)
	}
}

// Docs: modules/ui.mdx#options-and-startup
// "When the service starts it binds and validates the configuration, builds the template engine, and registers the static route. It does not read any template yet."
// Docs: modules/ui.mdx#compilation
// "Templates are compiled on the first render, not when the service starts"
func TestStartReadsNoTemplate(t *testing.T) {
	t.Parallel()
	store := layoutStore(t)
	a := startUI(t, store, appOpts{ui: []ui.Option{ui.WithStaticFS(fstest.MapFS{"a.css": {Data: []byte("css")}})}})
	if store.reads() != 0 {
		t.Fatalf("start read the store %d times, want 0", store.reads())
	}
	if a.svc.Engine() == nil {
		t.Fatal("no engine after start")
	}
	rr := httptest.NewRecorder()
	a.app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/static/a.css", nil))
	if rr.Body.String() != "css" {
		t.Fatalf("static route after start = %d %q", rr.Code, rr.Body.String())
	}
	if _, err := render(t, a.ui, "home", nil); err != nil {
		t.Fatal(err)
	}
	if store.reads() == 0 {
		t.Fatal("the first render did not read the store")
	}
}

// Docs: modules/ui.mdx#template-layout
// "Names under `layouts/` and `partials/` are parsed into every page." and "Every other template is a page, rendered by its name without the `.html` extension: `home.html` is `"home"`, `notes/list.html` is `"notes/list"`."
func TestTemplateKinds(t *testing.T) {
	t.Parallel()
	a := startUI(t, layoutStore(t), appOpts{})
	for name, want := range map[string]string{"home": "<html>home <nav>N</nav></html>", "notes/list": "<html>notes</html>", "404": "<html>missing</html>"} {
		if rr, err := render(t, a.ui, name, nil); err != nil || rr.Body.String() != want {
			t.Errorf("%s = %q, %v; want %q", name, rr.Body.String(), err, want)
		}
	}
	for _, name := range []string{"home.html", "layouts/base", "partials/nav"} {
		if _, err := render(t, a.ui, name, nil); err == nil {
			t.Errorf("%s rendered as a page", name)
		}
	}
}

// Docs: modules/ui.mdx#template-layout
// "When at least one layout exists, `Render` and `RenderWithStatus` render every page through the template that `ui.layoutname` names, `base` by default. A page fills the layout's blocks with `{{define}}`. With no layouts at all, a page renders on its own."
func TestLayoutEntryPoint(t *testing.T) {
	t.Parallel()
	store := newStore(t, map[string]string{
		"layouts/main.html": `{{define "base"}}[base]{{end}}{{define "main"}}<main>{{block "content" .}}{{end}}</main>{{end}}`,
		"home.html":         `{{define "content"}}home{{end}}`,
	})
	a := startUI(t, store, appOpts{values: map[string]any{"ui": map[string]any{"layoutname": "main"}}})
	if rr, err := render(t, a.ui, "home", nil); err != nil || rr.Body.String() != "<main>home</main>" {
		t.Errorf("layoutname main: %q, %v", rr.Body.String(), err)
	}
	rr := httptest.NewRecorder()
	if err := a.ui.RenderWithStatus(rr, http.StatusTeapot, "home", nil); err != nil || rr.Body.String() != "<main>home</main>" {
		t.Errorf("RenderWithStatus through the layout: %q, %v", rr.Body.String(), err)
	}
	def := startUI(t, newStore(t, map[string]string{
		"layouts/base.html": `{{define "base"}}<b>{{block "content" .}}{{end}}</b>{{end}}`,
		"home.html":         `{{define "content"}}home{{end}}`,
	}), appOpts{})
	if rr, err := render(t, def.ui, "home", nil); err != nil || rr.Body.String() != "<b>home</b>" {
		t.Errorf("default base: %q, %v", rr.Body.String(), err)
	}
	alone := startUI(t, newStore(t, map[string]string{"home.html": `standalone {{.}}`}), appOpts{})
	if rr, err := render(t, alone.ui, "home", "x"); err != nil || rr.Body.String() != "standalone x" {
		t.Errorf("no layouts: %q, %v", rr.Body.String(), err)
	}
}

// Docs: modules/ui.mdx#template-layout
// "Each page is compiled with all the layouts and partials, so two pages can both define `content` without clashing."
func TestPagesDoNotClash(t *testing.T) {
	t.Parallel()
	a := startUI(t, layoutStore(t), appOpts{})
	home, _ := render(t, a.ui, "home", nil)
	notes, _ := render(t, a.ui, "notes/list", nil)
	if !strings.Contains(home.Body.String(), "home") || notes.Body.String() != "<html>notes</html>" {
		t.Fatalf("home %q, notes %q", home.Body.String(), notes.Body.String())
	}
}

// Docs: modules/ui.mdx#rendering
// "`Render(w, name, data)` writes the page with status 200." and "`RenderWithStatus(w, status, name, data)` writes it with `status`." and "On success they set `Content-Type: text/html; charset=utf-8`." and "An unknown name returns `ui: template "<name>" not found`."
func TestRenderStatusAndType(t *testing.T) {
	t.Parallel()
	a := startUI(t, layoutStore(t), appOpts{})
	rr, err := render(t, a.ui, "home", nil)
	if err != nil || rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("Render: %d %q %v", rr.Code, rr.Header().Get("Content-Type"), err)
	}
	rr = httptest.NewRecorder()
	err = a.ui.RenderWithStatus(rr, http.StatusNotFound, "404", nil)
	if err != nil || rr.Code != http.StatusNotFound || rr.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("RenderWithStatus: %d %q %v", rr.Code, rr.Header().Get("Content-Type"), err)
	}
	if _, err := render(t, a.ui, "nope/missing", nil); err == nil || err.Error() != `ui: template "nope/missing" not found` {
		t.Errorf("unknown name: %v", err)
	}
}

// Docs: modules/ui.mdx#rendering
// "Both execute the template into a buffer first. A template error is returned and nothing is written, so a failed render never sends half a page."
// Docs: modules/ui.mdx#errors
// "| `ui: executing template ...`, `ui: executing fragment ...` | The render methods | Template execution fails; nothing is written |"
func TestFailedRenderWritesNothing(t *testing.T) {
	t.Parallel()
	a := startUI(t, layoutStore(t), appOpts{})
	data := map[string]any{"Items": []int{}}
	for name, call := range map[string]func(http.ResponseWriter) error{
		"Render":           func(w http.ResponseWriter) error { return a.ui.Render(w, "broken/exec", data) },
		"RenderWithStatus": func(w http.ResponseWriter) error { return a.ui.RenderWithStatus(w, 201, "broken/exec", data) },
	} {
		rr := httptest.NewRecorder()
		err := call(rr)
		if err == nil || !strings.HasPrefix(err.Error(), `ui: executing template "broken/exec"`) {
			t.Errorf("%s: %v, want ui: executing template", name, err)
		}
		if rr.Body.Len() != 0 || rr.Header().Get("Content-Type") != "" || rr.Code != http.StatusOK {
			t.Errorf("%s wrote %d %q %q after a failed render", name, rr.Code, rr.Header(), rr.Body.String())
		}
	}
	frag := newStore(t, map[string]string{"f.html": `before{{index .Items 5}}`})
	fa := startUI(t, frag, appOpts{})
	rr := httptest.NewRecorder()
	err := fa.ui.RenderFragment(rr, "f", data)
	if err == nil || !strings.HasPrefix(err.Error(), `ui: executing fragment "f"`) || rr.Body.Len() != 0 {
		t.Errorf("RenderFragment failure: %v, body %q", err, rr.Body.String())
	}
}

// Docs: modules/ui.mdx#compilation
// "When `GasEnv` is `development` or `testing`, every render recompiles the templates, so edits to a store that reads from disk show on the next request."
func TestDevModeRecompiles(t *testing.T) {
	t.Parallel()
	for env, recompiles := range map[string]bool{"development": true, "testing": true, "staging": false, "production": false, "": false} {
		store := newStore(t, map[string]string{"home.html": "v1"})
		a := startUI(t, store, appOpts{values: map[string]any{"gasenv": env}})
		first, _ := render(t, a.ui, "home", nil)
		store.put(t, "home.html", "v2")
		second, _ := render(t, a.ui, "home", nil)
		want := "v1"
		if recompiles {
			want = "v2"
		}
		if first.Body.String() != "v1" || second.Body.String() != want {
			t.Errorf("GasEnv %q: renders %q then %q, want v1 then %s", env, first.Body.String(), second.Body.String(), want)
		}
	}
}

// Docs: modules/ui.mdx#compilation
// "A failed compile returns its error, and every later render retries the build and returns the error until a compile succeeds; the previously compiled templates are not served in the meantime"
// In development every render compiles; a page that compiled before the
// failure is not served while another page fails to parse.
func TestFailedRecompile(t *testing.T) {
	t.Parallel()
	store := newStore(t, map[string]string{"home.html": "v1", "other.html": "o"})
	a := startUI(t, store, appOpts{values: map[string]any{"gasenv": "development"}})
	if rr, err := render(t, a.ui, "home", nil); err != nil || rr.Body.String() != "v1" {
		t.Fatalf("first render %q, %v", rr.Body.String(), err)
	}
	lists := store.lists.Load()
	store.put(t, "other.html", "{{if}}")
	for i := range 3 {
		rr, err := render(t, a.ui, "home", nil)
		if err == nil || !strings.HasPrefix(err.Error(), `ui: parsing page "other.html"`) || rr.Body.Len() != 0 {
			t.Fatalf("render %d during the failure = %q, %v; want the parse error and nothing served", i, rr.Body.String(), err)
		}
	}
	if got := store.lists.Load() - lists; got != 3 {
		t.Fatalf("%d builds over 3 failing renders, want one per render", got)
	}
	store.put(t, "other.html", "o")
	if rr, err := render(t, a.ui, "home", nil); err != nil || rr.Body.String() != "v1" {
		t.Fatalf("render after a good compile %q, %v", rr.Body.String(), err)
	}
}

// Docs: modules/ui.mdx#compilation
// "A failed compile returns its error, and every later render retries the build and returns the error until a compile succeeds; the previously compiled templates are not served in the meantime"
// Outside development, a recompile forced by RegisterFuncs that fails: the
// cached v1 is not served while it keeps failing, and a good compile
// recovers and then stays cached.
func TestFailedRecompileServesNothingUntilFixed(t *testing.T) {
	t.Parallel()
	store := newStore(t, map[string]string{"home.html": "v1", "other.html": "o"})
	a := startUI(t, store, appOpts{values: map[string]any{"gasenv": "production"}})
	if rr, err := render(t, a.ui, "home", nil); err != nil || rr.Body.String() != "v1" {
		t.Fatalf("first render %q, %v", rr.Body.String(), err)
	}
	store.put(t, "other.html", "{{if}}")
	a.ui.RegisterFuncs(template.FuncMap{"extra": func() string { return "" }})
	lists := store.lists.Load()
	for i := range 3 {
		rr, err := render(t, a.ui, "home", nil)
		if err == nil || rr.Body.Len() != 0 {
			t.Fatalf("render %d after the failed recompile = %q, %v; want the error and the previous v1 not served", i, rr.Body.String(), err)
		}
	}
	if got := store.lists.Load() - lists; got != 3 {
		t.Fatalf("%d builds over 3 failing renders, want one retry per render", got)
	}
	store.put(t, "home.html", "v2")
	store.put(t, "other.html", "o")
	if rr, err := render(t, a.ui, "home", nil); err != nil || rr.Body.String() != "v2" {
		t.Fatalf("render after a good compile = %q, %v; want the newly compiled v2", rr.Body.String(), err)
	}
	lists = store.lists.Load()
	if rr, err := render(t, a.ui, "home", nil); err != nil || rr.Body.String() != "v2" || store.lists.Load() != lists {
		t.Fatalf("render after recovery = %q, %v, builds %d; want the cached v2 without a rebuild", rr.Body.String(), err, store.lists.Load()-lists)
	}
}

// Docs: modules/ui.mdx#htmx-fragments
// "`Render` sees the list inside the layout; `RenderFragment` sees only the list."
// Docs: modules/ui.mdx#htmx-fragments
// "`RenderFragment(w, name, data)` renders a page without its layout, for an HTMX swap. It sets the same `Content-Type` and writes no explicit status."
func TestRenderFragment(t *testing.T) {
	t.Parallel()
	store := newStore(t, map[string]string{
		"layouts/base.html":   `{{define "base"}}<html>{{block "content" .}}{{end}}</html>{{end}}`,
		"partials/notes.html": `{{define "notes"}}<ul>{{range .}}<li>{{.}}</li>{{end}}</ul>{{end}}`,
		"notes/list.html":     `{{define "content"}}<h1>Notes</h1>{{template "notes" .}}{{end}}{{template "notes" .}}`,
	})
	a := startUI(t, store, appOpts{})
	data := []string{"a", "b"}
	full, err := render(t, a.ui, "notes/list", data)
	if err != nil || full.Body.String() != "<html><h1>Notes</h1><ul><li>a</li><li>b</li></ul></html>" {
		t.Fatalf("Render = %q, %v; want the list inside the layout", full.Body.String(), err)
	}
	frag := &statusRecorder{ResponseRecorder: httptest.NewRecorder()}
	if err := a.ui.RenderFragment(frag, "notes/list", data); err != nil {
		t.Fatal(err)
	}
	if frag.Body.String() != "<ul><li>a</li><li>b</li></ul>" || frag.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("RenderFragment = %q %q; want only the list", frag.Body.String(), frag.Header().Get("Content-Type"))
	}
	if frag.wroteHeader {
		t.Fatal("RenderFragment wrote an explicit status")
	}
}

// statusRecorder records whether WriteHeader was called explicitly.
type statusRecorder struct {
	*httptest.ResponseRecorder
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	r.wroteHeader = true
	r.ResponseRecorder.WriteHeader(code)
}

// Docs: modules/ui.mdx#htmx-fragments
// "`RenderFragment` executes only the page's text outside its `{{define}}` blocks. A page that keeps all its markup in `{{define "content"}}`, the usual convention, renders an empty fragment."
func TestRenderFragmentKnownIssue(t *testing.T) {
	t.Parallel()
	a := startUI(t, layoutStore(t), appOpts{})
	rr := httptest.NewRecorder()
	if err := a.ui.RenderFragment(rr, "notes/list", nil); err != nil {
		t.Fatal(err)
	}
	if rr.Body.String() != "" {
		t.Fatalf("fragment of a content-only page = %q, want empty", rr.Body.String())
	}
}

// Docs: modules/ui.mdx#errors
// "| `gas/ui: config binding: ...` | Startup | The config cannot be bound |" and "| `gas/ui: invalid configuration: ...` | Startup | A rule in Configuration fails |"
func TestStartupErrors(t *testing.T) {
	t.Parallel()
	failing := &configtest.MockConfig{BindFn: func(any, ...config.BindOption) error { return errors.New("bad bind") }}
	_, err := buildUI(t, newStore(t, nil), appOpts{cfg: failing})
	if err == nil || !strings.Contains(err.Error(), "gas/ui: config binding: ") {
		t.Errorf("bind failure: start = %v, want gas/ui: config binding:", err)
	}
	_, err = buildUI(t, newStore(t, nil), appOpts{values: map[string]any{"ui": map[string]any{"staticpath": "static/*"}}})
	if err == nil || !strings.Contains(err.Error(), "gas/ui: invalid configuration: ") {
		t.Errorf("invalid config: start = %v, want gas/ui: invalid configuration:", err)
	}
}

// Docs: modules/ui.mdx#errors
// "| `gas/ui: no template engine configured` | `Render`, `RenderWithStatus`, `RenderFragment` | Before the service has started; nothing is written |"
func TestRenderBeforeStart(t *testing.T) {
	t.Parallel()
	svc, _ := ui.New[gas.TemplateProvider]()(newStore(t, nil), nil, nil, &gas.NopLogger{})
	for name, call := range map[string]func(http.ResponseWriter) error{
		"Render":           func(w http.ResponseWriter) error { return svc.Render(w, "home", nil) },
		"RenderWithStatus": func(w http.ResponseWriter) error { return svc.RenderWithStatus(w, 200, "home", nil) },
		"RenderFragment":   func(w http.ResponseWriter) error { return svc.RenderFragment(w, "home", nil) },
	} {
		rr := httptest.NewRecorder()
		if err := call(rr); err == nil || err.Error() != "gas/ui: no template engine configured" {
			t.Errorf("%s before start = %v", name, err)
		}
		if rr.Body.Len() != 0 || len(rr.Header()) != 0 {
			t.Errorf("%s before start wrote %q %v", name, rr.Body.String(), rr.Header())
		}
	}
}

// Docs: modules/ui.mdx#errors
// "| `gas/ui: service is closed` | `Render`, `RenderWithStatus`, `RenderFragment` | After `Close`; the method also writes a 503 response |"
// Docs: modules/ui.mdx#health-and-readiness
// "`Close` marks the service closed: the render methods then answer 503 and return an error. The static route keeps serving."
func TestRenderAfterClose(t *testing.T) {
	t.Parallel()
	a := startUI(t, layoutStore(t), appOpts{ui: []ui.Option{ui.WithStaticFS(fstest.MapFS{"a.css": {Data: []byte("css")}})}})
	if err := a.svc.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func(http.ResponseWriter) error{
		"Render":           func(w http.ResponseWriter) error { return a.ui.Render(w, "home", nil) },
		"RenderWithStatus": func(w http.ResponseWriter) error { return a.ui.RenderWithStatus(w, 200, "home", nil) },
		"RenderFragment":   func(w http.ResponseWriter) error { return a.ui.RenderFragment(w, "home", nil) },
	} {
		rr := httptest.NewRecorder()
		if err := call(rr); err == nil || err.Error() != "gas/ui: service is closed" || rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s after Close = %v, status %d", name, err, rr.Code)
		}
	}
	rr := httptest.NewRecorder()
	a.app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/static/a.css", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "css" {
		t.Fatalf("static route after Close = %d %q, want it still serving", rr.Code, rr.Body.String())
	}
}

// Docs: modules/ui.mdx#errors
// "| `ui: listing templates: ...`, `ui: loading template ...`, `ui: parsing layout ...`, `ui: parsing partial ...`, `ui: parsing page ...` | The render that compiles | The provider or a template fails |"
func TestCompileErrors(t *testing.T) {
	t.Parallel()
	base := map[string]string{
		"layouts/base.html": `{{define "base"}}{{block "content" .}}{{end}}{{end}}`,
		"partials/nav.html": `{{define "nav"}}nav{{end}}`,
		"home.html":         `{{define "content"}}home{{end}}`,
	}
	cases := map[string]struct {
		mutate func(*countingStore)
		prefix string
	}{
		"list":    {func(s *countingStore) { s.failAll = true }, "ui: listing templates: "},
		"load":    {func(s *countingStore) { s.failGet = "partials/nav.html" }, "ui: loading template "},
		"layout":  {func(s *countingStore) { s.put(t, "layouts/base.html", "{{if}}") }, "ui: parsing layout "},
		"partial": {func(s *countingStore) { s.put(t, "partials/nav.html", "{{if}}") }, "ui: parsing partial "},
		"page":    {func(s *countingStore) { s.put(t, "home.html", "{{if}}") }, "ui: parsing page "},
	}
	for name, c := range cases {
		store := newStore(t, base)
		c.mutate(store)
		a := startUI(t, store, appOpts{})
		if _, err := render(t, a.ui, "home", nil); err == nil || !strings.HasPrefix(err.Error(), c.prefix) {
			t.Errorf("%s: render = %v, want prefix %q", name, err, c.prefix)
		}
	}
}

// Docs: modules/ui.mdx#errors
// "The package defines no sentinel errors. Errors from the service start with `gas/ui:`, and errors from the template engine with `ui:`."
// The sentinel half is an absence; this checks the prefixes across the
// service and engine errors.
func TestErrorPrefixes(t *testing.T) {
	t.Parallel()
	a := startUI(t, layoutStore(t), appOpts{})
	if _, err := render(t, a.ui, "missing", nil); err == nil || !strings.HasPrefix(err.Error(), "ui: ") {
		t.Errorf("engine error %v, want ui: prefix", err)
	}
	if err := a.svc.CheckReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = a.svc.Close(context.Background())
	if _, err := render(t, a.ui, "home", nil); err == nil || !strings.HasPrefix(err.Error(), "gas/ui: ") {
		t.Errorf("service error %v, want gas/ui: prefix", err)
	}
}

// Docs: modules/ui.mdx#errors
// "The render methods return the error and leave the response to you; a handler that returns it hands it to the router's error handler"
func TestRenderErrorToErrorHandler(t *testing.T) {
	t.Parallel()
	a := startUI(t, layoutStore(t), appOpts{})
	var handled error
	a.app.Router().SetErrorHandler(func(ctx gas.Context, err error) {
		handled = err
		_ = ctx.Text(http.StatusInternalServerError, "handled")
	})
	a.app.Router().Get(nil, "/page", func(ctx gas.Context) error {
		return a.ui.Render(ctx.ResponseWriter(), "missing", nil)
	})
	rr := httptest.NewRecorder()
	a.app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/page", nil))
	if handled == nil || !strings.Contains(handled.Error(), `ui: template "missing" not found`) || rr.Body.String() != "handled" {
		t.Fatalf("error handler got %v, body %q", handled, rr.Body.String())
	}
}

// Docs: modules/ui.mdx#health-and-readiness
// "`ui.Service` implements `gas.ReadyReporter`, not `gas.HealthReporter`. `CheckReady` returns `gas/ui: template engine not initialized` before the service has started and `gas/ui: service is closed` after `Close`."
func TestReadiness(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[*ui.Service]()
	if !typ.Implements(reflect.TypeFor[gas.ReadyReporter]()) || typ.Implements(reflect.TypeFor[gas.HealthReporter]()) {
		t.Fatal("ui.Service should implement ReadyReporter and not HealthReporter")
	}
	svc, _ := ui.New[gas.TemplateProvider]()(newStore(t, nil), nil, nil, &gas.NopLogger{})
	if err := svc.CheckReady(context.Background()); err == nil || err.Error() != "gas/ui: template engine not initialized" {
		t.Errorf("before start: %v", err)
	}
	a := startUI(t, layoutStore(t), appOpts{})
	if err := a.svc.CheckReady(context.Background()); err != nil {
		t.Errorf("started: %v", err)
	}
	_ = a.svc.Close(context.Background())
	if err := a.svc.CheckReady(context.Background()); err == nil || err.Error() != "gas/ui: service is closed" {
		t.Errorf("after Close: %v", err)
	}
}

// Docs: modules/ui.mdx#testing
// "To test the templates themselves, build an engine over a store with `ui.NewEngine(provider, funcMap, layout, devMode, logger)` and render into an `httptest.ResponseRecorder`"
func TestNewEngine(t *testing.T) {
	t.Parallel()
	e := ui.NewEngine(layoutStore(t), ui.DefaultFuncMap(gas.EnvTesting), "base", false, &gas.NopLogger{})
	rr := httptest.NewRecorder()
	if err := e.Render(rr, "home", nil); err != nil || rr.Body.String() != "<html>home <nav>N</nav></html>" {
		t.Fatalf("engine Render = %q, %v", rr.Body.String(), err)
	}
}
