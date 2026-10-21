package corehandlers_test

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache/cachetest"
)

// captured is an error handler that records what it receives and writes 599.
type captured struct {
	mu   sync.Mutex
	errs []error
}

func (c *captured) handler(ctx gas.Context, err error) {
	c.mu.Lock()
	c.errs = append(c.errs, err)
	c.mu.Unlock()
	ctx.ResponseWriter().WriteHeader(599)
}

func (c *captured) last() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.errs) == 0 {
		return nil
	}
	return c.errs[len(c.errs)-1]
}

// Docs: core/handlers.mdx#_top
// "A **plain handler** is an `http.HandlerFunc` or a
// `func(http.ResponseWriter, *http.Request)`. The router serves it as is, with
// one addition: a panic in it is recovered and sent to the
// [error handler](/gas/core/errors/).
// - A **DI-aware handler** is a function whose first parameter is `gas.Context`,
// whose remaining parameters are dependencies, and which returns `error`. A
// returned error goes to the error handler, and so does a recovered panic."
func TestHandlerFormsAndErrorHandler(t *testing.T) {
	t.Parallel()
	eh := &captured{}
	returned := errors.New("returned")
	app := gas.NewApp(gas.WithErrorHandler(eh.handler))
	r := app.Router()
	r.Get(nil, "/plain", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Plain", "as-is")
		w.WriteHeader(http.StatusTeapot)
	})
	r.Get(nil, "/plain-panic", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("plain boom") }))
	r.Get(nil, "/func-panic", func(http.ResponseWriter, *http.Request) { panic("func boom") })
	r.Get(nil, "/di-error", func(gas.Context) error { return returned })
	r.Get(nil, "/di-panic", func(gas.Context) error { panic("di boom") })
	start(t, app)

	if rr := get(app.Handler(), "/plain"); rr.Code != http.StatusTeapot || rr.Header().Get("X-Plain") != "as-is" {
		t.Errorf("plain handler = %d %q, want 418 as-is", rr.Code, rr.Header().Get("X-Plain"))
	}
	for path, want := range map[string]string{"/plain-panic": "plain boom", "/func-panic": "func boom", "/di-panic": "di boom"} {
		rr := get(app.Handler(), path)
		if rr.Code != 599 || eh.last() == nil || !strings.Contains(eh.last().Error(), want) {
			t.Errorf("GET %s = %d, error handler got %v; want the recovered panic %q", path, rr.Code, eh.last(), want)
		}
	}
	if rr := get(app.Handler(), "/di-error"); rr.Code != 599 || !errors.Is(eh.last(), returned) {
		t.Errorf("GET /di-error = %d, error handler got %v; want the returned error", rr.Code, eh.last())
	}
}

// Docs: core/handlers.mdx#di-aware-handlers
// "The signature rules are checked when the route is registered. `Handle` panics
// when the handler:
// - is not a function (an `http.Handler` value included; pass its `ServeHTTP`
// method instead),
// - has no parameters, or a first parameter that is not exactly `gas.Context`,
// - does not return exactly one result whose type implements `error` (so
// `func(gas.Context) *gas.Error` is accepted)."
func TestSignatureRules(t *testing.T) {
	t.Parallel()
	type embedsContext struct{ gas.Context }
	bad := map[string]any{
		"http.Handler value":       http.FileServerFS(fstest.MapFS{}),
		"string":                   "handler",
		"no parameters":            func() error { return nil },
		"context.Context first":    func(context.Context) error { return nil },
		"struct embedding Context": func(embedsContext) error { return nil },
		"Context not first":        func(int, gas.Context) error { return nil },
		"no result":                func(gas.Context) {},
		"two results":              func(gas.Context) (int, error) { return 0, nil },
		"non-error result":         func(gas.Context) string { return "" },
	}
	for name, h := range bad {
		r := gas.NewApp().Router()
		mustPanic(t, name, func() { r.Get(nil, "/x", h) })
	}
	fs := http.FileServerFS(fstest.MapFS{})
	gas.NewApp().Router().Get(nil, "/static/*", fs.ServeHTTP) // must not panic
}

// Docs: core/handlers.mdx#di-aware-handlers
// "does not return exactly one result whose type implements `error` (so
// `func(gas.Context) *gas.Error` is accepted)."
func TestConcreteErrorResultAccepted(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Get(nil, "/nil", func(ctx gas.Context) *gas.Error {
		_ = ctx.NoContent()
		return nil
	})
	app.Router().Get(nil, "/err", func(gas.Context) *gas.Error { return gas.NotFound("gone") })
	start(t, app)
	if rr := get(app.Handler(), "/nil"); rr.Code != http.StatusNoContent || rr.Body.Len() != 0 {
		t.Errorf("GET /nil = %d %q, want 204 with no error body (nil *gas.Error is no error)", rr.Code, rr.Body.String())
	}
	if rr := get(app.Handler(), "/err"); rr.Code != http.StatusNotFound {
		t.Errorf("GET /err = %d, want 404 from the returned *gas.Error", rr.Code)
	}
	r := gas.NewApp().Router()
	mustPanic(t, "non-error result", func() { r.Get(nil, "/x", func(gas.Context) string { return "" }) })
	mustPanic(t, "*gas.Error plus another result", func() {
		r.Get(nil, "/y", func(gas.Context) (*gas.Error, int) { return nil, 0 })
	})
}

type (
	single    struct{ n int }
	scopedA   struct{ n int }
	scopedB   struct{ a *scopedA }
	transient struct{ n int }
	closer    struct{ closed *int }
)

func (*closer) Name() string                  { return "closer" }
func (*closer) Init(context.Context) error    { return nil }
func (c *closer) Close(context.Context) error { *c.closed++; return nil }

// Docs: core/handlers.mdx#di-aware-handlers
// "On each request, the handler resolves its dependencies from that request's
// [scope](/gas/core/request-scopes/) by parameter type, in order, and then calls
// the handler. The lifetime of each registration decides what it gets: a
// singleton is the one shared instance, a scoped service is built once for the
// request and shared by everything that resolves it during that request, and a
// transient service is built fresh for each parameter. Scoped services that
// implement `gas.Service` are closed when the request ends."
func TestDependencyLifetimes(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var order []string
	note := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }
	counter := 0
	closed := 0
	app := gas.NewApp(
		gas.WithSingletonService[*single](func() *single { note("single"); counter++; return &single{n: counter} }),
		gas.WithScopedService[*scopedA](func() *scopedA { note("scopedA"); counter++; return &scopedA{n: counter} }),
		gas.WithScopedService[*scopedB](func(a *scopedA) *scopedB { note("scopedB"); return &scopedB{a: a} }),
		gas.WithTransientService[*transient](func() *transient { note("transient"); counter++; return &transient{n: counter} }),
		gas.WithScopedService[*closer](func() *closer { return &closer{closed: &closed} }),
	)
	type seen struct {
		single         *single
		a1, a2         *scopedA
		b              *scopedB
		t1, t2         *transient
		closedInHandle int
	}
	var reqs []seen
	app.Router().Get(nil, "/r", func(ctx gas.Context, b *scopedB, s *single, a1 *scopedA, t1 *transient, t2 *transient, a2 *scopedA, _ *closer) error {
		reqs = append(reqs, seen{single: s, a1: a1, a2: a2, b: b, t1: t1, t2: t2, closedInHandle: closed})
		return ctx.NoContent()
	})
	start(t, app)
	mu.Lock()
	order = nil // drop what Start built
	mu.Unlock()
	get(app.Handler(), "/r")
	mu.Lock()
	firstOrder := append([]string(nil), order...)
	mu.Unlock()
	get(app.Handler(), "/r")
	if len(reqs) != 2 {
		t.Fatalf("handler ran %d times, want 2", len(reqs))
	}
	r1, r2 := reqs[0], reqs[1]
	if r1.single != r2.single {
		t.Error("singleton differs between requests")
	}
	if r1.a1 != r1.a2 || r1.b.a != r1.a1 {
		t.Error("scoped service not shared within one request")
	}
	if r1.a1 == r2.a1 {
		t.Error("scoped service shared across requests")
	}
	if r1.t1 == r1.t2 {
		t.Error("transient shared between two parameters")
	}
	if want := []string{"scopedA", "scopedB", "transient", "transient"}; strings.Join(firstOrder, ",") != strings.Join(want, ",") {
		t.Errorf("constructors ran %v on the first request, want %v (parameter order, scopedB's own scopedA first)", firstOrder, want)
	}
	if r1.closedInHandle != 0 || closed != 2 {
		t.Errorf("scoped Service closed %d times during the handler and %d after two requests, want 0 and 2", r1.closedInHandle, closed)
	}
}

// Docs: core/handlers.mdx#di-aware-handlers
// "Parameter types are matched against registrations by exact type. A parameter
// of type `gas.CacheProvider` needs a registration under `gas.CacheProvider`; a
// concrete type registered under its own type does not satisfy it."
func TestExactTypeMatch(t *testing.T) {
	t.Parallel()
	h := func(ctx gas.Context, _ gas.CacheProvider) error { return ctx.NoContent() }

	concrete := gas.NewApp(gas.WithServiceInstance(&cachetest.MockCache{}))
	concrete.Router().Get(nil, "/x", h)
	if err := concrete.Start(t.Context()); err == nil {
		_ = concrete.Shutdown(context.Background())
		t.Error("Start succeeded with only *cachetest.MockCache registered for a gas.CacheProvider parameter")
	}

	iface := gas.NewApp(gas.WithServiceInstance[gas.CacheProvider](&cachetest.MockCache{}))
	iface.Router().Get(nil, "/x", h)
	start(t, iface)
	if rr := get(iface.Handler(), "/x"); rr.Code != http.StatusNoContent {
		t.Errorf("GET /x = %d, want 204", rr.Code)
	}
}

type NoteStore struct{}

// Docs: core/handlers.mdx#missing-dependencies-fail-at-startup
// "`Start` checks every DI-aware handler's parameter types against the container
// after all services have run `Init`. If one is not registered, `Start` returns an
// error that names the route, its owner, and the type, and the app never serves
// traffic ... gas: handler GET /x (service "gas/router"): dependency *main.NoteStore is not registered in the container"
func TestMissingDependencyFailsStart(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Get(nil, "/x", func(ctx gas.Context, _ *NoteStore) error { return ctx.NoContent() })
	err := app.Start(t.Context())
	want := `gas: handler GET /x (service "gas/router"): dependency *corehandlers_test.NoteStore is not registered in the container`
	if err == nil || err.Error() != want {
		t.Fatalf("Start = %v, want %s", err, want)
	}
	if rr := get(app.Handler(), "/x"); rr.Code == http.StatusNoContent {
		t.Error("the route served after Start failed")
	}

	// A route a service registers in its Init is checked too, and names that
	// service.
	late := gas.NewApp(withSvc[kA]("notes", func(s *svc[kA]) {
		s.router.Post(s, "/notes", func(ctx gas.Context, _ *NoteStore) error { return ctx.NoContent() })
	}))
	err = late.Start(t.Context())
	want = `gas: handler POST /notes (service "notes"): dependency *corehandlers_test.NoteStore is not registered in the container`
	if err == nil || err.Error() != want {
		t.Fatalf("Start with a route from Init = %v, want %s", err, want)
	}
}

// Docs: core/handlers.mdx#missing-dependencies-fail-at-startup
// "and the app never serves
// traffic"
func TestMissingDependencyRunServesNothing(t *testing.T) {
	t.Parallel()
	port := freePort(t)
	app := gas.NewApp(gas.WithServiceInstance(mustConfig(t, map[string]any{
		"server": map[string]any{"host": "127.0.0.1", "port": port},
	})))
	app.Router().Get(nil, "/x", func(ctx gas.Context, _ *NoteStore) error { return ctx.NoContent() })
	done := make(chan error, 1)
	go func() { done <- app.Run() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil with a missing handler dependency")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return; it may be serving")
	}
	if resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/x"); err == nil {
		_ = resp.Body.Close()
		t.Error("something answered on the app's port after Run failed")
	}
}

type failing struct{}

var errCtor = errors.New("constructor failed")

// Docs: core/handlers.mdx#missing-dependencies-fail-at-startup
// "The check confirms that each type is registered; it does not build anything. A
// dependency whose constructor fails at request time sends a wrapped
// `gas: resolving <type>: ...` error to the error handler, which the default
// handler renders as a `500`."
func TestCheckBuildsNothing(t *testing.T) {
	t.Parallel()
	built := 0
	h := func(ctx gas.Context, _ *failing) error { return ctx.NoContent() }
	ctor := func() (*failing, error) { built++; return nil, errCtor }

	eh := &captured{}
	app := gas.NewApp(gas.WithScopedService[*failing](ctor), gas.WithErrorHandler(eh.handler))
	app.Router().Get(nil, "/x", h)
	start(t, app)
	if built != 0 {
		t.Errorf("Start built the dependency %d times, want 0", built)
	}
	get(app.Handler(), "/x")
	err := eh.last()
	if err == nil || !strings.HasPrefix(err.Error(), "gas: resolving *corehandlers_test.failing: ") || !errors.Is(err, errCtor) {
		t.Errorf("error handler got %v, want gas: resolving *corehandlers_test.failing: ... wrapping the constructor error", err)
	}

	plain := gas.NewApp(gas.WithScopedService[*failing](ctor))
	plain.Router().Get(nil, "/x", h)
	start(t, plain)
	if rr := get(plain.Handler(), "/x"); rr.Code != http.StatusInternalServerError {
		t.Errorf("default error handler status = %d, want 500", rr.Code)
	}
}

// Docs: core/handlers.mdx#missing-dependencies-fail-at-startup
// "The container always holds `*gas.Router`, `*gas.EventBus`,
// `gas.HealthProvider`, and `gas.ReadyProvider`, so a handler can take any of them
// without registering anything."
func TestBuiltInDependencies(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	var gotRouter *gas.Router
	app.Router().Get(nil, "/x", func(ctx gas.Context, r *gas.Router, b *gas.EventBus, h gas.HealthProvider, rd gas.ReadyProvider) error {
		gotRouter = r
		if b == nil || h == nil || rd == nil {
			return errors.New("nil built-in dependency")
		}
		return ctx.NoContent()
	})
	start(t, app)
	if rr := get(app.Handler(), "/x"); rr.Code != http.StatusNoContent || gotRouter != app.Router() {
		t.Errorf("GET /x = %d, router %p; want 204 and the app's router", rr.Code, gotRouter)
	}
}
