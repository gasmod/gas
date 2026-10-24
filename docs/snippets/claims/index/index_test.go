package index_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// landingPkg is the app the landing page shows under "Start a new app".
const landingPkg = "github.com/gasmod/gas/docs/snippets/landing"

// Docs: index.mdx#start-a-new-app
// "This small program is a complete Gas app."
// The app, driven through Start and Handler instead of Run so the
// test binds no port.
func TestQuickstartIsACompleteApp(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Handle(nil, http.MethodGet, "/", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, "Hello, World!")
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "Hello, World!" {
		t.Fatalf("got %d %q, want 200 %q", resp.StatusCode, body, "Hello, World!")
	}
}

// Docs: index.mdx#start-a-new-app
// "This small program is a complete Gas app."
// The page's main.go builds on its own: no other file is needed.
func TestQuickstartBuildsAlone(t *testing.T) {
	t.Parallel()
	out, err := exec.CommandContext(t.Context(), "go", "build", "-o", os.DevNull, landingPkg).CombinedOutput()
	if err != nil {
		t.Fatalf("go build %s: %v\n%s", landingPkg, err, out)
	}
}

// Docs: index.mdx#start-a-new-app
// "`Run` starts the server, waits for a shutdown signal, and closes the app's
// services."
// The shutdown signals are SIGINT and SIGTERM.
// Not parallel: it signals the whole test process.
func TestRunBlocksUntilSignalThenShutsDown(t *testing.T) {
	requireSignals(t)
	// Keep the default action (exit) off for the whole test, so a signal
	// sent before Run has subscribed cannot kill the test binary.
	guard := make(chan os.Signal, 8)
	signal.Notify(guard, syscall.SIGINT, syscall.SIGTERM)
	t.Cleanup(func() { signal.Stop(guard) })

	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			port := freePort(t)
			a := &svc[kA]{}
			app := gas.NewApp(
				gas.WithServiceInstance[gas.ConfigProvider](serverConfig(t, port, nil)),
				gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return a }),
			)
			app.Router().Handle(nil, http.MethodGet, "/", func(ctx gas.Context) error {
				return ctx.Text(http.StatusOK, "Hello, World!")
			})
			done := make(chan error, 1)
			go func() { done <- app.Run() }()

			url := fmt.Sprintf("http://127.0.0.1:%d/", port)
			if !waitFor(5*time.Second, func() bool {
				resp, err := http.Get(url)
				if err != nil {
					return false
				}
				_ = resp.Body.Close()
				return resp.StatusCode == http.StatusOK
			}) {
				t.Fatal("Run never served the app")
			}

			time.Sleep(200 * time.Millisecond)
			select {
			case err := <-done:
				t.Fatalf("Run returned %v before any signal", err)
			default:
			}

			// Repeat the signal until Run returns, in case the first one
			// arrived before Run subscribed.
			var err error
			returned := waitFor(5*time.Second, func() bool {
				select {
				case err = <-done:
					return true
				default:
				}
				_ = sendSignal(sig)
				time.Sleep(100 * time.Millisecond)
				return false
			})
			if !returned {
				t.Fatalf("Run did not return after %v", sig)
			}
			if err != nil {
				t.Fatalf("Run after %v = %v, want nil", sig, err)
			}
			if a.closes() != 1 {
				t.Fatalf("service closed %d times after %v, want 1", a.closes(), sig)
			}
			if accepting(port) {
				t.Fatalf("port %d still accepts connections after Run returned", port)
			}
		})
	}
}

// Docs: index.mdx#what-is-gas
// "Each module is its own Go module"
func TestEachModuleIsItsOwnGoModule(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, dir := range listedModules {
		self := "github.com/gasmod/gas/" + dir
		data, err := os.ReadFile(filepath.Join(root, dir, "go.mod"))
		if err != nil {
			t.Errorf("%s has no go.mod: %v", dir, err)
			continue
		}
		if !slices.Contains(strings.Split(string(data), "\n"), "module "+self) {
			t.Errorf("%s/go.mod does not declare module %s", dir, self)
		}
	}
}

// goListDeps returns "module importpath" for every dependency of the
// non-test-support packages of module mod.
func goListDeps(t *testing.T, mod string) []string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "go", "list",
		"-f", "{{if .Module}}{{.Module.Path}}{{end}} {{.ImportPath}}", mod+"/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list %s/...: %v\n%s", mod, err, out)
	}
	var own []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		m, pkg, _ := strings.Cut(line, " ")
		if m == mod && !strings.Contains(pkg, "/internal/testutil") {
			own = append(own, pkg)
		}
	}
	args := append([]string{"list", "-deps", "-f", "{{if .Module}}{{.Module.Path}}{{end}} {{.ImportPath}}"}, own...)
	out, err = exec.CommandContext(t.Context(), "go", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s: %v\n%s", mod, err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// Docs: index.mdx#what-is-gas
// "Each module is its own Go module, so applications only add the dependencies
// they use."
// The page's app, which only serves HTTP, depends on no Gas module but
// the core module, github.com/gasmod/gas, apart from its own. Packages inside
// the core module, such as github.com/gasmod/gas/internal, are part of the
// core.
func TestHTTPOnlyAppDependsOnCoreAlone(t *testing.T) {
	t.Parallel()
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps",
		"-f", "{{.ImportPath}} {{if .Module}}{{.Module.Path}}{{end}}", landingPkg).CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	mods := map[string]string{} // import path -> module path
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		pkg, mod, _ := strings.Cut(line, " ")
		mods[pkg] = mod
	}
	var gasMods []string
	for _, mod := range mods {
		if strings.HasPrefix(mod, "github.com/gasmod/") && mod != mods[landingPkg] && !slices.Contains(gasMods, mod) {
			gasMods = append(gasMods, mod)
		}
	}
	slices.Sort(gasMods)
	if want := []string{"github.com/gasmod/gas"}; !slices.Equal(gasMods, want) {
		t.Fatalf("the page's app depends on Gas modules %v, want only %v", gasMods, want)
	}
}

// Docs: index.mdx#what-is-gas
// "Each module is its own Go module, so applications only add the dependencies
// they use."
// No module's own packages import another Gas module than core, so depending
// on one module pulls in no other, and what modules share comes from core.
// Assumption: internal/testutil packages are test support, not code an app
// can import, and are left out.
func TestModulesMeetOnlyThroughCore(t *testing.T) {
	t.Parallel()
	for _, dir := range listedModules {
		self := "github.com/gasmod/gas/" + dir
		for _, line := range goListDeps(t, self) {
			mod, pkg, _ := strings.Cut(line, " ")
			if mod != self && mod != "github.com/gasmod/gas" && strings.HasPrefix(mod, "github.com/gasmod/") {
				t.Errorf("module %s imports %s from module %s", self, pkg, mod)
			}
		}
	}
}

// Docs: index.mdx#what-is-gas
// "The framework has a small core package, `github.com/gasmod/gas`, and optional
// modules"
// The core and each module are separate Go modules in one repository.
func TestCoreAndModulesLiveInOneRepository(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	core, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(strings.Split(string(core), "\n"), "module github.com/gasmod/gas") {
		t.Fatal("the repository root go.mod does not declare module github.com/gasmod/gas")
	}
	work, err := os.ReadFile(filepath.Join(root, "go.work"))
	if err != nil {
		t.Fatal(err)
	}
	for card, d := range listedModules {
		mod := "github.com/gasmod/gas/" + d
		data, err := os.ReadFile(filepath.Join(root, d, "go.mod"))
		if err != nil {
			t.Errorf("%s: no go.mod in %s: %v", card, d, err)
			continue
		}
		if !slices.Contains(strings.Split(string(data), "\n"), "module "+mod) {
			t.Errorf("%s: %s/go.mod does not declare module %s", card, d, mod)
		}
		if !regexp.MustCompile(`(?m)^\s*\./` + regexp.QuoteMeta(d) + `\s*$`).Match(work) {
			t.Errorf("%s: go.work does not use ./%s", card, d)
		}
	}
}
