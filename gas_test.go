package gas_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

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
	err := router.Handle("auth", "GET", "/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, gas.MiddlewareByName("require-auth"))
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/test", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if !called {
		t.Fatal("middleware was not called")
	}
}

func TestRouter_HandleUnknownNamedMiddleware(t *testing.T) {
	router := gas.NewRouter()
	err := router.Handle("billing", "GET", "/test", func(w http.ResponseWriter, r *http.Request) {}, gas.MiddlewareByName("nonexistent"))
	if err == nil {
		t.Fatal("expected error for unregistered middleware")
	}
}

func TestRouter_RemoveByModule_RemovesMiddleware(t *testing.T) {
	router := gas.NewRouter()
	router.Register("auth", "require-auth", func(next http.Handler) http.Handler { return next })
	router.Register("auth", "rate-limit", func(next http.Handler) http.Handler { return next })
	router.Register("billing", "billing-mw", func(next http.Handler) http.Handler { return next })

	router.RemoveByModule("auth")

	// Auth middleware should be gone.
	err := router.Handle("test", "GET", "/a", func(w http.ResponseWriter, r *http.Request) {}, gas.MiddlewareByName("require-auth"))
	if err == nil {
		t.Fatal("expected require-auth to be removed")
	}
	err = router.Handle("test", "GET", "/b", func(w http.ResponseWriter, r *http.Request) {}, gas.MiddlewareByName("rate-limit"))
	if err == nil {
		t.Fatal("expected rate-limit to be removed")
	}

	// Billing middleware should still exist.
	err = router.Handle("test", "GET", "/c", func(w http.ResponseWriter, r *http.Request) {}, gas.MiddlewareByName("billing-mw"))
	if err != nil {
		t.Fatal("billing-mw should still exist")
	}
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

	err := router.Handle("auth", "POST", "/auth/login", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
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
	router := gas.NewRouter()
	router.Register("auth", "add-header", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Test", "applied")
			next.ServeHTTP(w, r)
		})
	})

	err := router.Handle("billing", "GET", "/billing/plans", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, gas.MiddlewareByName("add-header"))
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

func TestRouter_HandleWithFuncMiddleware(t *testing.T) {
	router := gas.NewRouter()

	err := router.Handle("billing", "GET", "/billing/plans", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, gas.MiddlewareFunc(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Inline", "yes")
			next.ServeHTTP(w, r)
		})
	}))
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/billing/plans", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Header().Get("X-Inline") != "yes" {
		t.Fatal("inline MiddlewareFunc middleware was not applied")
	}
}

func TestRouter_HandleUnknownMiddleware(t *testing.T) {
	router := gas.NewRouter()

	err := router.Handle("billing", "GET", "/test", func(w http.ResponseWriter, r *http.Request) {}, gas.MiddlewareByName("nonexistent"))
	if err == nil {
		t.Fatal("expected error for unknown middleware")
	}
}

func TestRouter_RemoveByModule(t *testing.T) {
	router := gas.NewRouter()

	err := router.Handle("auth", "GET", "/auth/me", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err != nil {
		t.Fatal(err)
	}

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

	err := router.Handle("auth", "GET", "/auth/me", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("auth"))
	})
	if err != nil {
		t.Fatal(err)
	}

	err = router.Handle("billing", "GET", "/billing/plans", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("billing"))
	})
	if err != nil {
		t.Fatal(err)
	}

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

	err := router.Handle("test", "GET", "/hello", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err != nil {
		t.Fatal(err)
	}

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

	err := router.Handle("test", "GET", "/hello", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err != nil {
		t.Fatal(err)
	}

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

	err := router.Handle("test", "GET", "/hello", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, gas.MiddlewareByName("add-global"), gas.MiddlewareByName("remove-global"))
	if err != nil {
		t.Fatal(err)
	}

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

	err := router.UseMiddlewareByName("add-global")
	if err != nil {
		t.Fatal(err)
	}

	err = router.Handle("test", "GET", "/hello", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/hello", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Header().Get("X-Named-Global") != "yes" {
		t.Fatal("MiddlewareByName Use middleware was not applied")
	}
}

func TestRouter_Use_UnknownNamed(t *testing.T) {
	router := gas.NewRouter()
	err := router.UseMiddlewareByName("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown named middleware in Use")
	}
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
		err := sub.Handle("test", "GET", "/grouped", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	// Route outside group should not have the middleware.
	err := router.Handle("test", "GET", "/ungrouped", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err != nil {
		t.Fatal(err)
	}

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
		err := sub.Handle("test", "GET", "/users", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("users"))
		})
		if err != nil {
			t.Fatal(err)
		}

		err = sub.Handle("test", "GET", "/items", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("items"))
		})
		if err != nil {
			t.Fatal(err)
		}
	})

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
	bus := gas.NewEventBus()
	router := gas.NewRouter()

	mod := &testModule{name: "test-mod"}

	app := gas.NewApp(
		gas.WithRouter(router),
		gas.WithEventBus(bus),
		gas.WithModule(mod),
	)

	if err := app.InitModules(); err != nil {
		t.Fatal(err)
	}

	// Register a route owned by this module.
	err := router.Handle("test-mod", "GET", "/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err != nil {
		t.Fatal(err)
	}

	// Track module-closed event.
	var closedName string
	bus.Subscribe(gas.SystemModuleClosed, func(data gas.EventData) {
		closedName, _ = data.GetString("module_name")
	})

	// Kill-switch.
	if err = app.CloseModule("test-mod"); err != nil {
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
	bus := gas.NewEventBus()
	router := gas.NewRouter()

	mod := &testModule{name: "test-mod"}

	app := gas.NewApp(
		gas.WithRouter(router),
		gas.WithEventBus(bus),
		gas.WithModule(mod),
	)

	if err := app.InitModules(); err != nil {
		t.Fatal(err)
	}

	if err := app.CloseModule("test-mod"); err != nil {
		t.Fatal(err)
	}

	// Track restart event.
	var restartedName string
	bus.Subscribe(gas.SystemModuleInitialized, func(data gas.EventData) {
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
	app := gas.NewApp()
	err := app.CloseModule("nonexistent")
	if err == nil {
		t.Fatal("expected error for non-active module")
	}
}

func TestApp_RestartModule_AlreadyActive(t *testing.T) {
	mod := &testModule{name: "test-mod"}
	app := gas.NewApp(gas.WithModule(mod))

	if err := app.InitModules(); err != nil {
		t.Fatal(err)
	}

	err := app.RestartModule("test-mod")
	if err == nil {
		t.Fatal("expected error for already-active module")
	}
}

func TestApp_RestartModule_NotFound(t *testing.T) {
	app := gas.NewApp()
	err := app.RestartModule("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown module")
	}
}

func TestApp_InitFailure(t *testing.T) {
	router := gas.NewRouter()

	failing := &testModule{name: "bad-mod", initErr: fmt.Errorf("init failed")}

	app := gas.NewApp(
		gas.WithRouter(router),
		gas.WithModule(failing),
	)

	err := app.InitModules()
	if err == nil {
		t.Fatal("expected error from failing module init")
	}
}

func TestApp_ActiveModules(t *testing.T) {
	mod1 := &testModule{name: "mod-a"}
	mod2 := &testModule{name: "mod-b"}

	app := gas.NewApp(gas.WithModule(mod1), gas.WithModule(mod2))
	if err := app.InitModules(); err != nil {
		t.Fatal(err)
	}

	names := app.ActiveModules()
	if len(names) != 2 {
		t.Fatalf("expected 2 active modules, got %d", len(names))
	}
}
