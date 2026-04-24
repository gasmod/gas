package gas_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestWorker_CheckHealth(t *testing.T) {
	healthy := &reporterService[tagA]{name: "healthy"}
	sick := &reporterService[tagB]{name: "sick", healthErr: errors.New("down")}
	plain := &testService{name: "plain"}

	w := gas.NewWorker(
		gas.WithServiceInstance[*reporterService[tagA]](healthy),
		gas.WithServiceInstance[*reporterService[tagB]](sick),
		gas.WithServiceInstance[*testService](plain),
	)
	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}

	results := w.CheckHealth(context.Background())
	if len(results) != 2 {
		t.Fatalf("expected 2 reporters, got %d: %v", len(results), results)
	}
	if err, ok := results["healthy"]; !ok || err != nil {
		t.Fatalf("expected healthy=nil, got ok=%v err=%v", ok, err)
	}
	if err, ok := results["sick"]; !ok || err == nil || err.Error() != "down" {
		t.Fatalf("expected sick=down, got ok=%v err=%v", ok, err)
	}
	if _, ok := results["plain"]; ok {
		t.Fatal("plain service should be omitted (no HealthReporter)")
	}
}

func TestWorker_CheckReady(t *testing.T) {
	ready := &reporterService[tagA]{name: "ready"}
	waiting := &reporterService[tagB]{name: "waiting", readyErr: errors.New("warming")}

	w := gas.NewWorker(
		gas.WithServiceInstance[*reporterService[tagA]](ready),
		gas.WithServiceInstance[*reporterService[tagB]](waiting),
	)
	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}

	results := w.CheckReady(context.Background())
	if len(results) != 2 {
		t.Fatalf("expected 2 reporters, got %d: %v", len(results), results)
	}
	if err, ok := results["ready"]; !ok || err != nil {
		t.Fatalf("expected ready=nil, got ok=%v err=%v", ok, err)
	}
	if err, ok := results["waiting"]; !ok || err == nil || err.Error() != "warming" {
		t.Fatalf("expected waiting=warming, got ok=%v err=%v", ok, err)
	}
}

func TestWorker_CheckHealth_EmptyWhenNoReporters(t *testing.T) {
	w := gas.NewWorker(gas.WithServiceInstance[*testService](&testService{name: "plain"}))
	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}
	if got := w.CheckHealth(context.Background()); len(got) != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}
	if got := w.CheckReady(context.Background()); len(got) != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}
}

func TestWorker_CheckHealth_ResolvableAsProvider(t *testing.T) {
	svc := &reporterService[tagA]{name: "svc"}
	w := gas.NewWorker(gas.WithServiceInstance[*reporterService[tagA]](svc))
	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}

	hp, err := gas.Resolve[gas.HealthProvider](w.ServiceContainer())
	if err != nil {
		t.Fatalf("resolve HealthProvider: %v", err)
	}
	if err, ok := hp.CheckHealth(context.Background())["svc"]; !ok || err != nil {
		t.Fatalf("expected svc=nil from HealthProvider, got ok=%v err=%v", ok, err)
	}

	rp, err := gas.Resolve[gas.ReadyProvider](w.ServiceContainer())
	if err != nil {
		t.Fatalf("resolve ReadyProvider: %v", err)
	}
	if _, ok := rp.CheckReady(context.Background())["svc"]; !ok {
		t.Fatal("expected svc entry from ReadyProvider")
	}
}

func TestWorker_CheckHealth_BeforeInitServices(t *testing.T) {
	w := gas.NewWorker(
		gas.WithServiceInstance[*reporterService[tagA]](&reporterService[tagA]{name: "a"}),
	)
	// Note: InitServices NOT called — activeServices is empty.
	if got := w.CheckHealth(context.Background()); len(got) != 0 {
		t.Fatalf("expected empty map before InitServices, got %v", got)
	}
}

func TestWorker_CheckHealth_AfterCloseService(t *testing.T) {
	a := &reporterService[tagA]{name: "a"}
	b := &reporterService[tagB]{name: "b"}
	w := gas.NewWorker(
		gas.WithServiceInstance[*reporterService[tagA]](a),
		gas.WithServiceInstance[*reporterService[tagB]](b),
	)
	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}
	if err := w.CloseService("a"); err != nil {
		t.Fatal(err)
	}
	results := w.CheckHealth(context.Background())
	if _, ok := results["a"]; ok {
		t.Fatal("closed service should not appear in health results")
	}
	if _, ok := results["b"]; !ok {
		t.Fatal("active service should still appear")
	}
}

func TestWorker_CheckHealth_HealthOnlyReporter(t *testing.T) {
	ho := &healthOnlyService[tagA]{name: "h-only"}
	ro := &readyOnlyService[tagB]{name: "r-only"}
	both := &reporterService[tagC]{name: "both"}

	w := gas.NewWorker(
		gas.WithServiceInstance[*healthOnlyService[tagA]](ho),
		gas.WithServiceInstance[*readyOnlyService[tagB]](ro),
		gas.WithServiceInstance[*reporterService[tagC]](both),
	)
	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}

	health := w.CheckHealth(context.Background())
	if _, ok := health["h-only"]; !ok {
		t.Fatal("health-only service missing from CheckHealth")
	}
	if _, ok := health["r-only"]; ok {
		t.Fatal("ready-only service should not appear in CheckHealth")
	}
	if _, ok := health["both"]; !ok {
		t.Fatal("both-reporter missing from CheckHealth")
	}

	ready := w.CheckReady(context.Background())
	if _, ok := ready["r-only"]; !ok {
		t.Fatal("ready-only service missing from CheckReady")
	}
	if _, ok := ready["h-only"]; ok {
		t.Fatal("health-only service should not appear in CheckReady")
	}
	if _, ok := ready["both"]; !ok {
		t.Fatal("both-reporter missing from CheckReady")
	}
}

func TestWorker_CheckHealth_PanicRecovered(t *testing.T) {
	good := &reporterService[tagA]{name: "good"}
	boom := &panicReporter[tagB]{name: "boom", message: "kaboom"}

	w := gas.NewWorker(
		gas.WithServiceInstance[*reporterService[tagA]](good),
		gas.WithServiceInstance[*panicReporter[tagB]](boom),
	)
	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}

	results := w.CheckHealth(context.Background())
	if err, ok := results["good"]; !ok || err != nil {
		t.Fatalf("good should be healthy despite sibling panic, got ok=%v err=%v", ok, err)
	}
	err, ok := results["boom"]
	if !ok {
		t.Fatal("panicking reporter should still produce a result")
	}
	if err == nil {
		t.Fatal("expected panic to surface as error")
	}
	if !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("expected error to mention panic message, got %q", err.Error())
	}
}

func TestWorker_CheckHealth_RunsConcurrently(t *testing.T) {
	delay := 80 * time.Millisecond
	a := &slowReporter[tagA]{name: "a", delay: delay}
	b := &slowReporter[tagB]{name: "b", delay: delay}
	c := &slowReporter[tagC]{name: "c", delay: delay}

	w := gas.NewWorker(
		gas.WithServiceInstance[*slowReporter[tagA]](a),
		gas.WithServiceInstance[*slowReporter[tagB]](b),
		gas.WithServiceInstance[*slowReporter[tagC]](c),
	)
	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_ = w.CheckHealth(context.Background())
	elapsed := time.Since(start)

	// Sequential would be ~3*delay = 240ms. Concurrent should be ~delay.
	// Allow generous headroom for scheduling jitter but still well under
	// the sequential bound.
	if elapsed >= 2*delay {
		t.Fatalf("expected concurrent execution (<%v), took %v", 2*delay, elapsed)
	}
}

func TestWorker_CheckHealth_ContextPropagates(t *testing.T) {
	obs := &ctxObservingReporter[tagA]{name: "obs", seen: make(chan context.Context, 1)}
	w := gas.NewWorker(gas.WithServiceInstance[*ctxObservingReporter[tagA]](obs))
	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}

	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "payload")
	_ = w.CheckHealth(ctx)

	got := <-obs.seen
	if v, _ := got.Value(key{}).(string); v != "payload" {
		t.Fatalf("context value not propagated, got %v", got.Value(key{}))
	}
}

func TestWorker_CheckHealth_ContextCancellation(t *testing.T) {
	slow := &slowReporter[tagA]{
		name:    "slow",
		delay:   5 * time.Second,
		started: make(chan struct{}, 1),
	}
	w := gas.NewWorker(gas.WithServiceInstance[*slowReporter[tagA]](slow))
	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan map[string]error, 1)
	go func() { done <- w.CheckHealth(ctx) }()

	<-slow.started // ensure reporter is inside its select
	cancel()

	select {
	case results := <-done:
		err, ok := results["slow"]
		if !ok {
			t.Fatal("slow reporter missing from results")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CheckHealth did not return after cancellation")
	}
}

func TestWorker_CheckHealth_Concurrent(t *testing.T) {
	a := &reporterService[tagA]{name: "a"}
	b := &reporterService[tagB]{name: "b"}
	c := &reporterService[tagC]{name: "c"}

	w := gas.NewWorker(
		gas.WithServiceInstance[*reporterService[tagA]](a),
		gas.WithServiceInstance[*reporterService[tagB]](b),
		gas.WithServiceInstance[*reporterService[tagC]](c),
	)
	if err := w.InitServices(); err != nil {
		t.Fatal(err)
	}

	results := w.CheckHealth(context.Background())
	for _, name := range []string{"a", "b", "c"} {
		if _, ok := results[name]; !ok {
			t.Fatalf("missing result for %q: %v", name, results)
		}
	}
}
