package coreworkers_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/configtest"
)

type healthy struct{ svc[kA] }

func (*healthy) CheckHealth(context.Context) error { return nil }
func (*healthy) CheckReady(context.Context) error  { return nil }

// Docs: core/workers.mdx#_top
// "It has the same DI container,
// service lifecycle, event bus, migrations, health and readiness checks, and kill
// switch as an App, and no router or server."
func TestWorkerHasAppCoreWithoutHTTP(t *testing.T) {
	t.Parallel()
	rec := &list{}
	h := &healthy{svc[kA]{name: "healthy"}}
	w := gas.NewWorker(
		gas.WithSingletonService[*healthy](func() *healthy { return h }),
		gas.WithSingletonService[gas.MigrationManager](func() gas.MigrationManager { return &migrations{rec: rec} }),
	)
	var closedEvent atomic.Bool
	w.EventBus().Subscribe[gas.SystemServiceClosed](func(gas.SystemServiceClosedPayload) { closedEvent.Store(true) })
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if !slices.Contains(rec.get(), "migrations") {
		t.Error("Start did not run pending migrations")
	}
	if _, ok := w.CheckHealth(t.Context())["healthy"]; !ok {
		t.Error("CheckHealth does not report the service")
	}
	if _, ok := w.CheckReady(t.Context())["healthy"]; !ok {
		t.Error("CheckReady does not report the service")
	}
	if err := w.CloseService[*healthy](t.Context()); err != nil || !closedEvent.Load() {
		t.Errorf("kill switch: CloseService = %v, event emitted = %v", err, closedEvent.Load())
	}
	if w.ServiceContainer().CanResolve[*gas.Router]() {
		t.Error("a Worker's container has a *gas.Router")
	}
}

// Docs: core/workers.mdx#_top
// "`gas.App` embeds a Worker and adds HTTP on top, so every registration option
// works with both"
func TestRegistrationOptionsWorkWithBoth(t *testing.T) {
	t.Parallel()
	type value struct{ n int }
	type scoped struct{}
	type transient struct{}
	opts := func(ready *atomic.Bool) []gas.Option {
		return []gas.Option{
			gas.WithServiceInstance(&value{n: 7}),
			gas.WithSingletonService[*healthy](func() *healthy { return &healthy{svc[kA]{name: "h"}} }),
			gas.WithScopedService[*scoped](func() *scoped { return &scoped{} }),
			gas.WithTransientService[*transient](func() *transient { return &transient{} }),
			gas.WithService[*svc[kB]](func() *svc[kB] { return &svc[kB]{name: "b"} }, gas.ServiceLifetimeSingleton),
			gas.WithReadyFunc(func(*gas.ServiceContainer) error { ready.Store(true); return nil }),
			gas.WithServiceInitTimeout(time.Minute),
		}
	}
	check := func(name string, c *gas.ServiceContainer, start func() error, ready *atomic.Bool) {
		if err := start(); err != nil {
			t.Fatalf("%s start: %v", name, err)
		}
		if c.MustResolve[*value]().n != 7 || c.MustResolve[*healthy]() == nil || c.MustResolve[*svc[kB]]() == nil ||
			c.MustResolve[*transient]() == nil || c.NewScope(t.Context()).MustResolve[*scoped]() == nil || !ready.Load() {
			t.Errorf("%s: not every registration option took effect", name)
		}
	}
	var wr, ar atomic.Bool
	w := gas.NewWorker(opts(&wr)...)
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	check("Worker", w.ServiceContainer(), func() error { return w.Start(t.Context()) }, &wr)
	app := gas.NewApp(opts(&ar)...)
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	check("App", app.ServiceContainer(), func() error { return app.Start(t.Context()) }, &ar)
}

type wantsRouter struct{}

// Docs: core/workers.mdx#what-a-worker-does-not-have
// "`*gas.Router` in the container | Not registered. A constructor that asks for it fails startup with `gas.ErrNotRegistered`."
func TestRouterNotRegistered(t *testing.T) {
	t.Parallel()
	w := gas.NewWorker(gas.WithSingletonService[*wantsRouter](func(*gas.Router) *wantsRouter { return &wantsRouter{} }))
	if err := w.Start(t.Context()); !errors.Is(err, gas.ErrNotRegistered) {
		t.Fatalf("Start with a constructor taking *gas.Router = %v, want ErrNotRegistered", err)
	}
}

// Docs: core/workers.mdx#what-a-worker-does-not-have
// "HTTP server, `Serve`, `Handler`, CSRF protection | None"
func TestWorkerHasNoHTTPMethods(t *testing.T) {
	t.Parallel()
	wt := reflect.TypeFor[*gas.Worker]()
	for _, m := range []string{"Serve", "Handler", "Server", "Router", "Stop"} {
		if _, ok := wt.MethodByName(m); ok {
			t.Errorf("*gas.Worker has method %s", m)
		}
	}
	if _, ok := reflect.TypeFor[*gas.App]().MethodByName("Serve"); !ok {
		t.Error("*gas.App has no Serve; the check above proves nothing")
	}
}

type perJob struct{ svc[kB] }

// Docs: core/workers.mdx#what-a-worker-does-not-have
// "A request scope per HTTP request | None; make scopes yourself"
// "Nothing in a Worker opens scopes automatically. Code that uses scoped services,
// such as a job handler that wants one instance per job, creates a scope with
// `ServiceContainer().NewScope(ctx)` and closes it when the job is done"
func TestWorkerScopes(t *testing.T) {
	t.Parallel()
	var built atomic.Int32
	w := gas.NewWorker(gas.WithScopedService[*perJob](func() *perJob {
		built.Add(1)
		return &perJob{svc[kB]{name: "job"}}
	}))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if built.Load() != 0 {
		t.Fatalf("Start built %d scoped instances, want 0", built.Load())
	}
	scope := w.ServiceContainer().NewScope(t.Context())
	job := scope.MustResolve[*perJob]()
	if err := scope.Close(t.Context()); err != nil || job.closes.Load() != 1 {
		t.Fatalf("Scope.Close = %v, job closes = %d; want nil and 1", err, job.closes.Load())
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if built.Load() != 1 || job.closes.Load() != 1 {
		t.Fatalf("built=%d closes=%d after shutdown, want 1 and 1: nothing opens scopes on its own", built.Load(), job.closes.Load())
	}
}

// Docs: core/workers.mdx#what-a-worker-does-not-have
// "Binding the App's `gas.Config` and emitting `gas.AppConfigUpdated` | Not done" and
// "`gas.SystemServerShuttingDown` | Not emitted"
func TestWorkerSkipsAppEvents(t *testing.T) {
	t.Parallel()
	cfg, err := configtest.NewMockConfigWithValues(map[string]any{"server": map[string]any{"port": 4713}})
	if err != nil {
		t.Fatal(err)
	}
	w := gas.NewWorker(gas.WithServiceInstance[gas.ConfigProvider](cfg))
	var updated, serverDown atomic.Bool
	w.EventBus().Subscribe[gas.AppConfigUpdated](func(gas.AppConfigUpdatedPayload) { updated.Store(true) })
	w.EventBus().Subscribe[gas.SystemServerShuttingDown](func(gas.SystemServerShuttingDownPayload) { serverDown.Store(true) })
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if updated.Load() || serverDown.Load() {
		t.Fatalf("AppConfigUpdated=%v SystemServerShuttingDown=%v, want neither", updated.Load(), serverDown.Load())
	}
}

// Docs: core/workers.mdx#what-a-worker-does-not-have
// "`gas.NewWorker` registers the `*gas.EventBus`, and the Worker itself as
// `gas.HealthProvider` and `gas.ReadyProvider`, in its container."
func TestNewWorkerRegistrations(t *testing.T) {
	t.Parallel()
	w := gas.NewWorker()
	c := w.ServiceContainer()
	if c.MustResolve[*gas.EventBus]() != w.EventBus() {
		t.Error("container *gas.EventBus is not the Worker's bus")
	}
	if hp, ok := c.MustResolve[gas.HealthProvider]().(*gas.Worker); !ok || hp != w {
		t.Error("gas.HealthProvider is not the Worker itself")
	}
	if rp, ok := c.MustResolve[gas.ReadyProvider]().(*gas.Worker); !ok || rp != w {
		t.Error("gas.ReadyProvider is not the Worker itself")
	}
}

// Docs: core/workers.mdx#what-a-worker-does-not-have
// "It accepts the
// same `gas.Option` values as `gas.NewApp`, but only the Worker options: passing
// an HTTP option such as `gas.WithErrorHandler` or `gas.WithTrustedOrigin`
// panics."
func TestHTTPOptionsPanicOnWorker(t *testing.T) {
	t.Parallel()
	for name, opt := range map[string]gas.Option{
		"WithErrorHandler":  gas.WithErrorHandler(func(gas.Context, error) {}),
		"WithTrustedOrigin": gas.WithTrustedOrigin("https://app.example.com"),
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("NewWorker(%s) did not panic", name)
				}
			}()
			gas.NewWorker(opt)
		}()
		gas.NewApp(opt) // the same value is accepted by NewApp
	}
}

// Docs: core/workers.mdx#run-or-start-and-shutdown
// "`w.Start(ctx)` builds and initializes every service, runs pending migrations,
// and runs the ready hooks, then returns. It does not block."
// "Services are initialized in the first step of startup, before migrations run
// and before ready hooks." and
// "start it from a ready hook
// (`gas.WithReadyFunc`) instead, which runs after them."
func TestStartOrder(t *testing.T) {
	t.Parallel()
	rec := &list{}
	w := gas.NewWorker(
		instance(&svc[kA]{name: "consumer", rec: rec}),
		gas.WithSingletonService[gas.MigrationManager](func() gas.MigrationManager { return &migrations{rec: rec} }),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error { rec.add("ready"); return nil }),
	)
	done := make(chan error, 1)
	go func() { done <- w.Start(t.Context()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("start: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start blocked")
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if got, want := rec.get(), []string{"init consumer", "migrations", "ready"}; !slices.Equal(got, want) {
		t.Fatalf("startup steps = %v, want %v", got, want)
	}
}

// Docs: core/workers.mdx#run-or-start-and-shutdown
// "`w.Shutdown(ctx)` emits `gas.SystemShuttingDown` and closes every service in
// reverse initialization order. It runs once; later calls return nil."
func TestShutdown(t *testing.T) {
	t.Parallel()
	rec := &list{}
	first := &svc[kA]{name: "first", rec: rec, onClose: func(context.Context) error { return errors.New("close failed") }}
	second := &svc[kB]{name: "second", rec: rec}
	w := gas.NewWorker(
		instance(first),
		gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] { return second }),
	)
	w.EventBus().Subscribe[gas.SystemShuttingDown](func(gas.SystemShuttingDownPayload) { rec.add("shutting down") })
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Shutdown(context.Background()); err == nil {
		t.Fatal("first Shutdown hid the Close error")
	}
	want := []string{"init first", "init second", "shutting down", "close second", "close first"}
	if got := rec.get(); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown = %v, want nil", err)
	}
	if first.closes.Load() != 1 || second.closes.Load() != 1 {
		t.Fatalf("closes first=%d second=%d after two Shutdowns, want 1 and 1", first.closes.Load(), second.closes.Load())
	}
}

// Docs: core/workers.mdx#run-or-start-and-shutdown
// "A failed `Start` closes what it had initialized and cannot be retried on the
// same Worker"
func TestFailedStart(t *testing.T) {
	t.Parallel()
	errReady := errors.New("ready failed 2c")
	s := &svc[kA]{name: "early"}
	w := gas.NewWorker(instance(s), gas.WithReadyFunc(func(*gas.ServiceContainer) error { return errReady }))
	err := w.Start(t.Context())
	if !errors.Is(err, errReady) {
		t.Fatalf("Start = %v, want the ready hook's error", err)
	}
	if s.closes.Load() != 1 {
		t.Fatalf("initialized service closed %d times after the failed Start, want 1", s.closes.Load())
	}
	if again := w.Start(t.Context()); !errors.Is(again, errReady) || s.inits.Load() != 1 {
		t.Fatalf("retry Start = %v, inits = %d; want the same error and no new Init", again, s.inits.Load())
	}
}

// Docs: core/workers.mdx#a-long-running-consumer
// "The loop must not use the context passed to `Init`. That context carries the
// service's init deadline and is canceled as soon as `Init` returns"
func TestInitContextDeadlineAndCancel(t *testing.T) {
	t.Parallel()
	var initCtx context.Context
	var hadDeadline bool
	s := &svc[kA]{name: "loop", onInit: func(ctx context.Context) error {
		initCtx = ctx
		_, hadDeadline = ctx.Deadline()
		return nil
	}}
	w := gas.NewWorker(instance(s))
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if !hadDeadline {
		t.Error("Init context has no deadline under the default init timeout")
	}
	if initCtx.Err() == nil {
		t.Error("Init context is still live after Init returned")
	}
}

type closeKey struct{}

// Docs: core/workers.mdx#a-long-running-consumer
// "`Close` gets the shutdown context"
// "pass a context with a deadline to `Shutdown` if you need shutdown to be
// bounded."
func TestCloseGetsShutdownContext(t *testing.T) {
	t.Parallel()
	var gotValue any
	var gotDeadline bool
	s := &svc[kA]{name: "loop", onClose: func(ctx context.Context) error {
		gotValue = ctx.Value(closeKey{})
		_, gotDeadline = ctx.Deadline()
		return nil
	}}
	w := gas.NewWorker(instance(s))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), closeKey{}, "shutdown"), time.Minute)
	defer cancel()
	if err := w.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if gotValue != "shutdown" || !gotDeadline {
		t.Fatalf("Close ctx value=%v deadline=%v, want the Shutdown context", gotValue, gotDeadline)
	}
}

type handler struct{ id int32 }

// Docs: core/workers.mdx#on-aws-lambda
// "Every singleton is built by now, so this only looks it up."
func TestResolveAfterStartOnlyLooksUp(t *testing.T) {
	t.Parallel()
	var built atomic.Int32
	w := gas.NewWorker(gas.WithSingletonService[*handler](func() *handler { return &handler{id: built.Add(1)} }))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if built.Load() != 1 {
		t.Fatalf("Start built the handler %d times, want 1", built.Load())
	}
	h1, h2 := w.ServiceContainer().MustResolve[*handler](), w.ServiceContainer().MustResolve[*handler]()
	if h1 != h2 || built.Load() != 1 {
		t.Fatalf("resolving after Start built %d handlers in total, want 1", built.Load())
	}
}
