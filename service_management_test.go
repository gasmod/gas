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

// managementHosts are the two ways to own the kill switch: a bare Worker, and
// an App, which adds the router and its built-in service on top.
var managementHosts = []struct {
	build    func(...gas.Option) *gas.Worker
	name     string
	builtins int // services active before any user service
}{
	{gas.NewWorker, "Worker", 1},
	{func(o ...gas.Option) *gas.Worker { return gas.NewApp(o...).Worker }, "App", 2},
}

// startHost builds a host holding svc and initializes it.
func startHost(t *testing.T, build func(...gas.Option) *gas.Worker, svc *testService) *gas.Worker {
	t.Helper()
	w := build(gas.WithServiceInstance[*testService](svc))
	if err := w.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	return w
}

// CloseService closes the service, emits SystemServiceClosed and removes it
// from the active services.
func TestCloseService(t *testing.T) {
	for _, h := range managementHosts {
		t.Run(h.name, func(t *testing.T) {
			svc := &testService{name: "test-svc"}
			w := startHost(t, h.build, svc)

			var closedName string
			w.EventBus().Subscribe[gas.SystemServiceClosed](
				func(data gas.SystemServiceClosedPayload) {
					closedName = data.ServiceName
				},
			)

			if err := w.CloseService[*testService](context.Background()); err != nil {
				t.Fatal(err)
			}
			if !svc.closed.Load() {
				t.Error("service not closed")
			}
			if closedName != "test-svc" {
				t.Errorf("service-closed event for %q, want test-svc", closedName)
			}
			if slices.Contains(w.ActiveServices(), "test-svc") {
				t.Error("test-svc still active")
			}
		})
	}
}

// On an App, a closed service's routes answer 503.
func TestApp_CloseService_RoutesAnswer503(t *testing.T) {
	svc := &testService{name: "test-svc"}
	app := gas.NewApp(gas.WithServiceInstance[*testService](svc))
	if err := app.InitServices(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.Router().Handle(svc, http.MethodGet, "/test", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	if err := app.CloseService[*testService](context.Background()); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	app.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/test", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /test = %d, want 503", rr.Code)
	}
}

// RestartService initializes a closed service again and emits
// SystemServiceInitialized.
func TestRestartService(t *testing.T) {
	for _, h := range managementHosts {
		t.Run(h.name, func(t *testing.T) {
			svc := &testService{name: "test-svc"}
			w := startHost(t, h.build, svc)
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
			// The container initializes a pre-registered instance like any
			// other service, so Init has run once at boot and once on restart.
			if got := svc.initCount.Load(); got != 2 {
				t.Errorf("Init called %d times, want 2 (boot + restart)", got)
			}
			if svc.closed.Load() {
				t.Error("service closed after restart")
			}
			if restartedName != "test-svc" {
				t.Errorf("service-initialized event for %q, want test-svc", restartedName)
			}
			if !slices.Contains(w.ActiveServices(), "test-svc") {
				t.Error("test-svc not active after restart")
			}
		})
	}
}

// CloseService and RestartService refuse services in the wrong state.
func TestServiceManagement_WrongState(t *testing.T) {
	for _, h := range managementHosts {
		t.Run(h.name, func(t *testing.T) {
			if err := h.build().CloseService[*testService](context.Background()); err == nil {
				t.Error("CloseService of a service never started = nil, want an error")
			}

			w := startHost(t, h.build, &testService{name: "test-svc"})
			if err := w.RestartService[*testService](context.Background()); err == nil {
				t.Error("RestartService of an active service = nil, want an error")
			}
			if err := w.CloseService[*testService](context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := w.CloseService[*testService](context.Background()); err == nil {
				t.Error("CloseService of a closed service = nil, want an error")
			}

			empty := h.build()
			if err := empty.InitServices(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := empty.RestartService[*testService](context.Background()); err == nil {
				t.Error("RestartService of an unregistered service = nil, want an error")
			}
		})
	}
}

// ActiveServices lists the user's services alongside the host's built-ins.
func TestActiveServices(t *testing.T) {
	for _, h := range managementHosts {
		t.Run(h.name, func(t *testing.T) {
			names := startHost(t, h.build, &testService{name: "svc-a"}).ActiveServices()
			if len(names) != h.builtins+1 || !slices.Contains(names, "svc-a") {
				t.Errorf("ActiveServices() = %v, want svc-a and %d built-ins", names, h.builtins)
			}
		})
	}
}

// A failing Close is logged, not returned: CloseService has already torn the
// service down, so it still returns nil and drops the service from the
// active set.
func TestCloseService_CloseError(t *testing.T) {
	for _, h := range managementHosts {
		t.Run(h.name, func(t *testing.T) {
			svc := &testService{name: "failing-close", closeErr: errors.New("close failed")}
			w := startHost(t, h.build, svc)

			if err := w.CloseService[*testService](context.Background()); err != nil {
				t.Errorf("CloseService() = %v, want nil when Close fails", err)
			}
			if _, ok := w.ActiveServicesMap()["failing-close"]; ok {
				t.Error("service still active after a failing Close")
			}
		})
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
