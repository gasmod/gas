package guideshtml_test

import (
	"bytes"
	"context"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/ui"
	"github.com/gasmod/gas/ui/uitest"
)

// conventionTemplates is the tree from the page's directory convention:
// a base layout, two partials, a page, a nested page, and a page that
// defines no blocks.
var conventionTemplates = map[string]string{
	"layouts/base.html":    `{{define "base"}}<html>{{template "nav" .}}{{block "content" .}}default{{end}}{{template "footer" .}}</html>{{end}}`,
	"partials/nav.html":    `{{define "nav"}}<nav>N</nav>{{end}}`,
	"partials/footer.html": `{{define "footer"}}<footer>F</footer>{{end}}`,
	"home.html":            `{{define "content"}}<h1>Hello {{.Name}}</h1>{{end}}`,
	"notes/list.html":      `{{define "content"}}<ul>{{range .Notes}}<li>{{.}}</li>{{end}}</ul>{{end}}`,
	"standalone.html":      `<p>standalone {{template "nav" .}}</p>`,
}

// Docs: guides/html.mdx#wiring
// "`ui.New[T]` names the **template provider** to inject. The
// `WithSingletonService[...]` parameter names the key the **ui service** is
// stored under. They are independent: register under `gas.UIProvider` so your
// services can inject it, and set `T` to whatever your template store is
// registered as."
func TestWiringTypeParameterIndependentOfRegistrationKey(t *testing.T) {
	t.Parallel()
	store := memStore(t, map[string]string{"page.html": `concrete store`})
	app := startApp(t,
		gas.WithServiceInstance[gas.ConfigProvider](config.New()),
		gas.WithServiceInstance[gas.Logger](newCaptureLogger()),
		gas.WithServiceInstance[*memory.Store](store),
		gas.WithSingletonService[gas.UIProvider](ui.New[*memory.Store](ui.WithStaticFS(emptyStaticFS()))),
	)
	p, err := app.ServiceContainer().Resolve[gas.UIProvider]()
	if err != nil {
		t.Fatalf("resolve gas.UIProvider: %v", err)
	}
	_, body := render(t, func(w http.ResponseWriter) error { return p.Render(w, "page", nil) })
	if body != "concrete store" {
		t.Fatalf("body = %q, want %q", body, "concrete store")
	}
}

// Docs: guides/html.mdx#directory-convention
// "home.html a page, rendered as `Render(w, "home", data)`" and
// "layouts/ base.html the entry point, `{{define "base"}}`" and
// "When `layouts/` holds any template, every page renders through the layout's
// entry point (`base` by default), whether or not it defines blocks."
func TestPageWithBlocksIsWrappedInLayout(t *testing.T) {
	t.Parallel()
	_, p := uiApp(t, memStore(t, conventionTemplates), nil, nil, ui.WithStaticFS(emptyStaticFS()))
	code, body := render(t, func(w http.ResponseWriter) error {
		return p.Render(w, "home", map[string]any{"Name": "Gas"})
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d", code, http.StatusOK)
	}
	want := `<html><nav>N</nav><h1>Hello Gas</h1><footer>F</footer></html>`
	if body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
}

// Docs: guides/html.mdx#directory-convention
// "notes/ list.html nested pages work, `"notes/list"`"
func TestNestedPageRendersByPath(t *testing.T) {
	t.Parallel()
	_, p := uiApp(t, memStore(t, conventionTemplates), nil, nil, ui.WithStaticFS(emptyStaticFS()))
	_, body := render(t, func(w http.ResponseWriter) error {
		return p.Render(w, "notes/list", map[string]any{"Notes": []string{"first", "second"}})
	})
	want := `<html><nav>N</nav><ul><li>first</li><li>second</li></ul><footer>F</footer></html>`
	if body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
}

// Docs: guides/html.mdx#directory-convention
// "When `layouts/` holds any template, every page renders through the layout's
// entry point (`base` by default), whether or not it defines blocks."
//
// The page defines no blocks, so the layout renders with its own default
// content block.
func TestPageWithoutBlocksRendersThroughLayout(t *testing.T) {
	t.Parallel()
	_, p := uiApp(t, memStore(t, conventionTemplates), nil, nil, ui.WithStaticFS(emptyStaticFS()))
	_, body := render(t, func(w http.ResponseWriter) error { return p.Render(w, "standalone", nil) })
	want := `<html><nav>N</nav>default<footer>F</footer></html>`
	if body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
}

// Docs: guides/html.mdx#directory-convention
// "Everything in `layouts/` and `partials/` is parsed into every render." and
// "A page renders standalone only when there are no layouts at all."
func TestPageRendersStandaloneWithoutLayouts(t *testing.T) {
	t.Parallel()
	noLayouts := map[string]string{}
	for name, src := range conventionTemplates {
		if !strings.HasPrefix(name, "layouts/") {
			noLayouts[name] = src
		}
	}
	_, p := uiApp(t, memStore(t, noLayouts), nil, nil, ui.WithStaticFS(emptyStaticFS()))
	_, body := render(t, func(w http.ResponseWriter) error { return p.Render(w, "standalone", nil) })
	want := `<p>standalone <nav>N</nav></p>`
	if body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
}

// Docs: guides/html.mdx#directory-convention
// "Everything else is a page."
// Assumption: a file under partials/ is not itself a page, so rendering it by
// name fails rather than producing output.
func TestPartialIsNotAPage(t *testing.T) {
	t.Parallel()
	_, p := uiApp(t, memStore(t, conventionTemplates), nil, nil, ui.WithStaticFS(emptyStaticFS()))
	if err := p.Render(newDiscard(), "partials/nav", nil); err == nil {
		t.Fatalf("Render(partials/nav) error = nil, want an error")
	}
}

// Docs: guides/html.mdx#rendering
// "Your service takes `gas.UIProvider` and never imports `gas/ui`, so it can be
// tested against `uitest.MockUI`."
func TestMockUIStandsInForUIProvider(t *testing.T) {
	t.Parallel()
	mock := &uitest.MockUI{}
	var p gas.UIProvider = mock
	if err := p.Render(newDiscard(), "home", nil); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := mock.CallCount("Render"); got != 1 {
		t.Fatalf("CallCount(Render) = %d, want 1", got)
	}
}

// KI-032: asserts the intended behavior; unskip when the fix lands.
func TestRenderFragmentSkipsLayout(t *testing.T) {
	t.Skip("KI-032: RenderFragment renders nothing for a page that only defines content; see KNOWN_ISSUES.md")
	t.Parallel()
	_, p := uiApp(t, memStore(t, conventionTemplates), nil, nil, ui.WithStaticFS(emptyStaticFS()))
	data := map[string]any{"Notes": []string{"first", "second"}}
	_, full := render(t, func(w http.ResponseWriter) error { return p.Render(w, "notes/list", data) })
	_, frag := render(t, func(w http.ResponseWriter) error { return p.RenderFragment(w, "notes/list", data) })
	want := `<ul><li>first</li><li>second</li></ul>`
	if frag != want {
		t.Fatalf("fragment = %q, want %q", frag, want)
	}
	if !strings.Contains(full, "<html>") || !strings.Contains(full, want) {
		t.Fatalf("full page = %q, want the layout around %q", full, want)
	}
}

// builtinFuncs is the page's list of built-in template functions.
var builtinFuncs = []string{
	"upper", "lower", "title", "trimSpace", "contains", "hasPrefix",
	"hasSuffix", "replace", "join", "split", "truncate", "now", "formatTime",
	"formatTimePtr", "add", "sub", "dict", "list", "json", "env", "buildId",
}

// Docs: guides/html.mdx#template-functions
// "Built in: `upper`, `lower`, `title`, `trimSpace`, `contains`, `hasPrefix`,
// `hasSuffix`, `replace`, `join`, `split`, `truncate`, `now`, `formatTime`,
// `formatTimePtr`, `add`, `sub`, `dict`, `list`, `json`, `env`, and `buildId`."
func TestBuiltinFuncsAvailable(t *testing.T) {
	t.Parallel()
	fm := ui.DefaultFuncMap(gas.EnvProduction)
	for _, name := range builtinFuncs {
		if _, ok := fm[name]; !ok {
			t.Errorf("DefaultFuncMap has no %q", name)
		}
	}

	// Every name also parses in a page rendered by the service; html/template
	// rejects a reference to an undefined function at parse time.
	var b strings.Builder
	b.WriteString("{{if false}}")
	for _, name := range builtinFuncs {
		b.WriteString("{{" + name + "}}")
	}
	b.WriteString("{{end}}ok")
	_, p := uiApp(t, memStore(t, map[string]string{"funcs.html": b.String()}), nil, nil, ui.WithStaticFS(emptyStaticFS()))
	_, body := render(t, func(w http.ResponseWriter) error { return p.Render(w, "funcs", nil) })
	if body != "ok" {
		t.Fatalf("body = %q, want %q", body, "ok")
	}
}

// Docs: guides/html.mdx#template-functions
// "`dict` is the one you will use most, since it is how a partial gets
// arbitrary data: `{{template "user-card" dict "Name" .UserName "Role" "admin"}}`."
func TestDictPassesDataToPartial(t *testing.T) {
	t.Parallel()
	store := memStore(t, map[string]string{
		"partials/user-card.html": `{{define "user-card"}}{{.Name}}:{{.Role}}{{end}}`,
		"card.html":               `{{template "user-card" dict "Name" .UserName "Role" "admin"}}`,
	})
	_, p := uiApp(t, store, nil, nil, ui.WithStaticFS(emptyStaticFS()))
	_, body := render(t, func(w http.ResponseWriter) error {
		return p.Render(w, "card", map[string]any{"UserName": "ada"})
	})
	if body != "ada:admin" {
		t.Fatalf("body = %q, want %q", body, "ada:admin")
	}
}

// execFuncs parses src with ui.DefaultFuncMap(env) and executes it.
func execFuncs(t *testing.T, env gas.Environment, src string) string {
	t.Helper()
	tmpl, err := template.New("t").Funcs(ui.DefaultFuncMap(env)).Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return buf.String()
}

// Docs: guides/html.mdx#template-functions
// "`buildId` returns a stable id per process, and a fresh one per call in
// development"
func TestBuildIDStablePerProcess(t *testing.T) {
	t.Parallel()
	first := execFuncs(t, gas.EnvProduction, `{{buildId}}|{{buildId}}`)
	parts := strings.Split(first, "|")
	if parts[0] == "" || parts[0] != parts[1] {
		t.Fatalf("production buildId calls = %q, want two equal non-empty ids", first)
	}
	again := execFuncs(t, gas.EnvProduction, `{{buildId}}`)
	if again != parts[0] {
		t.Fatalf("buildId from a second funcmap = %q, want %q", again, parts[0])
	}
}

// Docs: guides/html.mdx#template-functions
// "`buildId` returns a stable id per process, and a fresh one per call in
// development"
func TestBuildIDFreshPerCallInDevelopment(t *testing.T) {
	t.Parallel()
	out := execFuncs(t, gas.EnvDevelopment, `{{buildId}}|{{buildId}}`)
	parts := strings.Split(out, "|")
	if parts[0] == "" || parts[0] == parts[1] {
		t.Fatalf("development buildId calls = %q, want two different non-empty ids", out)
	}
}

// Docs: guides/html.mdx#template-functions
// "`title` maps to `strings.ToTitle`, which maps every letter to title case. On
// ASCII input that means the whole string comes back uppercased, not
// title-cased."
func TestTitleIsStringsToTitle(t *testing.T) {
	t.Parallel()
	if got := execFuncs(t, gas.EnvProduction, `{{title "hello world"}}`); got != "HELLO WORLD" {
		t.Fatalf("title(%q) = %q, want %q", "hello world", got, "HELLO WORLD")
	}
	in := "dž ǆ"
	if got, want := execFuncs(t, gas.EnvProduction, `{{title "`+in+`"}}`), strings.ToTitle(in); got != want {
		t.Fatalf("title(%q) = %q, want strings.ToTitle = %q", in, got, want)
	}
}

// contributor is a service that adds template functions during Init, as the
// page's funcs snippet does.
type contributor struct {
	ui    gas.UIProvider
	funcs template.FuncMap
}

func (c *contributor) Name() string                { return "contributor" }
func (c *contributor) Close(context.Context) error { return nil }
func (c *contributor) Init(context.Context) error {
	c.ui.RegisterFuncs(c.funcs)
	return nil
}

func contributorOpt(funcs template.FuncMap) gas.Option {
	return gas.WithSingletonService[*contributor](func(p gas.UIProvider) (*contributor, error) {
		return &contributor{ui: p, funcs: funcs}, nil
	})
}

// Docs: guides/html.mdx#template-functions
// "Any service can contribute helpers during `Init`" and "Templates are built
// lazily on first render, so registrations made during `Init` are always in
// time."
func TestRegisterFuncsDuringInitIsInTime(t *testing.T) {
	t.Parallel()
	funcs := template.FuncMap{"shout": func(s string) string { return strings.ToUpper(s) + "!" }}
	_, p := uiApp(t, memStore(t, map[string]string{"shout.html": `{{shout "hi"}}`}), nil,
		[]gas.Option{contributorOpt(funcs)}, ui.WithStaticFS(emptyStaticFS()))
	_, body := render(t, func(w http.ResponseWriter) error { return p.Render(w, "shout", nil) })
	if body != "HI!" {
		t.Fatalf("body = %q, want %q", body, "HI!")
	}
}

// Docs: guides/html.mdx#template-functions
// "A name that collides with a built-in logs a warning."
// Assumption: the collision is one made through RegisterFuncs, and the warning
// names the colliding function in its message or one of its fields.
func TestRegisterFuncsCollisionLogsWarning(t *testing.T) {
	t.Parallel()
	log := newCaptureLogger()
	funcs := template.FuncMap{"upper": func(s string) string { return s }}
	_, p := uiApp(t, memStore(t, map[string]string{"x.html": `{{upper "a"}}`}), log,
		[]gas.Option{contributorOpt(funcs)}, ui.WithStaticFS(emptyStaticFS()))
	render(t, func(w http.ResponseWriter) error { return p.Render(w, "x", nil) })

	for _, r := range log.records() {
		if r.level != "warn" {
			continue
		}
		if strings.Contains(r.msg, "upper") {
			return
		}
		for _, v := range r.fields {
			if strings.Contains(v, "upper") {
				return
			}
		}
	}
	t.Fatalf("no warn event naming %q; records = %+v", "upper", log.records())
}

// Docs: guides/html.mdx#development-mode
// "When `GasEnv` is development-like, templates rebuild on every request, so
// edits show up on refresh. In production they are built once."
func TestDevelopmentModeRebuilds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		env     gas.Environment
		rebuild bool
	}{
		{gas.EnvDevelopment, true},
		{gas.EnvTesting, true},
		{gas.EnvProduction, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.env), func(t *testing.T) {
			t.Parallel()
			store := memStore(t, map[string]string{"page.html": "v1"})
			cfg := config.New()
			cfg.Set("GasEnv", string(tc.env))
			app := startApp(t,
				gas.WithServiceInstance[gas.ConfigProvider](cfg),
				gas.WithServiceInstance[gas.Logger](newCaptureLogger()),
				gas.WithServiceInstance[gas.TemplateProvider](gas.TemplateProvider(store)),
				gas.WithSingletonService[gas.UIProvider](ui.New[gas.TemplateProvider](ui.WithStaticFS(emptyStaticFS()))),
			)
			p, err := app.ServiceContainer().Resolve[gas.UIProvider]()
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			_, first := render(t, func(w http.ResponseWriter) error { return p.Render(w, "page", nil) })
			if first != "v1" {
				t.Fatalf("first render = %q, want %q", first, "v1")
			}
			if err := store.Register(context.Background(), "page.html", []byte("v2")); err != nil {
				t.Fatalf("register: %v", err)
			}
			_, second := render(t, func(w http.ResponseWriter) error { return p.Render(w, "page", nil) })
			want := "v1"
			if tc.rebuild {
				want = "v2"
			}
			if second != want {
				t.Fatalf("GasEnv %s: render after edit = %q, want %q", tc.env, second, want)
			}
		})
	}
}

// Docs: guides/html.mdx#_top
// "That separation is what lets you develop against files on disk and ship a
// single binary with templates embedded, without the rendering code noticing."
// The disk side is template/fs over os.DirFS, as in the page's wiring snippet.
// An fstest.MapFS stands in for the embed.FS, since this directory holds only
// test files.
func TestDiskAndEmbeddedStoresRenderTheSame(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mapfs := fstest.MapFS{}
	for name, content := range conventionTemplates {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		mapfs[name] = &fstest.MapFile{Data: []byte(content)}
	}
	diskStore, err := newFSStore(os.DirFS(dir))
	if err != nil {
		t.Fatalf("fs store over os.DirFS: %v", err)
	}
	embedStore, err := newFSStore(mapfs)
	if err != nil {
		t.Fatalf("fs store over MapFS: %v", err)
	}
	_, onDisk := uiApp(t, diskStore, nil, nil, ui.WithStaticFS(emptyStaticFS()))
	_, embedded := uiApp(t, embedStore, nil, nil, ui.WithStaticFS(emptyStaticFS()))
	data := map[string]any{"Name": "Gas"}
	_, a := render(t, func(w http.ResponseWriter) error { return onDisk.Render(w, "home", data) })
	_, b := render(t, func(w http.ResponseWriter) error { return embedded.Render(w, "home", data) })
	if a == "" || a != b {
		t.Fatalf("disk render = %q, embedded render = %q, want equal and non-empty", a, b)
	}
}

// Docs: guides/html.mdx#htmx
// "It executes only the page's text outside its `{{define}}` blocks, so a page
// that keeps all its markup in `{{define "content"}}` renders an empty
// fragment."
func TestRenderFragmentExecutesOnlyTextOutsideDefines(t *testing.T) {
	t.Parallel()
	store := memStore(t, map[string]string{
		"layouts/base.html":    conventionTemplates["layouts/base.html"],
		"partials/nav.html":    conventionTemplates["partials/nav.html"],
		"partials/footer.html": conventionTemplates["partials/footer.html"],
		"mixed.html":           `before{{define "content"}}inside{{end}}after`,
		"content.html":         `{{define "content"}}<p>all markup</p>{{end}}`,
	})
	_, p := uiApp(t, store, nil, nil, ui.WithStaticFS(emptyStaticFS()))

	_, mixed := render(t, func(w http.ResponseWriter) error { return p.RenderFragment(w, "mixed", nil) })
	if mixed != "beforeafter" {
		t.Errorf("RenderFragment(mixed) = %q, want %q (only the text outside {{define}})", mixed, "beforeafter")
	}
	_, content := render(t, func(w http.ResponseWriter) error { return p.RenderFragment(w, "content", nil) })
	if content != "" {
		t.Errorf("RenderFragment(content) = %q, want an empty fragment", content)
	}
}

// readShownTemplates loads the templates the page shows from
// docs/snippets/htmlui/templates, keyed by their path under that directory.
func readShownTemplates(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "..", "htmlui", "templates")
	files := map[string]string{}
	for _, name := range []string{"layouts/base.html", "partials/notes.html", "notes/list.html"} {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("read shown template %s: %v", name, err)
		}
		files[name] = string(b)
	}
	return files
}

// Docs: guides/html.mdx#htmx
// "To serve one page both ways, put the markup in a partial and call it from
// both places:"
//
// The templates are the files the page shows (templates/partials/notes.html,
// templates/notes/list.html) plus the snippet tree's layouts/base.html.
// Assumption: "both ways" means Render places the partial's markup inside the
// layout and RenderFragment returns that same markup, ignoring the whitespace
// the page's text outside {{define}} contributes.
func TestPartialPatternRendersSameMarkupBothWays(t *testing.T) {
	t.Parallel()
	_, p := uiApp(t, memStore(t, readShownTemplates(t)), nil, nil, ui.WithStaticFS(emptyStaticFS()))
	data := map[string]any{"Notes": []string{"first", "second"}}
	want := `<ul id="notes"><li>first</li><li>second</li></ul>`

	_, full := render(t, func(w http.ResponseWriter) error { return p.Render(w, "notes/list", data) })
	_, frag := render(t, func(w http.ResponseWriter) error { return p.RenderFragment(w, "notes/list", data) })
	if !strings.Contains(full, "<html>") || !strings.Contains(full, "<main>"+want+"</main>") {
		t.Errorf("Render = %q, want the layout with %q in <main>", full, want)
	}
	if got := strings.TrimSpace(frag); got != want {
		t.Errorf("RenderFragment = %q, want %q", frag, want)
	}
}
