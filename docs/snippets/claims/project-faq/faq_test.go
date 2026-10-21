package projectfaq_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/authtest"
	"github.com/gasmod/gas/cache/cachetest"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/database"
	"github.com/gasmod/gas/email/emailtest"
	"github.com/gasmod/gas/migrate"
	"github.com/gasmod/gas/migrate/migratetest"
	"github.com/gasmod/gas/queue/queuetest"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/storagetest"
	"github.com/gasmod/gas/template/templatetest"
	"github.com/gasmod/gas/ui"
	"github.com/gasmod/gas/ui/uitest"
)

func serve(t *testing.T, h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr
}

// Docs: project/faq.mdx#why-not-plain-nethttp-or-another-framework
// "Handlers get a `gas.Context` that wraps the `http.ResponseWriter` and
// `*http.Request`"
func TestContextWrapsWriterAndRequest(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Get(nil, "/w", func(ctx gas.Context) error {
		r := ctx.Request()
		if r.Method != http.MethodGet || r.URL.Path != "/w" || r.Header.Get("X-In") != "in" {
			return ctx.Text(http.StatusTeapot, "request not the one sent")
		}
		w := ctx.ResponseWriter()
		w.Header().Set("X-Out", "out")
		w.WriteHeader(http.StatusAccepted)
		_, err := io.WriteString(w, "raw")
		return err
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	req := httptest.NewRequest(http.MethodGet, "/w", nil)
	req.Header.Set("X-In", "in")
	rr := serve(t, app.Handler(), req)
	if rr.Code != http.StatusAccepted || rr.Header().Get("X-Out") != "out" || rr.Body.String() != "raw" {
		t.Fatalf("got %d %q X-Out=%q, want 202 \"raw\" X-Out=out", rr.Code, rr.Body.String(), rr.Header().Get("X-Out"))
	}
}

// Docs: project/faq.mdx#why-not-plain-nethttp-or-another-framework
// "the router is built on [chi](https://github.com/go-chi/chi)"
// The core package imports chi, and chi's {param} patterns route.
func TestRouterIsBuiltOnChi(t *testing.T) {
	t.Parallel()
	out, err := exec.CommandContext(t.Context(), "go", "list", "-f", `{{join .Imports "\n"}}`, "github.com/gasmod/gas").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	if !slices.Contains(strings.Fields(string(out)), "github.com/go-chi/chi/v5") {
		t.Fatalf("github.com/gasmod/gas does not import github.com/go-chi/chi/v5; imports:\n%s", out)
	}
	app := gas.NewApp()
	app.Router().Get(nil, "/hello/{name}", func(ctx gas.Context) error { return ctx.Text(http.StatusOK, ctx.Param("name")) })
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if rr := serve(t, app.Handler(), httptest.NewRequest(http.MethodGet, "/hello/gas", nil)); rr.Body.String() != "gas" {
		t.Fatalf("GET /hello/gas = %d %q, want \"gas\"", rr.Code, rr.Body.String())
	}
}

// Docs: project/faq.mdx#why-not-plain-nethttp-or-another-framework
// "standard `func(http.Handler) http.Handler` middleware plugs in through
// `gas.MiddlewareFunc`, and `App.Handler()` returns an `http.Handler` you can
// mount or test like any other."
func TestStandardMiddlewareAndMountableHandler(t *testing.T) {
	t.Parallel()
	std := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Std", "1")
			next.ServeHTTP(w, r)
		})
	}
	app := gas.NewApp()
	app.Router().Use(gas.MiddlewareFunc(std))
	app.Router().Get(nil, "/ping", func(ctx gas.Context) error { return ctx.Text(http.StatusOK, "pong") })
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", app.Handler()))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/api/ping")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "pong" || resp.Header.Get("X-Std") != "1" {
		t.Fatalf("mounted GET /api/ping = %d %q X-Std=%q, want 200 \"pong\" X-Std=1",
			resp.StatusCode, body, resp.Header.Get("X-Std"))
	}
}

type (
	scoped    struct{ n int }
	transient struct{ n int }
)

// Docs: project/faq.mdx#why-not-plain-nethttp-or-another-framework
// "a DI container with singleton, scoped, and transient lifetimes, which builds
// services in dependency order, runs their `Init`, and closes them in reverse
// order on shutdown;"
func TestContainerLifetimesAndOrder(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	built := 0
	app := gas.NewApp(
		// Registered before its dependency, so the order is the container's.
		gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] {
			rec.add("build kB")
			return &svc[kB]{rec: rec}
		}),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
			rec.add("build kA")
			return &svc[kA]{rec: rec}
		}),
		gas.WithScopedService[*scoped](func() *scoped { built++; return &scoped{n: built} }),
		gas.WithTransientService[*transient](func() *transient { built++; return &transient{n: built} }),
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	c := app.ServiceContainer()
	if a1, a2 := c.MustResolve[*svc[kA]](), c.MustResolve[*svc[kA]](); a1 != a2 {
		t.Error("singleton resolved twice gave two instances")
	}
	s1, s2 := c.NewScope(t.Context()), c.NewScope(t.Context())
	t.Cleanup(func() { _ = s1.Close(context.Background()); _ = s2.Close(context.Background()) })
	if x1, x2 := s1.MustResolve[*scoped](), s1.MustResolve[*scoped](); x1 != x2 {
		t.Error("scoped resolved twice in one scope gave two instances")
	}
	if s1.MustResolve[*scoped]() == s2.MustResolve[*scoped]() {
		t.Error("scoped resolved in two scopes gave one instance")
	}
	if t1, t2 := s1.MustResolve[*transient](), s1.MustResolve[*transient](); t1 == t2 {
		t.Error("transient resolved twice gave one instance")
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// The page does not say whether every service is built before any Init
	// runs, so only the pairwise order is checked.
	got := rec.list()
	at := func(e string) int { return slices.Index(got, e) }
	for _, pair := range [][2]string{
		{"build kA", "build kB"}, {"build kA", "init kA"}, {"build kB", "init kB"},
		{"init kA", "init kB"}, {"init kB", "close kB"}, {"close kB", "close kA"},
	} {
		if at(pair[0]) < 0 || at(pair[1]) < 0 || at(pair[0]) > at(pair[1]) {
			t.Errorf("lifecycle %v: want %q before %q", got, pair[0], pair[1])
		}
	}
}

// owned registers a route and an event subscription owned by itself.
type owned struct {
	router *gas.Router
	bus    *gas.EventBus
	hits   chan struct{}
}

type ping struct{ gas.Event[struct{}] }

func (o *owned) Name() string { return "owned" }

func (o *owned) Init(context.Context) error {
	o.router.Get(o, "/owned", func(ctx gas.Context) error { return ctx.Text(http.StatusOK, "owned") })
	o.bus.SubscribeWithOwner[ping](o, func(struct{}) { o.hits <- struct{}{} })
	return nil
}

func (o *owned) Close(context.Context) error { return nil }

// Docs: project/faq.mdx#why-not-plain-nethttp-or-another-framework
// "route ownership, so each route belongs to the service that registered it and
// can be taken down with it at runtime;"
func TestRouteOwnershipAndRuntimeTakedown(t *testing.T) {
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
	get := func(path string) int {
		return serve(t, app.Handler(), httptest.NewRequest(http.MethodGet, path, nil)).Code
	}
	if code := get("/owned"); code != http.StatusOK {
		t.Fatalf("GET /owned before CloseService = %d, want 200", code)
	}
	if err := app.CloseService[*owned](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if code := get("/owned"); code != http.StatusServiceUnavailable {
		t.Errorf("GET /owned after CloseService = %d, want 503", code)
	}
	if code := get("/other"); code != http.StatusOK {
		t.Errorf("GET /other after CloseService = %d, want 200", code)
	}
}

// notes owns a migration, registered under itself during Init.
type notes struct{ mm gas.MigrationManager }

func (n *notes) Name() string { return "notes" }

func (n *notes) Init(context.Context) error {
	n.mm.Register(n, gas.Migration{
		Version:     "20260101_001",
		Description: "create notes",
		Up:          "CREATE TABLE notes (id INTEGER PRIMARY KEY)",
		Down:        "DROP TABLE notes",
	})
	return nil
}

func (n *notes) Close(context.Context) error { return nil }

type reporter[K any] struct {
	svc[K]
	err error
}

func (r *reporter[K]) CheckHealth(context.Context) error { return r.err }
func (r *reporter[K]) CheckReady(context.Context) error  { return r.err }

type noted struct{ gas.Event[string] }

// Docs: project/faq.mdx#why-not-plain-nethttp-or-another-framework
// "migrations owned by the services that need them, health and readiness
// aggregated across services, and an event bus;"
func TestMigrationsHealthAndEvents(t *testing.T) {
	t.Parallel()
	dbCfg := database.DefaultConfig()
	dbCfg.Database.Driver = database.DriverSQLite
	dbCfg.Database.DSN = "file:" + t.TempDir() + "/faq.db"
	down := errors.New("down")
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, nil)),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[gas.DatabaseProvider](database.New(database.WithConfig(dbCfg))),
		gas.WithSingletonService[gas.MigrationManager](migrate.New()),
		gas.WithSingletonService[*notes](func(mm gas.MigrationManager) *notes { return &notes{mm: mm} }),
		gas.WithSingletonService[*reporter[kA]](func() *reporter[kA] { return &reporter[kA]{} }),
		gas.WithSingletonService[*reporter[kB]](func() *reporter[kB] { return &reporter[kB]{err: down} }),
	)
	var got []string
	app.EventBus().Subscribe[noted](func(s string) { got = append(got, s) })
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })

	db := app.ServiceContainer().MustResolve[gas.DatabaseProvider]().DB()
	var owner string
	if err := db.QueryRowContext(t.Context(),
		"SELECT service FROM __gas_migrations WHERE version = '20260101_001'").Scan(&owner); err != nil {
		t.Fatalf("migration not applied at Start: %v", err)
	}
	if owner != "notes" {
		t.Errorf("migration owner = %q, want notes", owner)
	}
	if _, err := db.ExecContext(t.Context(), "INSERT INTO notes (id) VALUES (1)"); err != nil {
		t.Errorf("the migration's table is missing: %v", err)
	}

	for name, m := range map[string]map[string]error{"CheckHealth": app.CheckHealth(t.Context()), "CheckReady": app.CheckReady(t.Context())} {
		a, okA := m["kA"]
		b, okB := m["kB"]
		if !okA || !okB || a != nil || !errors.Is(b, down) {
			t.Errorf("%s = %v, want kA: nil and kB: down across both services", name, m)
		}
	}

	app.EventBus().Emit[noted]("hello").Wait()
	if !slices.Equal(got, []string{"hello"}) {
		t.Errorf("event bus delivered %v, want [hello]", got)
	}
}

// goListDeps returns "module importpath" for every dependency of the
// non-test packages of module mod, packages named *test (the mocks) and
// those under internal/testutil excepted.
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
		if m == mod && !strings.Contains(pkg, "/internal/testutil") && !strings.HasSuffix(pkg, "test") {
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

// Docs: project/faq.mdx#can-i-use-one-module-without-the-rest
// "Apart from test helpers, no module imports
// another: they meet through the provider interfaces declared in core"
// Docs: project/faq.mdx#why-not-plain-nethttp-or-another-framework
// "which meet only through interfaces in core."
// Assumption: "test helpers" are _test.go files, the mock packages named
// *test, and packages under internal/testutil.
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

// Docs: project/faq.mdx#can-i-use-one-module-without-the-rest
// "`gas/ui`, for example, renders
// whatever is registered under `gas.TemplateProvider` without importing
// `gas/template`."
func TestUIRendersAnyTemplateProvider(t *testing.T) {
	t.Parallel()
	for _, line := range goListDeps(t, "github.com/gasmod/gas/ui") {
		if mod, pkg, _ := strings.Cut(line, " "); mod == "github.com/gasmod/gas/template" {
			t.Errorf("gas/ui imports %s", pkg)
		}
	}
	tp := &templatetest.MockTemplate{}
	store := map[string][]byte{
		"layouts/base.html": []byte(`{{define "base"}}<main>{{template "content" .}}</main>{{end}}`),
		"home.html":         []byte(`{{define "content"}}hello {{.}}{{end}}`),
	}
	tp.GetFn = func(_ context.Context, name string) ([]byte, error) {
		if v, ok := store[name]; ok {
			return v, nil
		}
		return nil, errors.New("no template " + name)
	}
	tp.ListFn = func(context.Context) ([]string, error) {
		return []string{"home.html", "layouts/base.html"}, nil
	}
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, nil)),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.TemplateProvider](tp),
		gas.WithSingletonService[gas.UIProvider](ui.New[gas.TemplateProvider](ui.WithStaticFS(fstest.MapFS{}))),
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	rr := httptest.NewRecorder()
	if err := app.ServiceContainer().MustResolve[gas.UIProvider]().Render(rr, "home", "gas"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := rr.Body.String(); got != "<main>hello gas</main>" {
		t.Fatalf("Render = %q, want %q", got, "<main>hello gas</main>")
	}
}

// consumer is a service that takes the interface I.
type consumer[I any] struct{ got I }

func (c *consumer[I]) Name() string                { return "consumer-" + reflect.TypeFor[I]().Name() }
func (c *consumer[I]) Init(context.Context) error  { return nil }
func (c *consumer[I]) Close(context.Context) error { return nil }

// swapped registers impl under I and reports whether a service taking I
// received it.
func swapped[I any](t *testing.T, impl I) {
	t.Helper()
	name := reflect.TypeFor[I]().String()
	w := gas.NewWorker(
		gas.WithServiceInstance[I](impl),
		gas.WithSingletonService[*consumer[I]](func(v I) *consumer[I] { return &consumer[I]{got: v} }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Errorf("%s: start: %v", name, err)
		return
	}
	defer func() { _ = w.Shutdown(context.Background()) }()
	c, err := w.ServiceContainer().Resolve[*consumer[I]]()
	if err != nil {
		t.Errorf("%s: resolve consumer: %v", name, err)
		return
	}
	if any(c.got) != any(impl) {
		t.Errorf("%s: the service taking it got %v, want the registered %v", name, c.got, impl)
	}
}

// Each own* type implements one core interface by embedding it; the
// embedded value is never called.
type (
	ownCache    struct{ gas.CacheProvider }
	ownDB       struct{ gas.DatabaseProvider }
	ownQueue    struct{ gas.JobQueueProvider }
	ownEmail    struct{ gas.EmailProvider }
	ownTemplate struct{ gas.TemplateProvider }
	ownUI       struct{ gas.UIProvider }
	ownConfig   struct{ *config.Config }
	ownLogger   struct{ *gas.NopLogger }
)

// Docs: project/faq.mdx#how-do-i-swap-a-module-for-my-own-implementation
// "Implement the core interface and register your type under it. Every service
// that takes the interface then gets yours:"
// Docs: project/faq.mdx#how-do-i-swap-a-module-for-my-own-implementation
// "The same works for any provider interface in core: `gas.CacheProvider`,
// `gas.DatabaseProvider`, `gas.JobQueueProvider`, `gas.EmailProvider`,
// `gas.TemplateProvider`, `gas.UIProvider`, `gas.ConfigProvider`, `gas.Logger`,"
func TestSwapAnyProviderInterface(t *testing.T) {
	t.Parallel()
	swapped[gas.CacheProvider](t, &ownCache{})
	swapped[gas.DatabaseProvider](t, &ownDB{})
	swapped[gas.JobQueueProvider](t, &ownQueue{})
	swapped[gas.EmailProvider](t, &ownEmail{})
	swapped[gas.TemplateProvider](t, &ownTemplate{})
	swapped[gas.UIProvider](t, &ownUI{})
	swapped[gas.ConfigProvider](t, &ownConfig{config.New()})
	swapped[gas.Logger](t, &ownLogger{&gas.NopLogger{}})
}

// Docs: project/faq.mdx#how-do-i-swap-a-module-for-my-own-implementation
// "In tests, the modules' mock packages (`queuetest`, `uitest`, and
// so on) implement the same interfaces, so a test registers a mock the same
// way."
// Assumption: storagetest and authtest mock the interfaces their own
// modules declare (storage.Provider, auth.Authenticator), as no core
// interface covers them.
func TestMocksRegisterTheSameWay(t *testing.T) {
	t.Parallel()
	cfg, err := configtest.NewMockConfigWithValues(nil)
	if err != nil {
		t.Fatal(err)
	}
	swapped[gas.CacheProvider](t, &cachetest.MockCache{})
	swapped[gas.JobQueueProvider](t, &queuetest.MockQueue{})
	swapped[gas.EmailProvider](t, &emailtest.MockEmail{})
	swapped[gas.TemplateProvider](t, &templatetest.MockTemplate{})
	swapped[gas.UIProvider](t, &uitest.MockUI{})
	swapped[gas.ConfigProvider](t, gas.ConfigProvider(cfg))
	swapped[gas.MigrationManager](t, &migratetest.MockMigrationManager{})
	swapped[storage.Provider](t, &storagetest.MockProvider{})
	swapped[auth.Authenticator](t, &authtest.MockAuthenticator{})
}

// Docs: project/faq.mdx#can-i-run-gas-without-an-http-server
// "`gas.Worker` has the same container, service lifecycle, events, and
// migrations as `gas.App`, without the router or the server."
func TestWorkerIsAppWithoutRouterOrServer(t *testing.T) {
	t.Parallel()
	wt := reflect.TypeFor[*gas.Worker]()
	for _, m := range []string{"Router", "Handler", "Serve", "Server"} {
		if _, ok := wt.MethodByName(m); ok {
			t.Errorf("Worker has %s; want no router or server", m)
		}
		if _, ok := reflect.TypeFor[*gas.App]().MethodByName(m); !ok {
			t.Errorf("App lacks %s", m)
		}
	}
	if f, ok := reflect.TypeFor[gas.App]().FieldByName("Worker"); !ok || !f.Anonymous || f.Type != wt {
		t.Error("App does not embed *gas.Worker, so its container and lifecycle are not the Worker's")
	}
	if w := gas.NewWorker(); w.ServiceContainer().CanResolve[*gas.Router]() {
		t.Error("a Worker's container holds a *gas.Router")
	}

	rec := &recorder{}
	mm := &migratetest.MockMigrationManager{}
	var events []string
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.MigrationManager](mm),
		gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] { return &svc[kB]{rec: rec} }),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{rec: rec} }),
	)
	w.EventBus().Subscribe[noted](func(s string) { events = append(events, s) })
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	w.EventBus().Emit[noted]("x").Wait()
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if want := []string{"init kA", "init kB", "close kB", "close kA"}; !slices.Equal(rec.list(), want) {
		t.Errorf("Worker lifecycle = %v, want %v", rec.list(), want)
	}
	if !slices.Equal(events, []string{"x"}) {
		t.Errorf("Worker event bus delivered %v, want [x]", events)
	}
	ran := slices.ContainsFunc(mm.Calls, func(c migratetest.Call) bool { return c.Method == "RunPending" })
	if !ran {
		t.Error("Worker Start did not run pending migrations")
	}
}
