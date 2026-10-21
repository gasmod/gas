package gas_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gasmod/gas"
)

// A handler depending on an interface that only a registered concrete type
// implements cannot be served, because resolution is by exact type, so boot
// validation rejects it instead of letting every request fail.
func TestDIHandler_BootValidation_InterfaceNotRegistered(t *testing.T) {
	app := gas.NewApp(
		gas.WithService[*helloGreeter](newHelloGreeter, gas.ServiceLifetimeSingleton),
	)

	app.Router().Handle(nil, "GET", "/greet", func(ctx gas.Context, g Greeter) error {
		return nil
	})

	err := app.InitServices(context.Background())
	if err == nil {
		t.Fatal("InitServices() = nil, want an error for the unregistered Greeter")
	}
	if !strings.Contains(err.Error(), "Greeter") {
		t.Errorf("InitServices() error = %v, want it to name Greeter", err)
	}
}

// A DI-aware NotFound handler's dependencies are validated at boot like a
// route handler's.
func TestDIHandler_BootValidation_NotFound(t *testing.T) {
	app := gas.NewApp()

	app.Router().NotFound(nil, func(ctx gas.Context, dep *testDep) error {
		return nil
	})

	err := app.InitServices(context.Background())
	if err == nil {
		t.Fatal("InitServices() = nil, want an error for the NotFound handler's missing dependency")
	}
	if !strings.Contains(err.Error(), "testDep") {
		t.Errorf("InitServices() error = %v, want it to name testDep", err)
	}
}

// The same holds for a NotFound handler registered on a sub-router.
func TestDIHandler_BootValidation_NotFoundInRoute(t *testing.T) {
	app := gas.NewApp()

	app.Router().Route("/api", func(sub *gas.Router) {
		sub.NotFound(nil, func(ctx gas.Context, dep *testDep) error {
			return nil
		})
	})

	err := app.InitServices(context.Background())
	if err == nil {
		t.Fatal("InitServices() = nil, want an error for the NotFound handler's missing dependency")
	}
	if !strings.Contains(err.Error(), "testDep") {
		t.Errorf("InitServices() error = %v, want it to name testDep", err)
	}
}

func newTestDepB() *testDepB { return &testDepB{Count: 42} }

func TestDIHandler_HappyPath(t *testing.T) {
	app := gas.NewApp(
		gas.WithService[*testDep](newTestDep, gas.ServiceLifetimeScoped),
	)

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/di", func(ctx gas.Context, dep *testDep) error {
		return ctx.Text(http.StatusOK, dep.Value)
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/di", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != "resolved" {
		t.Fatalf("expected 'resolved', got %q", rr.Body.String())
	}
}

func TestDIHandler_ZeroDeps(t *testing.T) {
	app := gas.NewApp()

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/zero", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, "no deps")
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/zero", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != "no deps" {
		t.Fatalf("expected 'no deps', got %q", rr.Body.String())
	}
}

func TestDIHandler_MultipleDeps(t *testing.T) {
	app := gas.NewApp(
		gas.WithService[*testDep](newTestDep, gas.ServiceLifetimeScoped),
		gas.WithService[*testDepB](newTestDepB, gas.ServiceLifetimeScoped),
	)

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/multi", func(ctx gas.Context, a *testDep, b *testDepB) error {
		return ctx.JSON(http.StatusOK, map[string]any{
			"value": a.Value,
			"count": b.Count,
		})
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/multi", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var result map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result["value"] != "resolved" {
		t.Fatalf("expected value='resolved', got %v", result["value"])
	}
	if result["count"] != float64(42) {
		t.Fatalf("expected count=42, got %v", result["count"])
	}
}

func TestDIHandler_InterfaceDep(t *testing.T) {
	app := gas.NewApp(
		gas.WithService[Greeter](newHelloGreeter, gas.ServiceLifetimeSingleton),
	)

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/greet", func(ctx gas.Context, g Greeter) error {
		return ctx.Text(http.StatusOK, g.Greet())
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/greet", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != "hello" {
		t.Fatalf("expected 'hello', got %q", rr.Body.String())
	}
}

func TestDIHandler_BackwardCompat_HandlerFunc(t *testing.T) {
	app := gas.NewApp()

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	app.Router().
		Handle(nil, "GET", "/compat", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("compat"))
		}))

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/compat", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != "compat" {
		t.Fatalf("expected 'compat', got %q", rr.Body.String())
	}
}

func TestDIHandler_BackwardCompat_FuncLiteral(t *testing.T) {
	app := gas.NewApp()

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/literal", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("literal"))
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/literal", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != "literal" {
		t.Fatalf("expected 'literal', got %q", rr.Body.String())
	}
}

func TestDIHandler_Panic_NotAFunc(t *testing.T) {
	router := gas.NewRouter()
	assertPanics(t, "not a func", func() {
		router.Handle(nil, "GET", "/bad", "not a function")
	})
}

func TestDIHandler_Panic_NoParams(t *testing.T) {
	router := gas.NewRouter()
	assertPanics(t, "no params", func() {
		router.Handle(nil, "GET", "/bad", func() error { return nil })
	})
}

func TestDIHandler_Panic_WrongFirstParam(t *testing.T) {
	router := gas.NewRouter()
	assertPanics(t, "wrong first param", func() {
		router.Handle(nil, "GET", "/bad", func(s string) error { return nil })
	})
}

func TestDIHandler_Panic_WrongReturn(t *testing.T) {
	router := gas.NewRouter()
	assertPanics(t, "wrong return", func() {
		router.Handle(nil, "GET", "/bad", func(ctx gas.Context) string { return "" })
	})
}

func TestDIHandler_Panic_NoReturn(t *testing.T) {
	router := gas.NewRouter()
	assertPanics(t, "no return", func() {
		router.Handle(nil, "GET", "/bad", func(ctx gas.Context) {})
	})
}

func TestDIHandler_Panic_TooManyReturns(t *testing.T) {
	router := gas.NewRouter()
	assertPanics(t, "too many returns", func() {
		router.Handle(nil, "GET", "/bad", func(ctx gas.Context) (int, error) { return 0, nil })
	})
}

func TestDIHandler_BootValidation_MissingDep(t *testing.T) {
	app := gas.NewApp()

	// Register a DI handler that depends on *testDep, but don't register *testDep.
	app.Router().Handle(nil, "GET", "/missing", func(ctx gas.Context, dep *testDep) error {
		return nil
	})

	err := app.InitServices(context.Background())
	if err == nil {
		t.Fatal("expected error from InitServices for missing dependency")
	}
	if !strings.Contains(err.Error(), "testDep") {
		t.Fatalf("expected error to mention testDep, got: %s", err.Error())
	}
}

func TestDIHandler_BootValidation_Group(t *testing.T) {
	app := gas.NewApp()

	app.Router().Group(func(sub *gas.Router) {
		sub.Handle(nil, "GET", "/grouped", func(ctx gas.Context, dep *testDep) error {
			return nil
		})
	})

	err := app.InitServices(context.Background())
	if err == nil {
		t.Fatal("expected error from InitServices for missing dependency in Group")
	}
	if !strings.Contains(err.Error(), "testDep") {
		t.Fatalf("expected error to mention testDep, got: %s", err.Error())
	}
}

func TestDIHandler_BootValidation_Route(t *testing.T) {
	app := gas.NewApp()

	app.Router().Route("/api", func(sub *gas.Router) {
		sub.Handle(nil, "GET", "/users", func(ctx gas.Context, dep *testDep) error {
			return nil
		})
	})

	err := app.InitServices(context.Background())
	if err == nil {
		t.Fatal("expected error from InitServices for missing dependency in Route")
	}
	if !strings.Contains(err.Error(), "testDep") {
		t.Fatalf("expected error to mention testDep, got: %s", err.Error())
	}
}

func TestDIHandler_ErrorHandling_Default(t *testing.T) {
	app := gas.NewApp()

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/err", func(ctx gas.Context) error {
		return errors.New("something went wrong")
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/err", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}

	// Read the body once: decoding drains the recorder's buffer.
	raw := rr.Body.String()

	var body gas.ErrorResponse
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("expected a JSON error envelope, got %q: %v", raw, err)
	}
	if body.Error.Code != gas.CodeInternal {
		t.Fatalf("expected code %q, got %q", gas.CodeInternal, body.Error.Code)
	}
	if strings.Contains(raw, "something went wrong") {
		t.Fatalf("handler error text leaked to client: %q", raw)
	}
}

func TestDIHandler_ErrorHandling_Custom(t *testing.T) {
	app := gas.NewApp(
		gas.WithErrorHandler(func(ctx gas.Context, err error) {
			ctx.SetHeader("X-Custom-Error", "yes")
			http.Error(ctx.ResponseWriter(), "custom: "+err.Error(), http.StatusBadRequest)
		}),
	)

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/custom-err", func(ctx gas.Context) error {
		return errors.New("bad input")
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/custom-err", nil))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	if rr.Header().Get("X-Custom-Error") != "yes" {
		t.Fatal("expected custom error handler header")
	}
	if !strings.Contains(rr.Body.String(), "custom: bad input") {
		t.Fatalf("expected custom error message, got %q", rr.Body.String())
	}
}

func TestDIHandler_NilError(t *testing.T) {
	app := gas.NewApp()

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/ok", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, "all good")
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/ok", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != "all good" {
		t.Fatalf("expected 'all good', got %q", rr.Body.String())
	}
}

func TestDIHandler_ScopedIsolation(t *testing.T) {
	var callCount int

	app := gas.NewApp(
		gas.WithService[*testDep](func() *testDep {
			callCount++
			return &testDep{Value: "fresh"}
		}, gas.ServiceLifetimeScoped),
	)

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/scoped", func(ctx gas.Context, dep *testDep) error {
		return ctx.Text(http.StatusOK, dep.Value)
	})

	// Two requests should create two separate scope instances.
	for i := 0; i < 3; i++ {
		rr := httptest.NewRecorder()
		app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/scoped", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, rr.Code)
		}
	}

	if callCount != 3 {
		t.Fatalf("expected 3 scope constructions, got %d", callCount)
	}
}

func TestDIHandler_SingletonDep(t *testing.T) {
	app := gas.NewApp(
		gas.WithService[*testDep](newTestDep, gas.ServiceLifetimeSingleton),
	)

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(nil, "GET", "/single", func(ctx gas.Context, dep *testDep) error {
		return ctx.Text(http.StatusOK, dep.Value)
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/single", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != "resolved" {
		t.Fatalf("expected 'resolved', got %q", rr.Body.String())
	}
}

// A plain http.HandlerFunc that panics gets the same recovery as a DI-aware
// handler: the error handler answers 500.
func TestRouter_PlainHandlerPanicRecovered(t *testing.T) {
	var got error
	app := gas.NewApp(gas.WithErrorHandler(func(ctx gas.Context, err error) {
		got = err
		_ = ctx.Text(http.StatusInternalServerError, "recovered")
	}))
	app.Router().Handle(nil, http.MethodGet, "/plain", http.HandlerFunc(panicking))
	app.Router().Handle(nil, http.MethodGet, "/func", panicking)
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	for _, path := range []string{"/plain", "/func"} {
		got = nil
		rr := httptest.NewRecorder()
		app.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusInternalServerError || rr.Body.String() != "recovered" {
			t.Errorf("GET %s = %d %q, want 500 %q", path, rr.Code, rr.Body.String(), "recovered")
		}
		if got == nil {
			t.Errorf("GET %s: error handler not called", path)
		}
	}
}

// Recovery works on a router used without an App, where there is no
// request scope to resolve a logger from.
func TestRouter_PlainHandlerPanicWithoutApp(t *testing.T) {
	router := gas.NewRouter()
	router.Handle(nil, http.MethodGet, "/plain", panicking)
	router.Seal()

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/plain", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("GET /plain = %d, want 500", rr.Code)
	}
}

// http.ErrAbortHandler still aborts the request: it is re-panicked for
// net/http rather than turned into a response.
func TestRouter_PlainHandlerAbortNotRecovered(t *testing.T) {
	router := gas.NewRouter()
	router.Handle(nil, http.MethodGet, "/abort", func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})
	router.Seal()

	defer func() {
		rec := recover()
		if err, ok := rec.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
			t.Errorf("recovered %v, want http.ErrAbortHandler", rec)
		}
	}()
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/abort", nil))
}

func TestDIHandler_PanicRecovery(t *testing.T) {
	svc := &testService{name: "svc"}

	var capturedErr error
	app := gas.NewApp(
		gas.WithErrorHandler(func(ctx gas.Context, err error) {
			capturedErr = err
			_ = ctx.Text(http.StatusInternalServerError, "recovered")
		}),
	)

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle(svc, "GET", "/panic", func(ctx gas.Context) error {
		panic("boom")
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/panic", nil)
	app.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
	if capturedErr == nil || !strings.Contains(capturedErr.Error(), "boom") {
		t.Fatalf("expected error to contain 'boom', got: %v", capturedErr)
	}
}
