package conceptsownership_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// authSvc owns a route and the "require-auth" named middleware.
type authSvc struct {
	router *gas.Router
	inits  atomic.Int32
}

func (s *authSvc) Name() string { return "auth" }
func (s *authSvc) Init(context.Context) error {
	s.inits.Add(1)
	s.router.Register(s, "require-auth", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Auth", "checked")
			next.ServeHTTP(w, r)
		})
	})
	s.router.Handle(s, http.MethodGet, "/login", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return nil
}
func (s *authSvc) Close(context.Context) error { return nil }

// billingSvc owns /api/invoices, guarded by auth's middleware, plus an inline
// guarded route and an unowned route.
type billingSvc struct{ router *gas.Router }

func (s *billingSvc) Name() string { return "billing" }
func (s *billingSvc) Init(context.Context) error {
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	s.router.Route("/api", func(sub *gas.Router) {
		sub.Use(gas.MiddlewareByName("require-auth"))
		sub.Handle(s, http.MethodGet, "/invoices", ok)
	})
	s.router.Handle(s, http.MethodGet, "/inline", ok, gas.MiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Inline", "ran")
			next.ServeHTTP(w, r)
		})
	}))
	s.router.Handle(nil, http.MethodGet, "/unowned", ok)
	return nil
}
func (s *billingSvc) Close(context.Context) error { return nil }

func newApp(t *testing.T) (*gas.App, *authSvc) {
	t.Helper()
	auth := &authSvc{}
	app := gas.NewApp(
		gas.WithSingletonService[*authSvc](func(r *gas.Router) *authSvc { auth.router = r; return auth }),
		gas.WithSingletonService[*billingSvc](func(r *gas.Router, _ *authSvc) *billingSvc { return &billingSvc{router: r} }),
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	return app, auth
}

func get(app *gas.App, path string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

// Docs: concepts/ownership.mdx#_top
// "app.CloseService[*auth.Service](ctx)   // everything auth registered now returns 503 / app.RestartService[*auth.Service](ctx) // re-initializes and re-arms it"
func TestCloseAndRestartService(t *testing.T) {
	t.Parallel()
	app, auth := newApp(t)
	if got := get(app, "/login").Code; got != http.StatusOK {
		t.Fatalf("before teardown /login = %d, want 200", got)
	}
	if err := app.CloseService[*authSvc](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if got := get(app, "/login").Code; got != http.StatusServiceUnavailable {
		t.Fatalf("after CloseService /login = %d, want 503", got)
	}
	if err := app.RestartService[*authSvc](t.Context()); err != nil {
		t.Fatalf("RestartService: %v", err)
	}
	if auth.inits.Load() != 2 {
		t.Fatalf("Init ran %d times, want 2 after a restart", auth.inits.Load())
	}
	if got := get(app, "/login").Code; got != http.StatusOK {
		t.Fatalf("after RestartService /login = %d, want 200", got)
	}
	if got := get(app, "/api/invoices"); got.Code != http.StatusOK || got.Header().Get("X-Auth") != "checked" {
		t.Fatalf("after RestartService /api/invoices = %d X-Auth=%q, want 200 with the middleware re-armed",
			got.Code, got.Header().Get("X-Auth"))
	}
}

type authAPI interface {
	gas.Service
	login()
}

func (s *authSvc) login() {}

// Docs: concepts/ownership.mdx#_top
// "The type argument is the type the service was registered under, matched exactly."
func TestCloseServiceMatchesRegisteredTypeExactly(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(gas.WithSingletonService[authAPI](func(r *gas.Router) authAPI { return &authSvc{router: r} }))
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	if err := app.CloseService[*authSvc](t.Context()); err == nil {
		t.Fatal("CloseService[*authSvc] succeeded for a service registered under authAPI")
	}
	if err := app.CloseService[authAPI](t.Context()); err != nil {
		t.Fatalf("CloseService[authAPI]: %v", err)
	}
}

// Docs: concepts/ownership.mdx#_top
// "from inside a service you pass `s`; ownership is tracked by its `Name()`. Passing `nil` means no owning service."
func TestOwnershipByNameAndNilOwner(t *testing.T) {
	t.Parallel()
	app, _ := newApp(t)
	routes := app.Router().Routes()
	for owner, list := range routes {
		for _, r := range list {
			if r.Path == "/unowned" && owner == "billing" {
				t.Fatal("a nil-owner route is attributed to the service that registered it")
			}
		}
	}
	if len(routes["billing"]) == 0 || len(routes["auth"]) == 0 {
		t.Fatalf("Routes() = %v, want entries keyed by service Name()", routes)
	}
	if err := app.CloseService[*billingSvc](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if got := get(app, "/unowned").Code; got != http.StatusOK {
		t.Fatalf("nil-owner route after billing teardown = %d, want 200", got)
	}
}

// Docs: concepts/ownership.mdx#what-actually-gets-disabled
// "Everything the service registered is replaced with a static `503` in the [unified error shape]"
func TestTeardownServes503InUnifiedShape(t *testing.T) {
	t.Parallel()
	app, _ := newApp(t)
	if err := app.CloseService[*authSvc](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	rr := get(app, "/login")
	var body gas.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body.Error == nil {
		t.Fatalf("503 body %q is not the unified error shape: %v", rr.Body.String(), err)
	}
	if rr.Code != http.StatusServiceUnavailable || body.Error.Status != http.StatusServiceUnavailable ||
		body.Error.Code != gas.CodeUnavailable {
		t.Fatalf("got %d %+v, want 503 with code %s", rr.Code, body.Error, gas.CodeUnavailable)
	}
}

// Docs: concepts/ownership.mdx#what-actually-gets-disabled
// "**every named middleware it registered, wherever that middleware is referenced**, including on routes owned by services that are still running."
func TestNamedMiddlewareDisabledOnOtherServicesRoutes(t *testing.T) {
	t.Parallel()
	app, _ := newApp(t)
	if got := get(app, "/api/invoices").Code; got != http.StatusOK {
		t.Fatalf("before teardown /api/invoices = %d, want 200", got)
	}
	if err := app.CloseService[*authSvc](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if got := get(app, "/api/invoices").Code; got != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/invoices after auth teardown = %d, want 503", got)
	}
}

// Docs: concepts/ownership.mdx#what-actually-gets-disabled
// "Ownership is only tracked for names passed to `Register`; an inline `gas.MiddlewareFunc` has no owner and survives any teardown."
func TestInlineMiddlewareSurvivesTeardown(t *testing.T) {
	t.Parallel()
	app, _ := newApp(t)
	if _, ok := app.Router().NamedMiddleware()["require-auth"]; !ok {
		t.Fatal("registered name missing from NamedMiddleware()")
	}
	if got := len(app.Router().NamedMiddleware()); got != 1 {
		t.Fatalf("NamedMiddleware() has %d entries, want only the Register-ed one", got)
	}
	if err := app.CloseService[*authSvc](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	rr := get(app, "/inline")
	if rr.Code != http.StatusOK || rr.Header().Get("X-Inline") != "ran" {
		t.Fatalf("inline-guarded route after teardown = %d X-Inline=%q, want 200 with the middleware run",
			rr.Code, rr.Header().Get("X-Inline"))
	}
}

// Docs: concepts/ownership.mdx#reacting-to-it
// "bus.SubscribeWithOwner[gas.SystemServiceClosed](s, func(data gas.SystemServiceClosedPayload) { // enter degraded mode if data.ServiceName was a dependency })"
func TestSystemServiceClosedCarriesName(t *testing.T) {
	t.Parallel()
	app, _ := newApp(t)
	got := make(chan string, 4)
	app.EventBus().Subscribe[gas.SystemServiceClosed](func(p gas.SystemServiceClosedPayload) { got <- p.ServiceName })
	if err := app.CloseService[*authSvc](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	select {
	case name := <-got:
		if name != "auth" {
			t.Fatalf("SystemServiceClosed.ServiceName = %q, want auth", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SystemServiceClosed not fired by CloseService")
	}
}

type userCreatedPayload struct{ Email string }

type userCreated struct{ gas.Event[userCreatedPayload] }

type userInvited struct{ gas.Event[userCreatedPayload] }

// Docs: concepts/ownership.mdx#events
// "two events never collide even when their payloads are identical."
func TestEventsWithIdenticalPayloadsDoNotCollide(t *testing.T) {
	t.Parallel()
	bus := gas.NewEventBus()
	var created, invited atomic.Int32
	bus.Subscribe[userCreated](func(userCreatedPayload) { created.Add(1) })
	bus.Subscribe[userInvited](func(userCreatedPayload) { invited.Add(1) })
	bus.Emit[userCreated](userCreatedPayload{Email: "user@example.com"}).Wait()
	if created.Load() != 1 || invited.Load() != 0 {
		t.Fatalf("created=%d invited=%d, want 1 and 0", created.Load(), invited.Load())
	}
}

// Docs: concepts/ownership.mdx#events
// "`Emit` returns a `*sync.WaitGroup`, so the caller decides whether to wait for handlers or move on."
func TestEmitWaitGroup(t *testing.T) {
	t.Parallel()
	bus := gas.NewEventBus()
	release := make(chan struct{})
	var done atomic.Bool
	bus.Subscribe[userCreated](func(userCreatedPayload) {
		<-release
		done.Store(true)
	})
	wg := bus.Emit[userCreated](userCreatedPayload{})
	var _ *sync.WaitGroup = wg //nolint:staticcheck // asserts the documented return type
	if done.Load() {
		t.Fatal("Emit waited for the handler; want the caller to decide")
	}
	close(release)
	wg.Wait()
	if !done.Load() {
		t.Fatal("Wait returned before the handler finished")
	}
}

type subscriber struct {
	bus            *gas.EventBus
	owned, unowned *atomic.Int32
}

func (s *subscriber) Name() string { return "subscriber" }
func (s *subscriber) Init(context.Context) error {
	s.bus.SubscribeWithOwner[userCreated](s, func(userCreatedPayload) { s.owned.Add(1) })
	s.bus.Subscribe[userCreated](func(userCreatedPayload) { s.unowned.Add(1) })
	return nil
}
func (s *subscriber) Close(context.Context) error { return nil }

// Docs: concepts/ownership.mdx#events
// "Subscribing with `SubscribeWithOwner` is what ties the subscription to a service so teardown can remove it; an unowned subscription lives until the process exits."
func TestOwnedSubscriptionRemovedUnownedKept(t *testing.T) {
	t.Parallel()
	var owned, unowned atomic.Int32
	w := gas.NewWorker(gas.WithSingletonService[*subscriber](func(b *gas.EventBus) *subscriber {
		return &subscriber{bus: b, owned: &owned, unowned: &unowned}
	}))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if err := w.CloseService[*subscriber](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	w.EventBus().Emit[userCreated](userCreatedPayload{}).Wait()
	if owned.Load() != 0 || unowned.Load() != 1 {
		t.Fatalf("after teardown owned=%d unowned=%d, want 0 and 1", owned.Load(), unowned.Load())
	}
}

// Docs: concepts/ownership.mdx#system-events
// "| `gas.SystemServiceInitialized` | `RestartService` re-initializes a service; not emitted during startup |"
func TestSystemServiceInitializedOnRestartOnly(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(gas.WithSingletonService[*authSvc](func(r *gas.Router) *authSvc { return &authSvc{router: r} }))
	var mu sync.Mutex
	var names []string
	app.EventBus().Subscribe[gas.SystemServiceInitialized](func(p gas.SystemServiceInitializedPayload) {
		mu.Lock()
		defer mu.Unlock()
		names = append(names, p.ServiceName)
	})
	snapshot := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), names...)
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	time.Sleep(100 * time.Millisecond)
	if got := snapshot(); len(got) != 0 {
		t.Fatalf("SystemServiceInitialized fired during startup for %v, want none", got)
	}
	if err := app.CloseService[*authSvc](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if err := app.RestartService[*authSvc](t.Context()); err != nil {
		t.Fatalf("RestartService: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := snapshot(); len(got) != 1 || got[0] != "auth" {
		t.Fatalf("after RestartService the event fired for %v, want [auth]", got)
	}
}

// Docs: concepts/ownership.mdx#system-events
// "`gas.SystemAllServicesInitialized` | Every service has initialized successfully"
func TestSystemAllServicesInitialized(t *testing.T) {
	t.Parallel()
	fire := func(t *testing.T, fail bool) bool {
		t.Helper()
		var fired atomic.Bool
		w := gas.NewWorker(gas.WithSingletonService[*svc[kA]](func() *svc[kA] {
			return &svc[kA]{onInit: func(context.Context) error {
				if fail {
					return context.Canceled
				}
				return nil
			}}
		}))
		w.EventBus().Subscribe[gas.SystemAllServicesInitialized](func(gas.SystemAllServicesInitializedPayload) { fired.Store(true) })
		_ = w.Start(t.Context())
		_ = w.Shutdown(context.Background())
		time.Sleep(50 * time.Millisecond)
		return fired.Load()
	}
	if !fire(t, false) {
		t.Fatal("not fired after a successful start")
	}
	if fire(t, true) {
		t.Fatal("fired although a service failed Init")
	}
}

// Docs: concepts/ownership.mdx#system-events
// "`gas.SystemShuttingDown` | A Worker or App begins shutdown; always fires / `gas.SystemServerShuttingDown` | The HTTP server begins graceful shutdown; App only"
func TestShutdownEvents(t *testing.T) {
	t.Parallel()
	type seen struct{ shutting, server atomic.Bool }
	watch := func(bus *gas.EventBus) *seen {
		s := &seen{}
		bus.Subscribe[gas.SystemShuttingDown](func(gas.SystemShuttingDownPayload) { s.shutting.Store(true) })
		bus.Subscribe[gas.SystemServerShuttingDown](func(gas.SystemServerShuttingDownPayload) { s.server.Store(true) })
		return s
	}

	w := gas.NewWorker()
	ws := watch(w.EventBus())
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	_ = w.Shutdown(context.Background())

	app := gas.NewApp()
	as := watch(app.EventBus())
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("app start: %v", err)
	}
	_ = app.Stop(context.Background())
	time.Sleep(50 * time.Millisecond)

	if !ws.shutting.Load() || !as.shutting.Load() {
		t.Fatalf("SystemShuttingDown worker=%v app=%v, want both", ws.shutting.Load(), as.shutting.Load())
	}
	if ws.server.Load() || !as.server.Load() {
		t.Fatalf("SystemServerShuttingDown worker=%v app=%v, want App only", ws.server.Load(), as.server.Load())
	}
}

// Docs: concepts/ownership.mdx#system-events
// "`gas.AppConfigUpdated` | App config is updated after binding; App only"
func TestAppConfigUpdatedAfterBinding(t *testing.T) {
	t.Parallel()
	app := gas.NewApp(gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, map[string]any{
		"server": map[string]any{"port": 9393},
	})))
	got := make(chan int, 1)
	app.EventBus().Subscribe[gas.AppConfigUpdated](func(p gas.AppConfigUpdatedPayload) { got <- p.Config.Server.Port })
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	select {
	case port := <-got:
		if port != 9393 {
			t.Fatalf("AppConfigUpdated carried port %d, want the bound 9393", port)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AppConfigUpdated not fired by Start")
	}

	w := gas.NewWorker(gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, map[string]any{})))
	var fired atomic.Bool
	w.EventBus().Subscribe[gas.AppConfigUpdated](func(gas.AppConfigUpdatedPayload) { fired.Store(true) })
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	_ = w.Shutdown(context.Background())
	time.Sleep(50 * time.Millisecond)
	if fired.Load() {
		t.Fatal("AppConfigUpdated fired on a Worker")
	}
}

// Docs: concepts/ownership.mdx#when-to-reach-for-this
// "`router.Routes()` reports who owns what."
func TestRoutesReportsOwners(t *testing.T) {
	t.Parallel()
	app, _ := newApp(t)
	routes := app.Router().Routes()
	has := func(owner, method, path string) bool {
		for _, r := range routes[owner] {
			if r.Method == method && r.Path == path {
				return true
			}
		}
		return false
	}
	if !has("auth", http.MethodGet, "/login") || !has("billing", http.MethodGet, "/api/invoices") {
		t.Fatalf("Routes() = %v, want GET /login under auth and GET /api/invoices under billing", routes)
	}
}
