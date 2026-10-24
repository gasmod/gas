package projectknownissues_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/template/composite"
	templatedir "github.com/gasmod/gas/template/dir"
	"github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/ui"
)

func templateDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Docs: project/known-issues.mdx#templates
// "`dir.Store` declares `Close() error` and has no `Name` or `Init`, so the
// container rejects it at `Start` with
// `*dir.Store declares Close but does not implement gas.Service`."
func TestKI033DirStoreRejected(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[*templatedir.Store]()
	closeM, ok := typ.MethodByName("Close")
	if !ok || closeM.Type.NumIn() != 1 || closeM.Type.NumOut() != 1 || closeM.Type.Out(0) != reflect.TypeFor[error]() {
		t.Fatalf("*dir.Store Close = %v, want Close() error", closeM.Type)
	}
	for _, m := range []string{"Name", "Init"} {
		if _, ok := typ.MethodByName(m); ok {
			t.Errorf("*dir.Store has a %s method", m)
		}
	}
	app := gas.NewApp(gas.WithSingletonService[gas.TemplateProvider](templatedir.NewStore(t.TempDir())))
	err := app.Start(t.Context())
	if err == nil {
		_ = app.Stop(context.Background())
		t.Fatalf("Start with *dir.Store under gas.TemplateProvider error = nil, want it rejected")
	}
	if !strings.Contains(err.Error(), "*dir.Store declares Close but does not implement gas.Service") {
		t.Fatalf("Start error = %v, want the documented message", err)
	}
}

// Docs: project/known-issues.mdx#templates
// "**Workaround:** build the store yourself, wrap it in a `composite.Store`, and
// close it yourself."
func TestKI033Workaround(t *testing.T) {
	t.Parallel()
	dir := templateDir(t, map[string]string{"home.html": "from disk"})
	onDisk, err := templatedir.NewStore(dir)()
	if err != nil {
		t.Fatal(err)
	}
	app := gas.NewApp(gas.WithServiceInstance[gas.TemplateProvider](composite.NewStore(onDisk)))
	startApp(t, app)
	p, err := app.ServiceContainer().Resolve[gas.TemplateProvider]()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	b, err := p.Get(t.Context(), "home.html")
	if err != nil || string(b) != "from disk" {
		t.Fatalf("Get(home.html) = %q, %v, want from disk", b, err)
	}
	if err := onDisk.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// uiProvider starts an App rendering from files and returns its gas.UIProvider.
func uiProvider(t *testing.T, files map[string]string) gas.UIProvider {
	t.Helper()
	store := memory.NewStore()
	for name, content := range files {
		if err := store.Register(context.Background(), name, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](config.New()),
		gas.WithServiceInstance[gas.Logger](nopLogger()),
		gas.WithServiceInstance[gas.TemplateProvider](store),
		gas.WithSingletonService[gas.UIProvider](ui.New[gas.TemplateProvider](ui.WithStaticFS(fstest.MapFS{}))),
	)
	startApp(t, app)
	p, err := app.ServiceContainer().Resolve[gas.UIProvider]()
	if err != nil {
		t.Fatalf("resolve gas.UIProvider: %v", err)
	}
	return p
}

func renderTo(t *testing.T, fn func(http.ResponseWriter) error) string {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := fn(rec); err != nil {
		t.Fatalf("render: %v", err)
	}
	return rec.Body.String()
}

const layout = `{{define "base"}}<html><main>{{block "content" .}}{{end}}</main></html>{{end}}`

// Docs: project/known-issues.mdx#ui
// "`RenderFragment` executes only the page's text outside its `{{define}}`
// blocks. A page written to the usual convention, with all its markup in
// `{{define "content"}}`, renders an empty fragment"
func TestKI032FragmentEmpty(t *testing.T) {
	t.Parallel()
	p := uiProvider(t, map[string]string{
		"layouts/base.html": layout,
		"notes/list.html":   `{{define "content"}}<ul>{{range .Notes}}<li>{{.}}</li>{{end}}</ul>{{end}}`,
		"outside.html":      `{{define "content"}}inside{{end}}<p>outside</p>`,
	})
	data := map[string]any{"Notes": []string{"a", "b"}}
	if got := renderTo(t, func(w http.ResponseWriter) error { return p.RenderFragment(w, "notes/list", data) }); strings.TrimSpace(got) != "" {
		t.Fatalf("fragment of a content-only page = %q, want empty (KI-032)", got)
	}
	if got := renderTo(t, func(w http.ResponseWriter) error { return p.Render(w, "notes/list", data) }); !strings.Contains(got, "<li>a</li>") {
		t.Fatalf("full render = %q, want the list in the layout", got)
	}
	got := renderTo(t, func(w http.ResponseWriter) error { return p.RenderFragment(w, "outside", nil) })
	if strings.TrimSpace(got) != "<p>outside</p>" {
		t.Fatalf("fragment = %q, want only the text outside the define blocks", got)
	}
}

// Docs: project/known-issues.mdx#ui
// "**Workaround:** put the markup in a partial and call it both from the
// `content` block and from the page's top level."
func TestKI032Workaround(t *testing.T) {
	t.Parallel()
	p := uiProvider(t, map[string]string{
		"layouts/base.html":   layout,
		"partials/notes.html": `{{define "notes"}}<ul id="notes">{{range .Notes}}<li>{{.}}</li>{{end}}</ul>{{end}}`,
		"notes/list.html":     `{{define "content"}}{{template "notes" .}}{{end}}{{template "notes" .}}`,
	})
	data := map[string]any{"Notes": []string{"a", "b"}}
	const list = `<ul id="notes"><li>a</li><li>b</li></ul>`
	frag := renderTo(t, func(w http.ResponseWriter) error { return p.RenderFragment(w, "notes/list", data) })
	if strings.TrimSpace(frag) != list {
		t.Fatalf("fragment = %q, want only %q", frag, list)
	}
	full := renderTo(t, func(w http.ResponseWriter) error { return p.Render(w, "notes/list", data) })
	if !strings.Contains(full, "<main>"+list+"</main>") || strings.Count(full, list) != 1 {
		t.Fatalf("full page = %q, want the list once, inside the layout", full)
	}
}
