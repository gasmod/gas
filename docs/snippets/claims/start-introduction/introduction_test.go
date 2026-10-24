package startintroduction_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache/cachetest"
	cachemem "github.com/gasmod/gas/cache/memory"
	cachevk "github.com/gasmod/gas/cache/valkey"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/database"
	gaslog "github.com/gasmod/gas/log"
)

// Docs: start/introduction.mdx#_top
// "Around it sit optional modules for configuration, logging, databases,
// migrations, caching, authentication, templates and HTML, object storage,
// email, and job queues. Each module is its own Go module"
func TestEachModuleIsItsOwnGoModule(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for area, dir := range modules {
		self := "github.com/gasmod/gas/" + dir
		data, err := os.ReadFile(filepath.Join(root, dir, "go.mod"))
		if err != nil {
			t.Errorf("%s: %s has no go.mod: %v", area, dir, err)
			continue
		}
		if !slices.Contains(strings.Split(string(data), "\n"), "module "+self) {
			t.Errorf("%s: %s/go.mod does not declare module %s", area, dir, self)
		}
	}
}

// goListDeps returns "module importpath" for every dependency of the
// packages module mod ships, test support under internal/testutil excepted.
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

// Docs: start/introduction.mdx#_top
// "Each module is its own Go module, so `go get` pulls in
// only what you use."
// Docs: start/introduction.mdx#the-design
// "No module's production code imports
// another module; they meet only through the interfaces in core."
// Assumption: packages under internal/testutil are test support, not
// production code, and are left out.
func TestModulesImportOnlyCore(t *testing.T) {
	t.Parallel()
	for _, dir := range modules {
		self := "github.com/gasmod/gas/" + dir
		for _, line := range goListDeps(t, self) {
			mod, pkg, _ := strings.Cut(line, " ")
			if mod != self && mod != "github.com/gasmod/gas" && strings.HasPrefix(mod, "github.com/gasmod/") {
				t.Errorf("module %s imports %s from module %s", self, pkg, mod)
			}
		}
	}
}

// greeter is the page's Greeter: a router and a logger in, one route out.
type greeter struct {
	router *gas.Router
	logger gas.Logger
	rec    *recorder
}

func (g *greeter) Name() string { return "greeter" }

func (g *greeter) Init(context.Context) error {
	g.rec.add("init greeter")
	g.router.Get(g, "/hello/{name}", func(ctx gas.Context) error {
		g.logger.Info("greeting").Str("name", ctx.Param("name")).Send()
		return ctx.Text(http.StatusOK, "Hello, "+ctx.Param("name"))
	})
	return nil
}

func (g *greeter) Close(context.Context) error {
	g.rec.add("close greeter")
	return nil
}

// Docs: start/introduction.mdx#what-it-looks-like
// "`Greeter` is a **service**: a constructor that declares its dependencies as
// parameters, and three methods. The container builds it after the router and
// logger it asks for, calls `Init` at startup, where it registers its route, and
// calls `Close` at shutdown."
func TestGreeterBuiltAfterDepsInitRegistersRouteCloseAtShutdown(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	var logger gas.Logger = &gas.NopLogger{}
	var got *greeter
	// The greeter is registered before its logger, so only dependency order
	// builds the logger first.
	app := gas.NewApp(
		gas.WithSingletonService[*greeter](func(r *gas.Router, l gas.Logger) *greeter {
			rec.add("build greeter")
			got = &greeter{router: r, logger: l, rec: rec}
			return got
		}),
		gas.WithSingletonService[gas.Logger](func() gas.Logger {
			rec.add("build logger")
			return logger
		}),
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if got.router != app.Router() || got.logger != logger {
		t.Fatal("the constructor did not get the app's router and the registered logger")
	}
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/hello/ada", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "Hello, ada" {
		t.Fatalf("GET /hello/ada = %d %q, want 200 \"Hello, ada\"", rr.Code, rr.Body.String())
	}
	if routes := app.Router().Routes()["greeter"]; len(routes) != 1 || routes[0].Path != "/hello/{name}" {
		t.Fatalf("Routes()[greeter] = %v, want the one route Init registered", routes)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	want := []string{"build logger", "build greeter", "init greeter", "close greeter"}
	if got := rec.list(); !slices.Equal(got, want) {
		t.Fatalf("lifecycle = %v, want %v", got, want)
	}
}

// Docs: start/introduction.mdx#what-it-looks-like
// "`app.Run()` starts everything, serves HTTP on
// `0.0.0.0:8080` by default"
func TestDefaultAddress(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if got := app.Server().Addr; got != "0.0.0.0:8080" {
		t.Fatalf("Server().Addr = %q, want 0.0.0.0:8080", got)
	}
}

// Docs: start/introduction.mdx#the-design
// "The container
// initializes them in dependency order and closes them in reverse."
func TestInitInDependencyOrderCloseInReverse(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	// Dependents are registered first, so only dependency order puts kA first.
	w := gas.NewWorker(
		gas.WithSingletonService[*svc[kC]](func(*svc[kB]) *svc[kC] { return &svc[kC]{rec: rec} }),
		gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] { return &svc[kB]{rec: rec} }),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{rec: rec} }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	want := []string{"init kA", "init kB", "init kC", "close kC", "close kB", "close kA"}
	if got := rec.list(); !slices.Equal(got, want) {
		t.Fatalf("lifecycle = %v, want %v", got, want)
	}
}

// Docs: start/introduction.mdx#the-design
// "Core declares provider interfaces
// such as `gas.Logger`, `gas.ConfigProvider`, `gas.DatabaseProvider`, and
// `gas.CacheProvider`. Modules implement them"
func TestModulesImplementCoreProviderInterfaces(t *testing.T) {
	t.Parallel()
	cases := []struct {
		iface reflect.Type
		impl  reflect.Type
	}{
		{reflect.TypeFor[gas.Logger](), reflect.TypeFor[*gaslog.SlogLogger]()},
		{reflect.TypeFor[gas.Logger](), reflect.TypeFor[*gaslog.ZeroLogLogger]()},
		{reflect.TypeFor[gas.ConfigProvider](), reflect.TypeFor[*config.Config]()},
		{reflect.TypeFor[gas.DatabaseProvider](), reflect.TypeFor[*database.Service]()},
		{reflect.TypeFor[gas.CacheProvider](), reflect.TypeFor[*cachemem.Service]()},
		{reflect.TypeFor[gas.CacheProvider](), reflect.TypeFor[*cachevk.Service]()},
	}
	for _, c := range cases {
		if c.iface.Kind() != reflect.Interface || c.iface.PkgPath() != "github.com/gasmod/gas" {
			t.Errorf("%v is not an interface declared in core", c.iface)
			continue
		}
		if !c.impl.Implements(c.iface) {
			t.Errorf("%v does not implement %v", c.impl, c.iface)
		}
	}
}

// Docs: start/introduction.mdx#the-design
// "Modules implement them and are registered under them, and
// your services ask for the interfaces, never for a module's concrete type."
// Module constructors take only core interfaces, so a module registered under
// its concrete type would leave its consumers unresolved.
func TestModuleConstructorsAskForCoreInterfaces(t *testing.T) {
	t.Parallel()
	ctors := map[string]any{
		"database.New": database.New(),
		"memory.New":   cachemem.New(),
		"valkey.New":   cachevk.New(),
	}
	for name, ctor := range ctors {
		typ := reflect.TypeOf(ctor)
		for p := range typ.Ins() {
			if p.Kind() != reflect.Interface || p.PkgPath() != "github.com/gasmod/gas" {
				t.Errorf("%s constructor takes %v, want a core interface", name, p)
			}
		}
	}
}

// mailer is an interface a consumer asks for; smtpMailer implements it.
type mailer interface{ Send() error }

type smtpMailer struct{}

func (smtpMailer) Send() error { return nil }

type signup struct{ m mailer }

// Docs: start/introduction.mdx#the-design
// "The
// container resolves by exact type, with no guessing about which registration
// satisfies an interface."
func TestResolvesByExactType(t *testing.T) {
	t.Parallel()
	concrete := gas.NewWorker(
		gas.WithSingletonService[*smtpMailer](func() *smtpMailer { return &smtpMailer{} }),
		gas.WithSingletonService[*signup](func(m mailer) *signup { return &signup{m: m} }),
	)
	err := concrete.Start(t.Context())
	if !errors.Is(err, gas.ErrNotRegistered) {
		t.Fatalf("Start with the mailer under its concrete type = %v, want ErrNotRegistered", err)
	}

	iface := gas.NewWorker(
		gas.WithSingletonService[mailer](func() *smtpMailer { return &smtpMailer{} }),
		gas.WithSingletonService[*signup](func(m mailer) *signup { return &signup{m: m} }),
	)
	if err := iface.Start(t.Context()); err != nil {
		t.Fatalf("Start with the mailer under the interface: %v", err)
	}
	_ = iface.Shutdown(context.Background())
}

// mapCache is a gas.CacheProvider of this package's own.
type mapCache struct{ m map[string][]byte }

func (c *mapCache) Get(_ context.Context, k string) ([]byte, error) { return c.m[k], nil }
func (c *mapCache) Set(_ context.Context, k string, v []byte, _ time.Duration) error {
	c.m[k] = v
	return nil
}
func (c *mapCache) Delete(_ context.Context, k string) error { delete(c.m, k); return nil }
func (c *mapCache) Exists(_ context.Context, k string) (bool, error) {
	_, ok := c.m[k]
	return ok, nil
}

// counter depends on gas.CacheProvider and nothing else about the cache.
type counter struct{ cache gas.CacheProvider }

func (c *counter) roundTrip(ctx context.Context) (string, error) {
	if err := c.cache.Set(ctx, "visits", []byte("1"), time.Minute); err != nil {
		return "", err
	}
	v, err := c.cache.Get(ctx, "visits")
	return string(v), err
}

// runCounter wires counter against whatever is registered as the cache and
// returns what its round trip read back.
func runCounter(t *testing.T, cacheOpt gas.Option) string {
	t.Helper()
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, map[string]any{})),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		cacheOpt,
		gas.WithSingletonService[*counter](func(c gas.CacheProvider) *counter { return &counter{cache: c} }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	c := w.ServiceContainer().MustResolve[*counter]()
	got, err := c.roundTrip(t.Context())
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	return got
}

// Docs: start/introduction.mdx#the-design
// "A service that
// depends on `gas.CacheProvider` works the same against the in-memory cache, the
// Valkey cache, or a mock."
// Docs: start/introduction.mdx#the-design
// "Swapping a backend, or a test mock, is a change to one
// registration."
// Docs: start/introduction.mdx#when-gas-fits
// "Any provider interface can be implemented by
// your own type"
// The same counter, wiring and code, is run with only the cache registration
// changed.
func TestCacheConsumerWorksAgainstEveryBackend(t *testing.T) {
	t.Parallel()
	store := map[string][]byte{}
	mock := &cachetest.MockCache{
		SetFn: func(_ context.Context, k string, v []byte, _ time.Duration) error { store[k] = v; return nil },
		GetFn: func(_ context.Context, k string) ([]byte, error) { return store[k], nil },
	}
	cases := map[string]func(t *testing.T) gas.Option{
		"memory": func(*testing.T) gas.Option {
			return gas.WithSingletonService[gas.CacheProvider](cachemem.New())
		},
		"valkey": func(t *testing.T) gas.Option {
			cfg := cachevk.DefaultConfig()
			cfg.Cache.Addr = valkeyAddr(t)
			return gas.WithSingletonService[gas.CacheProvider](cachevk.New(cachevk.WithConfig(cfg)))
		},
		"mock": func(*testing.T) gas.Option {
			return gas.WithServiceInstance[gas.CacheProvider](mock)
		},
		"own type": func(*testing.T) gas.Option {
			return gas.WithServiceInstance[gas.CacheProvider](&mapCache{m: map[string][]byte{}})
		},
	}
	for name, opt := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := runCounter(t, opt(t)); got != "1" {
				t.Fatalf("round trip read %q, want \"1\"", got)
			}
		})
	}
}

func valkeyAddr(t *testing.T) string {
	t.Helper()
	requireDocker(t)
	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "valkey/valkey:8",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForAll(
				wait.ForLog("Ready to accept connections"),
				wait.ForListeningPort("6379/tcp"),
			).WithDeadline(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start valkey: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })
	addr, err := ctr.PortEndpoint(ctx, "6379/tcp", "")
	if err != nil {
		t.Fatalf("valkey address: %v", err)
	}
	return addr
}

type (
	missing      struct{}
	halfService  struct{}
	requestValue struct{}
	captive      struct{}
)

func (halfService) Init(context.Context) error { return nil }

// Docs: start/introduction.mdx#the-design
// "A missing registration, a service that
// implements only part of the lifecycle, a singleton holding a per-request value,
// a handler parameter nobody registered: each one fails `Start` with an error
// naming the types involved"
func TestWiringMistakesFailStartNamingTypes(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		build func() *gas.App
		types []any
	}{
		"missing registration": {
			build: func() *gas.App {
				return gas.NewApp(gas.WithSingletonService[*svc[kA]](func(*missing) *svc[kA] { return &svc[kA]{} }))
			},
			types: []any{(*missing)(nil), (*svc[kA])(nil)},
		},
		"partial service": {
			build: func() *gas.App {
				return gas.NewApp(gas.WithSingletonService[*halfService](func() *halfService { return &halfService{} }))
			},
			types: []any{(*halfService)(nil)},
		},
		"singleton holding a per-request value": {
			build: func() *gas.App {
				return gas.NewApp(
					gas.WithScopedService[*requestValue](func() *requestValue { return &requestValue{} }),
					gas.WithSingletonService[*captive](func(*requestValue) *captive { return &captive{} }),
				)
			},
			types: []any{(*captive)(nil), (*requestValue)(nil)},
		},
		"handler parameter nobody registered": {
			build: func() *gas.App {
				app := gas.NewApp()
				app.Router().Get(nil, "/x", func(gas.Context, *missing) error { return nil })
				return app
			},
			types: []any{(*missing)(nil)},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := tc.build().Start(t.Context())
			if err == nil {
				t.Fatal("Start succeeded; want it to fail")
			}
			for _, v := range tc.types {
				if typ := reflect.TypeOf(v).String(); !strings.Contains(err.Error(), typ) {
					t.Errorf("Start error %q does not name %s", err, typ)
				}
			}
		})
	}
}

// Docs: start/introduction.mdx#the-design
// "each one fails `Start` with an error
// naming the types involved, before the server accepts a request."
// Run on a free port returns the error and the port never accepts a
// connection.
func TestWiringMistakeRunNeverServes(t *testing.T) {
	t.Parallel()
	port := freePort(t)
	app := gas.NewApp(
		gas.WithServiceInstance(serverConfig(t, port)),
		gas.WithSingletonService[*svc[kA]](func(*missing) *svc[kA] { return &svc[kA]{} }),
	)
	done := make(chan error, 1)
	go func() { done <- app.Run() }()
	accepted := false
	var err error
	for loop := true; loop; {
		select {
		case err = <-done:
			loop = false
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return; want the startup error")
		default:
			if accepting(port) {
				accepted = true
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	if !errors.Is(err, gas.ErrNotRegistered) {
		t.Fatalf("Run = %v, want ErrNotRegistered", err)
	}
	if accepted || accepting(port) {
		t.Fatalf("port %d accepted a connection", port)
	}
}

// owned registers a route, a named middleware, and an event subscription,
// each owned by itself.
type owned struct {
	router *gas.Router
	bus    *gas.EventBus
	hits   chan struct{}
}

type ping struct{ gas.Event[struct{}] }

func (o *owned) Name() string { return "owned" }

func (o *owned) Init(context.Context) error {
	o.router.Get(o, "/owned", func(ctx gas.Context) error { return ctx.Text(http.StatusOK, "owned") })
	o.router.Register(o, "owned-mw", func(next http.Handler) http.Handler { return next })
	o.bus.SubscribeWithOwner[ping](o, func(struct{}) { o.hits <- struct{}{} })
	return nil
}

func (o *owned) Close(context.Context) error { return nil }

// Docs: start/introduction.mdx#the-design
// "Every route, named middleware, and event
// subscription records the service that registered it."
// Docs: start/introduction.mdx#the-design
// "it makes it possible to close one service at runtime, with
// its routes answering 503, without restarting the process."
func TestOwnershipAndRuntimeClose(t *testing.T) {
	t.Parallel()
	hits := make(chan struct{}, 4)
	app := gas.NewApp(gas.WithSingletonService[*owned](func(r *gas.Router, b *gas.EventBus) *owned {
		return &owned{router: r, bus: b, hits: hits}
	}))
	app.Router().Get(nil, "/other", func(ctx gas.Context) error { return ctx.Text(http.StatusOK, "other") })
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	if routes := app.Router().Routes()["owned"]; len(routes) != 1 || routes[0].Path != "/owned" {
		t.Fatalf("Routes()[owned] = %v, want /owned", routes)
	}
	if got := app.Router().NamedMiddleware()["owned-mw"]; got != "owned" {
		t.Fatalf("NamedMiddleware()[owned-mw] = %q, want owned", got)
	}
	app.EventBus().Emit[ping](struct{}{}).Wait()
	if len(hits) != 1 {
		t.Fatalf("subscriber ran %d times before close, want 1", len(hits))
	}
	<-hits

	if err := app.CloseService[*owned](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	get := func(path string) int {
		rr := httptest.NewRecorder()
		app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		return rr.Code
	}
	if code := get("/owned"); code != http.StatusServiceUnavailable {
		t.Errorf("GET /owned after CloseService = %d, want 503", code)
	}
	if code := get("/other"); code != http.StatusOK {
		t.Errorf("GET /other after CloseService = %d, want 200", code)
	}
	app.EventBus().Emit[ping](struct{}{}).Wait()
	if len(hits) != 0 {
		t.Error("the closed service's subscription still ran, so it was not recorded as its owner's")
	}
}

// Docs: start/introduction.mdx#the-design
// "`gas.App` serves HTTP; `gas.Worker` is the same container and lifecycle without a server"
func TestWorkerIsContainerAndLifecycleWithoutServer(t *testing.T) {
	t.Parallel()
	wt := reflect.TypeFor[*gas.Worker]()
	for _, m := range []string{"Serve", "Handler", "Router", "Server"} {
		if _, ok := wt.MethodByName(m); ok {
			t.Errorf("Worker has %s; want no server", m)
		}
		if _, ok := reflect.TypeFor[*gas.App]().MethodByName(m); !ok {
			t.Errorf("App lacks %s", m)
		}
	}
	if f, ok := reflect.TypeFor[gas.App]().FieldByName("Worker"); !ok || !f.Anonymous || f.Type != wt {
		t.Error("App does not embed *gas.Worker, so its container and lifecycle are not the Worker's")
	}
	rec := &recorder{}
	w := gas.NewWorker(
		gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] { return &svc[kB]{rec: rec} }),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{rec: rec} }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if want := []string{"init kA", "init kB", "close kB", "close kA"}; !slices.Equal(rec.list(), want) {
		t.Fatalf("Worker lifecycle = %v, want %v", rec.list(), want)
	}
}

// Docs: start/introduction.mdx#when-gas-fits
// "Gas is pre-1.0"
// The docs module requires a v0 release of core.
func TestPreOnePointZero(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "snippets", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*github\.com/gasmod/gas (v\S+)`).FindSubmatch(data)
	if m == nil {
		t.Fatal("docs/snippets/go.mod does not require github.com/gasmod/gas")
	}
	if v := string(m[1]); !strings.HasPrefix(v, "v0.") {
		t.Fatalf("github.com/gasmod/gas is required at %s, want a v0 version", v)
	}
}

// Docs: start/introduction.mdx#when-gas-fits
// "want the infrastructure (PostgreSQL or SQLite, Valkey, S3, SES, SQS) behind
// interfaces you can swap and mock."
// Docs: start/introduction.mdx#when-gas-fits
// "the official modules target the backends listed above."
// Each backend has a package, and each module ships a mock package.
func TestModulesTargetListedBackends(t *testing.T) {
	t.Parallel()
	if database.DriverPostgres != "postgres" || database.DriverSQLite != "sqlite" {
		t.Errorf("database drivers = %q, %q; want postgres and sqlite", database.DriverPostgres, database.DriverSQLite)
	}
	pkgs := []string{
		"github.com/gasmod/gas/cache/valkey",
		"github.com/gasmod/gas/storage/s3",
		"github.com/gasmod/gas/email/ses",
		"github.com/gasmod/gas/queue/sqs",
		"github.com/gasmod/gas/database",
		"github.com/gasmod/gas/cache/cachetest",
		"github.com/gasmod/gas/storage/storagetest",
		"github.com/gasmod/gas/email/emailtest",
		"github.com/gasmod/gas/queue/queuetest",
	}
	out, err := exec.CommandContext(t.Context(), "go", append([]string{"list"}, pkgs...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
}
