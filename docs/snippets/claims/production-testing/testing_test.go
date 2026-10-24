package productiontesting_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/authtest"
	"github.com/gasmod/gas/cache/cachetest"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/email/emailtest"
	"github.com/gasmod/gas/migrate/migratetest"
	"github.com/gasmod/gas/queue/queuetest"
	"github.com/gasmod/gas/storage/storagetest"
	"github.com/gasmod/gas/template/templatetest"
	"github.com/gasmod/gas/ui/uitest"
)

// mocks is the page's table: every mock it names, by package.
func mocks() map[string]any {
	return map[string]any{
		"authtest.MockAuthenticator":       &authtest.MockAuthenticator{},
		"authtest.MockAuthorizer":          &authtest.MockAuthorizer{},
		"authtest.MockRevoker":             &authtest.MockRevoker{},
		"cachetest.MockCache":              &cachetest.MockCache{},
		"configtest.MockConfig":            &configtest.MockConfig{},
		"emailtest.MockEmail":              &emailtest.MockEmail{},
		"migratetest.MockMigrationManager": &migratetest.MockMigrationManager{},
		"queuetest.MockQueue":              &queuetest.MockQueue{},
		"storagetest.MockProvider":         &storagetest.MockProvider{},
		"storagetest.MockPresigner":        &storagetest.MockPresigner{},
		"templatetest.MockTemplate":        &templatetest.MockTemplate{},
		"uitest.MockUI":                    &uitest.MockUI{},
	}
}

// Docs: production/testing.mdx#mock-packages
// "These modules ship mocks in a `*test` package:"
// Docs: production/testing.mdx#mock-packages
// "| [auth](/gas/modules/auth/) | `github.com/gasmod/gas/auth/authtest` | `MockAuthenticator`, `MockAuthorizer`, `MockRevoker` |"
// Docs: production/testing.mdx#mock-packages
// "`database` and `log` have no mock package."
// Each named mock lives at the listed import path, and the modules that ship a
// *test package are exactly the nine in the table.
func TestMockPackages(t *testing.T) {
	t.Parallel()
	for name, m := range mocks() {
		pkg, typ, _ := strings.Cut(name, ".")
		rt := reflect.TypeOf(m).Elem()
		mod := strings.TrimSuffix(pkg, "test")
		if want := "github.com/gasmod/gas/" + mod + "/" + pkg; rt.PkgPath() != want || rt.Name() != typ {
			t.Errorf("%s is %s.%s, want %s.%s", name, rt.PkgPath(), rt.Name(), want, typ)
		}
	}
	shipping := map[string]bool{}
	for _, mod := range []string{"auth", "cache", "config", "database", "email", "log", "migrate", "queue", "storage", "template", "ui"} {
		out, err := exec.CommandContext(t.Context(), "go", "list", "github.com/gasmod/gas/"+mod+"/...").CombinedOutput()
		if err != nil {
			t.Fatalf("go list %s: %v\n%s", mod, err, out)
		}
		for _, pkg := range strings.Fields(string(out)) {
			if strings.HasSuffix(pkg, "test") && !strings.Contains(pkg, "/internal/") {
				shipping[mod] = true
			}
		}
	}
	for _, mod := range []string{"database", "log"} {
		if shipping[mod] {
			t.Errorf("%s ships a *test package", mod)
		}
	}
	if len(shipping) != 9 {
		t.Errorf("modules with a *test package = %v, want the nine in the table", shipping)
	}
}

// Docs: production/testing.mdx#mock-packages
// "For a logger, core's
// `gas.NopLogger` discards everything; `&gas.NopLogger{}` is ready to use."
func TestNopLogger(t *testing.T) {
	t.Parallel()
	var l gas.Logger = &gas.NopLogger{}
	l.Trace("t").Str("k", "v").Send()
	l.Info("i").Err("e", context.Canceled).Send()
	l.Error("e").Send()
	sub := l.With().Str("k", "v").Logger()
	sub.Warn("w").Send()
	sub.Flush()
	if _, ok := sub.(*gas.NopLogger); !ok {
		t.Fatalf("With().Logger() = %T, want a *gas.NopLogger", sub)
	}
}

// recordedMethods lists the methods a mock records: everything but the
// bookkeeping methods.
func recordedMethods(v reflect.Value) []reflect.Method {
	var out []reflect.Method
	for m := range v.Type().Methods() {
		if m.Name != "CallCount" && m.Name != "Reset" {
			out = append(out, m)
		}
	}
	return out
}

// zeroArgs builds harmless arguments for a method: a live context, empty
// non-nil values for pointers, slices and maps, zero values otherwise.
func zeroArgs(m reflect.Method) []reflect.Value {
	typ := m.Type
	args := make([]reflect.Value, 0, typ.NumIn()-1)
	for i := 1; i < typ.NumIn(); i++ {
		in := typ.In(i)
		if typ.IsVariadic() && i == typ.NumIn()-1 {
			break
		}
		switch {
		case in == reflect.TypeFor[context.Context]():
			args = append(args, reflect.ValueOf(context.Background()))
		case in == reflect.TypeFor[http.ResponseWriter]():
			args = append(args, reflect.ValueOf(httptest.NewRecorder()))
		case in.Kind() == reflect.Pointer:
			args = append(args, reflect.New(in.Elem()))
		case in.Kind() == reflect.Slice:
			args = append(args, reflect.MakeSlice(in, 0, 0))
		case in.Kind() == reflect.Map:
			args = append(args, reflect.MakeMap(in))
		default:
			args = append(args, reflect.Zero(in))
		}
	}
	return args
}

func callCount(v reflect.Value, method string) int {
	return int(v.MethodByName("CallCount").Call([]reflect.Value{reflect.ValueOf(method)})[0].Int())
}

// Docs: production/testing.mdx#mock-packages
// "The mocks share one shape. Each has an `Fn` field per method that the method
// calls when it is set, records every call in its `Calls` slice, and has
// `CallCount(method)` and `Reset()`. They are safe for concurrent use, so a test
// that fans out goroutines can still assert on counts. A mock is usable as its
// zero value"
// Every method of every zero-value mock is called from many goroutines (under
// -race), then again with every Fn set.
func TestMockShape(t *testing.T) {
	t.Parallel()
	for name, mock := range mocks() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			v := reflect.ValueOf(mock)
			calls := v.Elem().FieldByName("Calls")
			if !calls.IsValid() || calls.Kind() != reflect.Slice {
				t.Fatalf("%s has no Calls slice", name)
			}
			for _, required := range []string{"CallCount", "Reset"} {
				if !v.MethodByName(required).IsValid() {
					t.Fatalf("%s has no %s method", name, required)
				}
			}
			methods := recordedMethods(v)
			const goroutines = 16
			var wg sync.WaitGroup
			for range goroutines {
				wg.Go(func() {
					for _, m := range methods {
						v.Method(m.Index).Call(zeroArgs(m))
					}
				})
			}
			wg.Wait()
			for _, m := range methods {
				if got := callCount(v, m.Name); got != goroutines {
					t.Errorf("CallCount(%q) = %d, want %d", m.Name, got, goroutines)
				}
			}
			if got := calls.Len(); got != goroutines*len(methods) {
				t.Errorf("recorded %d calls, want %d", got, goroutines*len(methods))
			}
			v.MethodByName("Reset").Call(nil)
			if got := v.Elem().FieldByName("Calls").Len(); got != 0 {
				t.Errorf("%d calls after Reset, want 0", got)
			}

			for _, m := range methods {
				f := v.Elem().FieldByName(m.Name + "Fn")
				if !f.IsValid() || f.Kind() != reflect.Func {
					t.Errorf("%s has no %sFn field", name, m.Name)
					continue
				}
				var called atomic.Bool
				f.Set(reflect.MakeFunc(f.Type(), func([]reflect.Value) []reflect.Value {
					called.Store(true)
					out := make([]reflect.Value, f.Type().NumOut())
					for i := range out {
						out[i] = reflect.Zero(f.Type().Out(i))
					}
					return out
				}))
				v.Method(m.Index).Call(zeroArgs(m))
				if !called.Load() {
					t.Errorf("%s.%s did not call %sFn", name, m.Name, m.Name)
				}
			}
		})
	}
}

type server struct {
	Host string
	Port int
}

type appSettings struct {
	Server server
	Name   string
}

// Docs: production/testing.mdx#mock-packages
// "`configtest.NewMockConfigWithValues(values)` returns a `MockConfig` whose
// methods delegate to a real configuration seeded with `values`, so `Get`,
// `Find`, and `Bind` behave as they do in production"
// Production here is a config.Config loaded from a JSON file with the same
// values. Values are compared as printed, since JSON decodes numbers as
// float64 where the seeded map holds an int; Bind converts both alike.
func TestMockConfigWithValuesBehavesLikeProduction(t *testing.T) {
	t.Parallel()
	values := map[string]any{"server": map[string]any{"host": "db.local", "port": 5432}, "name": "visits"}
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	prod := config.New(config.WithProvider(providers.NewJSONProvider(
		providers.WithJSONFileFS(fstest.MapFS{"config.json": {Data: raw}}),
		providers.WithJSONFilePath("config.json"),
	)))
	if err := prod.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	mock, err := configtest.NewMockConfigWithValues(values)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"server.host", "server.port", "SERVER.PORT", "name", "missing"} {
		pv, pok := prod.Find(key)
		mv, mok := mock.Find(key)
		if pok != mok || fmt.Sprint(pv) != fmt.Sprint(mv) {
			t.Errorf("Find(%q): production %v %v, mock %v %v", key, pv, pok, mv, mok)
		}
		if p, m := prod.Get(key), mock.Get(key); fmt.Sprint(p) != fmt.Sprint(m) {
			t.Errorf("Get(%q): production %v, mock %v", key, p, m)
		}
	}
	var fromProd, fromMock appSettings
	if err := prod.Bind(&fromProd); err != nil {
		t.Fatalf("production Bind: %v", err)
	}
	if err := mock.Bind(&fromMock); err != nil {
		t.Fatalf("mock Bind: %v", err)
	}
	if fromProd != fromMock || fromMock.Server.Port != 5432 {
		t.Errorf("Bind: production %+v, mock %+v", fromProd, fromMock)
	}
	if mock.CallCount("Bind") != 1 {
		t.Errorf("mock recorded %d Bind calls, want 1", mock.CallCount("Bind"))
	}
}

// visits is a consumer of gas.CacheProvider, like the page's service.
type visits struct{ cache gas.CacheProvider }

// Docs: production/testing.mdx#testing-through-the-app
// "Register each mock with `gas.WithServiceInstance` under the interface the code
// depends on, as the real provider is registered in production. The container
// matches by exact type, so a mock registered under its own type
// (`*cachetest.MockCache`) would not satisfy a `gas.CacheProvider` parameter."
func TestMockRegisteredUnderInterface(t *testing.T) {
	t.Parallel()
	mock := &cachetest.MockCache{}
	var got gas.CacheProvider
	app := gas.NewApp(
		gas.WithServiceInstance[gas.CacheProvider](mock),
		gas.WithSingletonService[*visits](func(c gas.CacheProvider) *visits { got = c; return &visits{cache: c} }),
	)
	startApp(t, app)
	if got != mock {
		t.Fatalf("consumer got %T, want the mock", got)
	}

	wrong := gas.NewApp(
		gas.WithServiceInstance(mock),
		gas.WithSingletonService[*visits](func(c gas.CacheProvider) *visits { return &visits{cache: c} }),
	)
	if err := wrong.Start(t.Context()); !errors.Is(err, gas.ErrNotRegistered) {
		t.Fatalf("Start with the mock under *cachetest.MockCache = %v, want ErrNotRegistered", err)
	}
}

// Docs: production/testing.mdx#testing-through-the-app
// "A
// mock that implements `gas.Service`, as `migratetest.MockMigrationManager` does,
// gets `Init` at startup and `Close` at shutdown like any registered service, and
// a `MockMigrationManager` registered as `gas.MigrationManager` has its
// `RunPending` called by `Start`."
func TestMockMigrationManagerLifecycle(t *testing.T) {
	t.Parallel()
	mm := &migratetest.MockMigrationManager{}
	if _, ok := any(mm).(gas.Service); !ok {
		t.Fatal("MockMigrationManager does not implement gas.Service")
	}
	app := gas.NewApp(gas.WithServiceInstance[gas.MigrationManager](mm))
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if mm.CallCount("Init") != 1 || mm.CallCount("RunPending") != 1 || mm.CallCount("Close") != 0 {
		t.Fatalf("after Start: Init=%d RunPending=%d Close=%d, want 1 1 0",
			mm.CallCount("Init"), mm.CallCount("RunPending"), mm.CallCount("Close"))
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if mm.CallCount("Close") != 1 {
		t.Fatalf("Close called %d times at shutdown, want 1", mm.CallCount("Close"))
	}
}

// Docs: production/testing.mdx#testing-through-the-app
// "`Start` runs the same startup as production (services, migrations, ready hooks)
// and returns without serving or waiting for a signal."
func TestStartReturnsWithoutServing(t *testing.T) {
	t.Parallel()
	port := freePort(t)
	s := &svc{}
	mm := &migratetest.MockMigrationManager{}
	var hook atomic.Bool
	app := gas.NewApp(
		gas.WithServiceInstance(serverConfig(t, port)),
		gas.WithServiceInstance(s),
		gas.WithServiceInstance[gas.MigrationManager](mm),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error { hook.Store(true); return nil }),
	)
	done := make(chan error, 1)
	go func() { done <- app.Start(t.Context()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("start: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return")
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	if inits, _ := s.counts(); inits != 1 || mm.CallCount("RunPending") != 1 || !hook.Load() {
		t.Fatalf("Start ran Init=%d RunPending=%d hook=%v", inits, mm.CallCount("RunPending"), hook.Load())
	}
	if accepting(port) {
		t.Fatal("Start is serving on the port")
	}
}

// Docs: production/testing.mdx#testing-through-the-app
// "`App.Handler()` returns the handler the server would serve: the router behind
// [cross-origin protection](/gas/core/middleware/#cross-origin-protection).
// Calling its `ServeHTTP` with an `httptest.ResponseRecorder` sends a request
// through all of it with no listener"
// Docs: production/testing.mdx#testing-through-the-app
// "Requests built by `httptest.NewRequest` or a Go `http.Client` carry neither a
// `Sec-Fetch-Site` nor an `Origin` header, so cross-origin protection lets them
// through."
func TestHandlerAndCrossOriginProtection(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var seen []http.Header
	app := gas.NewApp()
	app.Router().Post(nil, "/submit", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
	})
	startApp(t, app)
	if !sameHandler(app.Server().Handler, app.Handler()) {
		t.Fatalf("Server().Handler (%T) is not Handler() (%T)", app.Server().Handler, app.Handler())
	}

	cross := httptest.NewRequest(http.MethodPost, "/submit", nil)
	cross.Header.Set("Sec-Fetch-Site", "cross-site")
	cross.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, cross)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST through Handler() = %d, want 403", rr.Code)
	}

	rr = httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/submit", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("httptest.NewRequest POST = %d, want 200", rr.Code)
	}

	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/submit", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("client POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("http.Client POST = %d, want 200", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("handler saw %d requests, want 2", len(seen))
	}
	for i, h := range seen {
		if h.Get("Sec-Fetch-Site") != "" || h.Get("Origin") != "" {
			t.Errorf("request %d carried Sec-Fetch-Site=%q Origin=%q", i, h.Get("Sec-Fetch-Site"), h.Get("Origin"))
		}
	}
}

// sameHandler compares two values that may hold uncomparable dynamic types.
func sameHandler(a, b any) (same bool) {
	defer func() {
		if recover() != nil {
			same = reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
		}
	}()
	return a == b
}

// Docs: production/testing.mdx#testing-through-the-app
// "Error responses decode into `gas.ErrorResponse`"
func TestErrorResponsesDecode(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Get(nil, "/missing", func(gas.Context) error { return gas.NotFound("no such page") })
	startApp(t, app)
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/missing", nil))
	var resp gas.ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rr.Code != http.StatusNotFound || resp.Error == nil || resp.Error.Code != gas.CodeNotFound || resp.Error.Message != "no such page" {
		t.Fatalf("got %d %+v", rr.Code, resp.Error)
	}
}

// Docs: production/testing.mdx#testing-through-the-app
// "When a test needs a real client and network round trip,
// `httptest.NewServer(app.Handler())` serves the same handler on a loopback
// port"
func TestNewServerServesHandler(t *testing.T) {
	t.Parallel()
	app := gas.NewApp()
	app.Router().Get(nil, "/hi", func(ctx gas.Context) error { return ctx.Text(http.StatusOK, "hi") })
	startApp(t, app)
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	if !strings.HasPrefix(srv.URL, "http://127.0.0.1:") {
		t.Errorf("server URL %s is not loopback", srv.URL)
	}
	resp, err := srv.Client().Get(srv.URL + "/hi")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /hi = %d, want 200", resp.StatusCode)
	}
}

type unregistered struct{}

// Docs: production/testing.mdx#wiring-tests
// "`Start` returns an error when the wiring is wrong: a constructor or handler
// dependency that is not registered, a service that fails `Init`, a pending
// migration that fails, a ready hook that fails, or a configuration that does not
// bind."
func TestStartFailsOnWiringMistakes(t *testing.T) {
	t.Parallel()
	errBoom := errors.New("boom")
	cases := map[string]func(t *testing.T) *gas.App{
		"constructor dependency": func(*testing.T) *gas.App {
			return gas.NewApp(gas.WithSingletonService[*visits](func(*unregistered) *visits { return &visits{} }))
		},
		"handler dependency": func(*testing.T) *gas.App {
			app := gas.NewApp()
			app.Router().Get(nil, "/x", func(gas.Context, *unregistered) error { return nil })
			return app
		},
		"Init fails": func(*testing.T) *gas.App {
			return gas.NewApp(gas.WithServiceInstance(&svc{initErr: errBoom}))
		},
		"migration fails": func(*testing.T) *gas.App {
			return gas.NewApp(gas.WithServiceInstance[gas.MigrationManager](
				&migratetest.MockMigrationManager{RunPendingFn: func() error { return errBoom }}))
		},
		"ready hook fails": func(*testing.T) *gas.App {
			return gas.NewApp(gas.WithReadyFunc(func(*gas.ServiceContainer) error { return errBoom }))
		},
		"configuration does not bind": func(t *testing.T) *gas.App {
			return gas.NewApp(gas.WithServiceInstance(mustConfig(t, map[string]any{
				"server": map[string]any{"host": "not a host name!"},
			})))
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app := build(t)
			if err := app.Start(t.Context()); err == nil {
				_ = app.Shutdown(context.Background())
				t.Fatal("Start returned nil, want an error")
			}
		})
	}
}

// tracker is a scoped service.
type tracker struct {
	mu     sync.Mutex
	closes int
}

func (*tracker) Name() string               { return "tracker" }
func (*tracker) Init(context.Context) error { return nil }
func (tr *tracker) Close(context.Context) error {
	tr.mu.Lock()
	tr.closes++
	tr.mu.Unlock()
	return nil
}

// Docs: production/testing.mdx#scoped-services-outside-a-request
// "In a test, create a
// scope from a container yourself"
// Docs: production/testing.mdx#scoped-services-outside-a-request
// "// Close calls Close on every scoped Service resolved in this scope."
// Docs: production/testing.mdx#scoped-services-outside-a-request
// "`gas.WithRequestScope(ctx, scope)` attaches a scope to a context, so code that
// calls `gas.ResolveFromRequestScope` or `gas.RequestScope` on a request built
// with that context finds it."
func TestScopeOutsideARequest(t *testing.T) {
	t.Parallel()
	c := gas.NewServiceContainer()
	c.RegisterScopedService[*tracker](func() *tracker { return &tracker{} })
	scope := c.NewScope(t.Context())
	tr := scope.MustResolve[*tracker]()

	r := httptest.NewRequestWithContext(gas.WithRequestScope(t.Context(), scope), http.MethodGet, "/", nil)
	if got := gas.RequestScope(r); got != scope {
		t.Fatalf("RequestScope = %p, want the attached scope %p", got, scope)
	}
	if got, err := gas.ResolveFromRequestScope[*tracker](r); err != nil || got != tr {
		t.Fatalf("ResolveFromRequestScope = %p, %v; want the scope's instance", got, err)
	}
	if err := scope.Close(context.Background()); err != nil {
		t.Fatalf("scope close: %v", err)
	}
	if tr.closes != 1 {
		t.Fatalf("scope Close closed the tracker %d times, want 1", tr.closes)
	}
}

type principal struct{ subject string }

func (p principal) Subject() string               { return p.subject }
func (principal) Scheme() string                  { return "test" }
func (principal) CredentialID() string            { return "" }
func (principal) Metadata() gas.PrincipalMetadata { return gas.BasePrincipalMetadata{} }

// Docs: production/testing.mdx#testing-handlers-that-need-a-principal
// "A handler that calls `ctx.Principal()` reads the principal an authentication
// middleware attached to the request context. In a test, a middleware that
// attaches a fixed principal with `gas.WithPrincipal` stands in for
// authentication"
func TestPrincipalFromMiddleware(t *testing.T) {
	t.Parallel()
	asUser := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(gas.WithPrincipal(r.Context(), principal{subject: "user-1"})))
		})
	}
	me := func(ctx gas.Context) error {
		p, err := ctx.Principal()
		if err != nil {
			return ctx.Text(http.StatusUnauthorized, "none")
		}
		return ctx.Text(http.StatusOK, p.Subject())
	}
	app := gas.NewApp()
	app.Router().Get(nil, "/me", me, gas.MiddlewareFunc(asUser))
	app.Router().Get(nil, "/anon", me)
	startApp(t, app)
	for path, want := range map[string]string{"/me": "user-1", "/anon": "none"} {
		rr := httptest.NewRecorder()
		app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Body.String() != want {
			t.Errorf("GET %s = %d %q, want %q", path, rr.Code, rr.Body.String(), want)
		}
	}
}

// Docs: production/testing.mdx#unit-tests-with-a-mock
// "The service takes the router only to register routes in `Init`, which this test
// never calls, so a bare `gas.NewRouter()` is enough."
// Docs: production/testing.mdx#testing-through-the-app
// "Unit tests miss routing, middleware, dependency injection, binding, and error
// rendering. To cover those, build an `App` with mocks in place of the real
// providers and send requests through it."
// The page's own tests, in production-testing/service_test.go, all pass.
func TestPageTestsPass(t *testing.T) {
	t.Parallel()
	cmd := exec.CommandContext(t.Context(), "go", "test", "-count=1", "-v", "./production-testing")
	cmd.Dir = "../.."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go test ./production-testing: %v\n%s", err, out)
	}
	want := []string{"TestVisitIncrements", "TestRecordRoute", "TestRecordRouteCacheDown", "TestRecordOverHTTP",
		"TestWiringFailsWithoutCache", "TestScopedService", "TestWhoAmI"}
	for _, name := range want {
		if !strings.Contains(string(out), "--- PASS: "+name+" (") {
			t.Errorf("%s did not pass:\n%s", name, out)
		}
	}
}
