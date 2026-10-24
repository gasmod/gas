package modulesui_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/ui"
)

func get(a *uiApp, path string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	a.app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

func staticFS() fstest.MapFS {
	return fstest.MapFS{
		"app.css":        {Data: []byte("root-css")},
		"static/app.css": {Data: []byte("mirrored-css")},
		"css/app.css":    {Data: []byte("css-tree")},
		"js/app.js":      {Data: []byte("js-tree")},
		"sub/index.html": {Data: []byte("index")},
	}
}

// uiRoutes returns the routes the ui service owns.
func uiRoutes(a *uiApp) []gas.RegisteredRoute {
	return a.app.Router().Routes()["gas/ui"]
}

// Docs: modules/ui.mdx#static-files
// "The service registers a `GET` route for static files when it starts, owned by the ui service, on `ui.staticpath` (`/static/*` by default), or on each pattern in `ui.staticpaths` when that is set."
func TestStaticRoutes(t *testing.T) {
	t.Parallel()
	def := startUI(t, newStore(t, nil), appOpts{ui: []ui.Option{ui.WithStaticFS(staticFS())}})
	if got := uiRoutes(def); len(got) != 1 || got[0].Method != http.MethodGet || got[0].Path != "/static/*" {
		t.Errorf("default routes owned by gas/ui = %+v, want GET /static/*", got)
	}
	custom := startUI(t, newStore(t, nil), appOpts{
		values: map[string]any{"ui": map[string]any{"staticpath": "/assets/*"}},
		ui:     []ui.Option{ui.WithStaticFS(staticFS())},
	})
	if got := uiRoutes(custom); len(got) != 1 || got[0].Path != "/assets/*" {
		t.Errorf("ui.staticpath /assets/*: routes %+v", got)
	}
	many := startUI(t, newStore(t, nil), appOpts{
		values: map[string]any{"ui": map[string]any{"staticpaths": []any{"/css/*", "/js/*"}}},
		ui:     []ui.Option{ui.WithStaticFS(staticFS())},
	})
	var paths []string
	for _, r := range uiRoutes(many) {
		paths = append(paths, r.Method+" "+r.Path)
	}
	slices.Sort(paths)
	if !slices.Equal(paths, []string{"GET /css/*", "GET /js/*"}) {
		t.Errorf("ui.staticpaths: routes %v, want one GET per pattern and none on ui.staticpath", paths)
	}
}

// Docs: modules/ui.mdx#wiring
// "`ui.WithStaticFS(fsys)` serves static files from `fsys` instead of `ui.staticdir`."
// Docs: modules/ui.mdx#static-files
// "Files come from the `ui.WithStaticFS` filesystem when one was given, otherwise from `ui.staticdir`. With neither, no route is registered."
// Docs: modules/ui.mdx#configuration
// "| `ui.staticdir` | string | `static/` | Directory of static files. Empty disables the directory; ignored when `ui.WithStaticFS` is given |"
func TestStaticSource(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.css"), []byte("from-dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	fromDir := startUI(t, newStore(t, nil), appOpts{values: map[string]any{"ui": map[string]any{"staticdir": dir + "/"}}, ui: []ui.Option{}})
	if rr := get(fromDir, "/static/app.css"); rr.Body.String() != "from-dir" {
		t.Errorf("staticdir: %d %q", rr.Code, rr.Body.String())
	}
	fsWins := startUI(t, newStore(t, nil), appOpts{
		values: map[string]any{"ui": map[string]any{"staticdir": dir + "/"}},
		ui:     []ui.Option{ui.WithStaticFS(staticFS())},
	})
	if rr := get(fsWins, "/static/app.css"); rr.Body.String() != "root-css" {
		t.Errorf("WithStaticFS with staticdir set: %d %q, want the filesystem", rr.Code, rr.Body.String())
	}
	neither := startUI(t, newStore(t, nil), appOpts{values: map[string]any{"ui": map[string]any{"staticdir": ""}}, ui: []ui.Option{}})
	if got := uiRoutes(neither); len(got) != 0 {
		t.Errorf("no filesystem and empty staticdir: routes %+v, want none", got)
	}
}

// Docs: modules/ui.mdx#static-files
// "When `ui.staticstripprefix` is empty, it defaults to `ui.staticpath`'s literal part up to its first wildcard or path parameter: `/static/*` strips `/static/`, so `/static/app.css` is served from `app.css` at the root of the static directory or filesystem. If your files mirror the URLs instead (`static/app.css` served at `/static/app.css`), set `ui.staticstripprefix` to `/`. With `ui.staticpaths` set and no strip prefix, nothing is stripped"
func TestStaticStripPrefix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		values map[string]any
		path   string
		want   string
	}{
		{"default", nil, "/static/app.css", "root-css"},
		{"path parameter", map[string]any{"staticpath": "/files/{name}"}, "/files/app.css", "root-css"},
		{"mirrored", map[string]any{"staticstripprefix": "/"}, "/static/app.css", "mirrored-css"},
		{"staticpaths", map[string]any{"staticpaths": []any{"/css/*", "/js/*"}}, "/css/app.css", "css-tree"},
		{"staticpaths js", map[string]any{"staticpaths": []any{"/css/*", "/js/*"}}, "/js/app.js", "js-tree"},
	}
	for _, c := range cases {
		a := startUI(t, newStore(t, nil), appOpts{values: map[string]any{"ui": c.values}, ui: []ui.Option{ui.WithStaticFS(staticFS())}})
		if rr := get(a, c.path); rr.Code != http.StatusOK || rr.Body.String() != c.want {
			t.Errorf("%s: GET %s = %d %q, want %q", c.name, c.path, rr.Code, rr.Body.String(), c.want)
		}
	}
}

// Docs: modules/ui.mdx#static-files
// "A request whose path ends in `/`, or that names the strip prefix itself without the slash (`/static`), gets 404, so directory listings are never served."
func TestStaticNoListings(t *testing.T) {
	t.Parallel()
	a := startUI(t, newStore(t, nil), appOpts{
		values: map[string]any{"ui": map[string]any{"staticpath": "/static*"}},
		ui:     []ui.Option{ui.WithStaticFS(staticFS())},
	})
	for _, path := range []string{"/static/", "/static/sub/", "/static"} {
		if rr := get(a, path); rr.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d %q, want 404", path, rr.Code, rr.Body.String())
		}
	}
	if rr := get(a, "/static/app.css"); rr.Code != http.StatusOK {
		t.Errorf("GET /static/app.css = %d", rr.Code)
	}
}

// Docs: modules/ui.mdx#static-files
// "The same handler is exported as `ui.StaticHandler(prefix, dir)` and `ui.StaticHandlerFS(prefix, fsys)` for routes of your own."
func TestStaticHandlers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("dir-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, h := range map[string]http.HandlerFunc{
		"StaticHandler":   ui.StaticHandler("/files/", dir),
		"StaticHandlerFS": ui.StaticHandlerFS("/files/", fstest.MapFS{"a.txt": {Data: []byte("dir-file")}}),
	} {
		rr := httptest.NewRecorder()
		h(rr, httptest.NewRequest(http.MethodGet, "/files/a.txt", nil))
		if rr.Body.String() != "dir-file" {
			t.Errorf("%s: %d %q", name, rr.Code, rr.Body.String())
		}
		for _, path := range []string{"/files/", "/files"} {
			rr := httptest.NewRecorder()
			h(rr, httptest.NewRequest(http.MethodGet, path, nil))
			if rr.Code != http.StatusNotFound {
				t.Errorf("%s: GET %s = %d, want 404", name, path, rr.Code)
			}
		}
	}
}
