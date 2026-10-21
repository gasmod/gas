package gas_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

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

	if err := w.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	names := w.ActiveServices()
	if len(names) != 2 || !slices.Contains(names, "test-svc") {
		t.Fatalf("expected [gas/eventbus, test-svc], got %v", names)
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

func TestWorker_StartAndShutdown(t *testing.T) {
	svc := &testService{name: "svc"}
	w := gas.NewWorker(
		gas.WithSingletonService[*testService](func() *testService { return svc }),
	)

	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Service should have been initialized.
	if svc.initCount.Load() != 1 {
		t.Fatalf("expected Init called once, got %d", svc.initCount.Load())
	}

	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Service should have been closed.
	if !svc.closed.Load() {
		t.Fatal("expected service to be closed after shutdown")
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

// Run returns Start's error without blocking for a signal.
func TestWorker_Run_StartFailure(t *testing.T) {
	initErr := errors.New("init failed")
	w := gas.NewWorker(
		gas.WithServiceInstance[*testService](&testService{name: "bad", initErr: initErr}),
	)

	done := make(chan error, 1)
	go func() { done <- w.Run() }()
	select {
	case err := <-done:
		if !errors.Is(err, initErr) {
			t.Errorf("Run() = %v, want the init error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() blocked after a failed Start")
	}
}
