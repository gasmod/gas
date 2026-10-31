package ui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/ui"
)

// provider returns a configuration holding kv.
func provider(t *testing.T, kv map[string]any) *config.Config {
	t.Helper()

	c := config.New()

	for k, v := range kv {
		require.NoError(t, c.Set(k, v))
	}

	return c
}

// DefaultConfig holds the documented defaults.
func TestConfig_Defaults(t *testing.T) {
	t.Parallel()

	assert.Equal(t, &ui.Config{UI: ui.Settings{
		Extensions:    []string{".html"},
		StaticPath:    "/static/*",
		LayoutName:    "base",
		FragmentBlock: "content",
	}}, ui.DefaultConfig())
}

// Init binds every setting from the `ui` section over DefaultConfig, and
// serves static files from the bound directory on the bound routes.
func TestConfig_Bind(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	dir := staticTree(t)
	router := gas.NewRouter()

	s, err := ui.New[gas.TemplateProvider]()(newStoreExt(t, files{
		"layouts/l.html": `{{define "shell"}}[{{block "main" .}}{{end}}]{{end}}`,
		"pages/p.tmpl":   `{{define "main"}}M{{end}}`,
	}, ".html", ".tmpl"), router, provider(t, map[string]any{
		"ui.staticdir":         dir,
		"ui.staticpaths":       []any{"/css/*", "/assets/{file}"},
		"ui.staticstripprefix": "/css/",
		"ui.staticmaxage":      "1h",
		"ui.staticdotfiles":    true,
		"ui.layoutname":        "shell",
		"ui.fragmentblock":     "main",
		"ui.extensions":        []any{".tmpl", ".html"},
		"ui.pagesprefix":       "pages/",
		"ui.reload":            true,
	}), newRecorder())
	require.NoError(t, err)
	require.NoError(t, s.Init(ctx))

	defer func() { require.NoError(t, s.Close(context.Background())) }()

	assert.Equal(t, "[M]", body(t, s, "p", nil))
	assert.Equal(t, "M", fragmentBody(t, s, "p", nil))

	router.Seal()

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/css/.env", nil))
	assert.Equal(t, "DB_PASSWORD=hunter2", w.Body.String())
	assert.Equal(t, "public, max-age=3600", w.Header().Get("Cache-Control"))

	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodGet, "/assets/app.css", nil))
	assert.Equal(t, http.StatusNotFound, w.Code, "the strip prefix /css/ does not match")
}

// Without StaticStripPrefix, StaticPath's literal part up to the last
// `/` before a wildcard or parameter is removed, and with StaticPaths
// nothing is.
func TestConfig_DerivedStripPrefix(t *testing.T) {
	t.Parallel()

	dir := staticTree(t)

	cases := []struct {
		edit  func(*ui.Settings)
		paths map[string]int
	}{
		{
			func(set *ui.Settings) { set.StaticPath = "/s/{rest}" },
			map[string]int{"/s/app.css": 200},
		},
		{
			func(set *ui.Settings) { set.StaticPath = "/files/img-{name}" },
			map[string]int{"/files/img-x": 404},
		},
		{func(set *ui.Settings) { set.StaticPath = "/app.css" }, map[string]int{"/app.css": 200}},
		{
			func(set *ui.Settings) { set.StaticPaths = []string{"/sub/*"} },
			map[string]int{"/sub/index.html": 200},
		},
	}

	for _, c := range cases {
		router := gas.NewRouter()

		s, err := ui.New[gas.TemplateProvider](ui.WithConfig(cfgWith(func(set *ui.Settings) {
			set.StaticDir = dir
			c.edit(set)
		})))(newStore(t, nil), router, nil, newRecorder())
		require.NoError(t, err)
		require.NoError(t, s.Init(t.Context()))

		router.Seal()

		for path, code := range c.paths {
			w := httptest.NewRecorder()
			router.ServeHTTP(
				w,
				httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil),
			)
			assert.Equal(t, code, w.Code, path)
		}

		require.NoError(t, s.Close(t.Context()))
	}
}

// WithConfig takes a copy: the caller's struct and slices are never
// changed or read after Init, and a copy is checked against its tags.
func TestConfig_WithConfigCopies(t *testing.T) {
	t.Parallel()

	cfg := cfgWith(nil)
	s, _ := build(t, newStore(t, files{"a.html": `A`}), ui.WithConfig(cfg))
	require.NoError(t, s.Init(t.Context()))

	defer func() { require.NoError(t, s.Close(context.Background())) }()

	cfg.UI.Extensions[0] = ".txt"
	cfg.UI.LayoutName = ""

	assert.Equal(t, "A", body(t, s, "a", nil))

	err := initErr(t, ui.WithConfig(cfg))
	require.ErrorIs(t, err, ui.ErrInvalidConfig)

	var verrs validator.ValidationErrors

	require.ErrorAs(t, err, &verrs)
	require.EqualError(
		t,
		err,
		"gas/ui: init: invalid config: validate config: Key: 'Config.UI.LayoutName' "+
			"Error:Field validation for 'LayoutName' failed on the 'required' tag",
	)
}

// initErr returns the error of Init of a service over an empty store
// built with opts.
func initErr(t *testing.T, opts ...ui.Option) error {
	t.Helper()

	s, _ := build(t, newStore(t, nil), opts...)

	return s.Init(t.Context())
}

// The rules the tags cannot express fail Init with an error that names
// the setting, never its value.
func TestConfig_Rules(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	router := gas.NewRouter()

	cases := map[string]func(*ui.Settings){
		"UI.Extensions must hold at least one extension": func(set *ui.Settings) { set.Extensions = nil },
		"UI.Extensions[1] must be a dot and characters, none a slash, backslash or NUL": func(set *ui.Settings) {
			set.Extensions = []string{".html", "html"}
		},
		"UI.PagesPrefix must be empty or a clean path ending in /, not in layouts/ or partials/": func(
			set *ui.Settings,
		) {
			set.PagesPrefix = "layouts/x/"
		},
		"UI.StaticPath must be set when static files are served and UI.StaticPaths is empty": func(set *ui.Settings) {
			set.StaticDir, set.StaticPath = dir, ""
		},
		"UI.StaticPath must start with `/`, hold `*` only last, and pair `{` with `}`": func(
			set *ui.Settings,
		) {
			set.StaticDir, set.StaticPath = dir, "static/*"
		},
		"UI.StaticPaths[1] must start with `/`, hold `*` only last, and pair `{` with `}`": func(
			set *ui.Settings,
		) {
			set.StaticDir, set.StaticPaths = dir, []string{"/a/*", "/b/*/c"}
		},
		"UI.StaticStripPrefix must be empty or start and end with `/`": func(set *ui.Settings) {
			set.StaticDir, set.StaticStripPrefix = dir, "/static"
		},
		"UI.StaticMaxAge must be 0 or at least 1s": func(set *ui.Settings) {
			set.StaticDir, set.StaticMaxAge = dir, time.Millisecond
		},
	}

	for want, edit := range cases {
		s, err := ui.New[gas.TemplateProvider](
			ui.WithConfig(cfgWith(edit)),
		)(
			newStore(t, nil),
			router,
			nil,
			newRecorder(),
		)
		require.NoError(t, err)

		err = s.Init(t.Context())
		require.ErrorIs(t, err, ui.ErrInvalidConfig, want)
		require.EqualError(t, err, "gas/ui: init: invalid config: "+want)
	}
}

// The route and prefix rules accept what the router takes and refuse
// what it would panic on at Start.
func TestConfig_Routes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.css"), []byte("a"), 0o600))

	for path, ok := range map[string]bool{
		"/static/*": true, "/s/{rest}": true, "/{a}/{b}": true, "/": true, "/x": true,
		"": false, "x": false, "/*/x": false, "/{a": false, "/a}": false, "/{{a}}": false, "/{a}}": false,
	} {
		s, err := ui.New[gas.TemplateProvider](ui.WithConfig(cfgWith(func(set *ui.Settings) {
			set.StaticDir, set.StaticPath = dir, path
		})))(newStore(t, nil), gas.NewRouter(), nil, newRecorder())
		require.NoError(t, err)

		err = s.Init(t.Context())
		if ok {
			require.NoError(t, err, path)
			require.NoError(t, s.Close(t.Context()))
		} else {
			require.ErrorIs(t, err, ui.ErrInvalidConfig, path)
		}
	}

	for _, prefix := range []string{"layouts/", "partials/", "/abs/", "a", "a//", "./"} {
		err := initErr(
			t,
			ui.WithConfig(cfgWith(func(set *ui.Settings) { set.PagesPrefix = prefix })),
		)
		require.ErrorIs(t, err, ui.ErrInvalidConfig, prefix)
	}

	require.NoError(
		t,
		initClose(t, ui.WithConfig(cfgWith(func(set *ui.Settings) { set.PagesPrefix = "a/b/" }))),
	)
}

// initClose initializes and closes a service built with opts, and returns
// the error of Init.
func initClose(t *testing.T, opts ...ui.Option) error {
	t.Helper()

	s, _ := build(t, newStore(t, nil), opts...)

	err := s.Init(t.Context())
	if err != nil {
		return err
	}

	return s.Close(t.Context())
}

// A bind that fails wraps ErrInvalidConfig and the provider's error.
func TestConfig_BindError(t *testing.T) {
	t.Parallel()

	s, err := ui.New[gas.TemplateProvider]()(newStore(t, nil), nil, provider(t, map[string]any{
		"ui.staticmaxage": "soon",
	}), newRecorder())
	require.NoError(t, err)

	err = s.Init(t.Context())
	require.ErrorIs(t, err, ui.ErrInvalidConfig)
	assert.Contains(t, err.Error(), "gas/ui: init: invalid config: ")
}
