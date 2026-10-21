package coreownership_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

type ping struct{ gas.Event[pingPayload] }

type pingPayload struct{}

// Docs: core/ownership.mdx#what-a-service-owns
// "`router.Handle(s, method, path, h)`, and `Get`, `Post`, `Put`, `Patch`, `Delete`, `Head` | `s`" and
// "`router.Register(s, name, mw)`, a named middleware | `s`" and
// "Ownership is recorded by the service's `Name()`."
func TestRegistrationsOwnedByFirstArgument(t *testing.T) {
	t.Parallel()
	s := &svc[kA]{name: "notes"}
	s.onInit = func(_ context.Context, s *svc[kA]) error {
		r := s.router
		r.Handle(s, http.MethodOptions, "/h", ok)
		r.Get(s, "/g", ok)
		r.Post(s, "/p", ok)
		r.Put(s, "/u", ok)
		r.Patch(s, "/pa", ok)
		r.Delete(s, "/d", ok)
		r.Head(s, "/he", ok)
		r.Register(s, "notes-mw", tagger("notes"))
		return nil
	}
	app := gas.NewApp(withSvc(s))
	start(t, app)

	var got []string
	for _, rt := range app.Router().Routes()["notes"] {
		got = append(got, rt.Method+" "+rt.Path)
	}
	slices.Sort(got)
	want := []string{"DELETE /d", "GET /g", "HEAD /he", "OPTIONS /h", "PATCH /pa", "POST /p", "PUT /u"}
	if !slices.Equal(got, want) {
		t.Fatalf("routes owned by notes = %v, want %v", got, want)
	}
	if owner := app.Router().NamedMiddleware()["notes-mw"]; owner != "notes" {
		t.Fatalf("NamedMiddleware()[notes-mw] = %q, want notes", owner)
	}
}

// Docs: core/ownership.mdx#what-a-service-owns
// "`bus.SubscribeWithOwner[E](s, fn)` | `s`" and
// "`bus.Subscribe[E](fn)` | Nobody; no teardown removes it"
func TestSubscriptionOwnership(t *testing.T) {
	t.Parallel()
	var owned, unowned atomic.Int32
	s := &svc[kA]{name: "listener"}
	s.onInit = func(_ context.Context, s *svc[kA]) error {
		s.bus.SubscribeWithOwner[ping](s, func(pingPayload) { owned.Add(1) })
		s.bus.Subscribe[ping](func(pingPayload) { unowned.Add(1) })
		return nil
	}
	w := gas.NewWorker(withWorkerSvc(s))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if err := w.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	w.EventBus().Emit[ping](pingPayload{}).Wait()
	if owned.Load() != 0 || unowned.Load() != 1 {
		t.Fatalf("after teardown owned=%d unowned=%d, want 0 and 1", owned.Load(), unowned.Load())
	}
}

// Docs: core/ownership.mdx#what-a-service-owns
// "`gas.MiddlewareFunc(fn)` or `gas.MiddlewareFuncWithName(name, fn)` used inline | Nobody; no teardown disables it"
func TestInlineMiddlewareSurvivesTeardown(t *testing.T) {
	t.Parallel()
	s := &svc[kA]{name: "inline"}
	s.onInit = func(_ context.Context, s *svc[kA]) error {
		s.router.Use(gas.MiddlewareFunc(tagger("use")))
		s.router.Use(gas.MiddlewareFuncWithName("named-inline", tagger("use-named")))
		return nil
	}
	app := gas.NewApp(withSvc(s))
	app.Router().Get(owner("other"), "/x", ok,
		gas.MiddlewareFunc(tagger("route")), gas.MiddlewareFuncWithName("route-named", tagger("route-named")))
	start(t, app)
	if err := app.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	rr := get(app.Handler(), "/x")
	got := rr.Header().Values("X-Mw")
	slices.Sort(got)
	if want := []string{"route", "route-named", "use", "use-named"}; rr.Code != http.StatusOK || !slices.Equal(got, want) {
		t.Fatalf("after teardown status=%d middleware=%v, want 200 and %v", rr.Code, got, want)
	}
}

// Docs: core/ownership.mdx#what-a-service-owns
// "Passing `nil` as the owner of a
// route or a named middleware makes the router itself (`gas/router`) the owner,
// so closing one of your services never removes it."
func TestNilOwnerSurvivesTeardown(t *testing.T) {
	t.Parallel()
	s := &svc[kA]{name: "registrar"}
	s.onInit = func(_ context.Context, s *svc[kA]) error {
		s.router.Register(nil, "shared", tagger("shared"))
		s.router.Get(nil, "/shared", ok, gas.MiddlewareByName("shared"))
		s.router.Get(s, "/own", ok)
		return nil
	}
	app := gas.NewApp(withSvc(s))
	start(t, app)
	if len(app.Router().Routes()["gas/router"]) != 1 || app.Router().NamedMiddleware()["shared"] != "gas/router" {
		t.Fatalf("Routes()=%v NamedMiddleware()=%v, want the nil-owner registrations under gas/router",
			app.Router().Routes(), app.Router().NamedMiddleware())
	}
	if err := app.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if rr := get(app.Handler(), "/shared"); rr.Code != http.StatusOK || rr.Header().Get("X-Mw") != "shared" {
		t.Fatalf("nil-owner route after teardown = %d X-Mw=%q, want 200 with the middleware run", rr.Code, rr.Header().Get("X-Mw"))
	}
	if code := get(app.Handler(), "/own").Code; code != http.StatusServiceUnavailable {
		t.Fatalf("service-owned route after teardown = %d, want 503", code)
	}
}

// Docs: core/ownership.mdx#what-a-service-owns
// "`router.Routes()` returns the registered routes grouped by owning service (the
// `HEAD` route registered alongside each `GET` is left out), and
// `router.NamedMiddleware()` maps each named middleware to its owner."
func TestRoutesAndNamedMiddlewareSnapshots(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	r := app.Router()
	r.Get(owner("a"), "/a", ok)
	r.Post(owner("b"), "/b", ok)
	r.Register(owner("auth"), "require-auth", tagger("auth"))
	start(t, app)
	if code := do(app.Handler(), http.MethodHead, "/a").Code; code != http.StatusOK {
		t.Fatalf("HEAD /a = %d, want the implicit HEAD route to serve", code)
	}
	got := map[string][]string{}
	for name, routes := range r.Routes() {
		for _, rt := range routes {
			got[name] = append(got[name], rt.Method+" "+rt.Path)
		}
	}
	want := map[string][]string{"a": {"GET /a"}, "b": {"POST /b"}}
	if !maps.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("Routes() = %v, want %v", got, want)
	}
	if nm := r.NamedMiddleware(); !maps.Equal(nm, map[string]string{"require-auth": "auth"}) {
		t.Fatalf("NamedMiddleware() = %v, want require-auth -> auth", nm)
	}
}

// Docs: core/ownership.mdx#closing-a-service
// "`CloseService[T](ctx)` takes the type the service was registered under, matched
// exactly. It is a method on the Worker, so an App has it too."
func TestCloseServiceExactTypeOnWorker(t *testing.T) {
	t.Parallel()
	impl := &svc[kA]{name: "impl"}
	w := gas.NewWorker(gas.WithSingletonService[gas.Service](func() gas.Service { return impl }))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if err := w.CloseService[*svc[kA]](t.Context()); err == nil {
		t.Fatal("CloseService by the concrete type succeeded for a service registered under gas.Service")
	}
	if err := w.CloseService[gas.Service](t.Context()); err != nil {
		t.Fatalf("Worker.CloseService by the registered type: %v", err)
	}
	if impl.closes.Load() != 1 {
		t.Fatalf("Close ran %d times, want 1", impl.closes.Load())
	}
}

// Docs: core/ownership.mdx#closing-a-service
// "1. disables everything the service registered on the router (App only; see
// [below](#what-503-means)),
// 2. removes every subscription the service made with `SubscribeWithOwner`,
// 3. calls the service's `Close(ctx)`,
// 4. removes the service from the active services,
// 5. emits `gas.SystemServiceClosed` with the service's name, and waits for its
// subscribers."
func TestCloseServiceOrder(t *testing.T) {
	t.Parallel()
	var (
		mu              sync.Mutex
		routeInClose    int
		ownedSubInClose bool
		ctxInClose      any
		activeAtEvent   []string
		eventName       string
		subscriberDone  atomic.Bool
	)
	type ctxK struct{}
	var app *gas.App
	s := &svc[kA]{name: "auth"}
	s.onInit = func(_ context.Context, s *svc[kA]) error {
		s.router.Get(s, "/auth", ok)
		s.bus.SubscribeWithOwner[ping](s, func(pingPayload) {
			mu.Lock()
			ownedSubInClose = true
			mu.Unlock()
		})
		return nil
	}
	s.onClose = func(ctx context.Context, _ *svc[kA]) error {
		code := get(app.Handler(), "/auth").Code
		app.EventBus().Emit[ping](pingPayload{}).Wait()
		mu.Lock()
		routeInClose, ctxInClose = code, ctx.Value(ctxK{})
		mu.Unlock()
		return nil
	}
	app = gas.NewApp(withSvc(s))
	start(t, app)
	app.EventBus().Subscribe[gas.SystemServiceClosed](func(p gas.SystemServiceClosedPayload) {
		active := app.ActiveServices()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		activeAtEvent, eventName = active, p.ServiceName
		mu.Unlock()
		subscriberDone.Store(true)
	})

	ctx := context.WithValue(t.Context(), ctxK{}, "close-ctx")
	if err := app.CloseService[*svc[kA]](ctx); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if routeInClose != http.StatusServiceUnavailable {
		t.Errorf("route during Close = %d, want 503 already", routeInClose)
	}
	if ownedSubInClose {
		t.Error("owned subscription still received an event during Close")
	}
	if ctxInClose != "close-ctx" {
		t.Error("Close did not receive the ctx passed to CloseService")
	}
	if !subscriberDone.Load() {
		t.Error("CloseService returned before its SystemServiceClosed subscriber finished")
	}
	if eventName != "auth" || slices.Contains(activeAtEvent, "auth") {
		t.Errorf("event name %q, active at event %v; want auth, already removed", eventName, activeAtEvent)
	}
}

// Docs: core/ownership.mdx#closing-a-service
// "Routes and subscriptions go first, so they are gone even if `Close` fails. An
// error from `Close` is logged as `service close failed` and not returned, since
// the teardown has already happened."
func TestCloseFailureLoggedNotReturned(t *testing.T) {
	t.Parallel()
	sink, logger := newSink()
	var owned atomic.Int32
	s := &svc[kA]{name: "flaky"}
	s.onInit = func(_ context.Context, s *svc[kA]) error {
		s.router.Get(s, "/flaky", ok)
		s.bus.SubscribeWithOwner[ping](s, func(pingPayload) { owned.Add(1) })
		return nil
	}
	s.onClose = func(context.Context, *svc[kA]) error { return errors.New("close failed 0b7") }
	app := gas.NewApp(gas.WithSingletonService[gas.Logger](logger), withSvc(s))
	start(t, app)
	if err := app.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService returned %v, want nil when only Close fails", err)
	}
	if code := get(app.Handler(), "/flaky").Code; code != http.StatusServiceUnavailable {
		t.Errorf("route after a failed Close = %d, want 503", code)
	}
	app.EventBus().Emit[ping](pingPayload{}).Wait()
	if owned.Load() != 0 {
		t.Error("owned subscription survived a failed Close")
	}
	recs := withMsg(sink.records(t), "service close failed")
	if len(recs) != 1 || !strings.Contains(fmt.Sprint(recs[0]), "close failed 0b7") {
		t.Fatalf("service close failed records = %v, want one carrying the error", recs)
	}
}

type neverBuilt struct{ svc[kB] }

type scopedSvc struct{ svc[kC] }

// Docs: core/ownership.mdx#closing-a-service
// "`CloseService` returns an error when `T` has no built instance (it was never
// built, or it is registered under a different type), when the service is not
// active (it was already closed), or once shutdown has begun. Only singletons,
// including instances, have a built instance to close."
func TestCloseServiceErrors(t *testing.T) {
	t.Parallel()
	inst := &svc[kB]{name: "instance"}
	single := &svc[kA]{name: "single"}
	w := gas.NewWorker(
		withWorkerSvc(single),
		gas.WithServiceInstance(inst),
		gas.WithScopedService[*scopedSvc](func() *scopedSvc { return &scopedSvc{svc[kC]{name: "scoped"}} }),
	)
	if err := w.CloseService[*svc[kA]](t.Context()); err == nil {
		t.Error("CloseService before Start (never built) returned nil")
	}
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.CloseService[*neverBuilt](t.Context()); err == nil {
		t.Error("CloseService of an unregistered type returned nil")
	}
	if err := w.CloseService[*scopedSvc](t.Context()); err == nil {
		t.Error("CloseService of a scoped type returned nil")
	}
	if err := w.CloseService[*svc[kB]](t.Context()); err != nil {
		t.Errorf("CloseService of a registered instance: %v", err)
	}
	if err := w.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("first CloseService: %v", err)
	}
	if err := w.CloseService[*svc[kA]](t.Context()); err == nil {
		t.Error("CloseService of an already closed service returned nil")
	}
	if err := w.RestartService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("RestartService: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := w.CloseService[*svc[kA]](t.Context()); err == nil {
		t.Error("CloseService after Shutdown returned nil")
	}
}

// reporter is a service that reports health and readiness.
type reporter struct{ svc[kA] }

func (*reporter) CheckHealth(context.Context) error { return nil }
func (*reporter) CheckReady(context.Context) error  { return nil }

// Docs: core/ownership.mdx#closing-a-service
// "A closed service no longer appears in `ActiveServices()`, is left out of
// [health and readiness](/gas/core/lifecycle/#health-and-readiness) checks, and
// is not closed a second time at shutdown."
func TestClosedServiceLeavesActiveSet(t *testing.T) {
	t.Parallel()
	rep := &reporter{svc[kA]{name: "reporter"}}
	w := gas.NewWorker(gas.WithSingletonService[*reporter](func() *reporter { return rep }))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, ok := w.CheckHealth(t.Context())["reporter"]; !ok {
		t.Fatal("reporter missing from CheckHealth before teardown")
	}
	if err := w.CloseService[*reporter](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if slices.Contains(w.ActiveServices(), "reporter") {
		t.Error("closed service still in ActiveServices()")
	}
	if _, ok := w.CheckHealth(t.Context())["reporter"]; ok {
		t.Error("closed service still in CheckHealth")
	}
	if _, ok := w.CheckReady(t.Context())["reporter"]; ok {
		t.Error("closed service still in CheckReady")
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if n := rep.closes.Load(); n != 1 {
		t.Errorf("Close ran %d times, want once (not again at shutdown)", n)
	}
}

const body503 = `{"error":{"status":503,"code":"service_unavailable","message":"service unavailable"}}`

// Docs: core/ownership.mdx#what-503-means
// "After the teardown, every route the closed service owns answers
// `503 Service Unavailable` in the [unified error shape](/gas/core/errors/),
// including the `HEAD` route that came with each `GET`. The body does not name the
// service:"
// "{"error":{"status":503,"code":"service_unavailable","message":"service unavailable"}}"
func TestTeardown503Body(t *testing.T) {
	t.Parallel()
	s := &svc[kA]{name: "secretive"}
	s.onInit = func(_ context.Context, s *svc[kA]) error {
		s.router.Get(s, "/r", ok)
		s.router.Post(s, "/r", ok)
		return nil
	}
	app := gas.NewApp(withSvc(s))
	start(t, app)
	if err := app.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	for _, m := range []string{http.MethodGet, http.MethodPost} {
		rr := do(app.Handler(), m, "/r")
		var e gas.ErrorResponse
		if rr.Code != http.StatusServiceUnavailable || json.Unmarshal(rr.Body.Bytes(), &e) != nil || e.Error == nil {
			t.Fatalf("%s /r = %d %q, want 503 in the unified shape", m, rr.Code, rr.Body.String())
		}
		if got := strings.TrimSpace(rr.Body.String()); got != body503 {
			t.Errorf("%s /r body = %s, want %s", m, got, body503)
		}
		if strings.Contains(rr.Body.String(), "secretive") {
			t.Errorf("%s /r body names the service: %s", m, rr.Body.String())
		}
	}
	if code := do(app.Handler(), http.MethodHead, "/r").Code; code != http.StatusServiceUnavailable {
		t.Errorf("implicit HEAD /r = %d, want 503", code)
	}
}

// Docs: core/ownership.mdx#what-503-means
// "A client that asks for `text/html` without also accepting `application/json`
// gets the message as plain text instead."
func TestTeardown503PlainTextForHTML(t *testing.T) {
	t.Parallel()
	s := &svc[kA]{name: "pages"}
	s.onInit = func(_ context.Context, s *svc[kA]) error { s.router.Get(s, "/page", ok); return nil }
	app := gas.NewApp(withSvc(s))
	start(t, app)
	if err := app.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	send := func(accept string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/page", nil)
		req.Header.Set("Accept", accept)
		rr := httptest.NewRecorder()
		app.Handler().ServeHTTP(rr, req)
		return rr
	}
	rr := send("text/html")
	if rr.Code != http.StatusServiceUnavailable || strings.TrimSpace(rr.Body.String()) != "service unavailable" ||
		!strings.HasPrefix(rr.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("Accept text/html: %d %q (%s), want 503 plain text", rr.Code, rr.Body.String(), rr.Header().Get("Content-Type"))
	}
	if rr := send("text/html, application/json"); strings.TrimSpace(rr.Body.String()) != body503 {
		t.Errorf("Accept text/html and application/json: body %q, want JSON", rr.Body.String())
	}
}

// newSampleApp mirrors the page's sample: auth owns require-auth and
// /auth/me, billing owns /api/invoices guarded by require-auth.
func newSampleApp(t *testing.T, invoicesCalls *atomic.Int32) *gas.App {
	t.Helper()
	auth := &svc[kA]{name: "auth"}
	auth.onInit = func(_ context.Context, s *svc[kA]) error {
		s.router.Register(s, "require-auth", tagger("auth"))
		s.router.Get(s, "/auth/me", text("me"))
		return nil
	}
	billing := &svc[kB]{name: "billing"}
	billing.onInit = func(_ context.Context, s *svc[kB]) error {
		s.router.Route("/api", func(sub *gas.Router) {
			sub.Use(gas.MiddlewareByName("require-auth"))
			sub.Get(s, "/invoices", func(w http.ResponseWriter, _ *http.Request) {
				invoicesCalls.Add(1)
				_, _ = w.Write([]byte("invoices"))
			})
		})
		s.router.Group(func(g *gas.Router) {
			g.Get(s, "/grouped", ok, gas.MiddlewareByName("require-auth"))
		})
		s.router.Get(s, "/direct", ok, gas.MiddlewareByName("require-auth"))
		return nil
	}
	// Route-level names resolve at registration, so billing inits after auth.
	app := gas.NewApp(withSvc(auth), withSvcAfter[kB, kA](billing))
	start(t, app)
	return app
}

func line(h http.Handler, path string) string {
	rr := get(h, path)
	return fmt.Sprintf("GET %s -> %d %s", path, rr.Code, strings.TrimSpace(rr.Body.String()))
}

// Docs: core/ownership.mdx#what-503-means
// "Every **named middleware** the service registered is replaced with a 503 as
// well, **wherever it is referenced**: on routes owned by other services that are
// still running, and inside `Group` and `Route` blocks."
// "GET /api/invoices -> 200 invoices
// GET /auth/me -> 503 {"error":{"status":503,"code":"service_unavailable","message":"service unavailable"}}
// GET /api/invoices -> 503 {"error":{"status":503,"code":"service_unavailable","message":"service unavailable"}}"
func TestNamedMiddlewareDisabledWherever(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	app := newSampleApp(t, &calls)
	h := app.Handler()
	got := []string{line(h, "/api/invoices")}
	if err := app.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	got = append(got, line(h, "/auth/me"), line(h, "/api/invoices"))
	want := []string{
		"GET /api/invoices -> 200 invoices",
		"GET /auth/me -> 503 " + body503,
		"GET /api/invoices -> 503 " + body503,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sample output:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, p := range []string{"/grouped", "/direct"} {
		if code := get(h, p).Code; code != http.StatusServiceUnavailable {
			t.Errorf("GET %s using require-auth after auth teardown = %d, want 503", p, code)
		}
	}
	if !slices.Contains(app.ActiveServices(), "billing") {
		t.Error("billing is not active; the 503 must come from the middleware alone")
	}
}

// Docs: core/ownership.mdx#what-503-means
// "A torn-down middleware fails the request rather than letting it through."
func TestTornDownMiddlewareNeverSkipped(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	app := newSampleApp(t, &calls)
	if err := app.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	before := calls.Load()
	rr := get(app.Handler(), "/api/invoices")
	if calls.Load() != before || rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, handler ran %d times; want 503 without reaching the handler", rr.Code, calls.Load()-before)
	}
}

// Docs: core/ownership.mdx#what-503-means
// "The router applies the change by building a new routing tree and swapping it
// in atomically. Requests already in flight finish on the previous tree."
func TestInFlightRequestFinishesOnOldTree(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	s := &svc[kA]{name: "slow"}
	s.onInit = func(_ context.Context, s *svc[kA]) error {
		s.router.Register(s, "slow-mw", tagger("slow"))
		s.router.Get(s, "/slow", func(w http.ResponseWriter, _ *http.Request) {
			entered <- struct{}{}
			<-release
			_, _ = w.Write([]byte("finished"))
		}, gas.MiddlewareByName("slow-mw"))
		return nil
	}
	app := gas.NewApp(withSvc(s))
	start(t, app)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- get(app.Handler(), "/slow") }()
	<-entered
	if err := app.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	close(release)
	rr := <-done
	if rr.Code != http.StatusOK || rr.Body.String() != "finished" || rr.Header().Get("X-Mw") != "slow" {
		t.Fatalf("in-flight request = %d %q, want 200 finished on the previous tree", rr.Code, rr.Body.String())
	}
	if code := get(app.Handler(), "/slow").Code; code != http.StatusServiceUnavailable {
		t.Fatalf("new request after teardown = %d, want 503", code)
	}
}

// Docs: core/ownership.mdx#restarting-a-service
// "`RestartService[T](ctx)` runs `Init` again on the same instance, under the same
// [init deadline](/gas/core/lifecycle/#init-and-close-contexts) as at startup.
// Because `Init` registers the service's routes, named middleware, and
// subscriptions again, they serve again: in the sample, `GET /api/invoices`
// returns 200 once more. The service rejoins the active services, and
// `gas.SystemServiceInitialized` is emitted with its name."
func TestRestartService(t *testing.T) {
	t.Parallel()
	var (
		mu        sync.Mutex
		deadlines []time.Duration
		instances []*svc[kA]
		owned     atomic.Int32
		restarted []string
	)
	auth := &svc[kA]{name: "auth"}
	auth.onInit = func(ctx context.Context, s *svc[kA]) error {
		dl, has := ctx.Deadline()
		mu.Lock()
		if has {
			deadlines = append(deadlines, time.Until(dl).Round(time.Minute))
		} else {
			deadlines = append(deadlines, -1)
		}
		instances = append(instances, s)
		mu.Unlock()
		s.router.Register(s, "require-auth", tagger("auth"))
		s.router.Get(s, "/auth/me", text("me"))
		s.bus.SubscribeWithOwner[ping](s, func(pingPayload) { owned.Add(1) })
		return nil
	}
	billing := &svc[kB]{name: "billing"}
	billing.onInit = func(_ context.Context, s *svc[kB]) error {
		s.router.Get(s, "/api/invoices", text("invoices"), gas.MiddlewareByName("require-auth"))
		return nil
	}
	app := gas.NewApp(gas.WithServiceInitTimeout(10*time.Minute), withSvc(auth), withSvcAfter[kB, kA](billing))
	app.EventBus().Subscribe[gas.SystemServiceInitialized](func(p gas.SystemServiceInitializedPayload) {
		mu.Lock()
		restarted = append(restarted, p.ServiceName)
		mu.Unlock()
	})
	start(t, app)
	if err := app.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if err := app.RestartService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("RestartService: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(instances) != 2 || instances[0] != instances[1] {
		t.Errorf("Init ran on %d instances, want twice on the same one", len(instances))
	}
	if !slices.Equal(deadlines, []time.Duration{10 * time.Minute, 10 * time.Minute}) {
		t.Errorf("Init deadlines (startup, restart) = %v, want the 10m init deadline both times", deadlines)
	}
	if rr := get(app.Handler(), "/api/invoices"); rr.Code != http.StatusOK || rr.Header().Get("X-Mw") != "auth" {
		t.Errorf("GET /api/invoices after restart = %d, want 200 through require-auth", rr.Code)
	}
	if code := get(app.Handler(), "/auth/me").Code; code != http.StatusOK {
		t.Errorf("GET /auth/me after restart = %d, want 200", code)
	}
	app.EventBus().Emit[ping](pingPayload{}).Wait()
	if owned.Load() != 1 {
		t.Errorf("re-made owned subscription ran %d times, want 1", owned.Load())
	}
	if !slices.Contains(app.ActiveServices(), "auth") {
		t.Error("restarted service is not active")
	}
	if !slices.Equal(restarted, []string{"auth"}) {
		t.Errorf("SystemServiceInitialized names = %v, want [auth]", restarted)
	}
}

// Docs: core/ownership.mdx#restarting-a-service
// "`RestartService` returns an error when `T` has no built instance, when the
// service is already active, once shutdown has begun, or when `Init` fails; the
// last one is wrapped as `gas: re-init <name>: ...`."
func TestRestartServiceErrors(t *testing.T) {
	t.Parallel()
	errInit := errors.New("init failed 44c")
	var fail atomic.Bool
	s := &svc[kA]{name: "fragile"}
	s.onInit = func(context.Context, *svc[kA]) error {
		if fail.Load() {
			return errInit
		}
		return nil
	}
	w := gas.NewWorker(withWorkerSvc(s))
	if err := w.RestartService[*svc[kA]](t.Context()); err == nil {
		t.Error("RestartService before Start (no built instance) returned nil")
	}
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.RestartService[*neverBuilt](t.Context()); err == nil {
		t.Error("RestartService of an unregistered type returned nil")
	}
	if err := w.RestartService[*svc[kA]](t.Context()); err == nil {
		t.Error("RestartService of an active service returned nil")
	}
	if err := w.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	fail.Store(true)
	err := w.RestartService[*svc[kA]](t.Context())
	if !errors.Is(err, errInit) || !regexp.MustCompile(`^gas: re-init fragile: .*init failed 44c`).MatchString(err.Error()) {
		t.Errorf("RestartService with a failing Init = %v, want gas: re-init fragile: ... wrapping the Init error", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	fail.Store(false)
	if err := w.RestartService[*svc[kA]](t.Context()); err == nil {
		t.Error("RestartService after Shutdown returned nil")
	}
}

// Docs: core/ownership.mdx#reacting-to-a-teardown
// "The closed service's own subscriptions are removed before the event is emitted,
// so it does not receive its own `SystemServiceClosed`."
func TestClosedServiceMissesOwnEvent(t *testing.T) {
	t.Parallel()
	var self, other atomic.Int32
	a := &svc[kA]{name: "closing"}
	a.onInit = func(_ context.Context, s *svc[kA]) error {
		s.bus.SubscribeWithOwner[gas.SystemServiceClosed](s, func(gas.SystemServiceClosedPayload) { self.Add(1) })
		return nil
	}
	b := &svc[kB]{name: "watcher"}
	b.onInit = func(_ context.Context, s *svc[kB]) error {
		s.bus.SubscribeWithOwner[gas.SystemServiceClosed](s, func(gas.SystemServiceClosedPayload) { other.Add(1) })
		return nil
	}
	w := gas.NewWorker(withWorkerSvc(a), withWorkerSvc(b))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	if err := w.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if self.Load() != 0 || other.Load() != 1 {
		t.Fatalf("closed service got %d events, watcher %d; want 0 and 1", self.Load(), other.Load())
	}
}

// Docs: core/ownership.mdx#restarting-a-service
// "A route that the restarted `Init` does not register again does not stay at
// 503. It comes back serving the handler from the previous `Init`, and it is
// missing from `router.Routes()`."
// KI-054: this asserts the documented buggy behavior.
func TestRestartSkippedRouteServesOldHandler(t *testing.T) {
	t.Parallel()
	var restarted atomic.Bool
	s := &svc[kA]{name: "partial"}
	s.onInit = func(_ context.Context, s *svc[kA]) error {
		s.router.Get(s, "/kept", text("kept"))
		if !restarted.Load() {
			s.router.Get(s, "/dropped", text("first init"))
		}
		return nil
	}
	app := gas.NewApp(withSvc(s))
	start(t, app)
	if err := app.CloseService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if code := get(app.Handler(), "/dropped").Code; code != http.StatusServiceUnavailable {
		t.Fatalf("GET /dropped after teardown = %d, want 503", code)
	}
	restarted.Store(true)
	if err := app.RestartService[*svc[kA]](t.Context()); err != nil {
		t.Fatalf("RestartService: %v", err)
	}
	rr := get(app.Handler(), "/dropped")
	if rr.Code != http.StatusOK || rr.Body.String() != "first init" {
		t.Errorf("GET /dropped after restart = %d %q, want 200 from the previous Init's handler", rr.Code, rr.Body.String())
	}
	var paths []string
	for _, rt := range app.Router().Routes()["partial"] {
		paths = append(paths, rt.Path)
	}
	if !slices.Equal(paths, []string{"/kept"}) {
		t.Errorf("Routes()[partial] = %v, want only /kept (the skipped route missing)", paths)
	}
}
