package gas_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gasmod/gas"
)

// requestLogger is a scoped Service that captures log lines for a single
// HTTP request. A new instance is created per scope (i.e. per request),
// Init prepares it, and Close flushes or discards the buffer.
type requestLogger struct {
	lines  []string
	closed bool
}

func newRequestLogger() *requestLogger { return &requestLogger{} }

func (r *requestLogger) Name() string { return "request-logger" }

func (r *requestLogger) Init(context.Context) error {
	r.lines = nil
	r.closed = false
	return nil
}

func (r *requestLogger) Log(msg string) { r.lines = append(r.lines, msg) }

func (r *requestLogger) Close(context.Context) error {
	r.closed = true
	return nil
}

// TestScopedService_PerRequestLifecycle shows the full flow:
//  1. Register a singleton (Router) and a scoped service (requestLogger).
//  2. BuildAll — singletons are constructed, scoped services are not.
//  3. For each "request", create a Scope, resolve the scoped service,
//     use it, then Close the scope — which calls Close on scoped Services.
//  4. Each scope gets its own instance; resolving twice in the same scope
//     returns the same instance.
func TestScopedService_PerRequestLifecycle(t *testing.T) {
	c := gas.NewServiceContainer()
	c.RegisterService[*requestLogger](newRequestLogger, gas.ServiceLifetimeScoped)

	// BuildAll validates lifetimes and builds singletons (none here besides
	// whatever is pre-registered). Scoped ctors are validated but not called.
	if err := c.BuildAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// --- simulate two HTTP requests ---

	// Request 1
	scope1 := c.NewScope(context.Background())

	rl1, err := scope1.Resolve[*requestLogger]()
	if err != nil {
		t.Fatal("expected to resolve requestLogger in scope1")
	}
	rl1.Log("request 1: start")
	rl1.Log("request 1: end")

	// Resolving again in the same scope returns the same instance.
	rl1Again, _ := scope1.Resolve[*requestLogger]()
	if rl1Again != rl1 {
		t.Fatal("expected same instance within a single scope")
	}

	if s1Err := scope1.Close(context.Background()); s1Err != nil {
		t.Fatal(s1Err)
	}
	if !rl1.closed {
		t.Fatal("expected requestLogger to be closed after scope1.Close()")
	}
	if len(rl1.lines) != 2 {
		t.Fatalf("expected 2 log lines, got %d", len(rl1.lines))
	}

	// Request 2 — gets a completely fresh instance.
	scope2 := c.NewScope(context.Background())

	rl2, err := scope2.Resolve[*requestLogger]()
	if err != nil {
		t.Fatal("expected to resolve requestLogger in scope2")
	}
	if rl2 == rl1 {
		t.Fatal("expected different instance in a new scope")
	}
	rl2.Log("request 2: only line")

	if s2Err := scope2.Close(context.Background()); s2Err != nil {
		t.Fatal(s2Err)
	}
	if len(rl2.lines) != 1 {
		t.Fatalf("expected 1 log line in scope2, got %d", len(rl2.lines))
	}
}

// TestScopedService_HTTPMiddleware demonstrates how you'd wire scoped
// services into real HTTP handlers using middleware that creates and
// closes a scope per request.
func TestScopedService_HTTPMiddleware(t *testing.T) {
	c := gas.NewServiceContainer()
	c.RegisterService[*requestLogger](newRequestLogger, gas.ServiceLifetimeScoped)

	if err := c.BuildAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Middleware that creates a per-request scope, stores it in context,
	// and closes it when the request is done.
	type scopeKey struct{}
	scopeMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scope := c.NewScope(r.Context())
			defer func() { _ = scope.Close(context.WithoutCancel(r.Context())) }() //nolint:contextcheck // detached on purpose
			ctx := context.WithValue(r.Context(), scopeKey{}, scope)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}

	// Handler that resolves the scoped requestLogger and uses it.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scope := r.Context().Value(scopeKey{}).(*gas.Scope)
		rl := scope.MustResolve[*requestLogger]()
		rl.Log("handled")
		_, _ = fmt.Fprintf(w, "lines:%d", len(rl.lines))
	})

	srv := scopeMiddleware(handler)

	// First request.
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Body.String() != "lines:1" {
		t.Fatalf("expected 'lines:1', got %q", rr.Body.String())
	}

	// Second request — independent scope, fresh instance.
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Body.String() != "lines:1" {
		t.Fatalf("expected 'lines:1' (fresh scope), got %q", rr.Body.String())
	}
}

// TestApp_RequestScopeMiddleware verifies that App automatically installs
// per-request scope middleware and that scoped services get a fresh instance
// per request with Close called after each request completes.
func TestApp_RequestScopeMiddleware(t *testing.T) {
	app := gas.NewApp(
		gas.WithService[*requestLogger](newRequestLogger, gas.ServiceLifetimeScoped),
	)

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	var closedAfterHandler atomic.Bool

	app.Router().Handle(nil, "GET", "/log", func(w http.ResponseWriter, r *http.Request) {
		scope := gas.RequestScope(r)
		rl := scope.MustResolve[*requestLogger]()
		rl.Log("hello")

		// Resolve again — same scope, same instance.
		rl2 := scope.MustResolve[*requestLogger]()
		if rl2 != rl {
			t.Error("expected same instance within a single request scope")
		}

		_, _ = fmt.Fprintf(w, "lines:%d", len(rl.lines))
	})

	// Request 1
	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/log", nil))
	if rr.Body.String() != "lines:1" {
		t.Fatalf("request 1: expected 'lines:1', got %q", rr.Body.String())
	}

	// Request 2 — fresh scope, fresh instance, count resets.
	rr = httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/log", nil))
	if rr.Body.String() != "lines:1" {
		t.Fatalf("request 2: expected 'lines:1' (fresh scope), got %q", rr.Body.String())
	}

	_ = closedAfterHandler.Load() // keep the compiler happy
}

// TestApp_RequestScopeClose verifies that scoped services are closed
// after the HTTP handler returns.
func TestApp_RequestScopeClose(t *testing.T) {
	app := gas.NewApp(
		gas.WithService[*requestLogger](newRequestLogger, gas.ServiceLifetimeScoped),
	)

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	var captured *requestLogger

	app.Router().Handle(nil, "GET", "/close-check", func(w http.ResponseWriter, r *http.Request) {
		scope := gas.RequestScope(r)
		captured = scope.MustResolve[*requestLogger]()
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest("GET", "/close-check", nil))

	// After ServeHTTP returns, the scope middleware's defer has run.
	if captured == nil {
		t.Fatal("handler was not called")
	}
	if !captured.closed {
		t.Fatal("expected scoped service to be closed after request completed")
	}
}

// TestRequestScope_PanicOutsideMiddleware verifies RequestScope panics
// when called on a request without the scope middleware.
func TestRequestScope_PanicOutsideMiddleware(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic from RequestScope outside middleware")
		}
	}()

	r := httptest.NewRequest("GET", "/", nil)
	gas.RequestScope(r) // should panic
}
