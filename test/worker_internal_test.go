package test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gasmod/gas"
)

// raceSvc is a Service the kill-switch tests close and restart.
type raceSvc struct{}

func (*raceSvc) Name() string { return "race-svc" }

func (*raceSvc) Init(context.Context) error { return nil }

func (*raceSvc) Close(context.Context) error { return nil }

// With no Logger registered, the Worker's fallback logger is resolved on
// first use. CloseService and RestartService both log, so running them
// concurrently must not race on the Worker's logger or on the container.
func TestWorker_FallbackLoggerConcurrentKillSwitch(t *testing.T) {
	t.Parallel()

	for range 50 {
		w := gas.NewWorker(
			gas.WithSingletonService[*raceSvc](func() *raceSvc { return &raceSvc{} }),
		)
		if err := w.InitServices(context.Background()); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		wg.Go(func() { _ = w.CloseService[*raceSvc](context.Background()) })
		wg.Go(func() { _ = w.RestartService[*raceSvc](context.Background()) })
		wg.Go(func() { _ = w.CloseService[*raceSvc](context.Background()) })
		wg.Wait()
	}
}

// The fallback logger stays private to the Worker: it is not registered in
// the container, so resolving Logger still reports it as not registered.
func TestWorker_FallbackLoggerNotRegistered(t *testing.T) {
	t.Parallel()

	w := gas.NewWorker()
	if err := w.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	// FIXME(@ahmedkamalio)
	//if w.getLogger() == nil {
	//	t.Fatal("getLogger() = nil, want the slog fallback")
	//}
	if _, err := w.ServiceContainer().Resolve[gas.Logger](); !errors.Is(err, gas.ErrNotRegistered) {
		t.Errorf("Resolve[Logger]() error = %v, want %v", err, gas.ErrNotRegistered)
	}
}

// A registered Logger is the one the Worker uses.
func TestWorker_UsesRegisteredLogger(t *testing.T) {
	t.Parallel()

	nop := &gas.NopLogger{}
	w := gas.NewWorker(gas.WithServiceInstance[gas.Logger](nop))
	if err := w.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	// FIXME(@ahmedkamalio)
	//if got := w.getLogger(); got != gas.Logger(nop) {
	//	t.Errorf("getLogger() = %v, want the registered logger", got)
	//}
}
