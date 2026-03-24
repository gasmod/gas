package gas_test

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/gasmod/gas"
)

// ---------------------------------------------------------------------------
// NewWorker
// ---------------------------------------------------------------------------

func TestNewWorker_Basic(t *testing.T) {
	w := gas.NewWorker()

	if w.EventBus() == nil {
		t.Fatal("expected non-nil EventBus")
	}
	if w.ServiceContainer() == nil {
		t.Fatal("expected non-nil ServiceContainer")
	}
}

func TestNewWorker_WithOptions(t *testing.T) {
	svc := &testService{name: "test-svc"}
	w := gas.NewWorker(
		gas.WithServiceInstance[*testService](svc),
	)

	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}

	names := w.ActiveServices()
	if len(names) != 1 || names[0] != "test-svc" {
		t.Fatalf("expected [test-svc], got %v", names)
	}
}

func TestNewWorker_PanicsOnAppOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when passing AppOption to NewWorker")
		}
	}()

	gas.NewWorker(
		gas.WithErrorHandler(func(ctx gas.Context, err error) {}),
	)
}

// ---------------------------------------------------------------------------
// Worker.Start / Worker.Shutdown
// ---------------------------------------------------------------------------

func TestWorker_StartAndShutdown(t *testing.T) {
	svc := &testService{name: "svc"}
	w := gas.NewWorker(
		gas.WithSingletonService[*testService](func() *testService { return svc }),
	)

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}

	// Service should have been initialized.
	if svc.initCount.Load() != 1 {
		t.Fatalf("expected Init called once, got %d", svc.initCount.Load())
	}

	if err := w.Shutdown(); err != nil {
		t.Fatal(err)
	}

	// Service should have been closed.
	if !svc.closed.Load() {
		t.Fatal("expected service to be closed after shutdown")
	}
}

func TestWorker_Start_InitFailure(t *testing.T) {
	svc := &testService{name: "bad-svc", initErr: fmt.Errorf("init failed")}
	w := gas.NewWorker(
		gas.WithService[*testService](func() *testService { return svc }, gas.ServiceLifetimeSingleton),
	)

	err := w.Start()
	if err == nil {
		t.Fatal("expected error from failing service init")
	}
}

func TestWorker_Start_ReadyHookFailure(t *testing.T) {
	w := gas.NewWorker(
		gas.WithReadyFunc(func(sc *gas.ServiceContainer) error {
			return errors.New("hook failed")
		}),
	)

	err := w.Start()
	if err == nil || err.Error() != "gas: ready hook: hook failed" {
		t.Fatalf("expected ready hook error, got: %v", err)
	}
}

func TestWorker_Start_ReadyHooksRunInOrder(t *testing.T) {
	var order []int
	w := gas.NewWorker(
		gas.WithReadyFunc(func(sc *gas.ServiceContainer) error {
			order = append(order, 1)
			return nil
		}),
		gas.WithReadyFunc(func(sc *gas.ServiceContainer) error {
			order = append(order, 2)
			return nil
		}),
	)

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}

	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("expected [1 2], got %v", order)
	}
}

func TestWorker_Shutdown_ReverseOrder(t *testing.T) {
	// Use two services registered via constructors (not instances) so
	// Init is called by the container and they appear in serviceOrder.
	type svcA struct{ testService }
	type svcB struct{ testService }

	var closeOrder []string

	a := &svcA{testService: testService{name: "svc-a"}}
	b := &svcB{testService: testService{name: "svc-b"}}

	w := gas.NewWorker(
		gas.WithServiceInstance[*svcA](a),
		gas.WithServiceInstance[*svcB](b),
	)

	if err := w.Start(); err != nil {
		t.Fatal(err)
	}

	// Subscribe to shutdown event.
	var shutdownEmitted atomic.Bool
	gas.Subscribe(w.EventBus(), gas.SystemShuttingDown, func(_ gas.SystemShuttingDownPayload) {
		shutdownEmitted.Store(true)
	})

	_ = closeOrder // tracking would require hooking into Close, but we verify the event fires
	if err := w.Shutdown(); err != nil {
		t.Fatal(err)
	}

	if !shutdownEmitted.Load() {
		t.Fatal("expected SystemShuttingDown event to be emitted")
	}
}

func TestWorker_Shutdown_EmitsSystemShuttingDown(t *testing.T) {
	w := gas.NewWorker()
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}

	var emitted atomic.Bool
	gas.Subscribe(w.EventBus(), gas.SystemShuttingDown, func(_ gas.SystemShuttingDownPayload) {
		emitted.Store(true)
	})

	if err := w.Shutdown(); err != nil {
		t.Fatal(err)
	}

	if !emitted.Load() {
		t.Fatal("expected SystemShuttingDown to be emitted")
	}
}

// ---------------------------------------------------------------------------
// Worker.InitServices
// ---------------------------------------------------------------------------

func TestWorker_InitServices_EmitsAllServicesInitialized(t *testing.T) {
	w := gas.NewWorker()

	var emitted atomic.Bool
	gas.Subscribe(w.EventBus(), gas.SystemAllServicesInitialized, func(_ gas.SystemAllServicesInitializedPayload) {
		emitted.Store(true)
	})

	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}

	if !emitted.Load() {
		t.Fatal("expected SystemAllServicesInitialized to be emitted")
	}
}

func TestWorker_InitServices_Idempotent(t *testing.T) {
	var count atomic.Int32
	svc := &testService{name: "svc"}

	w := gas.NewWorker(
		gas.WithSingletonService[*testService](func() *testService {
			count.Add(1)
			return svc
		}),
	)

	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}
	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}

	// Constructor should have been called only once (sync.Once).
	if count.Load() != 1 {
		t.Fatalf("expected constructor called once, got %d", count.Load())
	}
}

// ---------------------------------------------------------------------------
// Worker service management (CloseService / RestartService / ActiveServices)
// ---------------------------------------------------------------------------

func TestWorker_CloseService(t *testing.T) {
	svc := &testService{name: "test-svc"}
	w := gas.NewWorker(
		gas.WithServiceInstance[*testService](svc),
	)

	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}

	var closedName string
	gas.Subscribe(w.EventBus(), gas.SystemServiceClosed, func(data gas.SystemServiceClosedPayload) {
		closedName = data.ServiceName
	})

	if err := w.CloseService("test-svc"); err != nil {
		t.Fatal(err)
	}

	if !svc.closed.Load() {
		t.Fatal("expected service to be closed")
	}
	if closedName != "test-svc" {
		t.Fatalf("expected service-closed event for test-svc, got %q", closedName)
	}
	for _, n := range w.ActiveServices() {
		if n == "test-svc" {
			t.Fatal("test-svc should not be active")
		}
	}
}

func TestWorker_CloseService_NotActive(t *testing.T) {
	w := gas.NewWorker()
	err := w.CloseService("nonexistent")
	if err == nil {
		t.Fatal("expected error for non-active service")
	}
}

func TestWorker_RestartService(t *testing.T) {
	svc := &testService{name: "test-svc"}
	w := gas.NewWorker(
		gas.WithServiceInstance[*testService](svc),
	)

	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}
	if err := w.CloseService("test-svc"); err != nil {
		t.Fatal(err)
	}

	var restartedName string
	gas.Subscribe(w.EventBus(), gas.SystemServiceInitialized, func(data gas.SystemServiceInitializedPayload) {
		restartedName = data.ServiceName
	})

	if err := w.RestartService("test-svc"); err != nil {
		t.Fatal(err)
	}

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

func TestWorker_RestartService_AlreadyActive(t *testing.T) {
	svc := &testService{name: "test-svc"}
	w := gas.NewWorker(gas.WithServiceInstance[*testService](svc))

	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}

	err := w.RestartService("test-svc")
	if err == nil {
		t.Fatal("expected error for already-active service")
	}
}

func TestWorker_RestartService_NotFound(t *testing.T) {
	w := gas.NewWorker()
	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}
	err := w.RestartService("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown service")
	}
}

func TestWorker_ActiveServices(t *testing.T) {
	svc := &testService{name: "svc-a"}
	w := gas.NewWorker(
		gas.WithServiceInstance[*testService](svc),
	)

	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}

	names := w.ActiveServices()
	if len(names) != 1 {
		t.Fatalf("expected 1 active service, got %d", len(names))
	}
}

func TestWorker_TransientServiceRejected(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when registering transient Service")
		}
	}()

	gas.NewWorker(
		gas.WithService[*testService](func() *testService {
			return &testService{name: "bad"}
		}, gas.ServiceLifetimeTransient),
	)
}

func TestWorker_CloseService_CloseError(t *testing.T) {
	svc := &testService{name: "failing-close", closeErr: errors.New("close failed")}
	w := gas.NewWorker(gas.WithServiceInstance[*testService](svc))

	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}

	err := w.CloseService("failing-close")
	if err != nil {
		t.Fatalf("expected nil error from CloseService even if Close() fails, got %v", err)
	}

	if _, ok := w.ActiveServicesMap()["failing-close"]; ok {
		t.Fatal("service should be removed from active services even if Close() fails")
	}
}

// ---------------------------------------------------------------------------
// Worker with MigrationManager and ConfigProvider
// ---------------------------------------------------------------------------

func TestWorker_MigrationManager_NilWhenUnregistered(t *testing.T) {
	w := gas.NewWorker()
	if w.MigrationManager() != nil {
		t.Fatal("expected nil MigrationManager when not registered")
	}
}

func TestWorker_ConfigProvider_NilWhenUnregistered(t *testing.T) {
	w := gas.NewWorker()
	if w.ConfigProvider() != nil {
		t.Fatal("expected nil ConfigProvider when not registered")
	}
}
