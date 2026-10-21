package modulesui_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gasmod/gas/ui"
)

// Docs: modules/ui.mdx#configuration
// "| `ui.staticdir` | string | `static/` |" and "| `ui.staticpath` | string | `/static/*` |" and "| `ui.staticpaths` | list of strings | empty |" and "| `ui.staticstripprefix` | string | empty |" and "| `ui.layoutname` | string | `base` |"
func TestDefaults(t *testing.T) {
	t.Parallel()
	got := ui.DefaultConfig().UI
	if got.StaticDir != "static/" || got.StaticPath != "/static/*" || len(got.StaticPaths) != 0 ||
		got.StaticStripPrefix != "" || got.LayoutName != "base" {
		t.Fatalf("DefaultConfig().UI = %+v", got)
	}
}

// Docs: modules/ui.mdx#configuration
// "Unless `ui.WithConfig` is given, the service binds its settings from the `gas.ConfigProvider` over `ui.DefaultConfig()` when it starts, so a key that is not set keeps its default. The service also reads `GasEnv`, the app environment"
func TestBindOverDefaults(t *testing.T) {
	t.Parallel()
	store := newStore(t, map[string]string{
		"layouts/l.html": `{{define "chosen"}}<c>{{block "content" .}}{{end}}</c>{{end}}`,
		"p.html":         `{{define "content"}}{{env}}{{end}}`,
	})
	a := startUI(t, store, appOpts{
		values: map[string]any{"gasenv": "staging", "ui": map[string]any{"layoutname": "chosen"}},
		ui:     []ui.Option{ui.WithStaticFS(fstest.MapFS{"x.css": {Data: []byte("x")}})},
	})
	if rr, err := render(t, a.ui, "p", nil); err != nil || rr.Body.String() != "<c>staging</c>" {
		t.Fatalf("render = %q, %v; want the bound layout and GasEnv", rr.Body.String(), err)
	}
	if got := uiRoutes(a); len(got) != 1 || got[0].Path != "/static/*" {
		t.Fatalf("routes %+v, want the default /static/* kept", got)
	}
}

// Docs: modules/ui.mdx#configuration
// "| `ui.staticpath` | string | `/static/*` | URL pattern of the static route. Must start with `/` |" and "Each must start with `/` |" and "Must start and end with `/` |" and "| `ui.layoutname` | string | `base` | The `{{define}}` name of the layout entry point. Must not be empty |"
// Docs: modules/ui.mdx#configuration
// "The service validates the configuration when it starts, whether it was bound or given with `ui.WithConfig`."
func TestValidationRules(t *testing.T) {
	t.Parallel()
	bad := map[string]func(*ui.Settings){
		"staticpath without /":            func(s *ui.Settings) { s.StaticPath = "static/*" },
		"staticpaths entry without /":     func(s *ui.Settings) { s.StaticPaths = []string{"/css/*", "js/*"} },
		"strip prefix without leading /":  func(s *ui.Settings) { s.StaticStripPrefix = "static/" },
		"strip prefix without trailing /": func(s *ui.Settings) { s.StaticStripPrefix = "/static" },
		"empty layoutname":                func(s *ui.Settings) { s.LayoutName = "" },
	}
	for name, mutate := range bad {
		cfg := ui.DefaultConfig()
		mutate(&cfg.UI)
		_, err := buildUI(t, newStore(t, nil), appOpts{ui: []ui.Option{ui.WithStaticFS(fstest.MapFS{}), ui.WithConfig(cfg)}})
		if err == nil || !strings.Contains(err.Error(), "gas/ui: invalid configuration: ") {
			t.Errorf("WithConfig, %s: start = %v, want an invalid configuration error", name, err)
		}
	}
	boundBad := map[string]map[string]any{
		"staticpath without /":         {"staticpath": "static/*"},
		"staticpaths entry without /":  {"staticpaths": []any{"js/*"}},
		"strip prefix without slashes": {"staticstripprefix": "static"},
		"empty layoutname":             {"layoutname": ""},
	}
	for name, v := range boundBad {
		_, err := buildUI(t, newStore(t, nil), appOpts{values: map[string]any{"ui": v}})
		if err == nil || !strings.Contains(err.Error(), "gas/ui: invalid configuration: ") {
			t.Errorf("bound, %s: start = %v, want an invalid configuration error", name, err)
		}
	}
	good := ui.DefaultConfig()
	good.UI.StaticStripPrefix = "/"
	if _, err := buildUI(t, newStore(t, nil), appOpts{ui: []ui.Option{ui.WithStaticFS(fstest.MapFS{}), ui.WithConfig(good)}}); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

// Docs: modules/ui.mdx#configuration
// "Besides the rules in the table, `ui.staticdir` must be an existing directory inside the working directory, unless a static filesystem was given; that check is skipped when running under `go test`."
func TestStaticDirCheckSkippedUnderGoTest(t *testing.T) {
	t.Parallel()
	_, err := buildUI(t, newStore(t, nil), appOpts{values: map[string]any{"ui": map[string]any{"staticdir": "docsclaim-no-such-dir/"}}, ui: []ui.Option{}})
	if err != nil {
		t.Fatalf("missing staticdir under go test: start = %v, want the check skipped", err)
	}
}

// Docs: modules/ui.mdx#configuration
// "`ui.staticdir` must be an existing directory inside the working directory, unless a static filesystem was given"
// Docs: modules/ui.mdx#configuration
// "With the defaults and no `ui.WithStaticFS`, an app started from a directory with no `static` subdirectory fails to start with `gas/ui: invalid configuration: UI.StaticDir: "static/" does not exist`. Create the directory, pass a static filesystem, or set `ui.staticdir` to empty."
// The check only runs outside go test, so this builds testdata/staticcheck
// and runs it in scratch directories.
func TestStaticDirCheckOutsideGoTest(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := filepath.Join(t.TempDir(), "staticcheck")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = filepath.Join("testdata", "staticcheck")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	run := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), bin, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("run %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	empty := t.TempDir()
	if got := run(t, empty); !strings.Contains(got, `gas/ui: invalid configuration: UI.StaticDir: "static/" does not exist`) {
		t.Errorf("defaults without ./static: %q", got)
	}
	if got := run(t, empty, "fs"); got != "started" {
		t.Errorf("with a static filesystem: %q, want started", got)
	}
	if got := run(t, empty, "dir="); got != "started" {
		t.Errorf("with ui.staticdir empty: %q, want started", got)
	}
	withStatic := t.TempDir()
	if err := os.Mkdir(filepath.Join(withStatic, "static"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := run(t, withStatic); got != "started" {
		t.Errorf("with ./static: %q, want started", got)
	}
	inner := filepath.Join(withStatic, "app")
	if err := os.Mkdir(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := run(t, inner, "dir=../static"); !strings.Contains(got, "gas/ui: invalid configuration: ") {
		t.Errorf("staticdir outside the working directory: %q, want an invalid configuration error", got)
	}
	if err := os.WriteFile(filepath.Join(withStatic, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := run(t, withStatic, "dir=file"); !strings.Contains(got, "gas/ui: invalid configuration: ") {
		t.Errorf("staticdir naming a file: %q, want an invalid configuration error", got)
	}
}
