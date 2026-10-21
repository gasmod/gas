package guideshtml_test

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/ui"
)

// staticApp starts an App whose ui service uses settings, plus WithStaticFS
// when fsys is non-nil.
func staticApp(t *testing.T, settings func(*ui.Settings), fsys fs.FS) *gas.App {
	t.Helper()
	cfg := ui.DefaultConfig()
	settings(&cfg.UI)
	opts := []ui.Option{ui.WithConfig(cfg)}
	if fsys != nil {
		opts = append(opts, ui.WithStaticFS(fsys))
	}
	app, _ := uiApp(t, memStore(t, nil), nil, nil, opts...)
	return app
}

func wantServed(t *testing.T, app *gas.App, path, body string) {
	t.Helper()
	code, got := get(t, app, path)
	if code != http.StatusOK || got != body {
		t.Fatalf("GET %s = %d %q, want %d %q", path, code, got, http.StatusOK, body)
	}
}

func assetTree() fstest.MapFS {
	return fstest.MapFS{
		"app.css":    &fstest.MapFile{Data: []byte("body{}")},
		"sub/app.js": &fstest.MapFile{Data: []byte("js()")},
	}
}

// Docs: guides/html.mdx#static-files
// "Set `UI.StaticDir` (or pass `ui.WithStaticFS` for an `embed.FS`) and the
// service registers a route serving it"
func TestStaticDirServed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := staticApp(t, func(s *ui.Settings) { s.StaticDir = dir }, nil)
	wantServed(t, app, "/static/app.css", "body{}")
}

// Docs: guides/html.mdx#static-files
// "Set `UI.StaticDir` (or pass `ui.WithStaticFS` for an `embed.FS`) and the
// service registers a route serving it"
func TestStaticFSServed(t *testing.T) {
	t.Parallel()
	app := staticApp(t, func(s *ui.Settings) { s.StaticDir = "" }, assetTree())
	wantServed(t, app, "/static/app.css", "body{}")
	wantServed(t, app, "/static/sub/app.js", "js()")
}

// Docs: guides/html.mdx#static-files
// "with directory listing blocked"
// Assumption: blocked means a directory request does not answer 200 and does
// not list the directory's entries; the exact status is not stated.
func TestStaticDirectoryListingBlocked(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app.css", "sub/app.js"} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	backends := map[string]*gas.App{
		"StaticDir":    staticApp(t, func(s *ui.Settings) { s.StaticDir = dir }, nil),
		"WithStaticFS": staticApp(t, func(s *ui.Settings) { s.StaticDir = "" }, assetTree()),
	}
	for name, app := range backends {
		for _, path := range []string{"/static/", "/static/sub/"} {
			code, body := get(t, app, path)
			if code == http.StatusOK || strings.Contains(body, "app.css") || strings.Contains(body, "app.js") {
				t.Errorf("%s: GET %s = %d %q, want a non-200 answer with no listing", name, path, code, body)
			}
		}
	}
}

// Docs: guides/html.mdx#static-files
// "When `StaticStripPrefix` is empty it defaults to the literal part of
// `StaticPath` up to the wildcard, so the default `/static/*` strips
// `/static/` and `/static/app.css` serves `app.css` from the root of the
// static directory."
func TestStaticDefaultPathAndStrip(t *testing.T) {
	t.Parallel()
	if got := ui.DefaultConfig().UI.StaticPath; got != "/static/*" {
		t.Fatalf("default StaticPath = %q, want %q", got, "/static/*")
	}
	app := staticApp(t, func(s *ui.Settings) { s.StaticDir = "" }, assetTree())
	wantServed(t, app, "/static/app.css", "body{}")
}

// Docs: guides/html.mdx#static-files
// "When `StaticStripPrefix` is empty it defaults to the literal part of
// `StaticPath` up to the wildcard"
func TestStaticCustomPathStripsLiteralPart(t *testing.T) {
	t.Parallel()
	app := staticApp(t, func(s *ui.Settings) {
		s.StaticDir = ""
		s.StaticPath = "/assets/*"
	}, assetTree())
	wantServed(t, app, "/assets/app.css", "body{}")
}

// Docs: guides/html.mdx#static-files
// "If the FS mirrors your URLs instead (`static/app.css` at `/static/app.css`),
// set `StaticStripPrefix: "/"` to strip nothing."
func TestStaticStripPrefixSlashStripsNothing(t *testing.T) {
	t.Parallel()
	mirrored := fstest.MapFS{"static/app.css": &fstest.MapFile{Data: []byte("mirrored")}}
	app := staticApp(t, func(s *ui.Settings) {
		s.StaticDir = ""
		s.StaticStripPrefix = "/"
	}, mirrored)
	wantServed(t, app, "/static/app.css", "mirrored")
}

// Docs: guides/html.mdx#static-files
// "`StaticPath` / `StaticPaths` | **Where** to serve it, as URL patterns" and
// "With `StaticPaths` and no `StaticStripPrefix`, nothing is stripped."
func TestStaticPathsStripNothing(t *testing.T) {
	t.Parallel()
	tree := fstest.MapFS{
		"css/app.css": &fstest.MapFile{Data: []byte("css")},
		"js/app.js":   &fstest.MapFile{Data: []byte("js")},
	}
	app := staticApp(t, func(s *ui.Settings) {
		s.StaticDir = ""
		s.StaticPaths = []string{"/css/*", "/js/*"}
	}, tree)
	wantServed(t, app, "/css/app.css", "css")
	wantServed(t, app, "/js/app.js", "js")
}
