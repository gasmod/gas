package corerequestscopes_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// Docs: core/request-scopes.mdx#_top
// "A scoped service has one instance per `gas.Scope`. An App opens a scope for
// every HTTP request, so a scoped service is effectively per-request: built the
// first time something in the request asks for it, shared by everything else in
// that request, and closed when the request ends."
func TestScopedInstancePerRequest(t *testing.T) {
	t.Parallel()
	tr := &tracker{}
	app := gas.NewApp(gas.WithScopedService[*scoped[kA]](newScoped[kA](tr)))
	var ids []int32
	var closedDuring int32
	app.Router().Get(nil, "/use", func(w http.ResponseWriter, r *http.Request) {
		first := gas.MustResolveFromRequestScope[*scoped[kA]](r)
		if gas.MustResolveFromRequestScope[*scoped[kA]](r) != first {
			w.WriteHeader(http.StatusConflict)
			return
		}
		ids = append(ids, first.id)
		closedDuring = tr.closes.Load()
	})
	app.Router().Get(nil, "/skip", func(w http.ResponseWriter, _ *http.Request) {})
	start(t, app)

	serve(app.Handler(), http.MethodGet, "/skip")
	if tr.built.Load() != 0 {
		t.Fatalf("a request that never asked for the scoped service built %d instances", tr.built.Load())
	}
	if rr := serve(app.Handler(), http.MethodGet, "/use"); rr.Code != http.StatusOK {
		t.Fatalf("two resolutions in one request gave different instances (status %d)", rr.Code)
	}
	if closedDuring != 0 || tr.closes.Load() != 1 {
		t.Fatalf("closes during handler=%d after=%d, want 0 then 1", closedDuring, tr.closes.Load())
	}
	serve(app.Handler(), http.MethodGet, "/use")
	if len(ids) != 2 || ids[0] == ids[1] || tr.built.Load() != 2 {
		t.Fatalf("instance ids = %v (built %d), want one new instance per request", ids, tr.built.Load())
	}
}

// Docs: core/request-scopes.mdx#a-scoped-service
// "Its `Init` runs on its first resolution in the scope, under
// the same init deadline as a singleton's (in an App its context is the
// request's), and its `Close` runs when the scope closes."
func TestScopedInitContextAndClose(t *testing.T) {
	t.Parallel()
	tr := &tracker{}
	app := gas.NewApp(
		gas.WithServiceInitTimeout(time.Minute),
		gas.WithScopedService[*scoped[kA]](newScoped[kA](tr)),
	)
	var initsBefore int32
	app.Router().Get(nil, "/", func(w http.ResponseWriter, r *http.Request) {
		initsBefore = tr.inits.Load()
		gas.MustResolveFromRequestScope[*scoped[kA]](r)
		gas.MustResolveFromRequestScope[*scoped[kA]](r)
	})
	start(t, app)

	ctx := context.WithValue(context.Background(), ctxKey{}, "req-7f1")
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))

	if initsBefore != 0 || tr.inits.Load() != 1 {
		t.Fatalf("Init ran %d times before resolution and %d after, want 0 and 1", initsBefore, tr.inits.Load())
	}
	initCtx, _ := tr.ctxs()
	if _, has := initCtx.Deadline(); !has {
		t.Error("scoped Init context doesn't have a deadline; want one added")
	}
	if initCtx.Value(ctxKey{}) != "req-7f1" {
		t.Error("scoped Init context is not derived from the request's context")
	}
	if tr.closes.Load() != 1 {
		t.Errorf("Close ran %d times after the request, want 1", tr.closes.Load())
	}
}

// plain is a scoped type with neither Init nor Close.
type plain struct{ n int32 }

// Docs: core/request-scopes.mdx#a-scoped-service
// "A scoped type that declares neither method is simply built once per scope."
func TestPlainScopedTypeBuiltOncePerScope(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	var built int32
	c.RegisterScopedService[*plain](func() *plain { built++; return &plain{n: built} })
	s1, s2 := c.NewScope(t.Context()), c.NewScope(t.Context())
	a, b := s1.MustResolve[*plain](), s1.MustResolve[*plain]()
	other := s2.MustResolve[*plain]()
	if a != b || a == other || built != 2 {
		t.Fatalf("same scope equal=%v, across scopes equal=%v, built=%d; want true, false, 2", a == b, a == other, built)
	}
}

type single struct{}

// needsBoth is scoped and depends on a singleton and another scoped type.
type needsBoth struct {
	single *single
	inner  *scoped[kA]
}

// holder is a singleton that would capture a scoped service.
type holder struct{}

// Docs: core/request-scopes.mdx#a-scoped-service
// "A scoped constructor can depend on singletons and on other scoped services. The
// reverse is not allowed: a singleton that depends on a scoped service fails
// startup with `gas.ErrCaptiveDependency`"
func TestScopedDependenciesAndCaptive(t *testing.T) {
	t.Parallel()
	tr := &tracker{}
	w := gas.NewWorker(
		gas.WithSingletonService[*single](func() *single { return &single{} }),
		gas.WithScopedService[*scoped[kA]](newScoped[kA](tr)),
		gas.WithScopedService[*needsBoth](func(s *single, in *scoped[kA]) *needsBoth {
			return &needsBoth{single: s, inner: in}
		}),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Shutdown(context.Background()) })
	scope := w.ServiceContainer().NewScope(t.Context())
	defer func() { _ = scope.Close(context.Background()) }()
	nb, err := scope.Resolve[*needsBoth]()
	if err != nil {
		t.Fatalf("resolve scoped with singleton and scoped deps: %v", err)
	}
	if nb.single != w.ServiceContainer().MustResolve[*single]() || nb.inner != scope.MustResolve[*scoped[kA]]() {
		t.Fatal("dependencies are not the container's singleton and the scope's instance")
	}

	captive := gas.NewWorker(
		gas.WithScopedService[*scoped[kB]](newScoped[kB](&tracker{})),
		gas.WithSingletonService[*holder](func(*scoped[kB]) *holder { return &holder{} }),
	)
	if err := captive.Start(t.Context()); !errors.Is(err, gas.ErrCaptiveDependency) {
		t.Fatalf("Start with a singleton holding a scoped service = %v, want ErrCaptiveDependency", err)
	}
}

// Docs: core/request-scopes.mdx#how-an-app-scopes-requests
// "1. creates a scope with `NewScope(r.Context())`,
// 2. attaches it to the request context and calls the rest of the chain,
// 3. closes the scope after the chain returns, with the request's context minus
// its cancellation, so cleanup still runs after the client has disconnected."
func TestMiddlewareScopeContexts(t *testing.T) {
	t.Parallel()
	tr := &tracker{}
	app := gas.NewApp(gas.WithScopedService[*scoped[kA]](newScoped[kA](tr)))
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ctxKey{}, "req-c4"))
	var scopeSeen bool
	app.Router().Get(nil, "/", func(w http.ResponseWriter, r *http.Request) {
		scopeSeen = gas.RequestScope(r) != nil
		gas.MustResolveFromRequestScope[*scoped[kA]](r)
		cancel() // the client disconnects before the request ends
	})
	start(t, app)
	app.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))

	initCtx, closeCtx := tr.ctxs()
	if !scopeSeen {
		t.Fatal("handler saw no scope on its request")
	}
	if initCtx == nil || initCtx.Value(ctxKey{}) != "req-c4" || initCtx.Err() == nil {
		t.Error("scope was not created from the request's context")
	}
	if closeCtx == nil || closeCtx.Value(ctxKey{}) != "req-c4" {
		t.Fatal("scope Close did not get the request's context values")
	}
	if closeCtx.Err() != nil {
		t.Errorf("scope Close context err = %v, want the request's cancellation removed", closeCtx.Err())
	}
}

// useSvc adds a router middleware from its Init.
type useSvc struct {
	router *gas.Router
	seen   *gas.Scope
}

func (s *useSvc) Name() string { return "use-svc" }
func (s *useSvc) Init(context.Context) error {
	s.router.UseMiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.seen = gas.RequestScope(r)
			next.ServeHTTP(w, r)
		})
	})
	return nil
}
func (s *useSvc) Close(context.Context) error { return nil }

// Docs: core/request-scopes.mdx#how-an-app-scopes-requests
// "The middleware is installed when the App is constructed, before any service's
// `Init` runs, so every middleware a service adds with `Use` runs inside the
// scope."
func TestServiceMiddlewareRunsInsideScope(t *testing.T) {
	t.Parallel()
	svc := &useSvc{}
	app := gas.NewApp(gas.WithSingletonService[*useSvc](func(r *gas.Router) *useSvc { svc.router = r; return svc }))
	var handlerScope *gas.Scope
	app.Router().Get(nil, "/", func(w http.ResponseWriter, r *http.Request) { handlerScope = gas.RequestScope(r) })
	start(t, app)
	if rr := serve(app.Handler(), http.MethodGet, "/"); rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; the service middleware found no scope", rr.Code)
	}
	if svc.seen == nil || svc.seen != handlerScope {
		t.Fatalf("service middleware saw scope %p, handler %p; want the same request scope", svc.seen, handlerScope)
	}
}

// Docs: core/request-scopes.mdx#how-an-app-scopes-requests
// "The scope closes even if the handler panics."
func TestScopeClosesOnPanic(t *testing.T) {
	t.Parallel()
	tr := &tracker{}
	app := gas.NewApp(gas.WithScopedService[*scoped[kA]](newScoped[kA](tr)))
	app.Router().Get(nil, "/plain", func(w http.ResponseWriter, r *http.Request) {
		gas.MustResolveFromRequestScope[*scoped[kA]](r)
		panic("plain handler failed")
	})
	app.Router().Get(nil, "/typed", func(gas.Context, *scoped[kA]) error { panic("typed handler failed") })
	start(t, app)
	for i, path := range []string{"/plain", "/typed"} {
		func() {
			defer func() { _ = recover() }()
			serve(app.Handler(), http.MethodGet, path)
		}()
		if got := tr.closes.Load(); got != int32(i+1) {
			t.Fatalf("after a panic in %s Close ran %d times in total, want %d", path, got, i+1)
		}
	}
}

// Docs: core/request-scopes.mdx#how-an-app-scopes-requests
// "CSRF protection wraps the router from outside, so a request it rejects never
// gets a scope."
func TestCSRFRejectedRequestHasNoScope(t *testing.T) {
	t.Parallel()
	tr := &tracker{}
	var denied, deniedHadScope, routed bool
	app := gas.NewApp(
		gas.WithScopedService[*scoped[kA]](newScoped[kA](tr)),
		gas.WithCSRFDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			denied = true
			func() {
				defer func() { deniedHadScope = recover() == nil }()
				gas.RequestScope(r)
			}()
			w.WriteHeader(http.StatusForbidden)
		})),
	)
	app.Router().UseMiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			routed = true
			next.ServeHTTP(w, r)
		})
	})
	app.Router().Post(nil, "/orders", func(w http.ResponseWriter, r *http.Request) {
		gas.MustResolveFromRequestScope[*scoped[kA]](r)
	})
	start(t, app)
	req := httptest.NewRequest(http.MethodPost, "/orders", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, req)
	if !denied || rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin POST: denied=%v status=%d, want CSRF to reject it", denied, rr.Code)
	}
	if deniedHadScope || routed || tr.built.Load() != 0 || tr.closes.Load() != 0 {
		t.Fatalf("rejected request: scope=%v routed=%v built=%d closed=%d, want no scope at all",
			deniedHadScope, routed, tr.built.Load(), tr.closes.Load())
	}
}

// outer is scoped and built from the scoped inner.
type outer struct {
	inner *scoped[kA]
	rec   *recorder
}

func (o *outer) Name() string               { return "outer" }
func (o *outer) Init(context.Context) error { return nil }
func (o *outer) Close(context.Context) error {
	o.rec.add("close outer (inner closed=%v)", o.inner.tr.closes.Load() > 0)
	return errors.New("outer close failed")
}

// Docs: core/request-scopes.mdx#how-an-app-scopes-requests
// "`Scope.Close` closes the scoped services in the **reverse of the order they
// were resolved**, so a scoped service can still use the scoped dependencies it
// was built from while it closes. A failing `Close` does not stop the others; the
// errors are joined."
func TestScopeCloseReverseOrderJoined(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	errInner := errors.New("inner close failed")
	trA := &tracker{rec: rec, closeErr: errInner}
	trB := &tracker{rec: rec}
	c := gas.NewServiceContainer()
	c.RegisterScopedService[*scoped[kA]](newScoped[kA](trA))
	c.RegisterScopedService[*scoped[kB]](newScoped[kB](trB))
	c.RegisterScopedService[*outer](func(in *scoped[kA]) *outer { return &outer{inner: in, rec: rec} })
	scope := c.NewScope(t.Context())
	scope.MustResolve[*scoped[kB]]()
	scope.MustResolve[*outer]() // resolves inner (kA) first, then outer
	err := scope.Close(context.Background())

	want := []string{
		"close outer (inner closed=false)",
		"close corerequestscopes_test.kA",
		"close corerequestscopes_test.kB",
	}
	if got := rec.list(); !slices.Equal(got, want) {
		t.Fatalf("close order = %v, want %v", got, want)
	}
	if err == nil || !errors.Is(err, errInner) || err.Error() == errInner.Error() {
		t.Fatalf("Scope.Close error = %v, want both Close errors joined", err)
	}
}

// Docs: core/request-scopes.mdx#how-an-app-scopes-requests
// "By then the response has been written, so the middleware
// logs the joined error as `request scope close failed`, with the request's method
// and path, instead of returning it."
func TestScopeCloseFailureLogged(t *testing.T) {
	t.Parallel()
	sink, logger := newSink()
	tr := &tracker{closeErr: errors.New("flush failed 5d1e")}
	app := gas.NewApp(
		gas.WithSingletonService[gas.Logger](logger),
		gas.WithScopedService[*scoped[kA]](newScoped[kA](tr)),
	)
	app.Router().Post(nil, "/orders/9", func(ctx gas.Context, _ *scoped[kA]) error { return ctx.NoContent() })
	start(t, app)
	if rr := serve(app.Handler(), http.MethodPost, "/orders/9"); rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want the handler's 204 to stand", rr.Code)
	}
	recs := sink.find(t, "request scope close failed")
	if len(recs) != 1 {
		t.Fatalf("got %d request scope close failed records, want 1; log:\n%s", len(recs), sink.String())
	}
	line := fmt.Sprint(recs[0])
	for _, want := range []string{http.MethodPost, "/orders/9", "flush failed 5d1e"} {
		if !contains(recs[0], want) {
			t.Errorf("log record %s lacks %q", line, want)
		}
	}
}

// contains reports whether any attribute of rec other than msg contains want.
func contains(rec map[string]any, want string) bool {
	for k, v := range rec {
		if k != "msg" && strings.Contains(fmt.Sprint(v), want) {
			return true
		}
	}
	return false
}

// Docs: core/request-scopes.mdx#in-a-di-aware-handler
// "Declare the scoped service as a parameter, and the router resolves it from that
// request's scope before calling the handler"
func TestTypedHandlerParamFromRequestScope(t *testing.T) {
	t.Parallel()
	tr := &tracker{}
	app := gas.NewApp(gas.WithScopedService[*scoped[kA]](newScoped[kA](tr)))
	var fromMiddleware, fromParam *scoped[kA]
	app.Router().UseMiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fromMiddleware = gas.MustResolveFromRequestScope[*scoped[kA]](r)
			next.ServeHTTP(w, r)
		})
	})
	app.Router().Get(nil, "/", func(ctx gas.Context, a *scoped[kA]) error {
		fromParam = a
		return ctx.NoContent()
	})
	start(t, app)
	serve(app.Handler(), http.MethodGet, "/")
	if fromParam == nil || fromParam != fromMiddleware {
		t.Fatalf("parameter %p, request scope %p; want the same instance", fromParam, fromMiddleware)
	}
}

// failing is scoped and its constructor always fails.
type failing struct{}

var errCtor = errors.New("failing constructor 8a3")

// Docs: core/request-scopes.mdx#in-a-di-aware-handler
// "If a parameter fails to resolve at request time, for example because a scoped
// constructor returned an error, the handler is not called and the error goes to
// the router's error handler; the default one renders it as a 500."
func TestParamResolveFailure(t *testing.T) {
	t.Parallel()
	build := func(opts ...gas.Option) (*gas.App, *bool) {
		called := new(bool)
		app := gas.NewApp(append(opts, gas.WithScopedService[*failing](func() (*failing, error) { return nil, errCtor }))...)
		app.Router().Get(nil, "/", func(ctx gas.Context, _ *failing) error {
			*called = true
			return ctx.NoContent()
		})
		start(t, app)
		return app, called
	}

	app, called := build()
	if rr := serve(app.Handler(), http.MethodGet, "/"); rr.Code != http.StatusInternalServerError || *called {
		t.Fatalf("default handler: status %d, handler called %v; want 500 and not called", rr.Code, *called)
	}

	var got error
	app, called = build(gas.WithErrorHandler(func(ctx gas.Context, err error) {
		got = err
		_ = ctx.Text(http.StatusTeapot, "custom")
	}))
	if rr := serve(app.Handler(), http.MethodGet, "/"); rr.Code != http.StatusTeapot || *called {
		t.Fatalf("custom handler: status %d, handler called %v; want 418 and not called", rr.Code, *called)
	}
	if !errors.Is(got, errCtor) {
		t.Fatalf("error handler got %v, want the constructor's error", got)
	}
}

type unregistered struct{}

// Docs: core/request-scopes.mdx#in-a-di-aware-handler
// "Whether each parameter type is
// registered at all is checked at startup."
func TestParamRegistrationCheckedAtStartup(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Get(nil, "/", func(ctx gas.Context, _ *unregistered) error { return ctx.NoContent() })
	err := app.Start(t.Context())
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	if err == nil {
		t.Fatal("Start accepted a handler parameter whose type is not registered")
	}
}

// Docs: core/request-scopes.mdx#in-a-plain-handler-or-middleware
// "`gas.ResolveFromRequestScope[T](r)` | `(T, error)` /
// `gas.MustResolveFromRequestScope[T](r)` | `T`, and panics if it cannot resolve /
// `gas.RequestScope(r)` | The request's `*gas.Scope`, to resolve several values from it"
func TestRequestScopeHelpers(t *testing.T) {
	t.Parallel()
	var resolve func(*http.Request) (*scoped[kA], error) = gas.ResolveFromRequestScope[*scoped[kA]] //nolint:staticcheck // asserts the documented signature
	var must func(*http.Request) *scoped[kA] = gas.MustResolveFromRequestScope[*scoped[kA]]         //nolint:staticcheck // asserts the documented signature
	var scopeOf func(*http.Request) *gas.Scope = gas.RequestScope                                   //nolint:staticcheck // asserts the documented signature

	tr := &tracker{}
	app := gas.NewApp(gas.WithScopedService[*scoped[kA]](newScoped[kA](tr)))
	var same bool
	var unregErr error
	var mustPanicked bool
	app.Router().Get(nil, "/", func(w http.ResponseWriter, r *http.Request) {
		viaScope := scopeOf(r).MustResolve[*scoped[kA]]()
		viaResolve, err := resolve(r)
		same = err == nil && viaResolve == viaScope && must(r) == viaScope
		_, unregErr = gas.ResolveFromRequestScope[*unregistered](r)
		func() {
			defer func() { mustPanicked = recover() != nil }()
			gas.MustResolveFromRequestScope[*unregistered](r)
		}()
	})
	start(t, app)
	serve(app.Handler(), http.MethodGet, "/")
	if !same {
		t.Error("the three helpers do not reach the same request-scope instance")
	}
	if unregErr == nil {
		t.Error("ResolveFromRequestScope of an unregistered type returned no error")
	}
	if !mustPanicked {
		t.Error("MustResolveFromRequestScope of an unregistered type did not panic")
	}
}

// Docs: core/request-scopes.mdx#in-a-plain-handler-or-middleware
// "All three panic if the request carries no scope, which is the case for a request
// that did not come through an App's router."
func TestHelpersPanicWithoutScope(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	mustPanic(t, "ResolveFromRequestScope", func() { _, _ = gas.ResolveFromRequestScope[*scoped[kA]](r) })
	mustPanic(t, "MustResolveFromRequestScope", func() { gas.MustResolveFromRequestScope[*scoped[kA]](r) })
	mustPanic(t, "RequestScope", func() { gas.RequestScope(r) })
}

// fresh is a transient value object.
type fresh struct{ n int }

// Docs: core/request-scopes.mdx#in-a-plain-handler-or-middleware
// "Within a scope, a scoped type resolves to the scope's instance, a singleton
// resolves to the container's instance, and a transient is built fresh each time."
func TestResolutionByLifetimeInScope(t *testing.T) {
	t.Parallel()
	tr := &tracker{}
	c := gas.NewServiceContainer()
	c.RegisterSingletonService[*single](func() *single { return &single{} })
	c.RegisterScopedService[*scoped[kA]](newScoped[kA](tr))
	n := 0
	c.RegisterTransientService[*fresh](func() *fresh { n++; return &fresh{n: n} })
	scope := c.NewScope(t.Context())
	defer func() { _ = scope.Close(context.Background()) }()
	if s1, s2 := scope.MustResolve[*scoped[kA]](), scope.MustResolve[*scoped[kA]](); s1 != s2 {
		t.Error("scoped type resolved to two instances in one scope")
	}
	if scope.MustResolve[*single]() != c.MustResolve[*single]() {
		t.Error("singleton in a scope is not the container's instance")
	}
	if f1, f2 := scope.MustResolve[*fresh](), scope.MustResolve[*fresh](); f1 == f2 {
		t.Error("transient resolved twice in a scope returned the same instance")
	}
}

// Docs: core/request-scopes.mdx#outside-http
// "The
// context passed to `NewScope` is the one each scoped service's `Init` receives,
// and closing the scope is your job"
func TestManualScopeContextAndClose(t *testing.T) {
	t.Parallel()
	tr := &tracker{}
	c := gas.NewServiceContainer()
	c.RegisterScopedService[*scoped[kA]](newScoped[kA](tr))
	ctx := context.WithValue(t.Context(), ctxKey{}, "job-31")
	scope := c.NewScope(ctx)
	if _, err := scope.Resolve[*scoped[kA]](); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	initCtx, _ := tr.ctxs()
	if initCtx == nil || initCtx.Value(ctxKey{}) != "job-31" {
		t.Fatal("scoped Init did not receive the context passed to NewScope")
	}
	if tr.closes.Load() != 0 {
		t.Fatal("scoped service closed before Scope.Close was called")
	}
	if err := scope.Close(ctx); err != nil || tr.closes.Load() != 1 {
		t.Fatalf("Scope.Close = %v, closes = %d; want nil and 1", err, tr.closes.Load())
	}
}

// Docs: core/request-scopes.mdx#outside-http
// "Resolving a scoped service from the container itself, rather than from a
// scope, fails with an error that wraps `gas.ErrScopeRequired`:"
// "scoped service *main.Audit cannot be resolved outside a scope; use container.NewScope(ctx)"
// The page prints the snippet's type; this checks the same text for a local type.
func TestContainerResolveOfScopedFails(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	c.RegisterScopedService[*scoped[kA]](newScoped[kA](&tracker{}))
	_, err := c.Resolve[*scoped[kA]]()
	if !errors.Is(err, gas.ErrScopeRequired) {
		t.Fatalf("container Resolve of a scoped type = %v, want ErrScopeRequired", err)
	}
	want := fmt.Sprintf("scoped service %v cannot be resolved outside a scope; use container.NewScope(ctx)",
		reflect.TypeFor[*scoped[kA]]())
	if err.Error() != want {
		t.Fatalf("error text = %q, want %q", err.Error(), want)
	}
}

// Docs: core/request-scopes.mdx#outside-http
// "To run code that expects a request scope, attach a scope to a context with
// `gas.WithRequestScope(ctx, scope)`. `gas.RequestScope(r)` finds it on any
// request built with that context"
func TestWithRequestScope(t *testing.T) {
	t.Parallel()
	tr := &tracker{}
	c := gas.NewServiceContainer()
	c.RegisterScopedService[*scoped[kA]](newScoped[kA](tr))
	scope := c.NewScope(t.Context())
	r := httptest.NewRequestWithContext(gas.WithRequestScope(t.Context(), scope), http.MethodGet, "/", nil)
	if gas.RequestScope(r) != scope {
		t.Fatal("RequestScope did not find the attached scope")
	}
	if gas.MustResolveFromRequestScope[*scoped[kA]](r) != scope.MustResolve[*scoped[kA]]() {
		t.Fatal("handler-side resolution did not use the attached scope")
	}
}

// Docs: core/request-scopes.mdx#choosing-a-lifetime
// "a transient cannot
// implement `Service`, so nothing ever closes it."
func TestTransientCannotImplementService(t *testing.T) {
	t.Parallel()
	mustPanic(t, "RegisterTransientService of a Service", func() {
		gas.NewServiceContainer().RegisterTransientService[*scoped[kA]](newScoped[kA](&tracker{}))
	})
}
