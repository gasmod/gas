package gas_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gasmod/gas"
)

// ---------------------------------------------------------------------------
// ServiceContainer Unhappy Paths
// ---------------------------------------------------------------------------

func TestServiceContainer_CircularDependency(t *testing.T) {
	c := gas.NewServiceContainer()

	type A struct{}
	type B struct{}

	gas.RegisterCtor[*A](c, func(b *B) *A { return &A{} }, gas.ServiceLifetimeSingleton)
	gas.RegisterCtor[*B](c, func(a *A) *B { return &B{} }, gas.ServiceLifetimeSingleton)

	err := c.BuildAll()
	if err == nil {
		t.Fatal("expected error for circular dependency")
	}
	if !strings.Contains(err.Error(), "circular dependency") {
		t.Fatalf("expected circular dependency error, got: %v", err)
	}
}

func TestServiceContainer_CaptiveDependency(t *testing.T) {
	t.Run("SingletonDependsOnScoped", func(t *testing.T) {
		c := gas.NewServiceContainer()
		type SvcScoped struct{}
		type SvcSingleton struct{}

		gas.RegisterCtor[*SvcScoped](c, func() *SvcScoped { return &SvcScoped{} }, gas.ServiceLifetimeScoped)
		gas.RegisterCtor[*SvcSingleton](c, func(s *SvcScoped) *SvcSingleton { return &SvcSingleton{} }, gas.ServiceLifetimeSingleton)

		err := c.BuildAll()
		if err == nil {
			t.Fatal("expected error for captive dependency (singleton -> scoped)")
		}
		if !strings.Contains(err.Error(), "captive dependency") {
			t.Fatalf("expected captive dependency error, got: %v", err)
		}
	})

	t.Run("SingletonDependsOnTransient", func(t *testing.T) {
		c := gas.NewServiceContainer()
		type SvcTransient struct{}
		type SvcSingleton struct{}

		gas.RegisterCtor[*SvcTransient](c, func() *SvcTransient { return &SvcTransient{} }, gas.ServiceLifetimeTransient)
		gas.RegisterCtor[*SvcSingleton](c, func(s *SvcTransient) *SvcSingleton { return &SvcSingleton{} }, gas.ServiceLifetimeSingleton)

		err := c.BuildAll()
		if err == nil {
			t.Fatal("expected error for captive dependency (singleton -> transient)")
		}
		if !strings.Contains(err.Error(), "captive dependency") {
			t.Fatalf("expected captive dependency error, got: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Router Unhappy Paths
// ---------------------------------------------------------------------------

func TestRouter_NotFound_PanicOnDoubleRegistration(t *testing.T) {
	router := gas.NewRouter()
	router.NotFound("svc1", func(ctx gas.Context) error { return nil })

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on double NotFound registration")
		}
	}()

	router.NotFound("svc2", func(ctx gas.Context) error { return nil })
}

func TestRouter_Handle_PanicOnMissingNamedMiddleware(t *testing.T) {
	router := gas.NewRouter()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when registering route with missing named middleware")
		}
	}()

	// Handle calls resolveMiddleware immediately, even if unsealed
	router.Handle("svc", "GET", "/", func(ctx gas.Context) error { return nil }, gas.MiddlewareByName("missing"))
}

// ---------------------------------------------------------------------------
// DI Handler / adaptHandler Unhappy Paths
// ---------------------------------------------------------------------------

func TestDIHandler_PanicRecovery(t *testing.T) {
	var capturedErr error
	app := gas.NewApp(
		gas.WithErrorHandler(func(ctx gas.Context, err error) {
			capturedErr = err
			_ = ctx.Text(http.StatusInternalServerError, "recovered")
		}),
	)

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	app.Router().Handle("test", "GET", "/panic", func(ctx gas.Context) error {
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

func TestServiceContainer_ConstructorError(t *testing.T) {
	c := gas.NewServiceContainer()
	type Svc struct{}
	gas.RegisterCtor[*Svc](c, func() (*Svc, error) {
		return nil, errors.New("ctor failed")
	}, gas.ServiceLifetimeSingleton)

	err := c.BuildAll()
	if err == nil || !strings.Contains(err.Error(), "ctor failed") {
		t.Fatalf("expected ctor failure, got: %v", err)
	}
}

func TestServiceContainer_InitError(t *testing.T) {
	c := gas.NewServiceContainer()
	svc := &testService{name: "failing", initErr: errors.New("init failed")}
	gas.RegisterInstance[*testService](c, svc)

	// BuildAll doesn't call Init on RegisterInstance.
	// We need to use RegisterCtor or a scenario where Init is called.
	c = gas.NewServiceContainer()
	gas.RegisterCtor[*testService](c, func() *testService {
		return svc
	}, gas.ServiceLifetimeSingleton)

	err := c.BuildAll()
	if err == nil || !strings.Contains(err.Error(), "init failed") {
		t.Fatalf("expected init failure, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Context Unhappy Paths
// ---------------------------------------------------------------------------

func TestContext_BindJSON_InvalidDest(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"a":1}`))
	ctx := gas.NewContext(req.Context(), httptest.NewRecorder(), req)

	var dest struct{ A int }
	// Should fail because it's not a pointer to struct (Wait, BindJSON calls json.NewDecoder.Decode(dest))
	// If dest is not a pointer, json.Unmarshal fails.
	err := ctx.BindJSON(dest)
	if err == nil {
		t.Fatal("expected error when binding to non-pointer")
	}
}

func TestContext_BindForm_InvalidDest(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`a=1`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := gas.NewContext(req.Context(), httptest.NewRecorder(), req)

	var dest struct{ A int }
	err := ctx.BindForm(dest)
	if err == nil {
		t.Fatal("expected error when binding form to non-pointer")
	}
}

func TestContext_JSON_SerializationError(t *testing.T) {
	ctx := gas.NewContext(context.Background(), httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	// Unsupported type (channel)
	err := ctx.JSON(http.StatusOK, make(chan int))
	if err == nil {
		t.Fatal("expected JSON serialization error")
	}
}

func TestContext_XML_SerializationError(t *testing.T) {
	ctx := gas.NewContext(context.Background(), httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	// Unsupported type (channel)
	err := ctx.XML(http.StatusOK, make(chan int))
	if err == nil {
		t.Fatal("expected XML serialization error")
	}
}

// ---------------------------------------------------------------------------
// App Unhappy Paths
// ---------------------------------------------------------------------------

func TestApp_CloseService_CloseError(t *testing.T) {
	svc := &testService{name: "failing-close", closeErr: errors.New("close failed")}
	app := gas.NewApp(gas.WithServiceInstance[*testService](svc))

	if err := app.InitServices(); err != nil {
		t.Fatal(err)
	}

	// CloseService should log the error but still return nil because it continues cleanup.
	// Actually, looking at the code:
	// if err := svc.Close(); err != nil {
	//     a.getLogger().Error("service close failed").Str("service", name).Err("error", err).Send()
	// }
	// delete(a.activeServices, name)
	// return nil

	err := app.CloseService("failing-close")
	if err != nil {
		t.Fatalf("expected nil error from CloseService even if Close() fails, got %v", err)
	}

	if _, ok := app.ActiveServicesMap()["failing-close"]; ok {
		t.Fatal("service should be removed from active services even if Close() fails")
	}
}

func TestEventBus_Emit_NoSubscribers_Concurrent(t *testing.T) {
	bus := gas.NewEventBus()
	// Should not panic and should return a WaitGroup that we can Wait on.
	bus.Emit("non-existent", nil).Wait()
}
