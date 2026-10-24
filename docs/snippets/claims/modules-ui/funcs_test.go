package modulesui_test

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/ui"
)

// execFunc renders src as a page with the built-in functions for env.
func execFunc(t *testing.T, env gas.Environment, src string, data any) string {
	t.Helper()
	e := ui.NewEngine(newStore(t, map[string]string{"p.html": src}), ui.DefaultFuncMap(env), "base", false, &gas.NopLogger{})
	rr := httptest.NewRecorder()
	if err := e.Render(rr, "p", data); err != nil {
		t.Fatalf("render %q: %v", src, err)
	}
	return rr.Body.String()
}

// Docs: modules/ui.mdx#template-functions
// "| `upper`, `lower`, `trimSpace` | `strings.ToUpper`, `strings.ToLower`, `strings.TrimSpace` | | `contains`, `hasPrefix`, `hasSuffix`, `join`, `split` | `strings.Contains`, `strings.HasPrefix`, `strings.HasSuffix`, `strings.Join`, `strings.Split` | | `replace` | `strings.ReplaceAll` | | `title` | `strings.ToTitle`, which upper-cases every letter; it does not capitalize words |"
func TestStringFuncs(t *testing.T) {
	t.Parallel()
	for src, want := range map[string]string{
		`{{upper "aB"}}`:                   "AB",
		`{{lower "aB"}}`:                   "ab",
		`{{trimSpace "  x  "}}`:            "x",
		`{{contains "seafood" "foo"}}`:     "true",
		`{{hasPrefix "seafood" "sea"}}`:    "true",
		`{{hasSuffix "seafood" "food"}}`:   "true",
		`{{join (split "a,b,c" ",") "-"}}`: "a-b-c",
		`{{len (split "a,b,c" ",")}}`:      "3",
		`{{replace "a-a-a" "-" "_"}}`:      "a_a_a",
		`{{title "hello world"}}`:          "HELLO WORLD",
	} {
		if got := execFunc(t, gas.EnvProduction, src, nil); got != want {
			t.Errorf("%s = %q, want %q", src, got, want)
		}
	}
}

// Docs: modules/ui.mdx#template-functions
// "| `truncate n s` | The first `n` bytes of `s` followed by `...`, or `s` when it is not longer than `n` |"
func TestTruncate(t *testing.T) {
	t.Parallel()
	for src, want := range map[string]string{
		`{{truncate 3 "abcdef"}}`: "abc...",
		`{{truncate 6 "abcdef"}}`: "abcdef",
		`{{truncate 9 "abc"}}`:    "abc",
	} {
		if got := execFunc(t, gas.EnvProduction, src, nil); got != want {
			t.Errorf("%s = %q, want %q", src, got, want)
		}
	}
	// n counts bytes, not runes.
	truncate := ui.DefaultFuncMap(gas.EnvProduction)["truncate"].(func(int, string) string)
	if got := truncate(1, "é"); got != "\xc3..." {
		t.Errorf("truncate 1 \"é\" = %q, want the first byte", got)
	}
}

// Docs: modules/ui.mdx#template-functions
// "| `now` | `time.Now()` | | `formatTime layout t`, `formatTimePtr layout t` | `t.Format(layout)`; the pointer form returns an empty string for nil | | `add a b`, `sub a b` | Integer arithmetic |"
func TestTimeAndMathFuncs(t *testing.T) {
	t.Parallel()
	when := time.Date(2026, 10, 21, 9, 5, 0, 0, time.UTC)
	data := map[string]any{"T": when, "P": &when, "Nil": (*time.Time)(nil)}
	for src, want := range map[string]string{
		`{{formatTime "2006-01-02" .T}}`:   "2026-10-21",
		`{{formatTimePtr "15:04" .P}}`:     "09:05",
		`[{{formatTimePtr "15:04" .Nil}}]`: "[]",
		`{{add 2 3}} {{sub 2 3}}`:          "5 -1",
		`{{(now).Year}}`:                   time.Now().Format("2006"),
	} {
		if got := execFunc(t, gas.EnvProduction, src, data); got != want {
			t.Errorf("%s = %q, want %q", src, got, want)
		}
	}
}

// Docs: modules/ui.mdx#template-functions
// "| `dict k1 v1 k2 v2 ...` | A `map[string]any`; pairs whose key is not a string are skipped | | `list a b ...` | A `[]any` |"
// Docs: modules/ui.mdx#template-functions
// "`dict` is how a partial gets more than one value: `{{template "card" dict "Name" .UserName "Role" "admin"}}`."
func TestDictAndList(t *testing.T) {
	t.Parallel()
	fm := ui.DefaultFuncMap(gas.EnvProduction)
	d := fm["dict"].(func(...any) map[string]any)("a", 1, 2, "skipped", "b", "x")
	if len(d) != 2 || d["a"] != 1 || d["b"] != "x" {
		t.Errorf("dict = %v, want a and b, the non-string key skipped", d)
	}
	l := fm["list"].(func(...any) []any)(1, "two")
	if len(l) != 2 || l[0] != 1 || l[1] != "two" {
		t.Errorf("list = %v", l)
	}
	store := newStore(t, map[string]string{
		"partials/card.html": `{{define "card"}}{{.Name}}:{{.Role}}{{end}}`,
		"p.html":             `{{template "card" dict "Name" .UserName "Role" "admin"}}`,
	})
	e := ui.NewEngine(store, fm, "base", false, &gas.NopLogger{})
	rr := httptest.NewRecorder()
	if err := e.Render(rr, "p", map[string]any{"UserName": "ana"}); err != nil || rr.Body.String() != "ana:admin" {
		t.Errorf("card partial = %q, %v", rr.Body.String(), err)
	}
}

// Docs: modules/ui.mdx#template-functions
// "| `json v` | `v` marshaled to JSON, or nil when it cannot be |"
func TestJSONFunc(t *testing.T) {
	t.Parallel()
	fn := ui.DefaultFuncMap(gas.EnvProduction)["json"].(func(any) json.RawMessage)
	if got := string(fn(map[string]any{"a": 1})); got != `{"a":1}` {
		t.Errorf("json(map) = %s", got)
	}
	if got := fn(make(chan int)); got != nil {
		t.Errorf("json(chan) = %q, want nil", got)
	}
}

// Docs: modules/ui.mdx#template-functions
// "| `env` | The app's `GasEnv` | | `buildId` | An ID fixed for the life of the process; in development or testing, a new `dev-` ID on every call |"
func TestEnvAndBuildID(t *testing.T) {
	t.Parallel()
	for env, dev := range map[gas.Environment]bool{gas.EnvDevelopment: true, gas.EnvTesting: true, gas.EnvStaging: false, gas.EnvProduction: false} {
		store := newStore(t, map[string]string{"p.html": `{{env}}|{{buildId}}|{{buildId}}`})
		a := startUI(t, store, appOpts{values: map[string]any{"gasenv": string(env)}})
		rr, err := render(t, a.ui, "p", nil)
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(rr.Body.String(), "|")
		if parts[0] != string(env) {
			t.Errorf("%s: env = %q", env, parts[0])
		}
		if dev {
			if !strings.HasPrefix(parts[1], "dev-") || parts[1] == parts[2] {
				t.Errorf("%s: buildId %q then %q, want a new dev- ID per call", env, parts[1], parts[2])
			}
		} else if parts[1] != parts[2] || strings.HasPrefix(parts[1], "dev-") || parts[1] == "" {
			t.Errorf("%s: buildId %q then %q, want one fixed ID", env, parts[1], parts[2])
		}
	}
	a := ui.DefaultFuncMap(gas.EnvProduction)["buildId"].(func() string)()
	b := ui.DefaultFuncMap(gas.EnvStaging)["buildId"].(func() string)()
	if a != b {
		t.Errorf("buildId differs between func maps in one process: %q and %q", a, b)
	}
}

// Docs: modules/ui.mdx#template-functions
// "`Config.FuncMap`, set through `ui.WithConfig`, is merged over the built-ins when the service starts, and a name there replaces the built-in."
func TestConfigFuncMap(t *testing.T) {
	t.Parallel()
	cfg := ui.DefaultConfig()
	cfg.FuncMap = template.FuncMap{"upper": func(string) string { return "mine" }, "extra": func() string { return "extra" }}
	store := newStore(t, map[string]string{"p.html": `{{upper "a"}} {{extra}} {{lower "B"}}`})
	a := startUI(t, store, appOpts{ui: []ui.Option{ui.WithStaticFS(nil), ui.WithConfig(cfg)}})
	if rr, err := render(t, a.ui, "p", nil); err != nil || rr.Body.String() != "mine extra b" {
		t.Fatalf("render = %q, %v", rr.Body.String(), err)
	}
}

// funcsService registers a function from its Init, as on the page.
type funcsService struct{ ui gas.UIProvider }

func (s *funcsService) Name() string { return "funcs" }
func (s *funcsService) Init(context.Context) error {
	s.ui.RegisterFuncs(template.FuncMap{"shout": func(v string) string { return strings.ToUpper(v) + "!" }})
	return nil
}
func (s *funcsService) Close(context.Context) error { return nil }

// Docs: modules/ui.mdx#template-functions
// "And any service can call `RegisterFuncs` from its `Init`"
// Docs: modules/ui.mdx#rendering
// "so template functions other services add while starting are in place."
func TestRegisterFuncsFromInit(t *testing.T) {
	t.Parallel()
	store := newStore(t, map[string]string{"p.html": `{{shout "hi"}}`})
	a := startUI(t, store, appOpts{extra: []gas.Option{
		gas.WithSingletonService[*funcsService](func(u gas.UIProvider) *funcsService { return &funcsService{ui: u} }),
	}})
	if rr, err := render(t, a.ui, "p", nil); err != nil || rr.Body.String() != "HI!" {
		t.Fatalf("render = %q, %v", rr.Body.String(), err)
	}
}

// Docs: modules/ui.mdx#template-functions
// "`RegisterFuncs` merges the functions into the engine and marks the templates for recompiling on the next render. A name that already exists is replaced, with a warning in the log."
func TestRegisterFuncsRecompiles(t *testing.T) {
	t.Parallel()
	store := newStore(t, map[string]string{"p.html": `{{upper "a"}}`})
	a := startUI(t, store, appOpts{values: map[string]any{"gasenv": "production"}})
	if rr, _ := render(t, a.ui, "p", nil); rr.Body.String() != "A" {
		t.Fatalf("before: %q", rr.Body.String())
	}
	lists := store.lists.Load()
	a.ui.RegisterFuncs(template.FuncMap{"upper": func(string) string { return "replaced" }})
	if rr, _ := render(t, a.ui, "p", nil); rr.Body.String() != "replaced" {
		t.Fatalf("after RegisterFuncs: %q, want the replaced function", rr.Body.String())
	}
	if store.lists.Load() == lists {
		t.Fatal("RegisterFuncs did not trigger a recompile")
	}
	found := false
	for _, w := range a.logs.warnings(t) {
		found = found || strings.Contains(w, "already exists")
	}
	if !found {
		t.Fatalf("no warning for the replaced name; warnings %v", a.logs.warnings(t))
	}
}

// Docs: modules/ui.mdx#template-functions
// "Called before the ui service has started, it logs a warning and drops the functions; a service that takes `gas.UIProvider` as a dependency always starts after it."
func TestRegisterFuncsBeforeStart(t *testing.T) {
	t.Parallel()
	sink := &logSink{}
	logger := sinkLogger(sink)
	store := newStore(t, map[string]string{"p.html": `{{upper "a"}}`})
	svc, _ := ui.New[gas.TemplateProvider](ui.WithStaticFS(nil))(store, nil, nil, logger)
	svc.RegisterFuncs(template.FuncMap{"early": func() string { return "early" }})
	if len(sink.warnings(t)) != 1 {
		t.Fatalf("warnings %v, want one", sink.warnings(t))
	}
	if err := svc.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.put(t, "q.html", `{{early}}`)
	rr := httptest.NewRecorder()
	if err := svc.Render(rr, "q", nil); err == nil {
		t.Fatalf("early function was kept: %q", rr.Body.String())
	}

	order := &orderLog{}
	startUI(t, newStore(t, nil), appOpts{extra: []gas.Option{
		gas.WithSingletonService[*orderedDependent](func(u gas.UIProvider) *orderedDependent {
			return &orderedDependent{ui: u, log: order}
		}),
	}})
	if order.readyErr != nil {
		t.Fatalf("dependent's Init saw the ui service not started: %v", order.readyErr)
	}
}

type orderLog struct{ readyErr error }

// orderedDependent checks, from its Init, that the ui service has started.
type orderedDependent struct {
	ui  gas.UIProvider
	log *orderLog
}

func (d *orderedDependent) Name() string { return "dependent" }
func (d *orderedDependent) Init(ctx context.Context) error {
	d.log.readyErr = d.ui.(gas.ReadyReporter).CheckReady(ctx)
	return nil
}
func (d *orderedDependent) Close(context.Context) error { return nil }
