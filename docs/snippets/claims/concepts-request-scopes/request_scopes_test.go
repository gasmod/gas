package conceptsrequestscopes_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/gasmod/gas"
)

// txLog is a scoped Service that counts its lifecycle calls.
type txLog struct {
	inits, closes *atomic.Int32
	id            int32
	onClose       func() error
	rec           *recorder
	name          string
}

func (l *txLog) Name() string { return "txlog" }
func (l *txLog) Init(context.Context) error {
	l.inits.Add(1)
	return nil
}
func (l *txLog) Close(context.Context) error {
	l.closes.Add(1)
	if l.rec != nil {
		l.rec.add("close %s", l.name)
	}
	if l.onClose != nil {
		return l.onClose()
	}
	return nil
}

type counters struct{ inits, closes, built atomic.Int32 }

func newTxLog(c *counters) func() *txLog {
	return func() *txLog { return &txLog{inits: &c.inits, closes: &c.closes, id: c.built.Add(1)} }
}

func startApp(t *testing.T, opts ...gas.Option) *gas.App {
	t.Helper()
	app := gas.NewApp(opts...)
	return app
}

func start(t *testing.T, app *gas.App) {
	t.Helper()
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
}

func serve(app *gas.App, method, path string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(method, path, nil))
	return rr
}

// Docs: concepts/request-scopes.mdx#_top
// "An `App` installs middleware that opens a DI `Scope` for every HTTP request."
func TestAppOpensScopePerRequest(t *testing.T) {
	t.Parallel()
	app := startApp(t)
	var scopes []*gas.Scope
	app.Router().Handle(nil, http.MethodGet, "/x", func(w http.ResponseWriter, r *http.Request) {
		scopes = append(scopes, gas.RequestScope(r))
	})
	start(t, app)
	serve(app, http.MethodGet, "/x")
	serve(app, http.MethodGet, "/x")
	if len(scopes) != 2 || scopes[0] == nil || scopes[1] == nil || scopes[0] == scopes[1] {
		t.Fatalf("scopes = %v, want a distinct non-nil scope per request", scopes)
	}
}

// Docs: concepts/request-scopes.mdx#_top
// "Services registered as scoped get a fresh instance per request: `Init` runs on first resolution inside the scope, and `Close` runs when the request finishes."
func TestScopedInstancePerRequest(t *testing.T) {
	t.Parallel()
	c := &counters{}
	app := startApp(t, gas.WithScopedService[*txLog](newTxLog(c)))
	var ids []int32
	var closesDuringHandler int32
	app.Router().Handle(nil, http.MethodGet, "/x", func(w http.ResponseWriter, r *http.Request) {
		first := gas.MustResolveFromRequestScope[*txLog](r)
		second := gas.MustResolveFromRequestScope[*txLog](r)
		if first != second {
			w.WriteHeader(http.StatusConflict)
		}
		ids = append(ids, first.id)
		closesDuringHandler = c.closes.Load()
	})
	start(t, app)

	if rr := serve(app, http.MethodGet, "/x"); rr.Code != http.StatusOK {
		t.Fatalf("two resolutions in one request returned different instances (status %d)", rr.Code)
	}
	if c.inits.Load() != 1 || closesDuringHandler != 0 || c.closes.Load() != 1 {
		t.Fatalf("after one request inits=%d closes(during)=%d closes(after)=%d, want 1, 0, 1",
			c.inits.Load(), closesDuringHandler, c.closes.Load())
	}
	serve(app, http.MethodGet, "/x")
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("instance ids = %v, want a fresh instance per request", ids)
	}
}

// Docs: concepts/request-scopes.mdx#_top
// "cleanup guaranteed even when the handler panics."
func TestScopedCloseRunsWhenHandlerPanics(t *testing.T) {
	t.Parallel()
	c := &counters{}
	app := startApp(t, gas.WithScopedService[*txLog](newTxLog(c)))
	app.Router().Handle(nil, http.MethodGet, "/plain", func(w http.ResponseWriter, r *http.Request) {
		gas.MustResolveFromRequestScope[*txLog](r)
		panic("plain handler blew up")
	})
	app.Router().Handle(nil, http.MethodGet, "/typed", func(_ gas.Context, _ *txLog) error {
		panic("typed handler blew up")
	})
	start(t, app)
	for i, path := range []string{"/plain", "/typed"} {
		func() {
			defer func() { _ = recover() }()
			serve(app, http.MethodGet, path)
		}()
		if got := c.closes.Load(); got != int32(i+1) {
			t.Fatalf("after a panic in %s, Close ran %d times in total, want %d", path, got, i+1)
		}
	}
}

// Docs: concepts/request-scopes.mdx#getting-hold-of-one
// "Declare the dependency as a parameter and the router resolves it from that request's scope"
func TestTypedHandlerGetsScopeInstance(t *testing.T) {
	t.Parallel()
	c := &counters{}
	app := startApp(t, gas.WithScopedService[*txLog](newTxLog(c)))
	var fromScope, fromParam *txLog
	app.Router().UseMiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fromScope = gas.MustResolveFromRequestScope[*txLog](r)
			next.ServeHTTP(w, r)
		})
	})
	app.Router().Handle(nil, http.MethodGet, "/x", func(ctx gas.Context, l *txLog) error {
		fromParam = l
		return ctx.NoContent()
	})
	start(t, app)
	serve(app, http.MethodGet, "/x")
	if fromParam == nil || fromParam != fromScope {
		t.Fatalf("parameter %p, scope %p; want the same instance", fromParam, fromScope)
	}
}

type audit struct{}

// Docs: concepts/request-scopes.mdx#getting-hold-of-one
// "txLog := gas.MustResolveFromRequestScope[*TransactionLog](r) / txLog, err := gas.ResolveFromRequestScope[*TransactionLog](r) // non-panicking ... Both wrap `gas.RequestScope(r)` plus a resolve."
func TestRequestScopeHelpers(t *testing.T) {
	t.Parallel()
	c := &counters{}
	app := startApp(t, gas.WithScopedService[*txLog](newTxLog(c)))
	var resolveErr error
	var mustPanicked, same bool
	app.Router().Handle(nil, http.MethodGet, "/x", func(w http.ResponseWriter, r *http.Request) {
		viaScope := gas.RequestScope(r).MustResolve[*txLog]()
		viaHelper, err := gas.ResolveFromRequestScope[*txLog](r)
		same = err == nil && viaHelper == viaScope && gas.MustResolveFromRequestScope[*txLog](r) == viaScope
		_, resolveErr = gas.ResolveFromRequestScope[*audit](r)
		func() {
			defer func() { mustPanicked = recover() != nil }()
			gas.MustResolveFromRequestScope[*audit](r)
		}()
	})
	start(t, app)
	serve(app, http.MethodGet, "/x")
	if !same {
		t.Fatal("helpers did not return the instance RequestScope(r) resolves")
	}
	if resolveErr == nil {
		t.Fatal("ResolveFromRequestScope of an unregistered type returned no error")
	}
	if !mustPanicked {
		t.Fatal("MustResolveFromRequestScope of an unregistered type did not panic")
	}
}

type (
	first  struct{ txLog }
	second struct{ txLog }
)

// Docs: concepts/request-scopes.mdx#closing-order
// "`Scope.Close(ctx)` closes scoped services in **reverse resolution order** ... A failing `Close` does not stop the rest; the errors come back joined."
func TestScopeCloseOrderAndJoinedErrors(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	var cnt counters
	errFirst, errSecond := errors.New("first close failed"), errors.New("second close failed")
	c := gas.NewServiceContainer()
	c.RegisterScopedService[*first](func() *first {
		return &first{txLog{inits: &cnt.inits, closes: &cnt.closes, rec: rec, name: "first", onClose: func() error { return errFirst }}}
	})
	c.RegisterScopedService[*second](func() *second {
		return &second{txLog{inits: &cnt.inits, closes: &cnt.closes, rec: rec, name: "second", onClose: func() error { return errSecond }}}
	})
	if err := c.BuildAll(t.Context()); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
	scope := c.NewScope(t.Context())
	scope.MustResolve[*first]()
	scope.MustResolve[*second]()
	err := scope.Close(context.Background())
	if got, want := rec.list(), []string{"close second", "close first"}; !slices.Equal(got, want) {
		t.Fatalf("close order = %v, want %v", got, want)
	}
	if !errors.Is(err, errFirst) || !errors.Is(err, errSecond) {
		t.Fatalf("Scope.Close error = %v, want both Close errors joined", err)
	}
}

// Docs: concepts/request-scopes.mdx#closing-order
// "In an `App`, the response is already written by then, so the request-scope middleware logs them as `request scope close failed`, with the request's method and path."
func TestRequestScopeCloseFailureLogged(t *testing.T) {
	t.Parallel()
	sink, logger := newSink()
	var cnt counters
	app := startApp(t,
		gas.WithSingletonService[gas.Logger](logger),
		gas.WithScopedService[*txLog](func() *txLog {
			return &txLog{inits: &cnt.inits, closes: &cnt.closes, onClose: func() error { return errors.New("flush failed 91c2") }}
		}),
	)
	app.Router().Handle(nil, http.MethodPost, "/orders/42", func(ctx gas.Context, _ *txLog) error {
		return ctx.NoContent()
	})
	start(t, app)
	if rr := serve(app, http.MethodPost, "/orders/42"); rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want the handler's 204 to stand", rr.Code)
	}
	lines := sink.find(t, "request scope close failed")
	if len(lines) != 1 {
		t.Fatalf("got %d %q lines, want 1; log:\n%s", len(lines), "request scope close failed", sink.String())
	}
	if !hasValue(lines[0], http.MethodPost) || !hasValue(lines[0], "/orders/42") {
		t.Fatalf("log line %v does not carry the request's method and path", lines[0])
	}
}

type holder struct{}

// Docs: concepts/request-scopes.mdx#closing-order
// "Registering a singleton that depends on a scoped service is rejected at startup with `captive dependency: singleton X depends on scoped Y`."
func TestCaptiveDependencyMessage(t *testing.T) {
	t.Parallel()
	var cnt counters
	w := gas.NewWorker(
		gas.WithScopedService[*txLog](newTxLog(&cnt)),
		gas.WithSingletonService[*holder](func(*txLog) *holder { return &holder{} }),
	)
	err := w.Start(t.Context())
	if err == nil {
		t.Fatal("Start accepted a singleton holding a scoped service")
	}
	re := regexp.MustCompile(`captive dependency: singleton \S*holder depends on scoped \S*txLog`)
	if !re.MatchString(err.Error()) {
		t.Fatalf("error %q does not match %s", err, re)
	}
}

// Docs: concepts/request-scopes.mdx#outside-http
// "scope := container.NewScope(ctx) / defer scope.Close(ctx) / svc, err := scope.Resolve[*MyScopedService]()"
func TestExplicitScopeOutsideHTTP(t *testing.T) {
	t.Parallel()
	var cnt counters
	w := gas.NewWorker(gas.WithScopedService[*txLog](newTxLog(&cnt)))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	scope := w.ServiceContainer().NewScope(t.Context())
	svc, err := scope.Resolve[*txLog]()
	if err != nil || svc == nil {
		t.Fatalf("Resolve = %v, %v", svc, err)
	}
	if err := scope.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if cnt.inits.Load() != 1 || cnt.closes.Load() != 1 {
		t.Fatalf("inits=%d closes=%d, want 1 and 1", cnt.inits.Load(), cnt.closes.Load())
	}
}

// Docs: concepts/request-scopes.mdx#outside-http
// "Code calling `gas.RequestScope(r)` on a request built from that context will find it"
func TestWithRequestScope(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	scope := c.NewScope(t.Context())
	ctx := gas.WithRequestScope(context.Background(), scope)
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	if got := gas.RequestScope(r); got != scope {
		t.Fatalf("RequestScope(r) = %p, want the attached scope %p", got, scope)
	}
}

// Docs: concepts/request-scopes.mdx#choosing-a-lifetime
// "transients cannot implement `Service` and are never closed."
func TestTransientCannotImplementService(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("registering a transient Service did not panic")
		}
	}()
	gas.NewServiceContainer().RegisterTransientService[*txLog](func() *txLog { return &txLog{} })
}
