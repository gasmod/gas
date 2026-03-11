package gas_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gasmod/gas"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// testDep is a simple dependency for DI-aware handler tests.
type testDep struct {
	Value string
}

func newTestDep() *testDep { return &testDep{Value: "resolved"} }

// testDepB is a second dependency for multi-dep tests.
type testDepB struct {
	Count int
}

func newTestDepB() *testDepB { return &testDepB{Count: 42} }

// Greeter is an interface dependency for interface resolution tests.
type Greeter interface {
	Greet() string
}

type helloGreeter struct{}

func (h *helloGreeter) Greet() string { return "hello" }
func newHelloGreeter() *helloGreeter  { return &helloGreeter{} }

// ---------------------------------------------------------------------------
// DI handler: happy path
// ---------------------------------------------------------------------------

func TestDIHandler_HappyPath(t *testing.T) {
	app := gas.NewApp(
		gas.WithService[*testDep](newTestDep, gas.ServiceLifetimeScoped),
	)

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle("test", "GET", "/di", func(ctx gas.Context, dep *testDep) error {
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

// ---------------------------------------------------------------------------
// DI handler: zero dependencies
// ---------------------------------------------------------------------------

func TestDIHandler_ZeroDeps(t *testing.T) {
	app := gas.NewApp()

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle("test", "GET", "/zero", func(ctx gas.Context) error {
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

// ---------------------------------------------------------------------------
// DI handler: multiple dependencies
// ---------------------------------------------------------------------------

func TestDIHandler_MultipleDeps(t *testing.T) {
	app := gas.NewApp(
		gas.WithService[*testDep](newTestDep, gas.ServiceLifetimeScoped),
		gas.WithService[*testDepB](newTestDepB, gas.ServiceLifetimeScoped),
	)

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle("test", "GET", "/multi", func(ctx gas.Context, a *testDep, b *testDepB) error {
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

// ---------------------------------------------------------------------------
// DI handler: interface dependency
// ---------------------------------------------------------------------------

func TestDIHandler_InterfaceDep(t *testing.T) {
	app := gas.NewApp(
		gas.WithService[Greeter](newHelloGreeter, gas.ServiceLifetimeSingleton),
	)

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle("test", "GET", "/greet", func(ctx gas.Context, g Greeter) error {
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

// ---------------------------------------------------------------------------
// Backward compatibility: http.HandlerFunc
// ---------------------------------------------------------------------------

func TestDIHandler_BackwardCompat_HandlerFunc(t *testing.T) {
	app := gas.NewApp()

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle("test", "GET", "/compat", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

// ---------------------------------------------------------------------------
// Backward compatibility: func literal
// ---------------------------------------------------------------------------

func TestDIHandler_BackwardCompat_FuncLiteral(t *testing.T) {
	app := gas.NewApp()

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle("test", "GET", "/literal", func(w http.ResponseWriter, r *http.Request) {
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

// ---------------------------------------------------------------------------
// Signature validation: panics
// ---------------------------------------------------------------------------

func TestDIHandler_Panic_NotAFunc(t *testing.T) {
	router := gas.NewRouter()
	assertPanics(t, "not a func", func() {
		router.Handle("test", "GET", "/bad", "not a function")
	})
}

func TestDIHandler_Panic_NoParams(t *testing.T) {
	router := gas.NewRouter()
	assertPanics(t, "no params", func() {
		router.Handle("test", "GET", "/bad", func() error { return nil })
	})
}

func TestDIHandler_Panic_WrongFirstParam(t *testing.T) {
	router := gas.NewRouter()
	assertPanics(t, "wrong first param", func() {
		router.Handle("test", "GET", "/bad", func(s string) error { return nil })
	})
}

func TestDIHandler_Panic_WrongReturn(t *testing.T) {
	router := gas.NewRouter()
	assertPanics(t, "wrong return", func() {
		router.Handle("test", "GET", "/bad", func(ctx gas.Context) string { return "" })
	})
}

func TestDIHandler_Panic_NoReturn(t *testing.T) {
	router := gas.NewRouter()
	assertPanics(t, "no return", func() {
		router.Handle("test", "GET", "/bad", func(ctx gas.Context) {})
	})
}

func TestDIHandler_Panic_TooManyReturns(t *testing.T) {
	router := gas.NewRouter()
	assertPanics(t, "too many returns", func() {
		router.Handle("test", "GET", "/bad", func(ctx gas.Context) (int, error) { return 0, nil })
	})
}

// ---------------------------------------------------------------------------
// Boot-time validation: missing dependency
// ---------------------------------------------------------------------------

func TestDIHandler_BootValidation_MissingDep(t *testing.T) {
	app := gas.NewApp()

	// Register a DI handler that depends on *testDep, but don't register *testDep.
	app.Router().Handle("test", "GET", "/missing", func(ctx gas.Context, dep *testDep) error {
		return nil
	})

	err := app.InitServices()
	if err == nil {
		t.Fatal("expected error from InitServices for missing dependency")
	}
	if !strings.Contains(err.Error(), "testDep") {
		t.Fatalf("expected error to mention testDep, got: %s", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Boot-time validation: Group/Route handlers are also validated
// ---------------------------------------------------------------------------

func TestDIHandler_BootValidation_Group(t *testing.T) {
	app := gas.NewApp()

	app.Router().Group(func(sub *gas.Router) {
		sub.Handle("test", "GET", "/grouped", func(ctx gas.Context, dep *testDep) error {
			return nil
		})
	})

	err := app.InitServices()
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
		sub.Handle("test", "GET", "/users", func(ctx gas.Context, dep *testDep) error {
			return nil
		})
	})

	err := app.InitServices()
	if err == nil {
		t.Fatal("expected error from InitServices for missing dependency in Route")
	}
	if !strings.Contains(err.Error(), "testDep") {
		t.Fatalf("expected error to mention testDep, got: %s", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Error handling: default (500)
// ---------------------------------------------------------------------------

func TestDIHandler_ErrorHandling_Default(t *testing.T) {
	app := gas.NewApp()

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle("test", "GET", "/err", func(ctx gas.Context) error {
		return errors.New("something went wrong")
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/err", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), http.StatusText(http.StatusInternalServerError)) {
		t.Fatalf("expected default error message in body, got %q", rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Error handling: custom ErrorHandler
// ---------------------------------------------------------------------------

func TestDIHandler_ErrorHandling_Custom(t *testing.T) {
	app := gas.NewApp(
		gas.WithErrorHandler(func(ctx gas.Context, err error) {
			ctx.SetHeader("X-Custom-Error", "yes")
			http.Error(ctx.ResponseWriter(), "custom: "+err.Error(), http.StatusBadRequest)
		}),
	)

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle("test", "GET", "/custom-err", func(ctx gas.Context) error {
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

// ---------------------------------------------------------------------------
// Error handling: nil error (no response mutation)
// ---------------------------------------------------------------------------

func TestDIHandler_NilError(t *testing.T) {
	app := gas.NewApp()

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle("test", "GET", "/ok", func(ctx gas.Context) error {
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

// ---------------------------------------------------------------------------
// Context helpers
// ---------------------------------------------------------------------------

func TestContext_JSON(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	ctx := gas.NewContext(req.Context(), rr, req)

	if err := ctx.JSON(http.StatusCreated, map[string]string{"key": "val"}); err != nil {
		t.Fatal(err)
	}

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected application/json, got %q", ct)
	}

	var result map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result["key"] != "val" {
		t.Fatalf("expected key=val, got %v", result)
	}
}

func TestContext_XML(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	ctx := gas.NewContext(req.Context(), rr, req)

	type data struct {
		Name string `xml:"name"`
	}
	if err := ctx.XML(http.StatusOK, data{Name: "alice"}); err != nil {
		t.Fatal(err)
	}

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/rss+xml; charset=utf-8" {
		t.Fatalf("expected application/rss+xml; charset=utf-8, got %q", ct)
	}
	expected := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<data><name>alice</name></data>`
	if rr.Body.String() != expected {
		t.Fatalf("expected %q, got %q", expected, rr.Body.String())
	}
}

func TestContext_HTML(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	ctx := gas.NewContext(req.Context(), rr, req)

	if err := ctx.HTML(http.StatusOK, "<h1>hello</h1>"); err != nil {
		t.Fatal(err)
	}

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("expected text/html; charset=utf-8, got %q", ct)
	}
	if rr.Body.String() != "<h1>hello</h1>" {
		t.Fatalf("expected '<h1>hello</h1>', got %q", rr.Body.String())
	}
}

func TestContext_Redirect(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	ctx := gas.NewContext(req.Context(), rr, req)

	ctx.Redirect(http.StatusFound, "/new-url")

	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/new-url" {
		t.Fatalf("expected /new-url, got %q", loc)
	}
}

func TestContext_Text(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	ctx := gas.NewContext(req.Context(), rr, req)

	if err := ctx.Text(http.StatusOK, "hello"); err != nil {
		t.Fatal(err)
	}

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("expected text/plain, got %q", ct)
	}
	if rr.Body.String() != "hello" {
		t.Fatalf("expected 'hello', got %q", rr.Body.String())
	}
}

func TestContext_NoContent(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	ctx := gas.NewContext(req.Context(), rr, req)

	if err := ctx.NoContent(); err != nil {
		t.Fatal(err)
	}

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
}

func TestContext_BindJSON(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		body := `{"name":"alice"}`
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		ctx := gas.NewContext(req.Context(), rr, req)

		var dest struct {
			Name string `json:"name"`
		}
		if err := ctx.BindJSON(&dest); err != nil {
			t.Fatal(err)
		}
		if dest.Name != "alice" {
			t.Fatalf("expected 'alice', got %q", dest.Name)
		}
	})

	t.Run("ValidationFailure", func(t *testing.T) {
		body := `{"name":""}`
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		ctx := gas.NewContext(req.Context(), rr, req)

		var dest struct {
			Name string `json:"name" validate:"required"`
		}
		err := ctx.BindJSON(&dest)
		if err == nil {
			t.Fatal("expected validation error, got nil")
		}
		if !strings.Contains(err.Error(), "validation failed") {
			t.Fatalf("expected validation error message, got: %v", err)
		}
	})
}

func TestContext_BindForm(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		form := "name=alice&age=30"
		req := httptest.NewRequest("POST", "/", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		ctx := gas.NewContext(req.Context(), rr, req)

		var dest struct {
			Name string `schema:"name"`
			Age  int    `schema:"age"`
		}
		if err := ctx.BindForm(&dest); err != nil {
			t.Fatal(err)
		}
		if dest.Name != "alice" || dest.Age != 30 {
			t.Fatalf("expected alice/30, got %q/%d", dest.Name, dest.Age)
		}
	})

	t.Run("ValidationFailure", func(t *testing.T) {
		form := "name=&age=30"
		req := httptest.NewRequest("POST", "/", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		ctx := gas.NewContext(req.Context(), rr, req)

		var dest struct {
			Name string `schema:"name" validate:"required"`
			Age  int    `schema:"age"`
		}
		err := ctx.BindForm(&dest)
		if err == nil {
			t.Fatal("expected validation error, got nil")
		}
		if !strings.Contains(err.Error(), "validation failed") {
			t.Fatalf("expected validation error message, got: %v", err)
		}
	})
}

func TestContext_Query(t *testing.T) {
	req := httptest.NewRequest("GET", "/search?q=hello&page=2", nil)
	ctx := gas.NewContext(req.Context(), httptest.NewRecorder(), req)

	if ctx.Query("q") != "hello" {
		t.Fatalf("expected q=hello, got %q", ctx.Query("q"))
	}
	if ctx.Query("page") != "2" {
		t.Fatalf("expected page=2, got %q", ctx.Query("page"))
	}
	if ctx.Query("missing") != "" {
		t.Fatalf("expected empty for missing key, got %q", ctx.Query("missing"))
	}
}

func TestContext_Header(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Custom", "value")
	ctx := gas.NewContext(req.Context(), httptest.NewRecorder(), req)

	if ctx.Header("X-Custom") != "value" {
		t.Fatalf("expected 'value', got %q", ctx.Header("X-Custom"))
	}
}

func TestContext_SetHeader(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	ctx := gas.NewContext(req.Context(), rr, req)

	ctx.SetHeader("X-Out", "set")

	if rr.Header().Get("X-Out") != "set" {
		t.Fatalf("expected 'set', got %q", rr.Header().Get("X-Out"))
	}
}

// ---------------------------------------------------------------------------
// NotFound with DI-aware handler
// ---------------------------------------------------------------------------

func TestRouter_NotFound_DIHandler(t *testing.T) {
	app := gas.NewApp()

	// Register NotFound before InitServices so it's in place before Seal().
	app.Router().NotFound("test", func(ctx gas.Context) error {
		return ctx.Text(http.StatusNotFound, "custom 404")
	})

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	// At least one route must exist so Chi builds the middleware handler
	// chain. Without any route, Chi's ServeHTTP bypasses middleware for
	// NotFound — this is a Chi implementation detail.
	app.Router().Handle("test", "GET", "/exists", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/nonexistent", nil))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
	if rr.Body.String() != "custom 404" {
		t.Fatalf("expected 'custom 404', got %q", rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Scoped deps: per-request isolation
// ---------------------------------------------------------------------------

func TestDIHandler_ScopedIsolation(t *testing.T) {
	var callCount int

	app := gas.NewApp(
		gas.WithService[*testDep](func() *testDep {
			callCount++
			return &testDep{Value: "fresh"}
		}, gas.ServiceLifetimeScoped),
	)

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle("test", "GET", "/scoped", func(ctx gas.Context, dep *testDep) error {
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

// ---------------------------------------------------------------------------
// Singleton dep in DI handler
// ---------------------------------------------------------------------------

func TestDIHandler_SingletonDep(t *testing.T) {
	app := gas.NewApp(
		gas.WithService[*testDep](newTestDep, gas.ServiceLifetimeSingleton),
	)

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle("test", "GET", "/single", func(ctx gas.Context, dep *testDep) error {
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
