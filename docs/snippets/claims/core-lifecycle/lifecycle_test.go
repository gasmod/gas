package corelifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/migrate/migratetest"
)

// Docs: core/lifecycle.mdx#_top
// "`gas.App` serves HTTP. It embeds a `gas.Worker`, which owns the DI container,
// the event bus, migrations, and service management; the App adds the router,
// CSRF protection, and the HTTP server."
func TestAppEmbedsWorkerAndAddsHTTP(t *testing.T) {
	t.Parallel()
	wt := reflect.TypeFor[*gas.Worker]()
	if f, ok := reflect.TypeFor[gas.App]().FieldByName("Worker"); !ok || !f.Anonymous || f.Type != wt {
		t.Fatal("App does not embed *gas.Worker")
	}
	for _, m := range []string{"ServiceContainer", "EventBus", "MigrationManager", "ActiveServices", "CheckHealth"} {
		if _, ok := wt.MethodByName(m); !ok {
			t.Errorf("Worker lacks %s", m)
		}
	}
	for _, m := range []string{"Router", "Handler", "Server", "Serve"} {
		if _, ok := wt.MethodByName(m); ok {
			t.Errorf("Worker has %s; want it on App only", m)
		}
		if _, ok := reflect.TypeFor[*gas.App]().MethodByName(m); !ok {
			t.Errorf("App lacks %s", m)
		}
	}
}

// Docs: core/lifecycle.mdx#run-or-its-parts
// "| `Start(ctx)` | Builds and initializes services, runs migrations and ready hooks, binds the config. Does not block or listen. |"
func TestStartDoesNotBlockOrListen(t *testing.T) {
	t.Parallel()
	port := freePort(t)
	a := &svc[kA]{}
	mm := &migratetest.MockMigrationManager{}
	var hookRan atomic.Bool
	app := gas.NewApp(
		gas.WithServiceInstance(serverConfig(t, port, nil)),
		gas.WithServiceInstance[gas.MigrationManager](mm),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return a }),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error { hookRan.Store(true); return nil }),
	)
	done := make(chan error, 1)
	go func() { done <- app.Start(t.Context()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("start: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start blocked")
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if !a.isInitialized() || mm.CallCount("RunPending") != 1 || !hookRan.Load() || app.Config().Server.Port != port {
		t.Fatalf("after Start: initialized=%v RunPending=%d hook=%v port=%d", a.isInitialized(),
			mm.CallCount("RunPending"), hookRan.Load(), app.Config().Server.Port)
	}
	if accepting(port) {
		t.Fatal("Start listens on the port")
	}
}

// Docs: core/lifecycle.mdx#run-or-its-parts
// "| `Serve()` | Starts the HTTP server and blocks until it stops. A clean shutdown returns nil. |"
// Docs: core/lifecycle.mdx#run-or-its-parts
// "| `Stop(ctx)` | Shuts the HTTP server down, then closes every service. |"
func TestServeBlocksAndStopShutsServerThenServices(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	port := freePort(t)
	started := make(chan struct{})
	app := gas.NewApp(
		gas.WithServiceInstance(serverConfig(t, port, nil)),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{rec: rec} }),
	)
	app.Router().Get(nil, "/ping", func(w http.ResponseWriter, _ *http.Request) {})
	app.Router().Get(nil, "/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(200 * time.Millisecond)
		rec.add("request done")
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- app.Serve() }()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitUp(t, base+"/ping")
	select {
	case err := <-served:
		t.Fatalf("Serve returned while serving: %v", err)
	default:
	}
	go func() {
		if resp, err := http.Get(base + "/slow"); err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve after a clean shutdown = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after Stop")
	}
	if got, want := rec.only("request", "close"), []string{"request done", "close kA"}; !slices.Equal(got, want) {
		t.Fatalf("Stop order = %v, want the in-flight request finished before Close", got)
	}
}

// Docs: core/lifecycle.mdx#run-or-its-parts
// "| `Handler()` | Returns what the server serves: the router behind CSRF protection. |"
// Docs: core/lifecycle.mdx#start-and-stop-in-tests
// "`Handler()` returns exactly what the server would serve, so a test can drive
// the full middleware stack without binding a port"
func TestHandlerIsRouterBehindCSRF(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Post(nil, "/submit", func(w http.ResponseWriter, _ *http.Request) {})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if !sameHandler(app.Server().Handler, app.Handler()) {
		t.Fatalf("Server().Handler (%T) is not Handler() (%T)", app.Server().Handler, app.Handler())
	}
	send := func(h http.Handler) int {
		req := httptest.NewRequest(http.MethodPost, "/submit", nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Origin", "https://evil.example")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr.Code
	}
	if got := send(app.Handler()); got != http.StatusForbidden {
		t.Fatalf("cross-site POST through Handler() = %d, want 403", got)
	}
	if got := send(app.Router()); got != http.StatusOK {
		t.Fatalf("cross-site POST straight to the router = %d, want 200", got)
	}
}

// Docs: core/lifecycle.mdx#startup-order
// "1. **Build singletons.** The container validates every registration, runs
// `Init` on every `Service` registered as an instance, then constructs every
// singleton in dependency order, calling each one's `Init` as it is built."
func TestBuildSingletons(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	w := gas.NewWorker(
		gas.WithSingletonService[*svc[kB]](func(a *svc[kA]) *svc[kB] {
			rec.add("build kB, kA initialized=%v", a.isInitialized())
			return &svc[kB]{rec: rec}
		}),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { rec.add("build kA"); return &svc[kA]{rec: rec} }),
		gas.WithServiceInstance(&svc[kC]{rec: rec}),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = w.Shutdown(context.Background())
	got := rec.only("init", "build")
	want := []string{"init kC", "build kA", "init kA", "build kB, kA initialized=true", "init kB"}
	if !slices.Equal(got, want) {
		t.Fatalf("startup = %v, want %v", got, want)
	}

	inst := &svc[kC]{}
	bad := gas.NewWorker(gas.WithServiceInstance(inst), gas.WithSingletonService[*halfService](func() *halfService { return &halfService{} }))
	if err := bad.Start(t.Context()); !errors.Is(err, gas.ErrInvalidServiceShape) {
		t.Fatalf("Start = %v, want ErrInvalidServiceShape", err)
	}
	if inst.inits() != 0 {
		t.Fatal("an instance's Init ran before every registration was validated")
	}
}

type halfService struct{}

func (halfService) Init(context.Context) error { return nil }

// Docs: core/lifecycle.mdx#startup-order
// "2. **Seal the router and check handlers** (App only). The router builds its
// routing tree from everything registered so far; until then, `Handler()`
// serves none of your routes."
func TestHandlerServesNoRoutesUntilSealed(t *testing.T) {
	t.Parallel()
	var app *gas.App
	var duringInit int
	app = gas.NewApp(gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
		return &svc[kA]{onInit: func(context.Context) error { duringInit = get(app.Handler(), "/x").Code; return nil }}
	}))
	app.Router().Get(nil, "/x", func(w http.ResponseWriter, _ *http.Request) {})
	if got := get(app.Handler(), "/x").Code; got == http.StatusOK {
		t.Fatal("Handler() served the route before Start")
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if duringInit == http.StatusOK {
		t.Fatal("Handler() served the route during Init, before the router was sealed")
	}
	if got := get(app.Handler(), "/x").Code; got != http.StatusOK {
		t.Fatalf("after Start GET /x = %d, want 200", got)
	}
}

type unregistered struct{}

// Docs: core/lifecycle.mdx#startup-order
// "Every [DI-aware handler](/gas/core/handlers/)'s
// parameter types are checked against the container."
// Docs: core/lifecycle.mdx#startup-order
// "3. **Emit `gas.SystemAllServicesInitialized`**"
// A handler that fails the check stops Start before step 3 and step 4.
func TestHandlerCheckBeforeEventAndMigrations(t *testing.T) {
	t.Parallel()
	var emitted atomic.Bool
	mm := &migratetest.MockMigrationManager{}
	app := gas.NewApp(gas.WithServiceInstance[gas.MigrationManager](mm))
	app.EventBus().Subscribe[gas.SystemAllServicesInitialized](func(gas.SystemAllServicesInitializedPayload) {
		emitted.Store(true)
	})
	app.Router().Get(nil, "/x", func(gas.Context, *unregistered) error { return nil })
	if err := app.Start(t.Context()); err == nil {
		t.Fatal("Start succeeded with an unresolvable handler parameter")
	}
	time.Sleep(50 * time.Millisecond)
	if emitted.Load() || mm.CallCount("RunPending") != 0 {
		t.Fatalf("after a failed handler check: event emitted=%v, RunPending=%d; want neither", emitted.Load(), mm.CallCount("RunPending"))
	}
}

// Docs: core/lifecycle.mdx#startup-order
// "3. **Emit `gas.SystemAllServicesInitialized`**, and wait for its subscribers to
// return."
// Docs: core/lifecycle.mdx#startup-order
// "4. **Run migrations.** If a `gas.MigrationManager` is registered, its
// `RunPending` applies every pending migration."
// Docs: core/lifecycle.mdx#startup-order
// "5. **Run ready hooks**, in registration order."
// Docs: core/lifecycle.mdx#startup-order
// "6. **Bind the config** (App only). If a `gas.ConfigProvider` is registered, the
// App's `Config` (server settings and environment) is bound from it, and
// `gas.AppConfigUpdated` is emitted with the result."
// Docs: core/lifecycle.mdx#startup-order
// "`Start` runs these steps in order."
func TestStartupOrder(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	mm := &migratetest.MockMigrationManager{RunPendingFn: func() error { rec.add("migrations"); return nil }}
	var app *gas.App
	var portInHook int
	var payload atomic.Value
	app = gas.NewApp(
		gas.WithServiceInstance(mustConfig(t, map[string]any{"server": map[string]any{"port": 9191}, "gasenv": "staging"})),
		gas.WithServiceInstance[gas.MigrationManager](mm),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{rec: rec} }),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error {
			rec.add("ready 1")
			portInHook = app.Config().Server.Port
			return nil
		}),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error { rec.add("ready 2"); return nil }),
	)
	app.EventBus().Subscribe[gas.SystemAllServicesInitialized](func(gas.SystemAllServicesInitializedPayload) {
		time.Sleep(100 * time.Millisecond)
		rec.add("all-initialized")
	})
	app.EventBus().Subscribe[gas.AppConfigUpdated](func(p gas.AppConfigUpdatedPayload) {
		payload.Store(p.Config)
		rec.add("config-bound")
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if !waitFor(time.Second, func() bool { return len(rec.only("config-bound")) == 1 }) {
		t.Fatalf("AppConfigUpdated never observed; events = %v", rec.list())
	}
	got := rec.only("init kA", "all-initialized", "migrations", "ready", "config-bound")
	want := []string{"init kA", "all-initialized", "migrations", "ready 1", "ready 2", "config-bound"}
	if !slices.Equal(got, want) {
		t.Fatalf("startup order = %v, want %v", got, want)
	}
	if portInHook == 9191 {
		t.Error("the config was bound before the ready hooks ran")
	}
	if app.Config().Server.Port != 9191 || app.Config().GasEnv != "staging" {
		t.Errorf("Config after Start = port %d env %q, want 9191 staging", app.Config().Server.Port, app.Config().GasEnv)
	}
	if cfg, _ := payload.Load().(gas.Config); cfg.Server.Port != 9191 || cfg.GasEnv != "staging" {
		t.Errorf("AppConfigUpdated carried port %d env %q, want the bound config", cfg.Server.Port, cfg.GasEnv)
	}
}

// Docs: core/lifecycle.mdx#startup-order
// "Everything on this page about startup and
// shutdown applies to a Worker too, except the steps marked App only."
// The Worker runs steps 1, 3, 4, and 5 of startup in the same order.
func TestWorkerStartupOrder(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	mm := &migratetest.MockMigrationManager{RunPendingFn: func() error { rec.add("migrations"); return nil }}
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.MigrationManager](mm),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{rec: rec} }),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error { rec.add("ready"); return nil }),
	)
	w.EventBus().Subscribe[gas.SystemAllServicesInitialized](func(gas.SystemAllServicesInitializedPayload) {
		rec.add("all-initialized")
	})
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if got, want := rec.list(), []string{"init kA", "all-initialized", "migrations", "ready"}; !slices.Equal(got, want) {
		t.Fatalf("Worker startup = %v, want %v", got, want)
	}
}

// Docs: core/lifecycle.mdx#startup-order
// "The event is emitted even
// when no provider is registered."
func TestAppConfigUpdatedWithoutProvider(t *testing.T) {
	t.Parallel()
	got := make(chan gas.Config, 1)
	app := gas.NewApp()
	app.EventBus().Subscribe[gas.AppConfigUpdated](func(p gas.AppConfigUpdatedPayload) { got <- p.Config })
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	select {
	case cfg := <-got:
		if cfg.Server.Port != 8080 {
			t.Errorf("payload port = %d, want the default 8080", cfg.Server.Port)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AppConfigUpdated was not emitted without a ConfigProvider")
	}
}

// failingStarts builds one App per startup step that can fail, each bound to
// port.
func failingStarts(t *testing.T, port int) map[string]func() *gas.App {
	cfg := func() gas.Option { return gas.WithServiceInstance(serverConfig(t, port, nil)) }
	return map[string]func() *gas.App{
		"constructor error": func() *gas.App {
			return gas.NewApp(cfg(), gas.WithSingletonService[*svc[kA]](func() (*svc[kA], error) { return nil, errBoom }))
		},
		"Init error": func() *gas.App {
			return gas.NewApp(cfg(), gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
				return &svc[kA]{onInit: func(context.Context) error { return errBoom }}
			}))
		},
		"handler check": func() *gas.App {
			app := gas.NewApp(cfg())
			app.Router().Get(nil, "/x", func(gas.Context, *unregistered) error { return nil })
			return app
		},
		"migration error": func() *gas.App {
			return gas.NewApp(cfg(), gas.WithServiceInstance[gas.MigrationManager](
				&migratetest.MockMigrationManager{RunPendingFn: func() error { return errBoom }}))
		},
		"ready hook error": func() *gas.App {
			return gas.NewApp(cfg(), gas.WithReadyFunc(func(*gas.ServiceContainer) error { return errBoom }))
		},
		"config binding error": func() *gas.App {
			return gas.NewApp(gas.WithServiceInstance(serverConfig(t, port, map[string]any{"host": "not a host name!"})))
		},
	}
}

// Docs: core/lifecycle.mdx#startup-order
// "A failure in any step returns an error from `Start`, and the server never
// accepts a request against a half-built app."
// Each failing app is run with Run, which must return the error without the
// port ever accepting a connection.
func TestAnyFailingStepStopsBeforeServing(t *testing.T) {
	t.Parallel()
	for name := range failingStarts(t, 0) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			port := freePort(t)
			app := failingStarts(t, port)[name]()
			done := make(chan error, 1)
			go func() { done <- app.Run() }()
			accepted := false
			var err error
			for loop := true; loop; {
				select {
				case err = <-done:
					loop = false
				case <-time.After(10 * time.Second):
					t.Fatal("Run did not return the startup error")
				default:
					accepted = accepted || accepting(port)
					time.Sleep(2 * time.Millisecond)
				}
			}
			if err == nil {
				t.Fatal("Run returned nil")
			}
			if accepted || accepting(port) {
				t.Fatalf("port %d accepted a connection", port)
			}
		})
	}
}

// Docs: core/lifecycle.mdx#startup-order
// "The error from a failed migration
// starts with `gas: migrations:`, from a ready hook with `gas: ready hook:`, and
// from config binding with `gas: failed to bind config:`."
func TestStartupErrorPrefixes(t *testing.T) {
	t.Parallel()
	builds := failingStarts(t, freePort(t))
	for name, prefix := range map[string]string{
		"migration error":      "gas: migrations:",
		"ready hook error":     "gas: ready hook:",
		"config binding error": "gas: failed to bind config:",
	} {
		err := builds[name]().Start(t.Context())
		if err == nil || !strings.HasPrefix(err.Error(), prefix) {
			t.Errorf("%s: Start = %v, want an error starting with %q", name, err, prefix)
		}
	}
}

// Docs: core/lifecycle.mdx#a-failed-start-leaves-nothing-running
// "When `Start` fails, it closes, in reverse order, every service it had already
// initialized, and returns the startup error joined with any `Close` errors. The
// `Close` calls get the context passed to `Start` with its cancellation removed,
// so cleanup runs even if that context is what failed."
func TestFailedStartClosesInReverse(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	errInit := errors.New("c init failed")
	errCloseA := errors.New("a close failed")
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), ctxKey{}, "start"))
	defer cancel()
	type seen struct {
		err error
		val any
	}
	var mu sync.Mutex
	var closeCtxs []seen
	track := func(ctx context.Context) {
		mu.Lock()
		defer mu.Unlock()
		closeCtxs = append(closeCtxs, seen{ctx.Err(), ctx.Value(ctxKey{})})
	}
	w := gas.NewWorker(
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
			return &svc[kA]{rec: rec, onClose: func(ctx context.Context) error { track(ctx); return errCloseA }}
		}),
		gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] {
			return &svc[kB]{rec: rec, onClose: func(ctx context.Context) error { track(ctx); return nil }}
		}),
		gas.WithSingletonService[*svc[kC]](func(*svc[kB]) *svc[kC] {
			return &svc[kC]{rec: rec, onInit: func(context.Context) error { cancel(); return errInit }}
		}),
	)
	err := w.Start(ctx)
	if got, want := rec.only("close"), []string{"close kB", "close kA"}; !slices.Equal(got, want) {
		t.Fatalf("close order = %v, want %v", got, want)
	}
	for i, s := range closeCtxs {
		if s.err != nil || s.val != "start" {
			t.Errorf("Close #%d got Err()=%v value=%v, want Start's context without its cancellation", i, s.err, s.val)
		}
	}
	if !errors.Is(err, errInit) || !errors.Is(err, errCloseA) {
		t.Fatalf("Start = %v, want the startup error joined with the Close error", err)
	}
}

// Docs: core/lifecycle.mdx#a-failed-start-leaves-nothing-running
// "The App or Worker cannot be started again: every later `Start` returns the same
// error, and a later `Shutdown` has nothing to close."
func TestFailedStartIsFinal(t *testing.T) {
	t.Parallel()
	mk := func() *svc[kB] { return &svc[kB]{onInit: func(context.Context) error { return errBoom }} }
	t.Run("Worker", func(t *testing.T) {
		t.Parallel()
		a := &svc[kA]{}
		w := gas.NewWorker(gas.WithServiceInstance(a), gas.WithSingletonService[*svc[kB]](mk))
		first, second := w.Start(t.Context()), w.Start(t.Context())
		if first == nil || second == nil || first.Error() != second.Error() {
			t.Fatalf("Start errors = %v, %v; want the same error twice", first, second)
		}
		if err := w.Shutdown(context.Background()); err != nil || a.closes() != 1 {
			t.Fatalf("Shutdown after a failed Start = %v with %d closes, want nothing more closed", err, a.closes())
		}
	})
	t.Run("App", func(t *testing.T) {
		t.Parallel()
		a := &svc[kA]{}
		app := gas.NewApp(gas.WithServiceInstance(a), gas.WithSingletonService[*svc[kB]](mk))
		first, second := app.Start(t.Context()), app.Start(t.Context())
		if first == nil || second == nil || first.Error() != second.Error() {
			t.Fatalf("Start errors = %v, %v; want the same error twice", first, second)
		}
		if err := app.Shutdown(context.Background()); err != nil || a.closes() != 1 {
			t.Fatalf("Shutdown after a failed Start = %v with %d closes, want nothing more closed", err, a.closes())
		}
	})
}

// hasType compiles only when v has type T.
func hasType[T any](v T) { _ = v }

// Docs: core/lifecycle.mdx#init-and-close-contexts
// "`Start`, `InitServices`, `Stop`, `Shutdown`, `CloseService`, `RestartService`,
// `ServiceContainer.BuildAll`, `ServiceContainer.NewScope`, and `Scope.Close` all
// take a context. `Run` takes none."
func TestLifecycleMethodSignatures(t *testing.T) {
	t.Parallel()
	hasType[func(*gas.Worker, context.Context) error]((*gas.Worker).Start)
	hasType[func(*gas.Worker, context.Context) error]((*gas.Worker).InitServices)
	hasType[func(*gas.Worker, context.Context) error]((*gas.Worker).Shutdown)
	hasType[func(*gas.App, context.Context) error]((*gas.App).Start)
	hasType[func(*gas.App, context.Context) error]((*gas.App).Stop)
	hasType[func(*gas.ServiceContainer, context.Context) error]((*gas.ServiceContainer).BuildAll)
	hasType[func(*gas.ServiceContainer, context.Context) *gas.Scope]((*gas.ServiceContainer).NewScope)
	hasType[func(*gas.Scope, context.Context) error]((*gas.Scope).Close)
	hasType[func(*gas.App) error]((*gas.App).Run)
	hasType[func(*gas.Worker) error]((*gas.Worker).Run)

	// CloseService and RestartService are generic methods, so they are
	// called rather than taken as values.
	w := gas.NewWorker(gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{} }))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if err := w.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if err := w.RestartService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("RestartService: %v", err)
	}
}

// initDeadline starts a Worker with opts plus a singleton that records the
// context its Init got, and returns that context and when Start was called.
func initDeadline(t *testing.T, ctx context.Context, opts ...gas.Option) (context.Context, time.Time) {
	t.Helper()
	var got context.Context
	opts = append(opts, gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
		return &svc[kA]{onInit: func(ctx context.Context) error { got = ctx; return nil }}
	}))
	w := gas.NewWorker(opts...)
	start := time.Now()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	return got, start
}

func about(got, want time.Duration) bool { return got <= want+time.Second && got > want-2*time.Second }

// Docs: core/lifecycle.mdx#init-and-close-contexts
// "**Init of a singleton** gets the context passed to `Start` (or to
// `RestartService`) with a per-service deadline added: 15 seconds by default."
// Docs: core/lifecycle.mdx#init-and-close-contexts
// "`gas.WithServiceInitTimeout(d)` changes the default for an App or Worker. `0`
// disables the deadline, and a negative value panics."
func TestSingletonInitContext(t *testing.T) {
	t.Parallel()
	ctx, start := initDeadline(t, context.WithValue(context.Background(), ctxKey{}, "start"))
	dl, ok := ctx.Deadline()
	if !ok || !about(dl.Sub(start), 15*time.Second) || ctx.Value(ctxKey{}) != "start" {
		t.Errorf("default: deadline %v (set=%v) value %v, want about 15s on Start's context", dl.Sub(start), ok, ctx.Value(ctxKey{}))
	}
	ctx, start = initDeadline(t, context.Background(), gas.WithServiceInitTimeout(5*time.Second))
	if dl, ok := ctx.Deadline(); !ok || !about(dl.Sub(start), 5*time.Second) {
		t.Errorf("WithServiceInitTimeout(5s): deadline %v (set=%v), want about 5s", dl.Sub(start), ok)
	}
	ctx, _ = initDeadline(t, context.Background(), gas.WithServiceInitTimeout(0))
	if _, ok := ctx.Deadline(); ok {
		t.Error("WithServiceInitTimeout(0): Init has a deadline")
	}
	appCtx := make(chan context.Context, 1)
	app := gas.NewApp(gas.WithServiceInitTimeout(3*time.Second), gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
		return &svc[kA]{onInit: func(ctx context.Context) error { appCtx <- ctx; return nil }}
	}))
	start = time.Now()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("app start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if dl, ok := (<-appCtx).Deadline(); !ok || !about(dl.Sub(start), 3*time.Second) {
		t.Errorf("App with WithServiceInitTimeout(3s): deadline %v (set=%v), want about 3s", dl.Sub(start), ok)
	}
	mustPanic(t, "negative WithServiceInitTimeout", func() { gas.WithServiceInitTimeout(-time.Second) })

	// RestartService passes its own context to Init.
	var restartVal any
	var restartDL bool
	w := gas.NewWorker(gas.WithSingletonService[*svc[kB]](func() *svc[kB] {
		return &svc[kB]{onInit: func(ctx context.Context) error {
			restartVal = ctx.Value(ctxKey{})
			_, restartDL = ctx.Deadline()
			return nil
		}}
	}))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if err := w.CloseService[*svc[kB]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if err := w.RestartService[*svc[kB]](context.WithValue(t.Context(), ctxKey{}, "restart")); err != nil {
		t.Fatalf("RestartService: %v", err)
	}
	if restartVal != "restart" || !restartDL {
		t.Errorf("Init on RestartService saw value %v deadline=%v, want RestartService's context with a deadline", restartVal, restartDL)
	}
}

// ownTimeout is a service that sets its own Init deadline.
type ownTimeout struct {
	timeout time.Duration
	ran     bool
	ctx     context.Context
}

func (s *ownTimeout) Name() string                   { return "own-timeout" }
func (s *ownTimeout) InitTimeout() time.Duration     { return s.timeout }
func (s *ownTimeout) Close(context.Context) error    { return nil }
func (s *ownTimeout) Init(ctx context.Context) error { s.ran, s.ctx = true, ctx; return nil }

// Docs: core/lifecycle.mdx#init-and-close-contexts
// "A service that implements `gas.ServiceInitTimeout` sets its own. `0` means no
// deadline, and a negative value fails its `Init` without running it."
func TestServiceInitTimeoutSetsItsOwn(t *testing.T) {
	t.Parallel()
	if !reflect.TypeFor[*ownTimeout]().Implements(reflect.TypeFor[gas.ServiceInitTimeout]()) {
		t.Fatal("ownTimeout does not implement gas.ServiceInitTimeout")
	}
	run := func(d time.Duration) (*ownTimeout, time.Time, error) {
		s := &ownTimeout{timeout: d}
		w := gas.NewWorker(gas.WithServiceInitTimeout(time.Hour), gas.WithServiceInstance(s))
		start := time.Now()
		err := w.Start(t.Context())
		t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
		return s, start, err
	}
	s, start, err := run(4 * time.Second)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if dl, ok := s.ctx.Deadline(); !ok || !about(dl.Sub(start), 4*time.Second) {
		t.Errorf("own 4s: deadline %v (set=%v), want about 4s", dl.Sub(start), ok)
	}
	s, _, err = run(0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if dl, ok := s.ctx.Deadline(); ok {
		t.Errorf("own 0: Init has a deadline %v", dl)
	}
	s, _, err = run(-time.Second)
	if err == nil {
		t.Fatal("own negative: Start succeeded")
	}
	if s.ran {
		t.Fatal("own negative: Init ran")
	}
}

// Docs: core/lifecycle.mdx#init-and-close-contexts
// "When the deadline passes, the container cancels the context, waits for `Init`
// to return, and fails with an error that wraps `context.DeadlineExceeded`."
func TestInitDeadlineCancelsWaitsAndFails(t *testing.T) {
	t.Parallel()
	var returned atomic.Bool
	w := gas.NewWorker(
		gas.WithServiceInitTimeout(50*time.Millisecond),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
			return &svc[kA]{onInit: func(ctx context.Context) error {
				<-ctx.Done()
				time.Sleep(100 * time.Millisecond)
				returned.Store(true)
				return ctx.Err()
			}}
		}),
	)
	err := w.Start(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start = %v, want an error wrapping context.DeadlineExceeded", err)
	}
	if !returned.Load() {
		t.Fatal("Start returned before Init did")
	}
}

// Docs: core/lifecycle.mdx#init-and-close-contexts
// "An
// `Init` that ignores the cancellation and returns nil late is closed again
// before the error is returned, so nothing it acquired is left open."
func TestLateInitIsClosed(t *testing.T) {
	t.Parallel()
	a := &svc[kA]{onInit: func(context.Context) error { time.Sleep(150 * time.Millisecond); return nil }}
	w := gas.NewWorker(gas.WithServiceInitTimeout(50*time.Millisecond), gas.WithServiceInstance(a))
	err := w.Start(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start = %v, want an error wrapping context.DeadlineExceeded", err)
	}
	if a.closes() != 1 {
		t.Fatalf("a late Init returning nil was closed %d times by the time Start returned, want 1", a.closes())
	}
}

// Docs: core/lifecycle.mdx#init-and-close-contexts
// "While a deadline applies, the context is canceled as soon as `Init` returns."
func TestInitContextCanceledWhenInitReturns(t *testing.T) {
	t.Parallel()
	ctx, _ := initDeadline(t, context.Background())
	if ctx.Err() == nil {
		t.Fatal("Init's context is still live after Init returned")
	}
}

// Docs: core/lifecycle.mdx#init-and-close-contexts
// "**Init of a scoped service** gets the scope's context with the same deadline
// added, the default or the service's own `InitTimeout`, and the same timeout
// handling. In an App that is the request's context."
func TestScopedInitContext(t *testing.T) {
	t.Parallel()
	var scopeCtx context.Context
	c := gas.NewServiceContainer()
	c.RegisterScopedService[*svc[kSc]](func() *svc[kSc] {
		return &svc[kSc]{onInit: func(ctx context.Context) error { scopeCtx = ctx; return nil }}
	})
	scope := c.NewScope(context.WithValue(context.Background(), ctxKey{}, "scope"))
	scope.MustResolve[*svc[kSc]]()
	if _, ok := scopeCtx.Deadline(); !ok || scopeCtx.Value(ctxKey{}) != "scope" {
		t.Errorf("scoped Init context: value %v, deadline set=%v; want the scope's context with deadline", scopeCtx.Value(ctxKey{}), ok)
	}

	var reqVal any
	app := gas.NewApp(gas.WithScopedService[*svc[kSc]](func() *svc[kSc] {
		return &svc[kSc]{onInit: func(ctx context.Context) error { reqVal = ctx.Value(ctxKey{}); return nil }}
	}))
	app.Router().Get(nil, "/scoped", func(ctx gas.Context, _ *svc[kSc]) error { return ctx.NoContent() })
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	req := httptest.NewRequestWithContext(context.WithValue(t.Context(), ctxKey{}, "request"), http.MethodGet, "/scoped", nil)
	app.Handler().ServeHTTP(httptest.NewRecorder(), req)
	if reqVal != "request" {
		t.Fatalf("scoped Init in an App saw %v, want the request's context", reqVal)
	}
}

// blockingScoped is a scoped service whose Init blocks until its context ends.
type blockingScoped struct{ timeout time.Duration }

func (s *blockingScoped) Name() string                   { return "blocking-scoped" }
func (s *blockingScoped) Close(context.Context) error    { return nil }
func (s *blockingScoped) Init(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

// ownScopedTimeout is a blockingScoped that implements gas.ServiceInitTimeout,
// so its timeout field sets its own Init deadline.
type ownScopedTimeout struct{ blockingScoped }

func (s *ownScopedTimeout) InitTimeout() time.Duration { return s.timeout }

// Docs: core/lifecycle.mdx#init-and-close-contexts
// "A scoped `Init` that times out fails the resolution."
func TestScopedInitTimeoutFailsResolution(t *testing.T) {
	t.Parallel()
	const short = 50 * time.Millisecond
	check := func(t *testing.T, c *gas.ServiceContainer, resolve func(*gas.Scope) error) {
		t.Helper()
		scope := c.NewScope(t.Context())
		t.Cleanup(func() { _ = scope.Close(context.Background()) })
		start := time.Now()
		err := resolve(scope)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("resolve: %v, want an error wrapping context.DeadlineExceeded", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("resolve took %v, want about %v", d, short)
		}
	}
	t.Run("own InitTimeout", func(t *testing.T) {
		t.Parallel()
		c := gas.NewServiceContainer()
		c.RegisterScopedService[*ownScopedTimeout](func() *ownScopedTimeout {
			return &ownScopedTimeout{blockingScoped{timeout: short}}
		})
		check(t, c, func(s *gas.Scope) error { _, err := s.Resolve[*ownScopedTimeout](); return err })
	})
	t.Run("container default", func(t *testing.T) {
		t.Parallel()
		c := gas.NewServiceContainer(gas.WithServiceContainerServiceTimeout(short))
		c.RegisterScopedService[*blockingScoped](func() *blockingScoped { return &blockingScoped{} })
		check(t, c, func(s *gas.Scope) error { _, err := s.Resolve[*blockingScoped](); return err })
	})
	t.Run("App WithServiceInitTimeout", func(t *testing.T) {
		t.Parallel()
		app := gas.NewApp(gas.WithServiceInitTimeout(short),
			gas.WithScopedService[*blockingScoped](func() *blockingScoped { return &blockingScoped{} }))
		app.Router().Get(nil, "/scoped", func(ctx gas.Context, _ *blockingScoped) error { return ctx.NoContent() })
		if err := app.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		t.Cleanup(func() { _ = app.Stop(context.Background()) })
		rec := httptest.NewRecorder()
		start := time.Now()
		app.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/scoped", nil))
		if rec.Code < 500 {
			t.Fatalf("status %d, want a server error from the failed resolution", rec.Code)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("request took %v, want about %v", d, short)
		}
	})
}

// Docs: core/lifecycle.mdx#init-and-close-contexts
// "**Close** gets the caller's shutdown context: the one passed to `Stop`,
// `Shutdown`, `CloseService`, or `Scope.Close`."
func TestCloseGetsShutdownContext(t *testing.T) {
	t.Parallel()
	newA := func(got *any) func() *svc[kA] {
		return func() *svc[kA] {
			return &svc[kA]{onClose: func(ctx context.Context) error { *got = ctx.Value(ctxKey{}); return nil }}
		}
	}
	val := context.WithValue(context.Background(), ctxKey{}, "caller")
	t.Run("Stop", func(t *testing.T) {
		t.Parallel()
		var got any
		app := gas.NewApp(gas.WithSingletonService[*svc[kA]](newA(&got)))
		if err := app.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		_ = app.Stop(val)
		if got != "caller" {
			t.Fatalf("Close saw %v, want the Stop context", got)
		}
	})
	t.Run("Shutdown", func(t *testing.T) {
		t.Parallel()
		var got any
		w := gas.NewWorker(gas.WithSingletonService[*svc[kA]](newA(&got)))
		if err := w.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		_ = w.Shutdown(val)
		if got != "caller" {
			t.Fatalf("Close saw %v, want the Shutdown context", got)
		}
	})
	t.Run("CloseService", func(t *testing.T) {
		t.Parallel()
		var got any
		w := gas.NewWorker(gas.WithSingletonService[*svc[kA]](newA(&got)))
		if err := w.Start(t.Context()); err != nil {
			t.Fatalf("start: %v", err)
		}
		t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
		if err := w.CloseService[*svc[kA]](val); err != nil {
			t.Fatalf("CloseService: %v", err)
		}
		if got != "caller" {
			t.Fatalf("Close saw %v, want the CloseService context", got)
		}
	})
	t.Run("Scope.Close", func(t *testing.T) {
		t.Parallel()
		var got any
		c := gas.NewServiceContainer()
		c.RegisterScopedService[*svc[kSc]](func() *svc[kSc] {
			return &svc[kSc]{onClose: func(ctx context.Context) error { got = ctx.Value(ctxKey{}); return nil }}
		})
		scope := c.NewScope(t.Context())
		scope.MustResolve[*svc[kSc]]()
		if err := scope.Close(val); err != nil {
			t.Fatalf("scope close: %v", err)
		}
		if got != "caller" {
			t.Fatalf("Close saw %v, want the Scope.Close context", got)
		}
	})
}

// Docs: core/lifecycle.mdx#init-and-close-contexts
// "In an App, a request's scoped
// services are closed with the request's context minus its cancellation, so they
// clean up even after the client has gone."
func TestRequestScopedCloseContext(t *testing.T) {
	t.Parallel()
	type seen struct {
		val any
		err error
	}
	closed := make(chan seen, 1)
	app := gas.NewApp(gas.WithScopedService[*svc[kSc]](func() *svc[kSc] {
		return &svc[kSc]{onClose: func(ctx context.Context) error { closed <- seen{ctx.Value(ctxKey{}), ctx.Err()}; return nil }}
	}))
	reqCtx, cancel := context.WithCancel(context.WithValue(t.Context(), ctxKey{}, "request"))
	app.Router().Get(nil, "/scoped", func(ctx gas.Context, _ *svc[kSc]) error {
		cancel() // the client goes away before the handler returns
		return ctx.NoContent()
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	app.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(reqCtx, http.MethodGet, "/scoped", nil))
	select {
	case s := <-closed:
		if s.val != "request" || s.err != nil {
			t.Fatalf("scoped Close saw value %v, Err()=%v; want the request's context without its cancellation", s.val, s.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the request's scoped service was not closed")
	}
}

// db is the page's DB: a singleton a ready hook resolves.
type db struct{ svc[kA] }

// Docs: core/lifecycle.mdx#ready-hooks
// "A ready hook is work that needs the built container but must finish before the
// app takes traffic"
// Docs: core/lifecycle.mdx#ready-hooks
// "Hooks run after migrations, so the schema exists, and before `Start` returns."
func TestReadyHookAfterMigrationsBeforeStartReturns(t *testing.T) {
	t.Parallel()
	d := &db{}
	var migrated atomic.Bool
	var sawMigrated, sawInitialized atomic.Bool
	var resolved *db
	var hookDone atomic.Bool
	app := gas.NewApp(
		gas.WithServiceInstance[gas.MigrationManager](&migratetest.MockMigrationManager{RunPendingFn: func() error {
			migrated.Store(true)
			return nil
		}}),
		gas.WithSingletonService[*db](func() *db { return d }),
		gas.WithReadyFunc(func(sc *gas.ServiceContainer) error {
			resolved = sc.MustResolve[*db]()
			sawMigrated.Store(migrated.Load())
			sawInitialized.Store(resolved.isInitialized())
			time.Sleep(50 * time.Millisecond)
			hookDone.Store(true)
			return nil
		}),
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if !hookDone.Load() {
		t.Fatal("Start returned before the ready hook finished")
	}
	if resolved != d || !sawInitialized.Load() || !sawMigrated.Load() {
		t.Fatalf("hook resolved the built DB=%v initialized=%v after migrations=%v", resolved == d, sawInitialized.Load(), sawMigrated.Load())
	}
}

// Docs: core/lifecycle.mdx#ready-hooks
// "They run in registration order, and the first error aborts startup; it is also
// logged as `ready hook failed`."
func TestReadyHookOrderAndFirstError(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	sink, logger := newSink()
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](logger),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error { rec.add("hook 1"); return nil }),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error { rec.add("hook 2"); return errBoom }),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error { rec.add("hook 3"); return nil }),
	)
	app.ReadyFunc(func(*gas.ServiceContainer) error { rec.add("hook 4"); return nil })
	if err := app.Start(t.Context()); !errors.Is(err, errBoom) {
		t.Fatalf("Start = %v, want the hook's error", err)
	}
	if got, want := rec.list(), []string{"hook 1", "hook 2"}; !slices.Equal(got, want) {
		t.Fatalf("hooks ran %v, want %v", got, want)
	}
	if indexOf(sink.records(t), "ready hook failed") < 0 {
		t.Fatal("no \"ready hook failed\" log record")
	}

	rec2 := &recorder{}
	ok := gas.NewWorker(gas.WithReadyFunc(func(*gas.ServiceContainer) error { rec2.add("option"); return nil }))
	ok.ReadyFunc(func(*gas.ServiceContainer) error { rec2.add("imperative"); return nil })
	if err := ok.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = ok.Shutdown(context.Background()) })
	if got, want := rec2.list(), []string{"option", "imperative"}; !slices.Equal(got, want) {
		t.Fatalf("hooks ran %v, want %v", got, want)
	}
}

// Docs: core/lifecycle.mdx#ready-hooks
// "`app.ReadyFunc(fn)` is the imperative form, and
// panics if it is called after the hooks have run, because the function would
// never run."
func TestReadyFuncAfterHooksPanics(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	mustPanic(t, "ReadyFunc after Start", func() { app.ReadyFunc(func(*gas.ServiceContainer) error { return nil }) })
}

// Docs: core/lifecycle.mdx#shutdown
// "1. Emit `gas.SystemServerShuttingDown` and wait for its subscribers (App only)."
// Docs: core/lifecycle.mdx#shutdown
// "3. Emit `gas.SystemShuttingDown` and wait for its subscribers."
// Docs: core/lifecycle.mdx#shutdown
// "4. Close every active service in reverse initialization order, passing `ctx` to
// each `Close`. Services already closed with
// [`CloseService`](/gas/core/ownership/) are skipped."
func TestStopOrder(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	app := gas.NewApp(
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{rec: rec} }),
		gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] { return &svc[kB]{rec: rec} }),
		gas.WithSingletonService[*svc[kC]](func(*svc[kB]) *svc[kC] { return &svc[kC]{rec: rec} }),
	)
	app.EventBus().Subscribe[gas.SystemServerShuttingDown](func(gas.SystemServerShuttingDownPayload) {
		time.Sleep(50 * time.Millisecond)
		rec.add("server-shutting-down")
	})
	app.EventBus().Subscribe[gas.SystemShuttingDown](func(gas.SystemShuttingDownPayload) {
		time.Sleep(50 * time.Millisecond)
		rec.add("shutting-down")
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := app.CloseService[*svc[kB]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	got := rec.only("server-shutting-down", "shutting-down", "close")
	want := []string{"close kB", "server-shutting-down", "shutting-down", "close kC", "close kA"}
	if !slices.Equal(got, want) {
		t.Fatalf("stop order = %v, want %v", got, want)
	}
}

// Docs: core/lifecycle.mdx#shutdown
// "Steps 3 and 4 are `Worker.Shutdown`, which is all a Worker does."
func TestWorkerShutdownIsSteps3And4(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	w := gas.NewWorker(gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return &svc[kA]{rec: rec} }))
	w.EventBus().Subscribe[gas.SystemServerShuttingDown](func(gas.SystemServerShuttingDownPayload) { rec.add("server-shutting-down") })
	w.EventBus().Subscribe[gas.SystemShuttingDown](func(gas.SystemShuttingDownPayload) { rec.add("shutting-down") })
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if got, want := rec.only("server", "shutting", "close"), []string{"shutting-down", "close kA"}; !slices.Equal(got, want) {
		t.Fatalf("Worker.Shutdown = %v, want %v", got, want)
	}
}

// Docs: core/lifecycle.mdx#shutdown
// "A failing
// `Close` does not stop the others; every `Close` error is returned, joined, each
// prefixed with `gas: close <name>:`."
func TestCloseErrorsJoinedAndPrefixed(t *testing.T) {
	t.Parallel()
	errA, errC := errors.New("a failed"), errors.New("c failed")
	b := &svc[kB]{}
	w := gas.NewWorker(
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
			return &svc[kA]{onClose: func(context.Context) error { return errA }}
		}),
		gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] { return b }),
		gas.WithSingletonService[*svc[kC]](func(*svc[kB]) *svc[kC] {
			return &svc[kC]{onClose: func(context.Context) error { return errC }}
		}),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	err := w.Shutdown(context.Background())
	if !errors.Is(err, errA) || !errors.Is(err, errC) {
		t.Fatalf("Shutdown = %v, want both Close errors", err)
	}
	if b.closes() != 1 {
		t.Fatal("a failing Close stopped a later one")
	}
	for _, want := range []string{"gas: close kA: a failed", "gas: close kC: c failed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Shutdown error %q lacks %q", err, want)
		}
	}
}

// Docs: core/lifecycle.mdx#shutdown
// "`Shutdown` runs once. Later calls, including concurrent ones, wait for the first
// to finish and return nil. Once it has begun, `CloseService` and
// `RestartService` return an error."
func TestShutdownRunsOnce(t *testing.T) {
	t.Parallel()
	closing := make(chan struct{})
	var finished atomic.Bool
	a := &svc[kA]{onClose: func(context.Context) error {
		close(closing)
		time.Sleep(200 * time.Millisecond)
		finished.Store(true)
		return errBoom
	}}
	w := gas.NewWorker(
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return a }),
		gas.WithSingletonService[*svc[kB]](func() *svc[kB] { return &svc[kB]{} }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	first := make(chan error, 1)
	go func() { first <- w.Shutdown(context.Background()) }()
	<-closing
	if err := w.CloseService[*svc[kB]](t.Context()); err == nil {
		t.Error("CloseService after Shutdown began returned nil")
	}
	if err := w.RestartService[*svc[kB]](t.Context()); err == nil {
		t.Error("RestartService after Shutdown began returned nil")
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Errorf("concurrent Shutdown = %v, want nil", err)
	}
	if !finished.Load() {
		t.Error("a concurrent Shutdown returned before the first finished")
	}
	if err := <-first; !errors.Is(err, errBoom) {
		t.Errorf("first Shutdown = %v, want the Close error", err)
	}
	if err := w.Shutdown(context.Background()); err != nil || a.closes() != 1 {
		t.Errorf("later Shutdown = %v with %d closes, want nil and one Close", err, a.closes())
	}
}

// Docs: core/lifecycle.mdx#health-and-readiness
// "type HealthReporter interface { CheckHealth(ctx context.Context) error // nil means healthy }"
// Docs: core/lifecycle.mdx#health-and-readiness
// "type ReadyReporter interface { CheckReady(ctx context.Context) error // nil means ready }"
func TestReporterInterfaces(t *testing.T) {
	t.Parallel()
	for typ, want := range map[reflect.Type]string{
		reflect.TypeFor[gas.HealthReporter](): "CheckHealth",
		reflect.TypeFor[gas.ReadyReporter]():  "CheckReady",
	} {
		m, ok := typ.MethodByName(want)
		if typ.NumMethod() != 1 || !ok || m.Type != reflect.TypeFor[func(context.Context) error]() {
			t.Errorf("%v = %v, want only %s(context.Context) error", typ, m.Type, want)
		}
	}
}

type meeter[K any] struct {
	svc[K]
	arrived chan struct{}
	other   chan struct{}
}

func (r *meeter[K]) meet() error {
	r.arrived <- struct{}{}
	select {
	case <-r.other:
		return nil
	case <-time.After(2 * time.Second):
		return errors.New("the other reporter was never called at the same time")
	}
}

func (r *meeter[K]) CheckHealth(context.Context) error { return r.meet() }
func (r *meeter[K]) CheckReady(context.Context) error  { return r.meet() }

// Docs: core/lifecycle.mdx#health-and-readiness
// "`Worker.CheckHealth(ctx)` and `Worker.CheckReady(ctx)` call every active service
// that implements the interface, concurrently, and return a map from service name
// to result. Services that do not implement it are left out"
func TestChecksAreConcurrentAndKeyedByName(t *testing.T) {
	t.Parallel()
	ab, ba := make(chan struct{}, 1), make(chan struct{}, 1)
	w := gas.NewWorker(
		gas.WithServiceInstance(&meeter[kA]{arrived: ab, other: ba}),
		gas.WithServiceInstance(&meeter[kB]{arrived: ba, other: ab}),
		gas.WithSingletonService[*svc[kC]](func() *svc[kC] { return &svc[kC]{} }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	for name, check := range map[string]func(context.Context) map[string]error{
		"CheckHealth": w.CheckHealth,
		"CheckReady":  w.CheckReady,
	} {
		got := check(t.Context())
		if len(got) != 2 {
			t.Fatalf("%s = %v, want exactly the two reporters", name, got)
		}
		for _, key := range []string{"kA", "kB"} {
			if err, ok := got[key]; !ok || err != nil {
				t.Fatalf("%s[%s] = %v (present=%v), want nil from a concurrent call", name, key, err, ok)
			}
		}
	}
}

type healthSvc struct{ svc[kA] }

func (*healthSvc) CheckHealth(context.Context) error { return nil }
func (*healthSvc) CheckReady(context.Context) error  { return nil }

type panicky struct{ svc[kB] }

func (*panicky) CheckHealth(context.Context) error { panic("health exploded") }
func (*panicky) CheckReady(context.Context) error  { panic("ready exploded") }

// Docs: core/lifecycle.mdx#health-and-readiness
// "and so are services
// closed with `CloseService`. A reporter that panics is recovered and reported as
// an error."
func TestChecksSkipClosedAndRecoverPanics(t *testing.T) {
	t.Parallel()
	w := gas.NewWorker(
		gas.WithServiceInstance(&healthSvc{}),
		gas.WithServiceInstance(&panicky{}),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	for name, check := range map[string]func(context.Context) map[string]error{
		"CheckHealth": w.CheckHealth,
		"CheckReady":  w.CheckReady,
	} {
		if err := check(t.Context())["kB"]; err == nil {
			t.Errorf("%s[kB] for a panicking reporter = nil, want an error", name)
		}
	}
	if err := w.CloseService[*healthSvc](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if _, ok := w.CheckHealth(t.Context())["kA"]; ok {
		t.Error("CheckHealth still calls a service closed with CloseService")
	}
	if _, ok := w.CheckReady(t.Context())["kA"]; ok {
		t.Error("CheckReady still calls a service closed with CloseService")
	}
}

// Docs: core/lifecycle.mdx#health-and-readiness
// "The Worker registers itself in the container as `gas.HealthProvider` and
// `gas.ReadyProvider`, the interfaces those two methods satisfy, so a service can
// inject them. Gas adds no probe endpoints of its own"
func TestWorkerRegistersItselfAsProviders(t *testing.T) {
	t.Parallel()
	var gotH gas.HealthProvider
	var gotR gas.ReadyProvider
	app := gas.NewApp(
		gas.WithSingletonService[*healthSvc](func() *healthSvc { return &healthSvc{} }),
		gas.WithSingletonService[*svc[kC]](func(h gas.HealthProvider, r gas.ReadyProvider) *svc[kC] {
			gotH, gotR = h, r
			return &svc[kC]{}
		}),
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if gotH != gas.HealthProvider(app.Worker) || gotR != gas.ReadyProvider(app.Worker) {
		t.Fatalf("injected %T and %T, want the App's Worker", gotH, gotR)
	}
	if _, ok := gotH.CheckHealth(t.Context())["kA"]; !ok {
		t.Error("the injected HealthProvider does not report kA")
	}
	for _, p := range []string{"/health", "/healthz", "/livez", "/ready", "/readyz"} {
		if code := get(app.Handler(), p).Code; code != http.StatusNotFound {
			t.Errorf("GET %s on an app with no routes = %d, want 404", p, code)
		}
	}
}
