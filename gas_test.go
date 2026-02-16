package gas

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// EventData tests
// ---------------------------------------------------------------------------

func TestEventData_SetAndGetString(t *testing.T) {
	d := NewEventData().Set("name", "alice")

	v, ok := d.GetString("name")
	if !ok || v != "alice" {
		t.Fatalf("expected (alice, true), got (%q, %v)", v, ok)
	}
}

func TestEventData_GetString_Missing(t *testing.T) {
	d := NewEventData()
	v, ok := d.GetString("missing")
	if ok || v != "" {
		t.Fatalf("expected (\"\", false), got (%q, %v)", v, ok)
	}
}

func TestEventData_GetString_WrongType(t *testing.T) {
	d := NewEventData().Set("num", 42)
	v, ok := d.GetString("num")
	if ok || v != "" {
		t.Fatalf("expected (\"\", false), got (%q, %v)", v, ok)
	}
}

func TestEventData_GetInt(t *testing.T) {
	d := NewEventData().Set("count", 7)
	v, ok := d.GetInt("count")
	if !ok || v != 7 {
		t.Fatalf("expected (7, true), got (%d, %v)", v, ok)
	}
}

func TestEventData_GetBool(t *testing.T) {
	d := NewEventData().Set("active", true)
	v, ok := d.GetBool("active")
	if !ok || !v {
		t.Fatalf("expected (true, true), got (%v, %v)", v, ok)
	}
}

func TestEventData_GetFloat64(t *testing.T) {
	d := NewEventData().Set("rate", 3.14)
	v, ok := d.GetFloat64("rate")
	if !ok || v != 3.14 {
		t.Fatalf("expected (3.14, true), got (%f, %v)", v, ok)
	}
}

func TestEventData_GetTime(t *testing.T) {
	now := time.Now()
	d := NewEventData().Set("ts", now)
	v, ok := d.GetTime("ts")
	if !ok || !v.Equal(now) {
		t.Fatalf("expected (%v, true), got (%v, %v)", now, v, ok)
	}
}

func TestEventData_GetStringSlice(t *testing.T) {
	d := NewEventData().Set("tags", []string{"a", "b"})
	v, ok := d.GetStringSlice("tags")
	if !ok || len(v) != 2 || v[0] != "a" || v[1] != "b" {
		t.Fatalf("expected ([a b], true), got (%v, %v)", v, ok)
	}
}

func TestEventData_Raw(t *testing.T) {
	d := NewEventData().Set("k", "v")
	raw := d.Raw()
	if raw["k"] != "v" {
		t.Fatalf("expected raw[k]=v, got %v", raw["k"])
	}
}

func TestEventData_Chaining(t *testing.T) {
	d := NewEventData().
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
// MiddlewareRegistry tests
// ---------------------------------------------------------------------------

func TestMiddlewareRegistry_RegisterAndGet(t *testing.T) {
	reg := NewMiddlewareRegistry()

	called := false
	reg.Register("auth", "require-auth", func(next http.Handler) http.Handler {
		called = true
		return next
	})

	mw, err := reg.Get("require-auth")
	if err != nil {
		t.Fatal(err)
	}

	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	if !called {
		t.Fatal("middleware was not called")
	}
}

func TestMiddlewareRegistry_GetNotFound(t *testing.T) {
	reg := NewMiddlewareRegistry()
	_, err := reg.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for unregistered middleware")
	}
}

func TestMiddlewareRegistry_RemoveByModule(t *testing.T) {
	reg := NewMiddlewareRegistry()
	reg.Register("auth", "require-auth", func(next http.Handler) http.Handler { return next })
	reg.Register("auth", "rate-limit", func(next http.Handler) http.Handler { return next })
	reg.Register("billing", "billing-mw", func(next http.Handler) http.Handler { return next })

	reg.RemoveByModule("auth")

	_, err := reg.Get("require-auth")
	if err == nil {
		t.Fatal("expected require-auth to be removed")
	}
	_, err = reg.Get("rate-limit")
	if err == nil {
		t.Fatal("expected rate-limit to be removed")
	}
	_, err = reg.Get("billing-mw")
	if err != nil {
		t.Fatal("billing-mw should still exist")
	}
}

// ---------------------------------------------------------------------------
// EventBus tests
// ---------------------------------------------------------------------------

func TestEventBus_EmitAndSubscribe(t *testing.T) {
	bus := NewEventBus()

	var received string
	bus.Subscribe("user:created", func(data EventData) {
		received, _ = data.GetString("email")
	})

	bus.Emit("user:created", NewEventData().Set("email", "test@example.com"))

	if received != "test@example.com" {
		t.Fatalf("expected test@example.com, got %q", received)
	}
}

func TestEventBus_SubscribeWithOwner(t *testing.T) {
	bus := NewEventBus()

	count := 0
	bus.SubscribeWithOwner("auth", "user:created", func(data EventData) {
		count++
	})
	bus.SubscribeWithOwner("billing", "user:created", func(data EventData) {
		count++
	})

	bus.Emit("user:created", NewEventData())
	if count != 2 {
		t.Fatalf("expected 2 handlers called, got %d", count)
	}
}

func TestEventBus_RemoveByModule(t *testing.T) {
	bus := NewEventBus()

	authCalled := false
	billingCalled := false

	bus.SubscribeWithOwner("auth", "test:event", func(data EventData) {
		authCalled = true
	})
	bus.SubscribeWithOwner("billing", "test:event", func(data EventData) {
		billingCalled = true
	})

	bus.RemoveByModule("auth")
	bus.Emit("test:event", NewEventData())

	if authCalled {
		t.Fatal("auth handler should not have been called after removal")
	}
	if !billingCalled {
		t.Fatal("billing handler should still be called")
	}
}

func TestEventBus_EmitNoSubscribers(t *testing.T) {
	bus := NewEventBus()
	// Should not panic.
	bus.Emit("nonexistent", NewEventData())
}

func TestEventBus_ConcurrentEmit(t *testing.T) {
	bus := NewEventBus()

	var mu sync.Mutex
	count := 0
	bus.Subscribe("inc", func(data EventData) {
		mu.Lock()
		count++
		mu.Unlock()
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bus.Emit("inc", NewEventData())
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
	reg := NewMiddlewareRegistry()
	router := NewRouter(reg)

	err := router.Handle("auth", "POST", "/auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	if err != nil {
		t.Fatal(err)
	}

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
	reg := NewMiddlewareRegistry()
	reg.Register("auth", "add-header", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Test", "applied")
			next.ServeHTTP(w, r)
		})
	})

	router := NewRouter(reg)
	err := router.Handle("billing", "GET", "/billing/plans", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, "add-header")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/billing/plans", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Header().Get("X-Test") != "applied" {
		t.Fatal("middleware was not applied")
	}
}

func TestRouter_HandleUnknownMiddleware(t *testing.T) {
	reg := NewMiddlewareRegistry()
	router := NewRouter(reg)

	err := router.Handle("billing", "GET", "/test", func(w http.ResponseWriter, r *http.Request) {}, "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown middleware")
	}
}

func TestRouter_RemoveByModule(t *testing.T) {
	reg := NewMiddlewareRegistry()
	router := NewRouter(reg)

	router.Handle("auth", "GET", "/auth/me", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

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
	reg := NewMiddlewareRegistry()
	router := NewRouter(reg)

	mux := router.Mux()
	if mux == nil {
		t.Fatal("expected non-nil Chi mux")
	}
}

func TestRouter_MultipleModules(t *testing.T) {
	reg := NewMiddlewareRegistry()
	router := NewRouter(reg)

	router.Handle("auth", "GET", "/auth/me", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("auth"))
	})
	router.Handle("billing", "GET", "/billing/plans", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("billing"))
	})

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
// App tests
// ---------------------------------------------------------------------------

// testModule is a minimal Module implementation for testing App lifecycle.
type testModule struct {
	initErr   error
	closeErr  error
	name      string
	initCount atomic.Int32
	closed    atomic.Bool
}

func (m *testModule) Name() string { return m.name }

func (m *testModule) Init() error {
	m.initCount.Add(1)
	m.closed.Store(false)
	return m.initErr
}

func (m *testModule) Close() error {
	m.closed.Store(true)
	return m.closeErr
}

func TestApp_CloseModule(t *testing.T) {
	bus := NewEventBus()
	reg := NewMiddlewareRegistry()
	router := NewRouter(reg)

	mod := &testModule{name: "test-mod"}

	app := NewApp(
		WithRouter(router),
		WithMiddlewareRegistry(reg),
		WithEventBus(bus),
		WithModule(mod),
	)

	// Manually init to populate activeModules (simulating Run's init phase).
	if err := mod.Init(); err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	app.activeModules[mod.Name()] = mod
	app.mu.Unlock()

	// Register a route owned by this module.
	router.Handle("test-mod", "GET", "/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Track module-closed event.
	var closedName string
	bus.Subscribe(SystemModuleClosed, func(data EventData) {
		closedName, _ = data.GetString("module_name")
	})

	// Kill-switch.
	if err := app.CloseModule("test-mod"); err != nil {
		t.Fatal(err)
	}

	// Module should be closed.
	if !mod.closed.Load() {
		t.Fatal("expected module to be closed")
	}

	// Route should return 503.
	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}

	// Event should have been emitted.
	if closedName != "test-mod" {
		t.Fatalf("expected module-closed event for test-mod, got %q", closedName)
	}

	// Should not be in active modules.
	names := app.ActiveModules()
	for _, n := range names {
		if n == "test-mod" {
			t.Fatal("test-mod should not be active")
		}
	}
}

func TestApp_RestartModule(t *testing.T) {
	bus := NewEventBus()
	reg := NewMiddlewareRegistry()
	router := NewRouter(reg)

	mod := &testModule{name: "test-mod"}

	app := NewApp(
		WithRouter(router),
		WithMiddlewareRegistry(reg),
		WithEventBus(bus),
		WithModule(mod),
	)

	// Simulate init + close.
	mod.Init()
	app.mu.Lock()
	app.activeModules[mod.Name()] = mod
	app.mu.Unlock()
	app.CloseModule("test-mod")

	// Track restart event.
	var restartedName string
	bus.Subscribe(SystemModuleInitialized, func(data EventData) {
		restartedName, _ = data.GetString("module_name")
	})

	// Restart.
	if err := app.RestartModule("test-mod"); err != nil {
		t.Fatal(err)
	}

	if mod.initCount.Load() != 2 {
		t.Fatalf("expected Init called twice, got %d", mod.initCount.Load())
	}
	if mod.closed.Load() {
		t.Fatal("module should not be closed after restart")
	}
	if restartedName != "test-mod" {
		t.Fatalf("expected module-initialized event for test-mod, got %q", restartedName)
	}
}

func TestApp_CloseModule_NotActive(t *testing.T) {
	app := NewApp()
	err := app.CloseModule("nonexistent")
	if err == nil {
		t.Fatal("expected error for non-active module")
	}
}

func TestApp_RestartModule_AlreadyActive(t *testing.T) {
	mod := &testModule{name: "test-mod"}
	app := NewApp(WithModule(mod))

	// Mark as active.
	app.mu.Lock()
	app.activeModules["test-mod"] = mod
	app.mu.Unlock()

	err := app.RestartModule("test-mod")
	if err == nil {
		t.Fatal("expected error for already-active module")
	}
}

func TestApp_RestartModule_NotFound(t *testing.T) {
	app := NewApp()
	err := app.RestartModule("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown module")
	}
}

func TestApp_InitFailure(t *testing.T) {
	reg := NewMiddlewareRegistry()
	router := NewRouter(reg)

	failing := &testModule{name: "bad-mod", initErr: fmt.Errorf("init failed")}

	app := NewApp(
		WithRouter(router),
		WithModule(failing),
	)

	err := app.Run()
	if err == nil {
		t.Fatal("expected error from failing module init")
	}
}

func TestApp_ActiveModules(t *testing.T) {
	mod1 := &testModule{name: "mod-a"}
	mod2 := &testModule{name: "mod-b"}

	app := NewApp(WithModule(mod1), WithModule(mod2))
	app.mu.Lock()
	app.activeModules["mod-a"] = mod1
	app.activeModules["mod-b"] = mod2
	app.mu.Unlock()

	names := app.ActiveModules()
	if len(names) != 2 {
		t.Fatalf("expected 2 active modules, got %d", len(names))
	}
}
