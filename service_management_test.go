package gas_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

func TestApp_CloseService(t *testing.T) {
	svc := &testService{name: "test-svc"}

	app := gas.NewApp(
		gas.WithServiceInstance[*testService](svc),
	)

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	router := app.Router()

	// Register a route owned by this service.
	router.Handle(svc, "GET", "/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Track service-closed event.
	var closedName string
	app.EventBus().Subscribe[gas.SystemServiceClosed](func(data gas.SystemServiceClosedPayload) {
		closedName = data.ServiceName
	})

	// Kill-switch.
	if err := app.CloseService[*testService](context.Background()); err != nil {
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

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := app.CloseService[*testService](context.Background()); err != nil {
		t.Fatal(err)
	}

	// Track restart event.
	var restartedName string
	app.EventBus().Subscribe[gas.SystemServiceInitialized](
		func(data gas.SystemServiceInitializedPayload) {
			restartedName = data.ServiceName
		},
	)

	// Restart.
	if err := app.RestartService[*testService](context.Background()); err != nil {
		t.Fatal(err)
	}

	// The container initializes a pre-registered instance like any other
	// service, so Init has run once at boot and once more on restart.
	if svc.initCount.Load() != 2 {
		t.Fatalf("expected Init called twice (boot + restart), got %d", svc.initCount.Load())
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
	err := app.CloseService[*testService](context.Background())
	if err == nil {
		t.Fatal("expected error for non-active service")
	}
}

func TestApp_RestartService_AlreadyActive(t *testing.T) {
	svc := &testService{name: "test-svc"}
	app := gas.NewApp(gas.WithServiceInstance[*testService](svc))

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	err := app.RestartService[*testService](context.Background())
	if err == nil {
		t.Fatal("expected error for already-active service")
	}
}

func TestApp_RestartService_NotFound(t *testing.T) {
	app := gas.NewApp()
	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := app.RestartService[*testService](context.Background())
	if err == nil {
		t.Fatal("expected error for unknown service")
	}
}

func TestApp_ActiveServices(t *testing.T) {
	svc1 := &testService{name: "svc-a"}

	app := gas.NewApp(
		gas.WithServiceInstance[*testService](svc1),
	)

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	names := app.ActiveServices()
	if len(names) != 3 { // 2 built-in services (gas/router, gas/eventbus) + "svc-a"
		t.Fatalf("expected 3 active service, got %d", len(names))
	}
}

// reporterService is a Service that optionally reports health and readiness.
// The phantom type parameter K lets tests register several instances under
// distinct container types (e.g. *reporterService[tagA], *reporterService[tagB]).
type reporterService[K any] struct {
	healthErr error
	readyErr  error
	name      string
}

func (s *reporterService[K]) Name() string { return s.name }

func (s *reporterService[K]) Init(context.Context) error { return nil }

func (s *reporterService[K]) Close(context.Context) error { return nil }

func (s *reporterService[K]) CheckHealth(_ context.Context) error { return s.healthErr }

func (s *reporterService[K]) CheckReady(_ context.Context) error { return s.readyErr }

type (
	tagA struct{}
	tagB struct{}
	tagC struct{}
)

// healthOnlyService implements HealthReporter but not ReadyReporter.
type healthOnlyService[K any] struct {
	err  error
	name string
}

func (s *healthOnlyService[K]) Name() string { return s.name }

func (s *healthOnlyService[K]) Init(context.Context) error { return nil }

func (s *healthOnlyService[K]) Close(context.Context) error { return nil }

func (s *healthOnlyService[K]) CheckHealth(_ context.Context) error { return s.err }

// readyOnlyService implements ReadyReporter but not HealthReporter.
type readyOnlyService[K any] struct {
	err  error
	name string
}

func (s *readyOnlyService[K]) Name() string { return s.name }

func (s *readyOnlyService[K]) Init(context.Context) error { return nil }

func (s *readyOnlyService[K]) Close(context.Context) error { return nil }

func (s *readyOnlyService[K]) CheckReady(_ context.Context) error { return s.err }

// slowReporter blocks inside CheckHealth for the given delay. Used to verify
// that CheckHealth runs reporters concurrently.
type slowReporter[K any] struct {
	started chan struct{}
	name    string
	delay   time.Duration
}

func (s *slowReporter[K]) Name() string { return s.name }

func (s *slowReporter[K]) Init(context.Context) error { return nil }

func (s *slowReporter[K]) Close(context.Context) error { return nil }

func (s *slowReporter[K]) CheckHealth(ctx context.Context) error {
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	select {
	case <-time.After(s.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// panicReporter panics inside CheckHealth. Used to verify that
// a misbehaving reporter cannot crash the aggregator.
type panicReporter[K any] struct {
	name    string
	message string
}

func (s *panicReporter[K]) Name() string { return s.name }

func (s *panicReporter[K]) Init(context.Context) error { return nil }

func (s *panicReporter[K]) Close(context.Context) error { return nil }

func (s *panicReporter[K]) CheckHealth(_ context.Context) error { panic(s.message) }

// ctxObservingReporter records the context it was called with.
type ctxObservingReporter[K any] struct {
	seen chan context.Context
	name string
}

func (s *ctxObservingReporter[K]) Name() string { return s.name }

func (s *ctxObservingReporter[K]) Init(context.Context) error { return nil }

func (s *ctxObservingReporter[K]) Close(context.Context) error { return nil }

func (s *ctxObservingReporter[K]) CheckHealth(ctx context.Context) error {
	s.seen <- ctx
	return nil
}

var (
	_ gas.Service        = (*reporterService[tagA])(nil)
	_ gas.HealthReporter = (*reporterService[tagA])(nil)
	_ gas.ReadyReporter  = (*reporterService[tagA])(nil)
)

func TestApp_CloseService_CloseError(t *testing.T) {
	svc := &testService{name: "failing-close", closeErr: errors.New("close failed")}
	app := gas.NewApp(gas.WithServiceInstance[*testService](svc))

	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	// CloseService should log the error but still return nil because it continues cleanup.
	// Actually, looking at the code:
	// if err := svc.Close(ctx); err != nil {
	//     a.getLogger().Error("service close failed").Str("service", name).Err("error", err).Send()
	// }
	// delete(a.activeServices, name)
	// return nil

	err := app.CloseService[*testService](context.Background())
	if err != nil {
		t.Fatalf("expected nil error from CloseService even if Close() fails, got %v", err)
	}

	if _, ok := app.ActiveServicesMap()["failing-close"]; ok {
		t.Fatal("service should be removed from active services even if Close() fails")
	}
}

func TestWorker_CloseService(t *testing.T) {
	svc := &testService{name: "test-svc"}
	w := gas.NewWorker(
		gas.WithServiceInstance[*testService](svc),
	)

	if err := w.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	var closedName string
	w.EventBus().Subscribe[gas.SystemServiceClosed](func(data gas.SystemServiceClosedPayload) {
		closedName = data.ServiceName
	})

	if err := w.CloseService[*testService](context.Background()); err != nil {
		t.Fatal(err)
	}

	if !svc.closed.Load() {
		t.Fatal("expected service to be closed")
	}
	if closedName != svc.Name() {
		t.Fatalf("expected service-closed event for test-svc, got %q", closedName)
	}
	for _, n := range w.ActiveServices() {
		if n == svc.Name() {
			t.Fatal("test-svc should not be active")
		}
	}
}

func TestWorker_CloseService_NotActive(t *testing.T) {
	w := gas.NewWorker()
	err := w.CloseService[*testService](context.Background())
	if err == nil {
		t.Fatal("expected error for non-active service")
	}
}

func TestWorker_RestartService(t *testing.T) {
	svc := &testService{name: "test-svc"}
	w := gas.NewWorker(
		gas.WithServiceInstance[*testService](svc),
	)

	if err := w.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := w.CloseService[*testService](context.Background()); err != nil {
		t.Fatal(err)
	}

	var restartedName string
	w.EventBus().Subscribe[gas.SystemServiceInitialized](
		func(data gas.SystemServiceInitializedPayload) {
			restartedName = data.ServiceName
		},
	)

	if err := w.RestartService[*testService](context.Background()); err != nil {
		t.Fatal(err)
	}

	// The container initializes a pre-registered instance like any other
	// service, so Init has run once at boot and once more on restart.
	if svc.initCount.Load() != 2 {
		t.Fatalf("expected Init called twice (boot + restart), got %d", svc.initCount.Load())
	}
	if svc.closed.Load() {
		t.Fatal("service should not be closed after restart")
	}
	if restartedName != svc.Name() {
		t.Fatalf("expected service-initialized event for test-svc, got %q", restartedName)
	}
}

func TestWorker_RestartService_AlreadyActive(t *testing.T) {
	svc := &testService{name: "test-svc"}
	w := gas.NewWorker(gas.WithServiceInstance[*testService](svc))

	if err := w.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	err := w.RestartService[*testService](context.Background())
	if err == nil {
		t.Fatal("expected error for already-active service")
	}
}

func TestWorker_RestartService_NotFound(t *testing.T) {
	w := gas.NewWorker()
	if err := w.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := w.RestartService[*testService](context.Background())
	if err == nil {
		t.Fatal("expected error for unknown service")
	}
}

func TestWorker_ActiveServices(t *testing.T) {
	svc := &testService{name: "svc-a"}
	w := gas.NewWorker(
		gas.WithServiceInstance[*testService](svc),
	)

	if err := w.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	names := w.ActiveServices()
	if len(names) != 2 ||
		!slices.Contains(names, "svc-a") { // 1 built-in service (gas/eventbus) + "svc-a"
		t.Fatalf("expected 2 active services, got %d", len(names))
	}
}

func TestWorker_CloseService_CloseError(t *testing.T) {
	svc := &testService{name: "failing-close", closeErr: errors.New("close failed")}
	w := gas.NewWorker(gas.WithServiceInstance[*testService](svc))

	if err := w.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	err := w.CloseService[*testService](context.Background())
	if err != nil {
		t.Fatalf("expected nil error from CloseService even if Close() fails, got %v", err)
	}

	if _, ok := w.ActiveServicesMap()[svc.Name()]; ok {
		t.Fatal("service should be removed from active services even if Close() fails")
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
	if err := w.InitServices(context.Background()); err != nil {
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
	if err := w.InitServices(context.Background()); err != nil {
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
	if err := w.InitServices(context.Background()); err != nil {
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
	if err := w.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	hp, err := w.ServiceContainer().Resolve[gas.HealthProvider]()
	if err != nil {
		t.Fatalf("resolve HealthProvider: %v", err)
	}
	if hErr, ok := hp.CheckHealth(context.Background())["svc"]; !ok || hErr != nil {
		t.Fatalf("expected svc=nil from HealthProvider, got ok=%v err=%v", ok, hErr)
	}

	rp, err := w.ServiceContainer().Resolve[gas.ReadyProvider]()
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
	if err := w.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := w.CloseService[*reporterService[tagA]](context.Background()); err != nil {
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
	if err := w.InitServices(context.Background()); err != nil {
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
	if err := w.InitServices(context.Background()); err != nil {
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
	if err := w.InitServices(context.Background()); err != nil {
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
	if err := w.InitServices(context.Background()); err != nil {
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
	if err := w.InitServices(context.Background()); err != nil {
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
	if err := w.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}

	results := w.CheckHealth(context.Background())
	for _, name := range []string{"a", "b", "c"} {
		if _, ok := results[name]; !ok {
			t.Fatalf("missing result for %q: %v", name, results)
		}
	}
}
