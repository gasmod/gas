package startinstallation_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/docs/snippets/claims/internal/testport"
)

// helloPkg is the app the page shows.
const helloPkg = "github.com/gasmod/gas/docs/snippets/start-installation"

// modules is the list the page gives.
var modules = []string{"auth", "cache", "config", "database", "email", "log", "migrate", "queue", "storage", "template", "ui"}

// repoRoot is the directory holding go.work.
func repoRoot(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(d, "go.work")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			t.Fatal("go.work not found")
		}
		d = filepath.Dir(d)
	}
}

// Docs: start/installation.mdx#requirements
// "Go 1.27.3 or newer. The core and every module declare `go 1.27.3` in their `go.mod`."
func TestEveryModuleDeclaresGo1273(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, d := range append([]string{"."}, modules...) {
		data, err := os.ReadFile(filepath.Join(root, d, "go.mod"))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(strings.Split(string(data), "\n"), "go 1.27.3") {
			t.Errorf("%s/go.mod does not declare go 1.27.3", d)
		}
	}
}

// Docs: start/installation.mdx#requirements
// "These docs describe v0.6.0."
// The version the docs site shows, and the version every Gas module
// requirement and go.work replace names, are v0.6.0.
func TestDocsDescribeV060(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	ver, err := os.ReadFile(filepath.Join(root, "docs/src/lib/version.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ver), "'v0.6.0'") {
		t.Fatalf("docs/src/lib/version.ts does not say v0.6.0:\n%s", ver)
	}
	re := regexp.MustCompile(`(github\.com/gasmod/gas(?:/[a-z]+)?)\s+(v\d+\.\d+\.\d+\S*)`)
	work, err := os.ReadFile(filepath.Join(root, "go.work"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range re.FindAllStringSubmatch(string(work), -1) {
		if m[2] != "v0.6.0" {
			t.Errorf("go.work pins %s at %s, want v0.6.0", m[1], m[2])
		}
	}
}

// Docs: start/installation.mdx#install-the-core
// "The core holds the app, the DI container, the router, handlers, middleware, errors, and events. It is enough for an HTTP app with no infrastructure."
func TestCoreHoldsTheBasicsAndIsEnough(t *testing.T) {
	t.Parallel()
	// Each of these lives in the core package.
	for _, v := range []any{(*gas.App)(nil), (*gas.ServiceContainer)(nil), (*gas.Router)(nil), (*gas.Context)(nil),
		(*gas.Middleware)(nil), (*gas.Error)(nil), (*gas.EventBus)(nil)} {
		if pkg := fmt.Sprintf("%T", v); !strings.HasPrefix(pkg, "*gas.") {
			t.Errorf("%s is not in the core package", pkg)
		}
	}
	// The app may depend on packages inside the core module, such as
	// github.com/gasmod/gas/internal, but on no other Gas module apart from
	// its own.
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps",
		"-f", "{{.ImportPath}} {{if .Module}}{{.Module.Path}}{{end}}", helloPkg).CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	mods := map[string]string{} // import path -> module path
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		pkg, mod, _ := strings.Cut(line, " ")
		mods[pkg] = mod
	}
	for pkg, mod := range mods {
		if strings.HasPrefix(mod, "github.com/gasmod/") && mod != mods[helloPkg] && mod != "github.com/gasmod/gas" {
			t.Errorf("the page's HTTP app imports %s from module %s, want only the core module", pkg, mod)
		}
	}
}

// Docs: start/installation.mdx#add-modules
// "Everything else is a separate Go module under `github.com/gasmod/gas/`" and "The modules are `auth`, `cache`, `config`, `database`, `email`, `log`, `migrate`, `queue`, `storage`, `template`, and `ui`."
// The released modules are the ones go.work pins to a version; the list
// must match them exactly.
func TestModuleList(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	work, err := os.ReadFile(filepath.Join(root, "go.work"))
	if err != nil {
		t.Fatal(err)
	}
	var pinned []string
	for _, m := range regexp.MustCompile(`github\.com/gasmod/gas/([a-z]+)\s+v\d`).FindAllStringSubmatch(string(work), -1) {
		pinned = append(pinned, m[1])
	}
	slices.Sort(pinned)
	if !slices.Equal(pinned, modules) {
		t.Fatalf("modules pinned in go.work = %v, the page lists %v", pinned, modules)
	}
	for _, d := range modules {
		data, err := os.ReadFile(filepath.Join(root, d, "go.mod"))
		if err != nil || !strings.HasPrefix(string(data), "module github.com/gasmod/gas/"+d+"\n") {
			t.Errorf("%s/go.mod does not declare module github.com/gasmod/gas/%s (err %v)", d, d, err)
		}
	}
}

// Docs: start/installation.mdx#add-modules
// "`gas/database` does not import a SQLite driver."
func TestDatabaseImportsNoSQLiteDriver(t *testing.T) {
	t.Parallel()
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", "-f", "{{.ImportPath}}", "github.com/gasmod/gas/database/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.Contains(strings.ToLower(pkg), "sqlite") {
			t.Errorf("gas/database depends on %s", pkg)
		}
	}
}

// Docs: start/installation.mdx#the-smallest-app
// "`gas.NewApp` creates the app with its router, event bus, and DI container. `Router().Get` registers a handler for `GET /`."
func TestNewAppAndGetRoute(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	if app.Router() == nil || app.EventBus() == nil || app.ServiceContainer() == nil {
		t.Fatal("NewApp has no router, event bus, or container")
	}
	if r, err := app.ServiceContainer().Resolve[*gas.Router](); err != nil || r != app.Router() {
		t.Fatalf("container's *gas.Router = %p, %v, want the app's router", r, err)
	}
	if b, err := app.ServiceContainer().Resolve[*gas.EventBus](); err != nil || b != app.EventBus() {
		t.Fatalf("container's *gas.EventBus = %p, %v, want the app's bus", b, err)
	}
	app.Router().Get(nil, "/", func(ctx gas.Context) error { return ctx.Text(http.StatusOK, "Hello, Gas!") })
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	for method, want := range map[string]int{http.MethodGet: http.StatusOK, http.MethodPost: http.StatusMethodNotAllowed} {
		rr := httptest.NewRecorder()
		app.Handler().ServeHTTP(rr, httptest.NewRequest(method, "/", nil))
		if rr.Code != want {
			t.Errorf("%s / = %d, want %d", method, rr.Code, want)
		}
	}
}

// owner owns a route.
type owner struct{ router *gas.Router }

func (*owner) Name() string { return "owner" }
func (o *owner) Init(context.Context) error {
	o.router.Get(o, "/owned", func(ctx gas.Context) error { return ctx.Text(http.StatusOK, "ok") })
	return nil
}
func (*owner) Close(context.Context) error { return nil }

// Docs: start/installation.mdx#the-smallest-app
// "A service passes itself, which ties the route to that service"
func TestServicePassesItself(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(gas.WithSingletonService[*owner](func(r *gas.Router) *owner { return &owner{router: r} }))
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if err := app.CloseService[*owner](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/owned", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("owned route after its service closed = %d, want 503", rr.Code)
	}
}

// Docs: start/installation.mdx#check-that-it-works
// "The server listens on `0.0.0.0:8080`, the default `Server.Host` and `Server.Port`."
func TestDefaultHostAndPort(t *testing.T) {
	t.Parallel()
	s := gas.DefaultConfig().Server
	if s.Host != "0.0.0.0" || s.Port != 8080 {
		t.Fatalf("default Host:Port = %s:%d, want 0.0.0.0:8080", s.Host, s.Port)
	}
}

// slogLine matches a line slog.Default writes through the standard logger:
// date, time, level, message.
var slogLine = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} (WARN|INFO|ERROR|DEBUG) (.*)$`)

// Docs: start/installation.mdx#the-smallest-app
// "the log below names `gas/router`" and "`Run` initializes the registered services, starts the HTTP server, and blocks until the process gets `SIGINT` or `SIGTERM`."
// Docs: start/installation.mdx#check-that-it-works
// "... WARN no logger registered reason="no registration for gas.Logger"" and "... INFO route registered service=gas/router method=GET path=/" and "... INFO server started environment="" addr=0.0.0.0:8080" and "Hello, Gas!"
// Docs: start/installation.mdx#check-that-it-works
// "No `gas.Logger` is registered, so Gas warns once and logs through `slog.Default()`."
// Docs: start/installation.mdx#check-that-it-works
// "Stop the app with Ctrl+C. It closes its services and exits:" and "... INFO shutdown signal received signal=interrupt" and "... INFO closing service service=gas/router" and "... INFO closing service service=gas/eventbus" and "... INFO shutdown complete"
// The page's main.go is built and run as its own process on port 8080,
// curled, and stopped with SIGINT, as the page does by hand.
// Assumption: "SIGTERM" stops it the same way; only SIGINT, the Ctrl+C
// the page shows, is sent here.
func TestRunTheSmallestApp(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("sends SIGINT to the app process")
	}
	// Take turns on 8080 with other claims packages that run an app on it,
	// and probe by dialing so no listener of ours can be inherited by a
	// child process.
	testport.Hold(t, 8080)
	if testport.Accepting(8080) {
		t.Skip("port 8080 is in use, which the page's app needs")
	}
	bin := filepath.Join(t.TempDir(), "hello")
	if out, err := exec.CommandContext(t.Context(), "go", "build", "-o", bin, helloPkg).CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin) //nolint:gosec // the binary this test just built
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start app: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	var logged []string
	readUntil := func(substr string) {
		t.Helper()
		deadline := time.After(15 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("app output ended before %q; got %q", substr, logged)
				}
				logged = append(logged, l)
				if strings.Contains(l, substr) {
					return
				}
			case <-deadline:
				t.Fatalf("no %q within 15s; got %q", substr, logged)
			}
		}
	}
	readUntil("server started")

	var body string
	ok := false
	for range 50 {
		resp, err := http.Get("http://localhost:8080/")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			body, ok = string(b), true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ok || body != "Hello, Gas!" {
		t.Fatalf("curl localhost:8080/ = %q (reached %v), want Hello, Gas!", body, ok)
	}

	// Still running: Run blocks until a signal.
	time.Sleep(200 * time.Millisecond)
	if cmd.ProcessState != nil {
		t.Fatal("the app exited before any signal")
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal: %v", err)
	}
	for l := range lines {
		logged = append(logged, l)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("app exit after SIGINT: %v; output %q", err, logged)
	}

	var msgs []string
	for _, l := range logged {
		m := slogLine.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("line %q is not in slog.Default's format", l)
		}
		msgs = append(msgs, m[1]+" "+m[2])
	}
	want := []string{
		`WARN no logger registered reason="no registration for gas.Logger"`,
		`INFO route registered service=gas/router method=GET path=/`,
		`INFO server started environment="" addr=0.0.0.0:8080`,
		`INFO shutdown signal received signal=interrupt`,
		`INFO closing service service=gas/router`,
		`INFO closing service service=gas/eventbus`,
		`INFO shutdown complete`,
	}
	if !slices.Equal(msgs, want) {
		t.Fatalf("app logged\n%s\nwant\n%s", strings.Join(msgs, "\n"), strings.Join(want, "\n"))
	}
}
