package ui_test

import (
	"context"
	"html/template"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/ui"
)

// The example in the package documentation's Usage section.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	logger := &gas.NopLogger{}

	store, err := memory.NewStore()()
	require.NoError(t, err)

	err = store.Register(ctx, "layouts/base.html",
		[]byte(`{{define "base"}}<main>{{block "content" .}}{{end}}</main>{{end}}`))
	require.NoError(t, err)

	err = store.Register(ctx, "users.html",
		[]byte(`{{define "content"}}<h1>{{.}}</h1>{{end}}`))
	require.NoError(t, err)

	cfg := ui.DefaultConfig()

	// The call below is the example's, line for line, so that the example
	// keeps the constructor's shape: the store, the router, the
	// ConfigProvider and the logger.
	s, err := ui.New[gas.TemplateProvider](ui.WithConfig(cfg))(store, gas.NewRouter(), nil, logger)
	require.NoError(t, err)
	require.Equal(
		t,
		[]string{
			"s, err := ui.New[gas.TemplateProvider](ui.WithConfig(cfg))(store, gas.NewRouter(), nil, logger)",
		},
		docLines(t, "s, err := ui.New["),
	)
	require.Equal(t, []string{"cfg := ui.DefaultConfig()"}, docLines(t, "cfg := "))
	require.NoError(t, s.Init(ctx))

	defer func() { require.NoError(t, s.Close(ctx)) }()

	w, w2 := httptest.NewRecorder(), httptest.NewRecorder()
	require.NoError(t, s.Render(ctx, w, "users", "Users"))
	require.NoError(t, s.RenderFragment(ctx, w2, "users", "Users"))

	assert.Equal(t, "<main><h1>Users</h1></main>", w.Body.String())
	assert.Equal(t, "<h1>Users</h1>", w2.Body.String())

	for _, r := range []*httptest.ResponseRecorder{w, w2} {
		assert.Equal(t, "text/html; charset=utf-8", r.Header().Get("Content-Type"))
	}

	app := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](logger),
		gas.WithServiceInstance[gas.ConfigProvider](gas.ConfigProvider(provider(t, nil))),
		gas.WithSingletonService[gas.TemplateProvider](memory.NewStore()),
		gas.WithSingletonService[ui.Provider](ui.New[gas.TemplateProvider]()),
	)
	require.NoError(t, app.Start(ctx))
	require.NoError(t, app.Stop(ctx))
}

// The error messages the package documentation lists are the ones the
// service returns, and it lists every message this test makes.
//
//nolint:funlen,maintidx,contextcheck // one case per documented message, made with the test helpers.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	users := func(fs files) *ui.Service {
		if fs == nil {
			fs = files{"users.html": `U`}
		}

		return initialized(t, fs, nil)
	}
	renderErr := func(s *ui.Service, name string, opts ...ui.RenderOption) error {
		return s.Render(ctx, newSpy(), name, nil, opts...)
	}
	fragmentErr := func(s *ui.Service, name string, opts ...ui.RenderOption) error {
		return s.RenderFragment(ctx, newSpy(), name, nil, opts...)
	}
	rule := func(edit func(*ui.Settings)) error {
		s, err := ui.New[gas.TemplateProvider](ui.WithConfig(cfgWith(edit)))(
			newStore(t, nil), gas.NewRouter(), nil, newRecorder())
		require.NoError(t, err)

		return s.Init(ctx)
	}
	down := errStoreDown
	canceled, cancel := context.WithCancel(ctx)
	cancel()

	cases := map[string]func() error{
		"gas/ui: logger is nil": func() error {
			_, err := ui.New[gas.TemplateProvider]()(newStore(t, nil), nil, nil, nil)

			return err
		},
		"gas/ui: template provider is nil": func() error {
			_, err := ui.New[gas.TemplateProvider]()(nil, nil, nil, newRecorder())

			return err
		},
		"gas/ui: init: config is nil":      func() error { return initErr(t, ui.WithConfig(nil)) },
		"gas/ui: init: no config provider": func() error { return initErr(t) },
		"gas/ui: init: invalid config: UI.Extensions must hold at least one extension": func() error {
			return rule(func(set *ui.Settings) { set.Extensions = []string{} })
		},
		"gas/ui: init: invalid config: UI.Extensions[0] must be a dot and characters, none a slash, backslash " +
			"or NUL": func() error {
			return rule(func(set *ui.Settings) { set.Extensions = []string{"."} })
		},
		"gas/ui: init: invalid config: UI.PagesPrefix must be empty or a clean path ending in /, not in layouts/ " +
			"or partials/": func() error {
			return rule(func(set *ui.Settings) { set.PagesPrefix = "pages" })
		},
		"gas/ui: init: invalid config: UI.StaticPath must be set when static files are served and UI.StaticPaths " +
			"is empty": func() error {
			return rule(func(set *ui.Settings) { set.StaticDir, set.StaticPath = t.TempDir(), "" })
		},
		"gas/ui: init: invalid config: UI.StaticPath must start with `/`, hold `*` only last, and pair `{` " +
			"with `}`": func() error {
			return rule(
				func(set *ui.Settings) { set.StaticDir, set.StaticPath = t.TempDir(), "/{x" },
			)
		},
		"gas/ui: init: invalid config: UI.StaticPaths[0] must start with `/`, hold `*` only last, and pair `{` " +
			"with `}`": func() error {
			return rule(
				func(set *ui.Settings) { set.StaticDir, set.StaticPaths = t.TempDir(), []string{"x"} },
			)
		},
		"gas/ui: init: invalid config: UI.StaticStripPrefix must be empty or start and end with `/`": func() error {
			return rule(
				func(set *ui.Settings) { set.StaticDir, set.StaticStripPrefix = t.TempDir(), "x/" },
			)
		},
		"gas/ui: init: invalid config: UI.StaticMaxAge must be 0 or at least 1s": func() error {
			return rule(
				func(set *ui.Settings) { set.StaticDir, set.StaticMaxAge = t.TempDir(), time.Millisecond },
			)
		},
		"gas/ui: init: invalid config: UI.StaticDir cannot be opened as a directory: " +
			"no such file or directory": func() error {
			return rule(
				func(set *ui.Settings) { set.StaticDir = filepath.Join(t.TempDir(), "missing") },
			)
		},
		`gas/ui: init: invalid func: "bad-name": not a Go identifier`: func() error {
			return initErr(t, ui.WithConfig(cfgWith(nil)),
				ui.WithFuncs(template.FuncMap{"bad-name": func() string { return "" }}))
		},
		"gas/ui: init: router is nil": func() error {
			return initErr(
				t,
				ui.WithConfig(cfgWith(func(set *ui.Settings) { set.StaticDir = t.TempDir() })),
			)
		},
		"gas/ui: init: file system is nil": func() error {
			return initErr(t, ui.WithConfig(cfgWith(nil)), ui.WithStaticFS(nil))
		},
		"gas/ui: init: service already initialized": func() error { return users(nil).Init(ctx) },
		"gas/ui: init: context canceled": func() error {
			s, _ := build(t, newStore(t, nil), ui.WithConfig(cfgWith(nil)))

			return s.Init(canceled)
		},
		`gas/ui: register funcs: invalid func: "f": nil`: func() error {
			return users(nil).RegisterFuncs(template.FuncMap{"f": nil})
		},
		`gas/ui: register funcs: invalid func: "f": not a function`: func() error {
			return users(nil).RegisterFuncs(template.FuncMap{"f": 1})
		},
		`gas/ui: register funcs: invalid func: "f": must return one value, or a value and an error`: func() error {
			return users(nil).RegisterFuncs(template.FuncMap{"f": func() {}})
		},
		`gas/ui: register funcs: invalid func: "html": reserved by html/template`: func() error {
			return users(
				nil,
			).RegisterFuncs(template.FuncMap{"html": func(s string) string { return s }})
		},
		"gas/ui: register funcs: service closed": func() error {
			s, _ := build(t, newStore(t, nil), ui.WithConfig(cfgWith(nil)))
			require.NoError(t, s.Init(ctx))
			require.NoError(t, s.Close(ctx))

			return s.RegisterFuncs(nil)
		},
		"gas/ui: render: invalid name: empty":                  func() error { return renderErr(users(nil), "") },
		"gas/ui: render: invalid name: not valid UTF-8":        func() error { return renderErr(users(nil), "\xff") },
		"gas/ui: render: invalid name: holds a NUL byte":       func() error { return renderErr(users(nil), "a\x00") },
		"gas/ui: render: invalid name: holds a backslash":      func() error { return renderErr(users(nil), `a\b`) },
		"gas/ui: render: invalid name: not a clean slash path": func() error { return renderErr(users(nil), "../a") },
		`gas/ui: render "users": service not initialized`: func() error {
			s, _ := build(t, newStore(t, nil))

			return renderErr(s, "users")
		},
		`gas/ui: render "users": service closed`: func() error {
			s, _ := build(t, newStore(t, nil), ui.WithConfig(cfgWith(nil)))
			require.NoError(t, s.Init(ctx))
			require.NoError(t, s.Close(ctx))

			return renderErr(s, "users")
		},
		`gas/ui: render "users": context canceled`: func() error {
			return users(nil).Render(canceled, newSpy(), "users", nil)
		},
		`gas/ui: render "users": page not found`: func() error { return renderErr(users(files{}), "users") },
		`gas/ui: render "users": invalid option: nil`: func() error {
			return renderErr(users(nil), "users", nil)
		},
		`gas/ui: render "users": invalid option: WithBlock applies to RenderFragment only`: func() error {
			return renderErr(users(nil), "users", ui.WithBlock("x"))
		},
		`gas/ui: render fragment "users": invalid option: WithLayout applies to Render only`: func() error {
			return fragmentErr(users(nil), "users", ui.WithLayout("x"))
		},
		`gas/ui: render "users": invalid status: must be 200 to 599, and not 204 or 304`: func() error {
			return renderErr(users(nil), "users", ui.WithStatus(http.StatusNoContent))
		},
		`gas/ui: render "users": layout not found: "base"`: func() error {
			return renderErr(users(files{"users.html": `U`, "layouts/l.html": `L`}), "users")
		},
		`gas/ui: render fragment "users": block not found: "sidebar"`: func() error {
			return fragmentErr(users(nil), "users", ui.WithBlock("sidebar"))
		},
		`gas/ui: render fragment "users": no fragment: the page defines no "content" block and has no top-level ` +
			`text`: func() error {
			return fragmentErr(users(files{"users.html": `{{define "x"}}X{{end}}`}), "users")
		},
		`gas/ui: render "users": load templates failed: store down`: func() error {
			st := newFlaky(t, files{"users.html": `U`})
			st.listErr.Store(&down)

			s, _ := initializedLog(t, st, nil)

			return renderErr(s, "users")
		},
		`gas/ui: render "users": load templates failed: "users.html": store down`: func() error {
			st := newFlaky(t, files{"users.html": `U`})
			st.getErr.Store(&down)

			s, _ := initializedLog(t, st, nil)

			return renderErr(s, "users")
		},
		`gas/ui: render "users": compile failed: template: users.html:1: function "nope" not defined`: func() error {
			return renderErr(users(files{"users.html": `{{nope}}`}), "users")
		},
		`gas/ui: render "u": compile failed: u.html defines "home.html", a name ending in an extension, which ` +
			`only files have`: func() error {
			return renderErr(users(files{"u.html": `{{define "home.html"}}{{end}}`}), "u")
		},
		`gas/ui: render "a": compile failed: "a.htm" and "a.html" are both page "a"`: func() error {
			s, _ := initializedLog(
				t,
				newStoreExt(t, files{"a.htm": `1`, "a.html": `2`}, ".html", ".htm"),
				func(set *ui.Settings) { set.Extensions = []string{".html", ".htm"} },
			)

			return renderErr(s, "a")
		},
		`gas/ui: reload: compile failed: ".html" leaves no page name`: func() error {
			return users(files{".html": `x`}).Reload(ctx)
		},
		`gas/ui: render "u": execute failed: template: u.html:1:2: executing "u.html" at <fail>: ` +
			`error calling fail: boom`: func() error {
			s := initialized(t, files{"u.html": `{{fail}}`}, nil, ui.WithFuncs(template.FuncMap{
				"fail": func() (string, error) { return "", errBoom },
			}))

			return renderErr(s, "u")
		},
		`gas/ui: render "users": write failed: client gone`: func() error {
			w := newSpy()

			w.err = errClientGone

			return users(nil).Render(ctx, w, "users", nil)
		},
		"gas/ui: ready: load templates failed: store down": func() error {
			st := newFlaky(t, files{"users.html": `U`})
			s, _ := initializedLog(t, st, nil)
			st.listErr.Store(&down)

			return s.CheckReady(ctx)
		},
		"gas/ui: static handler: file system is nil": func() error {
			_, err := ui.StaticHandler("/s/", nil)

			return err
		},
		"gas/ui: static handler: invalid prefix: must be empty or start and end with `/`": func() error {
			_, err := ui.StaticHandler("s", fstest.MapFS{})

			return err
		},
		"gas/ui: static handler: invalid option: nil": func() error {
			_, err := ui.StaticHandler("/s/", fstest.MapFS{}, nil)

			return err
		},
		"gas/ui: static handler: invalid option: max age must be 0 or at least 1s": func() error {
			_, err := ui.StaticHandler("/s/", fstest.MapFS{}, ui.WithMaxAge(time.Millisecond))

			return err
		},
	}

	for _, text := range slices.Sorted(maps.Keys(cases)) {
		require.EqualError(t, cases[text](), text)
	}

	assert.ElementsMatch(t, slices.Collect(maps.Keys(cases)), docLines(t, "gas/ui: "))

	cfg := cfgWith(func(set *ui.Settings) { set.LayoutName = "" })
	require.EqualError(
		t,
		initErr(t, ui.WithConfig(cfg)),
		"gas/ui: init: invalid config: validate config: "+
			docLines(t, "Key: ")[0],
	)
}
