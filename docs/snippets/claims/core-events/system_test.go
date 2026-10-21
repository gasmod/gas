package coreevents_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/docs/snippets/claims/internal/testport"
)

// Docs: core/events.mdx#system-events
// "the payload types are named after the event with a `Payload` suffix."
// "`gas.SystemAllServicesInitialized` | none" and
// "`gas.SystemServiceClosed` | `ServiceName`" and
// "`gas.SystemServiceInitialized` | `ServiceName`" and
// "`gas.SystemServerShuttingDown` | none" and
// "`gas.SystemShuttingDown` | none" and
// "`gas.AppConfigUpdated` | `Config`, a copy of the bound `gas.Config`"
func TestSystemEventPayloadFields(t *testing.T) {
	t.Parallel()
	fields := func(v any) string {
		rt := reflect.TypeOf(v)
		var out []string
		for f := range rt.Fields() {
			out = append(out, f.Name+" "+f.Type.String())
		}
		return fmt.Sprint(out)
	}
	cases := []struct {
		got  string
		want string
	}{
		{fields(gas.SystemAllServicesInitializedPayload{}), "[]"},
		{fields(gas.SystemServiceClosedPayload{}), "[ServiceName string]"},
		{fields(gas.SystemServiceInitializedPayload{}), "[ServiceName string]"},
		{fields(gas.SystemServerShuttingDownPayload{}), "[]"},
		{fields(gas.SystemShuttingDownPayload{}), "[]"},
		{fields(gas.AppConfigUpdatedPayload{}), "[Config gas.Config]"},
	}
	for i, c := range cases {
		if c.got != c.want {
			t.Errorf("payload %d fields = %s, want %s", i, c.got, c.want)
		}
	}
	// Subscribing through the event type binds each to its Payload type.
	bus := gas.NewEventBus()
	bus.Subscribe[gas.SystemAllServicesInitialized](func(gas.SystemAllServicesInitializedPayload) {})
	bus.Subscribe[gas.SystemServiceClosed](func(gas.SystemServiceClosedPayload) {})
	bus.Subscribe[gas.SystemServiceInitialized](func(gas.SystemServiceInitializedPayload) {})
	bus.Subscribe[gas.SystemServerShuttingDown](func(gas.SystemServerShuttingDownPayload) {})
	bus.Subscribe[gas.SystemShuttingDown](func(gas.SystemShuttingDownPayload) {})
	bus.Subscribe[gas.AppConfigUpdated](func(gas.AppConfigUpdatedPayload) {})
}

// migrations stands in for the migration manager and records when it runs.
type migrations struct {
	gas.MigrationManager
	ran atomic.Bool
	// onRun runs at the start of RunPending.
	onRun func()
}

func (*migrations) Name() string                { return "migrations" }
func (*migrations) Init(context.Context) error  { return nil }
func (*migrations) Close(context.Context) error { return nil }
func (m *migrations) RunPending() error {
	if m.onRun != nil {
		m.onRun()
	}
	m.ran.Store(true)
	return nil
}

type missingDep struct{}

// Docs: core/events.mdx#system-events
// "`gas.SystemAllServicesInitialized` | none | During `Start`, once every singleton is built and initialized (and, in an App, the router is sealed and its handlers checked), before migrations run"
// "Gas waits for every subscriber of a system event to return before it moves on,
// so a slow handler delays the step that emitted it."
func TestAllServicesInitialized(t *testing.T) {
	t.Parallel()
	inited := &svc[kA]{name: "early"}
	var initRan atomic.Bool
	inited.onInit = func(context.Context, *svc[kA]) error { initRan.Store(true); return nil }
	mig := &migrations{}
	app := gas.NewApp(
		withSvc(inited),
		gas.WithSingletonService[gas.MigrationManager](func() gas.MigrationManager { return mig }),
	)
	app.Router().Get(nil, "/sealed", func(w http.ResponseWriter, _ *http.Request) {})
	var fired atomic.Int32
	var initSeen, migratedSeen, slowDone atomic.Bool
	var status atomic.Int32
	app.EventBus().Subscribe[gas.SystemAllServicesInitialized](func(gas.SystemAllServicesInitializedPayload) {
		fired.Add(1)
		initSeen.Store(initRan.Load())
		migratedSeen.Store(mig.ran.Load())
		rr := httptest.NewRecorder()
		app.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/sealed", nil))
		status.Store(int32(rr.Code))
		time.Sleep(50 * time.Millisecond)
		slowDone.Store(true)
	})
	var slowBeforeMigrations atomic.Bool
	mig.onRun = func() { slowBeforeMigrations.Store(slowDone.Load()) }
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	if fired.Load() != 1 {
		t.Fatalf("event fired %d times during Start, want 1", fired.Load())
	}
	if !initSeen.Load() {
		t.Error("event fired before the singleton was initialized")
	}
	if status.Load() != http.StatusOK {
		t.Errorf("route served %d during the event, want 200 from a sealed router", status.Load())
	}
	if migratedSeen.Load() || !mig.ran.Load() {
		t.Errorf("migrations ran before the event=%v, ran at all=%v; want after", migratedSeen.Load(), mig.ran.Load())
	}
	if !slowBeforeMigrations.Load() {
		t.Error("Start moved on before the slow subscriber returned")
	}

	bad := gas.NewApp()
	bad.Router().Get(nil, "/x", func(ctx gas.Context, _ *missingDep) error { return ctx.NoContent() })
	var badFired atomic.Bool
	bad.EventBus().Subscribe[gas.SystemAllServicesInitialized](func(gas.SystemAllServicesInitializedPayload) { badFired.Store(true) })
	if err := bad.Start(t.Context()); err == nil {
		t.Fatal("Start accepted a handler with an unregistered dependency")
	}
	if badFired.Load() {
		t.Error("event fired although the handler check failed")
	}
}

// Docs: core/events.mdx#system-events
// "`gas.SystemServiceClosed` | `ServiceName` | By `CloseService`, after the service is closed"
// "Gas waits for every subscriber of a system event to return before it moves on"
func TestServiceClosedEvent(t *testing.T) {
	t.Parallel()
	s := &svc[kA]{name: "victim"}
	w := gas.NewWorker(withSvc(s))
	var name atomic.Value
	var closedAtEvent, done atomic.Bool
	w.EventBus().Subscribe[gas.SystemServiceClosed](func(p gas.SystemServiceClosedPayload) {
		closedAtEvent.Store(s.closes.Load() == 1)
		time.Sleep(50 * time.Millisecond)
		name.Store(p.ServiceName)
		done.Store(true)
	})
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if err := w.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if !done.Load() {
		t.Fatal("CloseService returned before the subscriber did")
	}
	if !closedAtEvent.Load() || name.Load() != "victim" {
		t.Fatalf("closed at event=%v name=%v, want true and victim", closedAtEvent.Load(), name.Load())
	}
}

// Docs: core/events.mdx#system-events
// "`gas.SystemServiceInitialized` | `ServiceName` | By `RestartService`, after `Init` succeeds; not emitted at startup"
func TestServiceInitializedEvent(t *testing.T) {
	t.Parallel()
	var failInit, initDone atomic.Bool
	s := &svc[kA]{name: "phoenix"}
	s.onInit = func(context.Context, *svc[kA]) error {
		if failInit.Load() {
			return errors.New("init failed")
		}
		initDone.Store(true)
		return nil
	}
	w := gas.NewWorker(withSvc(s))
	got := &list{}
	var initBeforeEvent atomic.Bool
	w.EventBus().Subscribe[gas.SystemServiceInitialized](func(p gas.SystemServiceInitializedPayload) {
		initBeforeEvent.Store(initDone.Load())
		time.Sleep(50 * time.Millisecond)
		got.add(p.ServiceName)
	})
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if n := len(got.get()); n != 0 {
		t.Fatalf("event fired %d times at startup, want 0", n)
	}
	if err := w.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	failInit.Store(true)
	if err := w.RestartService[*svc[kA]](t.Context()); err == nil {
		t.Fatal("RestartService with a failing Init returned nil")
	}
	if n := len(got.get()); n != 0 {
		t.Fatalf("event fired %d times after a failed Init, want 0", n)
	}
	failInit.Store(false)
	initDone.Store(false)
	if err := w.RestartService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("RestartService: %v", err)
	}
	if !slices.Equal(got.get(), []string{"phoenix"}) || !initBeforeEvent.Load() {
		t.Fatalf("events %v, Init done before event=%v; want [phoenix] after Init, waited for", got.get(), initBeforeEvent.Load())
	}
}

// Docs: core/events.mdx#system-events
// "`gas.SystemServerShuttingDown` | none | By `App.Stop`, before the HTTP server shuts down; App only"
func TestServerShuttingDownEvent(t *testing.T) {
	t.Parallel()
	port := freePort(t)
	app := gas.NewApp(gas.WithServiceInstance(mustConfig(t, map[string]any{
		"server": map[string]any{"host": "127.0.0.1", "port": port},
	})))
	app.Router().Get(nil, "/up", func(w http.ResponseWriter, _ *http.Request) {})
	url := fmt.Sprintf("http://127.0.0.1:%d/up", port)
	var fired atomic.Int32
	var servedDuring atomic.Bool
	app.EventBus().Subscribe[gas.SystemServerShuttingDown](func(gas.SystemServerShuttingDownPayload) {
		fired.Add(1)
		time.Sleep(50 * time.Millisecond) // the server must still be up after a slow subscriber
		if resp, err := http.Get(url); err == nil {
			_ = resp.Body.Close()
			servedDuring.Store(resp.StatusCode == http.StatusOK)
		}
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- app.Serve() }()
	if err := testport.WaitServing(10*time.Second, func() bool {
		resp, err := http.Get(url)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return true
	}, served); err != nil {
		t.Fatal(err)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := <-served; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if fired.Load() != 1 || !servedDuring.Load() {
		t.Fatalf("fired=%d, server answered during the event=%v; want 1 and true", fired.Load(), servedDuring.Load())
	}

	w := gas.NewWorker()
	var workerFired atomic.Bool
	w.EventBus().Subscribe[gas.SystemServerShuttingDown](func(gas.SystemServerShuttingDownPayload) { workerFired.Store(true) })
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("worker shutdown: %v", err)
	}
	if workerFired.Load() {
		t.Error("a Worker emitted SystemServerShuttingDown")
	}
}

// Docs: core/events.mdx#system-events
// "`gas.SystemShuttingDown` | none | When shutdown begins, before any service is closed; for both App and Worker"
// "Gas waits for every subscriber of a system event to return before it moves on"
func TestShuttingDownEvent(t *testing.T) {
	t.Parallel()
	check := func(t *testing.T, bus *gas.EventBus, s *svc[kA], stop func() error) {
		t.Helper()
		var fired, closedBefore, slowDone, closeSawDone atomic.Bool
		s.onClose = func(context.Context, *svc[kA]) error { closeSawDone.Store(slowDone.Load()); return nil }
		bus.Subscribe[gas.SystemShuttingDown](func(gas.SystemShuttingDownPayload) {
			fired.Store(true)
			closedBefore.Store(s.closes.Load() != 0)
			time.Sleep(50 * time.Millisecond)
			slowDone.Store(true)
		})
		if err := stop(); err != nil {
			t.Fatalf("stop: %v", err)
		}
		if !fired.Load() || closedBefore.Load() {
			t.Fatalf("fired=%v, a service closed before it=%v; want true, false", fired.Load(), closedBefore.Load())
		}
		if !closeSawDone.Load() {
			t.Fatal("services were closed before the slow subscriber returned")
		}
	}

	sw := &svc[kA]{name: "worker-svc"}
	w := gas.NewWorker(withSvc(sw))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	check(t, w.EventBus(), sw, func() error { return w.Shutdown(context.Background()) })

	sa := &svc[kA]{name: "app-svc"}
	app := gas.NewApp(withSvc(sa))
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("app start: %v", err)
	}
	check(t, app.EventBus(), sa, func() error { return app.Stop(context.Background()) })
}

// Docs: core/events.mdx#system-events
// "`gas.AppConfigUpdated` | `Config`, a copy of the bound `gas.Config` | During `App.Start`, after the config is bound; App only"
func TestAppConfigUpdatedEvent(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(gas.WithServiceInstance(mustConfig(t, map[string]any{
		"server": map[string]any{"port": 4711},
	})))
	var port atomic.Int32
	var fired atomic.Int32
	var done atomic.Bool
	app.EventBus().Subscribe[gas.AppConfigUpdated](func(p gas.AppConfigUpdatedPayload) {
		fired.Add(1)
		port.Store(int32(p.Config.Server.Port))
		p.Config.Server.Port = 1 // a copy: this must not reach the App's config
		time.Sleep(50 * time.Millisecond)
		done.Store(true)
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	if fired.Load() != 1 || port.Load() != 4711 {
		t.Fatalf("fired=%d, payload port=%d; want 1 and the bound 4711", fired.Load(), port.Load())
	}
	if !done.Load() {
		t.Error("Start returned before the subscriber did")
	}
	if got := app.Config().Server.Port; got != 4711 {
		t.Errorf("App config port = %d after a subscriber changed its payload, want 4711", got)
	}

	w := gas.NewWorker(gas.WithServiceInstance(mustConfig(t, map[string]any{"server": map[string]any{"port": 4712}})))
	var workerFired atomic.Bool
	w.EventBus().Subscribe[gas.AppConfigUpdated](func(gas.AppConfigUpdatedPayload) { workerFired.Store(true) })
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	_ = w.Shutdown(context.Background())
	if workerFired.Load() {
		t.Error("a Worker emitted AppConfigUpdated")
	}
}

// Docs: core/events.mdx#system-events
// "`SystemAllServicesInitialized` is emitted once, during `Start`. A service that
// subscribes in its `Init` receives it; a subscription made after `Start` has
// returned does not."
func TestSubscribeInInitSeesStartupEvent(t *testing.T) {
	t.Parallel()
	var inInit, after atomic.Int32
	s := &svc[kA]{name: "listener"}
	s.onInit = func(_ context.Context, s *svc[kA]) error {
		s.bus.SubscribeWithOwner[gas.SystemAllServicesInitialized](s, func(gas.SystemAllServicesInitializedPayload) { inInit.Add(1) })
		return nil
	}
	w := gas.NewWorker(withSvc(s))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	w.EventBus().Subscribe[gas.SystemAllServicesInitialized](func(gas.SystemAllServicesInitializedPayload) { after.Add(1) })
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if inInit.Load() != 1 || after.Load() != 0 {
		t.Fatalf("Init subscriber got %d, late subscriber got %d; want 1 and 0", inInit.Load(), after.Load())
	}
}

// Docs: core/events.mdx#system-events
// "For
// releasing a service's own resources, use its `Close` instead, which runs in
// reverse dependency order."
func TestCloseRunsInReverseDependencyOrder(t *testing.T) {
	t.Parallel()
	got := &list{}
	dep := &svc[kA]{name: "dep", onClose: func(context.Context, *svc[kA]) error { got.add("dep"); return nil }}
	user := &svc[kB]{name: "user", onClose: func(context.Context, *svc[kB]) error { got.add("user"); return nil }}
	w := gas.NewWorker(
		gas.WithSingletonService[*svc[kB]](func(*svc[kA]) *svc[kB] { return user }),
		gas.WithSingletonService[*svc[kA]](func() *svc[kA] { return dep }),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if want := []string{"user", "dep"}; !slices.Equal(got.get(), want) {
		t.Fatalf("close order = %v, want %v", got.get(), want)
	}
}
