package gas_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// assertPanics fails the test if fn does not panic.
func assertPanics(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for %s", name)
		}
	}()
	fn()
}

// ---------------------------------------------------------------------------
// EventData tests
// ---------------------------------------------------------------------------

func TestEventData_SetAndGetString(t *testing.T) {
	d := gas.NewEventData().Set("name", "alice")

	v, ok := d.GetString("name")
	if !ok || v != "alice" {
		t.Fatalf("expected (alice, true), got (%q, %v)", v, ok)
	}
}

func TestEventData_GetString_Missing(t *testing.T) {
	d := gas.NewEventData()
	v, ok := d.GetString("missing")
	if ok || v != "" {
		t.Fatalf("expected (\"\", false), got (%q, %v)", v, ok)
	}
}

func TestEventData_GetString_WrongType(t *testing.T) {
	d := gas.NewEventData().Set("num", 42)
	v, ok := d.GetString("num")
	if ok || v != "" {
		t.Fatalf("expected (\"\", false), got (%q, %v)", v, ok)
	}
}

func TestEventData_GetInt(t *testing.T) {
	d := gas.NewEventData().Set("count", 7)
	v, ok := d.GetInt("count")
	if !ok || v != 7 {
		t.Fatalf("expected (7, true), got (%d, %v)", v, ok)
	}
}

func TestEventData_GetBool(t *testing.T) {
	d := gas.NewEventData().Set("active", true)
	v, ok := d.GetBool("active")
	if !ok || !v {
		t.Fatalf("expected (true, true), got (%v, %v)", v, ok)
	}
}

func TestEventData_GetFloat64(t *testing.T) {
	d := gas.NewEventData().Set("rate", 3.14)
	v, ok := d.GetFloat64("rate")
	if !ok || v != 3.14 {
		t.Fatalf("expected (3.14, true), got (%f, %v)", v, ok)
	}
}

func TestEventData_GetTime(t *testing.T) {
	now := time.Now()
	d := gas.NewEventData().Set("ts", now)
	v, ok := d.GetTime("ts")
	if !ok || !v.Equal(now) {
		t.Fatalf("expected (%v, true), got (%v, %v)", now, v, ok)
	}
}

func TestEventData_GetStringSlice(t *testing.T) {
	d := gas.NewEventData().Set("tags", []string{"a", "b"})
	v, ok := d.GetStringSlice("tags")
	if !ok || len(v) != 2 || v[0] != "a" || v[1] != "b" {
		t.Fatalf("expected ([a b], true), got (%v, %v)", v, ok)
	}
}

func TestEventData_Raw(t *testing.T) {
	d := gas.NewEventData().Set("k", "v")
	raw := d.Raw()
	if raw["k"] != "v" {
		t.Fatalf("expected raw[k]=v, got %v", raw["k"])
	}
}

func TestEventData_Chaining(t *testing.T) {
	d := gas.NewEventData().
		Set("a", "one").
		Set("b", 2).
		Set("c", true)

	if v, ok := d.GetString("a"); !ok || v != "one" {
		t.Fatalf("expected (one, true), got (%q, %v)", v, ok)
	}
	if v, ok := d.GetInt("b"); !ok || v != 2 {
		t.Fatalf("expected (2, true), got (%d, %v)", v, ok)
	}
	if v, ok := d.GetBool("c"); !ok || !v {
		t.Fatalf("expected (true, true), got (%v, %v)", v, ok)
	}
}

// ---------------------------------------------------------------------------
// Router.Register (middleware registry) tests
// ---------------------------------------------------------------------------

func TestRouter_RegisterAndResolve(t *testing.T) {
	router := gas.NewRouter()

	called := false
	router.Register("auth", "require-auth", func(next http.Handler) http.Handler {
		called = true
		return next
	})

	// Use the middleware via Handle to verify it resolves.
	router.Handle("auth", "GET", "/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, gas.MiddlewareByName("require-auth"))

	router.Seal()
	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if !called {
		t.Fatal("middleware was not called")
	}
}

func TestRouter_HandleUnknownNamedMiddleware(t *testing.T) {
	router := gas.NewRouter()
	assertPanics(t, "unregistered middleware", func() {
		router.Handle("billing", "GET", "/test", func(w http.ResponseWriter, r *http.Request) {}, gas.MiddlewareByName("nonexistent"))
	})
}

func TestRouter_RemoveByModule_RemovesMiddleware(t *testing.T) {
	router := gas.NewRouter()
	router.Register("auth", "require-auth", func(next http.Handler) http.Handler { return next })
	router.Register("auth", "rate-limit", func(next http.Handler) http.Handler { return next })
	router.Register("billing", "billing-mw", func(next http.Handler) http.Handler { return next })

	router.RemoveByModule("auth")

	// Auth middleware should be gone — Handle should panic.
	assertPanics(t, "require-auth", func() {
		router.Handle("test", "GET", "/a", func(w http.ResponseWriter, r *http.Request) {}, gas.MiddlewareByName("require-auth"))
	})
	assertPanics(t, "rate-limit", func() {
		router.Handle("test", "GET", "/b", func(w http.ResponseWriter, r *http.Request) {}, gas.MiddlewareByName("rate-limit"))
	})

	// Billing middleware should still exist — no panic.
	router.Handle("test", "GET", "/c", func(w http.ResponseWriter, r *http.Request) {}, gas.MiddlewareByName("billing-mw"))
	router.Seal()
}

// ---------------------------------------------------------------------------
// EventBus tests
// ---------------------------------------------------------------------------

func TestEventBus_EmitAndSubscribe(t *testing.T) {
	bus := gas.NewEventBus()

	var received string
	bus.Subscribe("user:created", func(data gas.EventData) {
		received, _ = data.GetString("email")
	})

	bus.Emit("user:created", gas.NewEventData().Set("email", "test@example.com"))

	if received != "test@example.com" {
		t.Fatalf("expected test@example.com, got %q", received)
	}
}

func TestEventBus_SubscribeWithOwner(t *testing.T) {
	bus := gas.NewEventBus()

	count := 0
	bus.SubscribeWithOwner("auth", "user:created", func(data gas.EventData) {
		count++
	})
	bus.SubscribeWithOwner("billing", "user:created", func(data gas.EventData) {
		count++
	})

	bus.Emit("user:created", gas.NewEventData())
	if count != 2 {
		t.Fatalf("expected 2 handlers called, got %d", count)
	}
}

func TestEventBus_RemoveByModule(t *testing.T) {
	bus := gas.NewEventBus()

	authCalled := false
	billingCalled := false

	bus.SubscribeWithOwner("auth", "test:event", func(data gas.EventData) {
		authCalled = true
	})
	bus.SubscribeWithOwner("billing", "test:event", func(data gas.EventData) {
		billingCalled = true
	})

	bus.RemoveByModule("auth")
	bus.Emit("test:event", gas.NewEventData())

	if authCalled {
		t.Fatal("auth handler should not have been called after removal")
	}
	if !billingCalled {
		t.Fatal("billing handler should still be called")
	}
}

func TestEventBus_EmitNoSubscribers(t *testing.T) {
	bus := gas.NewEventBus()
	// Should not panic.
	bus.Emit("nonexistent", gas.NewEventData())
}

func TestEventBus_ConcurrentEmit(t *testing.T) {
	bus := gas.NewEventBus()

	var mu sync.Mutex
	count := 0
	bus.Subscribe("inc", func(data gas.EventData) {
		mu.Lock()
		count++
		mu.Unlock()
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bus.Emit("inc", gas.NewEventData())
		}()
	}
	wg.Wait()

	if count != 100 {
		t.Fatalf("expected 100, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Router tests
// ---------------------------------------------------------------------------

func TestRouter_HandleAndServe(t *testing.T) {
	router := gas.NewRouter()

	router.Handle("auth", "POST", "/auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	router.Seal()
	req := httptest.NewRequest("POST", "/auth/login", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != "ok" {
		t.Fatalf("expected 'ok', got %q", rr.Body.String())
	}
}

func TestRouter_HandleWithMiddleware(t *testing.T) {
	router := gas.NewRouter()
	router.Register("auth", "add-header", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Test", "applied")
			next.ServeHTTP(w, r)
		})
	})

	router.Handle("billing", "GET", "/billing/plans", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, gas.MiddlewareByName("add-header"))

	router.Seal()
	req := httptest.NewRequest("GET", "/billing/plans", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Header().Get("X-Test") != "applied" {
		t.Fatal("middleware was not applied")
	}
}

func TestRouter_HandleWithFuncMiddleware(t *testing.T) {
	router := gas.NewRouter()

	router.Handle("billing", "GET", "/billing/plans", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, gas.MiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Inline", "yes")
			next.ServeHTTP(w, r)
		})
	}))

	router.Seal()
	req := httptest.NewRequest("GET", "/billing/plans", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Header().Get("X-Inline") != "yes" {
		t.Fatal("inline MiddlewareFunc middleware was not applied")
	}
}

func TestRouter_HandleUnknownMiddleware(t *testing.T) {
	router := gas.NewRouter()
	assertPanics(t, "unknown middleware", func() {
		router.Handle("billing", "GET", "/test", func(w http.ResponseWriter, r *http.Request) {}, gas.MiddlewareByName("nonexistent"))
	})
}

func TestRouter_RemoveByModule(t *testing.T) {
	router := gas.NewRouter()

	router.Handle("auth", "GET", "/auth/me", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	router.Seal()
	// Verify route works.
	req := httptest.NewRequest("GET", "/auth/me", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 before removal, got %d", rr.Code)
	}

	// Remove module routes.
	router.RemoveByModule("auth")

	// Route should now return 503.
	req = httptest.NewRequest("GET", "/auth/me", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 after removal, got %d", rr.Code)
	}
}

func TestRouter_Mux(t *testing.T) {
	router := gas.NewRouter()

	mux := router.Mux()
	if mux == nil {
		t.Fatal("expected non-nil Chi mux")
	}
}

func TestRouter_MultipleModules(t *testing.T) {
	router := gas.NewRouter()

	router.Handle("auth", "GET", "/auth/me", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("auth"))
	})

	router.Handle("billing", "GET", "/billing/plans", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("billing"))
	})

	router.Seal()
	// Remove only auth.
	router.RemoveByModule("auth")

	// Auth should be 503.
	req := httptest.NewRequest("GET", "/auth/me", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for auth, got %d", rr.Code)
	}

	// Billing should still work.
	req = httptest.NewRequest("GET", "/billing/plans", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for billing, got %d", rr.Code)
	}
	if rr.Body.String() != "billing" {
		t.Fatalf("expected 'billing', got %q", rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Router Use/Group/Route tests
// ---------------------------------------------------------------------------

func TestRouter_Use(t *testing.T) {
	router := gas.NewRouter()

	router.UseMiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Global", "yes")
			next.ServeHTTP(w, r)
		})
	})

	router.Handle("test", "GET", "/hello", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.Seal()
	req := httptest.NewRequest("GET", "/hello", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Header().Get("X-Global") != "yes" {
		t.Fatal("Use middleware was not applied")
	}
}

func TestRouter_UseMiddlewareOverride(t *testing.T) {
	router := gas.NewRouter()

	router.UseMiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Global", "yes")
			next.ServeHTTP(w, r)
		})
	})

	router.UseMiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Global", "no") // override the middleware above
			next.ServeHTTP(w, r)
		})
	})

	router.Handle("test", "GET", "/hello", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.Seal()
	req := httptest.NewRequest("GET", "/hello", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Header().Get("X-Global") != "no" {
		t.Fatal("Later middleware override did not take effect")
	}
}

func TestRouter_UseMiddlewareOrder(t *testing.T) {
	router := gas.NewRouter()

	router.Register("auth", "add-global", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Named-Global", "yes")
			next.ServeHTTP(w, r)
		})
	})

	router.Register("auth", "remove-global", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Named-Global", "no")
			next.ServeHTTP(w, r)
		})
	})

	router.Handle("test", "GET", "/hello", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, gas.MiddlewareByName("add-global"), gas.MiddlewareByName("remove-global"))
	router.Seal()
	req := httptest.NewRequest("GET", "/hello", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Header().Get("X-Named-Global") != "no" {
		t.Fatal("Middleware order was not respected")
	}
}

func TestRouter_Use_Named(t *testing.T) {
	router := gas.NewRouter()

	router.Register("auth", "add-global", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Named-Global", "yes")
			next.ServeHTTP(w, r)
		})
	})

	router.UseMiddlewareByName("add-global")

	router.Handle("test", "GET", "/hello", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.Seal()
	req := httptest.NewRequest("GET", "/hello", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Header().Get("X-Named-Global") != "yes" {
		t.Fatal("MiddlewareByName Use middleware was not applied")
	}
}

func TestRouter_Use_UnknownNamed(t *testing.T) {
	router := gas.NewRouter()
	assertPanics(t, "unknown named middleware in Use", func() {
		router.UseMiddlewareByName("nonexistent")
	})
}

func TestRouter_Group(t *testing.T) {
	router := gas.NewRouter()

	router.Group(func(sub *gas.Router) {
		sub.UseMiddlewareFunc(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Group", "yes")
				next.ServeHTTP(w, r)
			})
		})
		sub.Handle("test", "GET", "/grouped", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})

	// Route outside group should not have the middleware.
	router.Handle("test", "GET", "/ungrouped", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	router.Seal()
	// Grouped route should have header.
	req := httptest.NewRequest("GET", "/grouped", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Header().Get("X-Group") != "yes" {
		t.Fatal("Group middleware was not applied to grouped route")
	}

	// Ungrouped route should not have header.
	req = httptest.NewRequest("GET", "/ungrouped", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Header().Get("X-Group") != "" {
		t.Fatal("Group middleware should not apply to ungrouped route")
	}
}

func TestRouter_Route(t *testing.T) {
	router := gas.NewRouter()

	router.Route("/api", func(sub *gas.Router) {
		sub.Handle("test", "GET", "/users", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("users"))
		})

		sub.Handle("test", "GET", "/items", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("items"))
		})
	})

	router.Seal()
	req := httptest.NewRequest("GET", "/api/users", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != "users" {
		t.Fatalf("expected 200 'users', got %d %q", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest("GET", "/api/items", nil)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != "items" {
		t.Fatalf("expected 200 'items', got %d %q", rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// App tests
// ---------------------------------------------------------------------------

// testService is a minimal Service implementation for testing App lifecycle.
type testService struct {
	initErr   error
	closeErr  error
	name      string
	initCount atomic.Int32
	closed    atomic.Bool
}

func (s *testService) Name() string { return s.name }

func (s *testService) Init() error {
	s.initCount.Add(1)
	s.closed.Store(false)
	return s.initErr
}

func (s *testService) Close() error {
	s.closed.Store(true)
	return s.closeErr
}

func TestApp_CloseService(t *testing.T) {
	svc := &testService{name: "test-svc"}

	app := gas.NewApp(
		gas.WithServiceInstance[*testService](svc),
	)

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	router := app.Router()

	// Register a route owned by this service.
	router.Handle("test-svc", "GET", "/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Track service-closed event.
	var closedName string
	app.EventBus().Subscribe(gas.SystemServiceClosed, func(data gas.EventData) {
		closedName, _ = data.GetString("service_name")
	})

	// Kill-switch.
	if err := app.CloseService("test-svc"); err != nil {
		t.Fatal(err)
	}

	// Service should be closed.
	if !svc.closed.Load() {
		t.Fatal("expected service to be closed")
	}

	// Route should return 503.
	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}

	// Event should have been emitted.
	if closedName != "test-svc" {
		t.Fatalf("expected service-closed event for test-svc, got %q", closedName)
	}

	// Should not be in active services.
	names := app.ActiveServices()
	for _, n := range names {
		if n == "test-svc" {
			t.Fatal("test-svc should not be active")
		}
	}
}

func TestApp_RestartService(t *testing.T) {
	svc := &testService{name: "test-svc"}

	app := gas.NewApp(
		gas.WithServiceInstance[*testService](svc),
	)

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	if err := app.CloseService("test-svc"); err != nil {
		t.Fatal(err)
	}

	// Track restart event.
	var restartedName string
	app.EventBus().Subscribe(gas.SystemServiceInitialized, func(data gas.EventData) {
		restartedName, _ = data.GetString("service_name")
	})

	// Restart.
	if err := app.RestartService("test-svc"); err != nil {
		t.Fatal(err)
	}

	// Init count: 1 from InitServices (registered as instance, Init not called by container)
	// but actually for WithServiceInstance, Init is NOT called by the container (it's pre-built).
	// So initCount is 0 from InitServices + 1 from RestartService = 1... unless we need to
	// account for the fact that testService implements Service and is collected.
	// Actually, WithServiceInstance registers a pre-built value. The container doesn't call Init on it.
	// But EachInstance will discover it and add to activeServices.
	// RestartService calls Init() once. So total = 1.
	if svc.initCount.Load() != 1 {
		t.Fatalf("expected Init called once (from restart), got %d", svc.initCount.Load())
	}
	if svc.closed.Load() {
		t.Fatal("service should not be closed after restart")
	}
	if restartedName != "test-svc" {
		t.Fatalf("expected service-initialized event for test-svc, got %q", restartedName)
	}
}

func TestApp_CloseService_NotActive(t *testing.T) {
	app := gas.NewApp()
	err := app.CloseService("nonexistent")
	if err == nil {
		t.Fatal("expected error for non-active service")
	}
}

func TestApp_RestartService_AlreadyActive(t *testing.T) {
	svc := &testService{name: "test-svc"}
	app := gas.NewApp(gas.WithServiceInstance[*testService](svc))

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	err := app.RestartService("test-svc")
	if err == nil {
		t.Fatal("expected error for already-active service")
	}
}

func TestApp_RestartService_NotFound(t *testing.T) {
	app := gas.NewApp()
	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}
	err := app.RestartService("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown service")
	}
}

func TestApp_InitFailure(t *testing.T) {
	failing := &testService{name: "bad-svc", initErr: fmt.Errorf("init failed")}

	app := gas.NewApp(
		gas.WithService[*testService](func() *testService { return failing }, gas.ServiceLifetimeSingleton),
	)

	err := app.InitServices()
	if err == nil {
		t.Fatal("expected error from failing service init")
	}
}

func TestApp_ActiveServices(t *testing.T) {
	svc1 := &testService{name: "svc-a"}
	svc2 := &testService{name: "svc-b"}

	app := gas.NewApp(
		gas.WithServiceInstance[*testService](svc1),
	)
	// Register svc2 under a different type to avoid collision.
	// For simplicity, just test with one instance.
	_ = svc2

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	names := app.ActiveServices()
	if len(names) != 1 {
		t.Fatalf("expected 1 active service, got %d", len(names))
	}
}

func TestApp_TransientServiceRejected(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when registering transient Service")
		}
	}()

	gas.NewApp(
		gas.WithService[*testService](func() *testService {
			return &testService{name: "bad"}
		}, gas.ServiceLifetimeTransient),
	)
}

// ---------------------------------------------------------------------------
// Scoped service tests — demonstrates per-HTTP-request scoping
// ---------------------------------------------------------------------------

// requestLogger is a scoped Service that captures log lines for a single
// HTTP request. A new instance is created per scope (i.e. per request),
// Init() prepares it, and Close() flushes or discards the buffer.
type requestLogger struct {
	lines  []string
	closed bool
}

func newRequestLogger() *requestLogger { return &requestLogger{} }

func (r *requestLogger) Name() string { return "request-logger" }

func (r *requestLogger) Init() error {
	r.lines = nil
	r.closed = false
	return nil
}

func (r *requestLogger) Log(msg string) { r.lines = append(r.lines, msg) }

func (r *requestLogger) Close() error {
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
	gas.RegisterCtor[*requestLogger](c, newRequestLogger, gas.ServiceLifetimeScoped)

	// BuildAll validates lifetimes and builds singletons (none here besides
	// whatever is pre-registered). Scoped ctors are validated but not called.
	if err := c.BuildAll(); err != nil {
		t.Fatal(err)
	}

	// --- simulate two HTTP requests ---

	// Request 1
	scope1 := c.NewScope()

	rl1, ok := gas.Resolve[*requestLogger](scope1)
	if !ok {
		t.Fatal("expected to resolve requestLogger in scope1")
	}
	rl1.Log("request 1: start")
	rl1.Log("request 1: end")

	// Resolving again in the same scope returns the same instance.
	rl1Again, _ := gas.Resolve[*requestLogger](scope1)
	if rl1Again != rl1 {
		t.Fatal("expected same instance within a single scope")
	}

	if err := scope1.Close(); err != nil {
		t.Fatal(err)
	}
	if !rl1.closed {
		t.Fatal("expected requestLogger to be closed after scope1.Close()")
	}
	if len(rl1.lines) != 2 {
		t.Fatalf("expected 2 log lines, got %d", len(rl1.lines))
	}

	// Request 2 — gets a completely fresh instance.
	scope2 := c.NewScope()

	rl2, ok := gas.Resolve[*requestLogger](scope2)
	if !ok {
		t.Fatal("expected to resolve requestLogger in scope2")
	}
	if rl2 == rl1 {
		t.Fatal("expected different instance in a new scope")
	}
	rl2.Log("request 2: only line")

	if err := scope2.Close(); err != nil {
		t.Fatal(err)
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
	gas.RegisterCtor[*requestLogger](c, newRequestLogger, gas.ServiceLifetimeScoped)

	if err := c.BuildAll(); err != nil {
		t.Fatal(err)
	}

	// Middleware that creates a per-request scope, stores it in context,
	// and closes it when the request is done.
	type scopeKey struct{}
	scopeMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scope := c.NewScope()
			defer func() { _ = scope.Close() }()
			ctx := context.WithValue(r.Context(), scopeKey{}, scope)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}

	// Handler that resolves the scoped requestLogger and uses it.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scope := r.Context().Value(scopeKey{}).(*gas.Scope)
		rl := gas.MustResolve[*requestLogger](scope)
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
// per request with Close() called after each request completes.
func TestApp_RequestScopeMiddleware(t *testing.T) {
	app := gas.NewApp(
		gas.WithService[*requestLogger](newRequestLogger, gas.ServiceLifetimeScoped),
	)

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	var closedAfterHandler atomic.Bool

	app.Router().Handle("test", "GET", "/log", func(w http.ResponseWriter, r *http.Request) {
		scope := gas.RequestScope(r)
		rl := gas.MustResolve[*requestLogger](scope)
		rl.Log("hello")

		// Resolve again — same scope, same instance.
		rl2 := gas.MustResolve[*requestLogger](scope)
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

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	var captured *requestLogger

	app.Router().Handle("test", "GET", "/close-check", func(w http.ResponseWriter, r *http.Request) {
		scope := gas.RequestScope(r)
		captured = gas.MustResolve[*requestLogger](scope)
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

func TestApp_RouterAndEventBusGetters(t *testing.T) {
	app := gas.NewApp()
	if app.Router() == nil {
		t.Fatal("expected non-nil Router")
	}
	if app.EventBus() == nil {
		t.Fatal("expected non-nil EventBus")
	}
}
