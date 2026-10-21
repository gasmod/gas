package index_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/jwt"
	"github.com/gasmod/gas/auth/session"
	"github.com/gasmod/gas/auth/token"
	"github.com/gasmod/gas/cache/cachetest"
	cachemem "github.com/gasmod/gas/cache/memory"
	cachevk "github.com/gasmod/gas/cache/valkey"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/email/ses"
	gaslog "github.com/gasmod/gas/log"
	"github.com/gasmod/gas/migrate"
	"github.com/gasmod/gas/queue/sqs"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir"
	"github.com/gasmod/gas/storage/s3"
	"github.com/gasmod/gas/storage/storagetest"
	"github.com/gasmod/gas/template/composite"
	templatedb "github.com/gasmod/gas/template/db"
	templatedir "github.com/gasmod/gas/template/dir"
	templatefs "github.com/gasmod/gas/template/fs"
	"github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/ui"
)

// landingPkg is the app the landing page's Quickstart shows.
const landingPkg = "github.com/gasmod/gas/docs/snippets/landing"

// Docs: index.mdx#quickstart
// "That is a complete Gas app."
// The quickstart app, driven through Start and Handler instead of Run so the
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

// Docs: index.mdx#quickstart
// "That is a complete Gas app."
// The page's main.go builds on its own: no other file is needed.
func TestQuickstartBuildsAlone(t *testing.T) {
	t.Parallel()
	out, err := exec.CommandContext(t.Context(), "go", "build", "-o", os.DevNull, landingPkg).CombinedOutput()
	if err != nil {
		t.Fatalf("go build %s: %v\n%s", landingPkg, err, out)
	}
}

// Docs: index.mdx#quickstart
// "`Run` starts the server and blocks until the process receives SIGINT or SIGTERM, then shuts the app down."
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

// Docs: index.mdx#why-gas
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

// Docs: index.mdx#why-gas
// "An app that just serves HTTP depends on the core package alone."
// The Quickstart app, which only serves HTTP, imports no Gas package but
// github.com/gasmod/gas itself.
func TestHTTPOnlyAppDependsOnCoreAlone(t *testing.T) {
	t.Parallel()
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps",
		"-f", "{{.ImportPath}}", landingPkg).CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	var gasPkgs []string
	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasPrefix(pkg, "github.com/gasmod/") && pkg != landingPkg {
			gasPkgs = append(gasPkgs, pkg)
		}
	}
	if want := []string{"github.com/gasmod/gas"}; !slices.Equal(gasPkgs, want) {
		t.Fatalf("the Quickstart app imports Gas packages %v, want only %v", gasPkgs, want)
	}
}

// Docs: index.mdx#why-gas
// "A service is three methods: `Name`, `Init`, `Close`."
func TestServiceIsThreeMethods(t *testing.T) {
	t.Parallel()
	st := reflect.TypeFor[gas.Service]()
	var names []string
	for m := range st.Methods() {
		names = append(names, m.Name)
	}
	sort.Strings(names)
	if want := []string{"Close", "Init", "Name"}; !slices.Equal(names, want) {
		t.Fatalf("gas.Service methods = %v, want %v", names, want)
	}
}

// Docs: index.mdx#why-gas
// "The container resolves constructor parameters, initializes services in dependency order, and closes them in reverse."
func TestContainerResolvesInitsInOrderClosesInReverse(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	a := &svc[kA]{rec: rec}
	var gotA *svc[kA]
	var gotB *svc[kB]
	// Registered dependents first, so only dependency order puts kA first.
	w := gas.NewWorker(
		gas.WithSingletonService[*svc[kC]](func(b *svc[kB]) *svc[kC] { gotB = b; return &svc[kC]{rec: rec} }),
		gas.WithSingletonService[*svc[kB]](func(a *svc[kA]) *svc[kB] { gotA = a; return &svc[kB]{rec: rec} }),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return a }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if gotA != a {
		t.Fatal("kB's constructor did not receive the registered kA")
	}
	b, err := w.ServiceContainer().Resolve[*svc[kB]]()
	if err != nil || gotB != b {
		t.Fatalf("kC's constructor received %p, the container holds %p (err %v)", gotB, b, err)
	}
	want := []string{"init kA", "init kB", "init kC", "close kC", "close kB", "close kA"}
	if got := rec.list(); !slices.Equal(got, want) {
		t.Fatalf("lifecycle = %v, want %v", got, want)
	}
}

type fakeCache struct{}

func (fakeCache) Get(context.Context, string) ([]byte, error)              { return []byte("mine"), nil }
func (fakeCache) Set(context.Context, string, []byte, time.Duration) error { return nil }
func (fakeCache) Delete(context.Context, string) error                     { return nil }
func (fakeCache) Exists(context.Context, string) (bool, error)             { return true, nil }

// Docs: index.mdx#why-gas
// "so any of them swaps for your own implementation or a test mock."
// A consumer that depends on a service's interface gets whatever is
// registered under it: an implementation of your own, or the module's test
// mock. Shown for an interface in core and for one in a module.
func TestProviderInterfaceSwapsForOwnImplementationOrMock(t *testing.T) {
	t.Parallel()
	cases := map[string]gas.CacheProvider{
		"own implementation": fakeCache{},
		"test mock":          &cachetest.MockCache{},
	}
	for name, impl := range cases {
		t.Run("core interface/"+name, func(t *testing.T) {
			t.Parallel()
			var got gas.CacheProvider
			w := gas.NewWorker(
				gas.WithServiceInstance[gas.CacheProvider](impl),
				gas.WithSingletonService[*svc[kA]](func(c gas.CacheProvider) *svc[kA] { got = c; return &svc[kA]{} }),
			)
			if err := w.Start(t.Context()); err != nil {
				t.Fatalf("start: %v", err)
			}
			t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
			if got != impl {
				t.Fatalf("consumer got %T, want the registered %T", got, impl)
			}
		})
	}
	t.Run("module interface/test mock", func(t *testing.T) {
		t.Parallel()
		mock := &storagetest.MockProvider{}
		var got storage.Provider
		w := gas.NewWorker(
			gas.WithServiceInstance[storage.Provider](mock),
			gas.WithSingletonService[*svc[kA]](func(p storage.Provider) *svc[kA] { got = p; return &svc[kA]{} }),
		)
		if err := w.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
		if got != storage.Provider(mock) {
			t.Fatalf("consumer got %T, want the registered mock", got)
		}
	})
}

// Docs: index.mdx#why-gas
// "Each module's service sits behind an interface, in core or in the module itself (`storage.Provider`, `auth.Authenticator`)"
// Assumption: a module's service is each backend type its packages export
// for an app to register; each must implement an interface declared in core
// or in its own module.
func TestEachModuleServiceSitsBehindAnInterface(t *testing.T) {
	t.Parallel()
	cases := []struct {
		impl, iface reflect.Type
		module      string
	}{
		{reflect.TypeFor[*config.Config](), reflect.TypeFor[gas.ConfigProvider](), "config"},
		{reflect.TypeFor[*gaslog.SlogLogger](), reflect.TypeFor[gas.Logger](), "log"},
		{reflect.TypeFor[*gaslog.ZeroLogLogger](), reflect.TypeFor[gas.Logger](), "log"},
		{reflect.TypeFor[*database.Service](), reflect.TypeFor[gas.DatabaseProvider](), "database"},
		{reflect.TypeFor[*migrate.Service](), reflect.TypeFor[gas.MigrationManager](), "migrate"},
		{reflect.TypeFor[*cachemem.Service](), reflect.TypeFor[gas.CacheProvider](), "cache"},
		{reflect.TypeFor[*cachevk.Service](), reflect.TypeFor[gas.CacheProvider](), "cache"},
		{reflect.TypeFor[*jwt.Service](), reflect.TypeFor[auth.Authenticator](), "auth"},
		{reflect.TypeFor[*session.Service](), reflect.TypeFor[auth.Authenticator](), "auth"},
		{reflect.TypeFor[*apikey.Service](), reflect.TypeFor[auth.Authenticator](), "auth"},
		{reflect.TypeFor[*token.Service](), reflect.TypeFor[token.Provider](), "auth"},
		{reflect.TypeFor[*memory.Store](), reflect.TypeFor[gas.TemplateProvider](), "template"},
		{reflect.TypeFor[*templatedir.Store](), reflect.TypeFor[gas.TemplateProvider](), "template"},
		{reflect.TypeFor[*templatefs.Store](), reflect.TypeFor[gas.TemplateProvider](), "template"},
		{reflect.TypeFor[*templatedb.Store](), reflect.TypeFor[gas.TemplateProvider](), "template"},
		{reflect.TypeFor[*composite.Store](), reflect.TypeFor[gas.TemplateProvider](), "template"},
		{reflect.TypeFor[*ui.Service](), reflect.TypeFor[gas.UIProvider](), "ui"},
		{reflect.TypeFor[*dir.Provider](), reflect.TypeFor[storage.Provider](), "storage"},
		{reflect.TypeFor[*s3.Provider](), reflect.TypeFor[storage.Provider](), "storage"},
		{reflect.TypeFor[*ses.Service](), reflect.TypeFor[gas.EmailProvider](), "email"},
		{reflect.TypeFor[*sqs.Service](), reflect.TypeFor[gas.JobQueueProvider](), "queue"},
	}
	covered := map[string]bool{}
	for _, c := range cases {
		covered[c.module] = true
		if !c.impl.Implements(c.iface) {
			t.Errorf("%v does not implement %v", c.impl, c.iface)
		}
		pkg := c.iface.PkgPath()
		if pkg != "github.com/gasmod/gas" && !strings.HasPrefix(pkg, "github.com/gasmod/gas/"+c.module) {
			t.Errorf("%v's interface %v is declared in %s, neither core nor module %s", c.impl, c.iface, pkg, c.module)
		}
	}
	for _, d := range listedModules {
		if !covered[d] {
			t.Errorf("no service listed for module %s", d)
		}
	}
}

// Docs: index.mdx#why-gas
// "Each module is its own Go module, so `go get` pulls in only what you use."
// Docs: index.mdx#why-gas
// "Modules meet through provider interfaces declared in core, not by importing each other."
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

type halfService struct{}

func (halfService) Init(context.Context) error { return nil }

type unregistered struct{}

// Docs: index.mdx#why-gas
// "Unresolved dependencies, malformed services, and handler signature mismatches are all rejected before the server accepts traffic."
// Each misconfigured app is run with Run on a free port. It must either panic
// while it is being built or have Run return an error, and its port must
// never accept a connection.
// Not parallel: a Run that wrongly keeps serving is stopped with a signal.
func TestMisconfigurationRejectedBeforeTraffic(t *testing.T) {
	requireSignals(t)
	guard := make(chan os.Signal, 8)
	signal.Notify(guard, syscall.SIGINT, syscall.SIGTERM)
	t.Cleanup(func() { signal.Stop(guard) })

	cases := []struct {
		name  string
		build func(gas.ConfigProvider) *gas.App
		want  error // the sentinel Run's error wraps; nil when building panics
	}{
		{"unresolved dependency", func(c gas.ConfigProvider) *gas.App {
			return gas.NewApp(gas.WithServiceInstance[gas.ConfigProvider](c),
				gas.WithSingletonService[*svc[kA]](func(*unregistered) *svc[kA] { return &svc[kA]{} }))
		}, gas.ErrNotRegistered},
		{"malformed service", func(c gas.ConfigProvider) *gas.App {
			return gas.NewApp(gas.WithServiceInstance[gas.ConfigProvider](c),
				gas.WithSingletonService[*halfService](func() *halfService { return &halfService{} }))
		}, gas.ErrInvalidServiceShape},
		{"handler parameter not in the container", func(c gas.ConfigProvider) *gas.App {
			app := gas.NewApp(gas.WithServiceInstance[gas.ConfigProvider](c))
			app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context, *unregistered) error { return nil })
			return app
		}, nil},
		{"handler with the wrong return type", func(c gas.ConfigProvider) *gas.App {
			app := gas.NewApp(gas.WithServiceInstance[gas.ConfigProvider](c))
			app.Router().Handle(nil, http.MethodGet, "/x", func(gas.Context) string { return "" })
			return app
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			port := freePort(t)
			var accepted atomic.Bool
			stop := make(chan struct{})
			polled := make(chan struct{})
			go func() {
				defer close(polled)
				for {
					select {
					case <-stop:
						return
					default:
					}
					if accepting(port) {
						accepted.Store(true)
					}
					time.Sleep(2 * time.Millisecond)
				}
			}()

			done := make(chan error, 1)
			go func() {
				defer func() {
					if r := recover(); r != nil {
						done <- fmt.Errorf("panicked: %v", r)
					}
				}()
				done <- tc.build(serverConfig(t, port, nil)).Run()
			}()

			var err error
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				_ = sendSignal(syscall.SIGTERM)
				late := <-done
				close(stop)
				<-polled
				t.Fatalf("Run kept going until signaled (then returned %v); want the app rejected", late)
			}
			time.Sleep(50 * time.Millisecond)
			close(stop)
			<-polled

			if err == nil {
				t.Fatal("Run returned nil; want the app rejected")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("Run = %v, want it to wrap %v", err, tc.want)
			}
			if accepted.Load() {
				t.Fatalf("port %d accepted a connection before the app was rejected", port)
			}
		})
	}
}

// Docs: index.mdx#why-gas
// "On shutdown the HTTP server drains in-flight requests within `ShutdownTimeout`, 30 seconds by default"
func TestShutdownTimeoutDefaultsTo30s(t *testing.T) {
	t.Parallel()
	if got := gas.DefaultConfig().Server.ShutdownTimeout; got != 30*time.Second {
		t.Fatalf("DefaultConfig ShutdownTimeout = %v, want 30s", got)
	}
	app := gas.NewApp(gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, map[string]any{})))
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if got := app.Config().Server.ShutdownTimeout; got != 30*time.Second {
		t.Fatalf("bound ShutdownTimeout with no server keys set = %v, want 30s", got)
	}
}

// serveApp starts app and serves its server on a listener of its own, so
// parallel tests cannot race for a port, and waits until it answers /ping.
func serveApp(t *testing.T, app *gas.App) string {
	t.Helper()
	app.Router().Handle(nil, http.MethodGet, "/ping", func(http.ResponseWriter, *http.Request) {})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- app.Server().Serve(l) }()
	base := "http://" + l.Addr().String()
	if !waitFor(5*time.Second, func() bool {
		resp, err := http.Get(base + "/ping")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return true
	}) {
		select {
		case err := <-served:
			t.Fatalf("server never came up: %v", err)
		default:
			t.Fatal("server never came up")
		}
	}
	return base
}

// getAsync sends a GET and delivers its status, or -1 on a transport error.
func getAsync(url string) <-chan int {
	result := make(chan int, 1)
	go func() {
		resp, err := http.Get(url)
		if err != nil {
			result <- -1
			return
		}
		_ = resp.Body.Close()
		result <- resp.StatusCode
	}()
	return result
}

// Docs: index.mdx#why-gas
// "On shutdown the HTTP server drains in-flight requests within `ShutdownTimeout`, 30 seconds by default, and then every active service's `Close` runs."
// Assumption: "active" excludes a service already torn down with
// CloseService, whose Close is not run a second time.
func TestShutdownDrainsThenClosesEveryActiveService(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	a, b, c := &svc[kA]{rec: rec}, &svc[kB]{rec: rec}, &svc[kC]{rec: rec}
	app := gas.NewApp(
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return a }),
		gas.WithSingletonService[*svc[kB]](func() *svc[kB] { return b }),
		gas.WithSingletonService[*svc[kC]](func() *svc[kC] { return c }),
	)
	started := make(chan struct{})
	app.Router().Handle(nil, http.MethodGet, "/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(300 * time.Millisecond)
		rec.add("request done")
		_, _ = w.Write([]byte("done"))
	})
	base := serveApp(t, app)
	if err := app.CloseService[*svc[kC]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}

	result := getAsync(base + "/slow")
	<-started
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := <-result; got != http.StatusOK {
		t.Fatalf("in-flight request got %d, want 200", got)
	}
	events := rec.list()
	done := slices.Index(events, "request done")
	for _, want := range []string{"close kA", "close kB"} {
		i := slices.Index(events, want)
		if i < 0 {
			t.Fatalf("%s missing after shutdown: %v", want, events)
		}
		if i < done {
			t.Fatalf("%s ran before the in-flight request finished: %v", want, events)
		}
	}
	if c.closes() != 1 {
		t.Fatalf("service closed by CloseService had Close run %d times, want 1", c.closes())
	}
}

// Docs: index.mdx#why-gas
// "On shutdown the HTTP server drains in-flight requests within `ShutdownTimeout`, 30 seconds by default, and then every active service's `Close` runs."
// A request that outlives ShutdownTimeout does not hold up shutdown, and
// Close still runs once the timeout has passed.
func TestShutdownStopsWaitingAtShutdownTimeout(t *testing.T) {
	t.Parallel()
	a := &svc[kA]{}
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, map[string]any{"server": map[string]any{"shutdowntimeout": "200ms"}})),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return a }),
	)
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	app.Router().Handle(nil, http.MethodGet, "/stuck", func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
	})
	base := serveApp(t, app)
	_ = getAsync(base + "/stuck")
	<-started

	begin := time.Now()
	_ = app.Stop(context.Background())
	took := time.Since(begin)
	if took < 150*time.Millisecond || took > 2*time.Second {
		t.Fatalf("Stop took %v with a 200ms ShutdownTimeout, want about 200ms", took)
	}
	if a.closes() != 1 {
		t.Fatalf("Close ran %d times after the timeout, want 1", a.closes())
	}
}

// Docs: index.mdx#why-gas
// "drains in-flight requests within `ShutdownTimeout`, 30 seconds by default"
// With no ShutdownTimeout set, Stop waits for a request that takes well over
// a second.
func TestDefaultShutdownWaitsForSlowRequest(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	started := make(chan struct{})
	app.Router().Handle(nil, http.MethodGet, "/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(1500 * time.Millisecond)
		_, _ = w.Write([]byte("done"))
	})
	base := serveApp(t, app)
	result := getAsync(base + "/slow")
	<-started
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := <-result; got != http.StatusOK {
		t.Fatalf("1.5s request during a default shutdown got %d, want 200", got)
	}
}

type pinged struct{ gas.Event[string] }

// routed owns a route at /<kind> and a subscription to pinged.
type routed[K any] struct {
	svc[K]
	router *gas.Router
	bus    *gas.EventBus
	got    atomic.Int32
	plain  atomic.Int32 // hits of a subscription made with plain Subscribe
}

func (r *routed[K]) Init(ctx context.Context) error {
	r.router.Handle(r, http.MethodGet, "/"+r.Name(), func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, "ok")
	})
	r.bus.SubscribeWithOwner[pinged](r, func(string) { r.got.Add(1) })
	r.bus.Subscribe[pinged](func(string) { r.plain.Add(1) })
	return r.svc.Init(ctx)
}

func newRouted[K any](r *gas.Router, b *gas.EventBus) *routed[K] {
	return &routed[K]{router: r, bus: b}
}

// Docs: index.mdx#why-gas
// "`CloseService` tears down one running service: the event subscriptions it made with `SubscribeWithOwner` are removed and its routes answer 503 while the rest of the app keeps serving."
func TestCloseServiceTearsDownOneService(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(
		gas.WithSingletonService[*routed[kA]](newRouted[kA]),
		gas.WithSingletonService[*routed[kB]](newRouted[kB]),
	)
	app.Router().Handle(nil, http.MethodGet, "/unowned", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, "ok")
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	a, _ := app.ServiceContainer().Resolve[*routed[kA]]()
	b, _ := app.ServiceContainer().Resolve[*routed[kB]]()

	get := func(path string) int {
		rr := httptest.NewRecorder()
		app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		return rr.Code
	}

	if err := app.CloseService[*routed[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if a.closes() != 1 {
		t.Fatalf("closed service's Close ran %d times, want 1", a.closes())
	}
	app.EventBus().Emit[pinged]("after").Wait()
	if n := a.got.Load(); n != 0 {
		t.Fatalf("closed service's subscription ran %d times, want 0", n)
	}
	if n := a.plain.Load(); n != 1 {
		t.Fatalf("closed service's plain Subscribe subscription ran %d times, want 1 (only SubscribeWithOwner ones are removed)", n)
	}
	if n := b.got.Load(); n != 1 {
		t.Fatalf("other service's subscription ran %d times, want 1", n)
	}
	if got := get("/kA"); got != http.StatusServiceUnavailable {
		t.Fatalf("closed service's route = %d, want 503", got)
	}
	if got := get("/kB"); got != http.StatusOK {
		t.Fatalf("other service's route = %d, want 200", got)
	}
	if got := get("/unowned"); got != http.StatusOK {
		t.Fatalf("unowned route = %d, want 200", got)
	}
	if active := app.ActiveServices(); slices.Contains(active, "kA") || !slices.Contains(active, "kB") {
		t.Fatalf("active services = %v, want kB but not kA", active)
	}
}
