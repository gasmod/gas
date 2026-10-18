package gas_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// Init and Close take a context. The container gives every Init a deadline
// (WithServiceInitTimeout, or the service's own ServiceInitTimeout), cancels
// it when the deadline passes, and waits for Init to return before failing
// startup, so no Init is ever left running behind a failed Start. An Init
// that ignores its context and succeeds late is closed again, because
// startup has already failed and nothing else would release what it holds.
//
// Shutdown closes each active service once with the caller's context, skips
// services the kill switch already closed, and returns Close errors instead
// of logging them.

type ctxKey struct{}

// lcProbe is a Service that records the context its Init and Close receive.
// The phantom K gives each registration its own container type.
type lcProbe[K any] struct {
	initDeadline time.Time
	initValue    any
	closeValue   any
	closeErr     error
	initFn       func(ctx context.Context) error
	name         string
	mu           sync.Mutex
	initCalls    atomic.Int32
	closeCalls   atomic.Int32
	initReturned atomic.Bool
	initHasDL    bool
}

func (p *lcProbe[K]) Name() string { return p.name }

func (p *lcProbe[K]) Init(ctx context.Context) error {
	p.initCalls.Add(1)
	p.mu.Lock()
	p.initDeadline, p.initHasDL = ctx.Deadline()
	p.initValue = ctx.Value(ctxKey{})
	p.mu.Unlock()

	var err error
	if p.initFn != nil {
		err = p.initFn(ctx)
	}
	p.initReturned.Store(true)
	return err
}

func (p *lcProbe[K]) Close(ctx context.Context) error {
	p.closeCalls.Add(1)
	p.mu.Lock()
	p.closeValue = ctx.Value(ctxKey{})
	p.mu.Unlock()
	return p.closeErr
}

func (p *lcProbe[K]) deadline() (time.Time, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.initDeadline, p.initHasDL
}

func (p *lcProbe[K]) values() (initVal, closeVal any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.initValue, p.closeValue
}

// timedProbe is an lcProbe that sets its own init deadline.
type timedProbe[K any] struct {
	lcProbe[K]

	timeout time.Duration
}

func (p *timedProbe[K]) InitTimeout() time.Duration { return p.timeout }

type (
	lcTag0 struct{}
	lcTag1 struct{}
)

// waitForCancel blocks until ctx is done and returns its error. It gives up
// after two seconds so a missing deadline fails the test instead of hanging it.
func waitForCancel(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		return errors.New("init context was never canceled")
	}
}

// lcRegistration builds a worker holding probe either as a pre-built instance
// or behind a singleton constructor, the two ways a singleton Service can
// reach the container.
type lcRegistration struct {
	reg  func(p *lcProbe[lcTag0]) gas.WorkerOption
	name string
}

var lcRegistrations = []lcRegistration{
	{name: "instance", reg: gas.WithServiceInstance[*lcProbe[lcTag0]]},
	{name: "constructed", reg: func(p *lcProbe[lcTag0]) gas.WorkerOption {
		return gas.WithSingletonService[*lcProbe[lcTag0]](func() *lcProbe[lcTag0] { return p })
	}},
}

func TestInitContextCarriesDeadline(t *testing.T) {
	for _, r := range lcRegistrations {
		t.Run(r.name, func(t *testing.T) {
			p := &lcProbe[lcTag0]{name: "probe"}
			w := gas.NewWorker(gas.WithServiceInitTimeout(5*time.Second), r.reg(p))

			before := time.Now()
			if err := w.Start(t.Context()); err != nil {
				t.Fatalf("Start: %v", err)
			}

			dl, ok := p.deadline()
			if !ok {
				t.Fatal("Init context has no deadline")
			}
			if got := dl.Sub(before); got <= 0 || got > 5*time.Second+time.Second {
				t.Errorf("Init deadline is %v after Start, want about 5s", got)
			}
		})
	}
}

func TestInitContextCarriesCallerValues(t *testing.T) {
	for _, r := range lcRegistrations {
		t.Run(r.name, func(t *testing.T) {
			p := &lcProbe[lcTag0]{name: "probe"}
			w := gas.NewWorker(r.reg(p))

			ctx := context.WithValue(t.Context(), ctxKey{}, "caller")
			if err := w.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if got, _ := p.values(); got != "caller" {
				t.Errorf("Init saw context value %v, want caller", got)
			}
		})
	}
}

// When the deadline passes, the container cancels Init's context and waits
// for Init to return before Start reports the failure.
func TestInitDeadlineCancelsAndWaits(t *testing.T) {
	for _, r := range lcRegistrations {
		t.Run(r.name, func(t *testing.T) {
			p := &lcProbe[lcTag0]{name: "probe", initFn: func(ctx context.Context) error {
				err := waitForCancel(ctx)
				time.Sleep(50 * time.Millisecond) // cleanup after cancellation
				return err
			}}
			w := gas.NewWorker(gas.WithServiceInitTimeout(30*time.Millisecond), r.reg(p))

			err := w.Start(t.Context())
			if !p.initReturned.Load() {
				t.Error("Start returned while Init was still running")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Start error = %v, want context.DeadlineExceeded", err)
			}
			if !strings.Contains(err.Error(), "timed out after 30ms") {
				t.Errorf("Start error %q does not name the 30ms timeout", err)
			}
			if got := p.closeCalls.Load(); got != 0 {
				t.Errorf("Close called %d times on an Init that failed, want 0", got)
			}
		})
	}
}

// An Init that ignores its context and succeeds after the deadline has
// acquired whatever it acquires, but startup has failed and nothing will
// shut it down, so the container closes it.
func TestInitSucceedingAfterDeadlineIsClosed(t *testing.T) {
	for _, r := range lcRegistrations {
		t.Run(r.name, func(t *testing.T) {
			p := &lcProbe[lcTag0]{name: "probe", initFn: func(context.Context) error {
				time.Sleep(80 * time.Millisecond)
				return nil
			}}
			w := gas.NewWorker(gas.WithServiceInitTimeout(20*time.Millisecond), r.reg(p))

			err := w.Start(t.Context())
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Start error = %v, want context.DeadlineExceeded", err)
			}
			if !p.initReturned.Load() {
				t.Error("Start returned while Init was still running")
			}
			if got := p.closeCalls.Load(); got != 1 {
				t.Errorf("Close called %d times after a late Init, want 1", got)
			}
		})
	}
}

// The error a late Init's Close returns is reported alongside the timeout.
func TestInitSucceedingAfterDeadlineReportsCloseError(t *testing.T) {
	errClose := errors.New("close failed")
	p := &lcProbe[lcTag0]{name: "probe", closeErr: errClose, initFn: func(context.Context) error {
		time.Sleep(50 * time.Millisecond)
		return nil
	}}
	w := gas.NewWorker(gas.WithServiceInitTimeout(10*time.Millisecond), gas.WithServiceInstance(p))

	err := w.Start(t.Context())
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errClose) {
		t.Fatalf("Start error = %v, want both context.DeadlineExceeded and the Close error", err)
	}
}

func TestInitTimeoutZeroMeansNoDeadline(t *testing.T) {
	for _, r := range lcRegistrations {
		t.Run(r.name, func(t *testing.T) {
			for range 50 {
				p := &lcProbe[lcTag0]{name: "probe"}
				w := gas.NewWorker(gas.WithServiceInitTimeout(0), r.reg(p))
				if err := w.Start(t.Context()); err != nil {
					t.Fatalf("Start with a zero timeout: %v", err)
				}
				if _, ok := p.deadline(); ok {
					t.Fatal("Init context has a deadline with a zero timeout")
				}
			}
		})
	}
}

func TestInitTimeoutZeroKeepsCallerDeadline(t *testing.T) {
	p := &lcProbe[lcTag0]{name: "probe"}
	w := gas.NewWorker(gas.WithServiceInitTimeout(0), gas.WithServiceInstance(p))

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	want, _ := ctx.Deadline()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got, ok := p.deadline(); !ok || !got.Equal(want) {
		t.Errorf("Init deadline = %v (set %v), want the caller's %v", got, ok, want)
	}
}

func requirePanicPrefix(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Fatal("expected a panic")
		}
		if msg, _ := r.(string); !strings.HasPrefix(msg, "gas: ") {
			t.Errorf("panic %v does not start with gas:", r)
		}
	}()
	fn()
}

func TestInitTimeoutNegativePanics(t *testing.T) {
	t.Run("WithServiceInitTimeout", func(t *testing.T) {
		requirePanicPrefix(t, func() { gas.NewWorker(gas.WithServiceInitTimeout(-time.Second)) })
	})
	t.Run("WithServiceContainerServiceTimeout", func(t *testing.T) {
		requirePanicPrefix(t, func() {
			gas.NewServiceContainer(gas.WithServiceContainerServiceTimeout(-time.Second))
		})
	})
}

// A service's own InitTimeout replaces the worker default, for constructed
// services as well as instances.
func TestServiceInitTimeoutOverridesDefault(t *testing.T) {
	p := &timedProbe[lcTag1]{lcProbe: lcProbe[lcTag1]{name: "timed"}, timeout: 2 * time.Second}
	w := gas.NewWorker(
		gas.WithServiceInitTimeout(time.Hour),
		gas.WithSingletonService[*timedProbe[lcTag1]](func() *timedProbe[lcTag1] { return p }),
	)

	before := time.Now()
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	dl, ok := p.deadline()
	if !ok || dl.Sub(before) > 3*time.Second {
		t.Errorf("Init deadline %v after Start (set %v), want about 2s", dl.Sub(before), ok)
	}
}

func TestServiceInitTimeoutNegativeFailsStart(t *testing.T) {
	p := &timedProbe[lcTag1]{lcProbe: lcProbe[lcTag1]{name: "timed"}, timeout: -time.Second}
	w := gas.NewWorker(gas.WithServiceInstance(p))

	err := w.Start(t.Context())
	if err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("Start error = %v, want an error about the negative InitTimeout", err)
	}
	if p.initCalls.Load() != 0 {
		t.Error("Init ran despite a negative InitTimeout")
	}
}

func TestStartWithCanceledContextFails(t *testing.T) {
	for _, r := range lcRegistrations {
		t.Run(r.name, func(t *testing.T) {
			p := &lcProbe[lcTag0]{name: "probe", initFn: func(ctx context.Context) error {
				return ctx.Err()
			}}
			w := gas.NewWorker(r.reg(p))

			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := w.Start(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("Start error = %v, want context.Canceled", err)
			}
		})
	}
}

func TestRestartServiceInitHasDeadline(t *testing.T) {
	p := &lcProbe[lcTag0]{name: "probe"}
	w := gas.NewWorker(gas.WithServiceInitTimeout(5*time.Second), gas.WithServiceInstance(p))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := w.CloseService[*lcProbe[lcTag0]](context.Background()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}

	before := time.Now()
	if err := w.RestartService[*lcProbe[lcTag0]](t.Context()); err != nil {
		t.Fatalf("RestartService: %v", err)
	}
	dl, ok := p.deadline()
	if !ok || dl.Sub(before) > 6*time.Second {
		t.Errorf(
			"restart Init deadline %v after restart (set %v), want about 5s",
			dl.Sub(before),
			ok,
		)
	}
}

func TestRestartServiceInitDeadlineCancelsAndWaits(t *testing.T) {
	var restarting atomic.Bool
	p := &lcProbe[lcTag0]{name: "probe", initFn: func(ctx context.Context) error {
		if !restarting.Load() {
			return nil
		}
		return waitForCancel(ctx)
	}}
	w := gas.NewWorker(gas.WithServiceInitTimeout(30*time.Millisecond), gas.WithServiceInstance(p))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := w.CloseService[*lcProbe[lcTag0]](context.Background()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}

	restarting.Store(true)
	p.initReturned.Store(false)
	err := w.RestartService[*lcProbe[lcTag0]](t.Context())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RestartService error = %v, want context.DeadlineExceeded", err)
	}
	if !p.initReturned.Load() {
		t.Error("RestartService returned while Init was still running")
	}
}

// Scoped services get the scope's context, not a startup deadline.
func TestScopedInitGetsScopeContext(t *testing.T) {
	c := gas.NewServiceContainer()
	c.RegisterScopedService[*lcProbe[lcTag0]](func() *lcProbe[lcTag0] {
		return &lcProbe[lcTag0]{name: "scoped"}
	})
	if err := c.BuildAll(t.Context()); err != nil {
		t.Fatalf("BuildAll: %v", err)
	}

	scope := c.NewScope(context.WithValue(t.Context(), ctxKey{}, "request"))
	p, err := scope.Resolve[*lcProbe[lcTag0]]()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, _ := p.values(); got != "request" {
		t.Errorf("scoped Init saw context value %v, want request", got)
	}
	if _, ok := p.deadline(); ok {
		t.Error("scoped Init context has a deadline")
	}

	if err := scope.Close(context.WithValue(context.Background(), ctxKey{}, "close")); err != nil {
		t.Fatalf("Scope.Close: %v", err)
	}
	if _, got := p.values(); got != "close" {
		t.Errorf("scoped Close saw context value %v, want close", got)
	}
}

// --- Shutdown ---

func TestShutdownTwiceClosesOnce(t *testing.T) {
	p := &lcProbe[lcTag0]{name: "probe"}
	w := gas.NewWorker(gas.WithServiceInstance(p))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	for range 3 {
		if err := w.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}
	if got := p.closeCalls.Load(); got != 1 {
		t.Errorf("Close called %d times, want 1", got)
	}
}

func TestShutdownSkipsServiceClosedByKillSwitch(t *testing.T) {
	p := &lcProbe[lcTag0]{name: "probe"}
	w := gas.NewWorker(gas.WithServiceInstance(p))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := w.CloseService[*lcProbe[lcTag0]](context.Background()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := p.closeCalls.Load(); got != 1 {
		t.Errorf("Close called %d times after CloseService and Shutdown, want 1", got)
	}
}

// A service restarted after the kill switch is active again, so Shutdown
// closes it.
func TestShutdownClosesRestartedService(t *testing.T) {
	p := &lcProbe[lcTag0]{name: "probe"}
	w := gas.NewWorker(gas.WithServiceInstance(p))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := w.CloseService[*lcProbe[lcTag0]](context.Background()); err != nil {
		t.Fatalf("CloseService: %v", err)
	}
	if err := w.RestartService[*lcProbe[lcTag0]](t.Context()); err != nil {
		t.Fatalf("RestartService: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := p.closeCalls.Load(); got != 2 {
		t.Errorf("Close called %d times, want 2 (kill switch, then shutdown)", got)
	}
}

func TestShutdownReturnsCloseErrorsWithoutLogging(t *testing.T) {
	errA, errB := errors.New("a failed"), errors.New("b failed")
	a := &lcProbe[lcTag0]{name: "a", closeErr: errA}
	b := &lcProbe[lcTag1]{name: "b", closeErr: errB}
	rec := newRecordingLogger()
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](rec),
		gas.WithServiceInstance(a),
		gas.WithServiceInstance(b),
	)
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	err := w.Shutdown(context.Background())
	if !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Fatalf("Shutdown error = %v, want both Close errors", err)
	}
	if a.closeCalls.Load() != 1 || b.closeCalls.Load() != 1 {
		t.Error("a failing Close stopped the remaining services from closing")
	}
	for _, level := range rec.seen() {
		if level == "error" {
			t.Error("Shutdown both logged and returned a Close error")
		}
	}
}

func TestShutdownPassesCallerContextToClose(t *testing.T) {
	p := &lcProbe[lcTag0]{name: "probe"}
	w := gas.NewWorker(gas.WithServiceInstance(p))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := w.Shutdown(
		context.WithValue(context.Background(), ctxKey{}, "shutdown"),
	); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if _, got := p.values(); got != "shutdown" {
		t.Errorf("Close saw context value %v, want shutdown", got)
	}
}

// Concurrent Shutdown calls close each service once; every call returns only
// after the first has finished closing.
func TestShutdownConcurrentCallsCloseOnce(t *testing.T) {
	release := make(chan struct{})
	p := &blockingCloser{release: release}
	w := gas.NewWorker(gas.WithServiceInstance(p))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var wg sync.WaitGroup
	returned := make(chan struct{}, 8)
	for range 8 {
		wg.Go(func() {
			_ = w.Shutdown(context.Background())
			returned <- struct{}{}
		})
	}
	select {
	case <-returned:
		t.Fatal("a Shutdown call returned while the first was still closing")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	if got := p.closeCalls.Load(); got != 1 {
		t.Errorf("Close called %d times by concurrent Shutdowns, want 1", got)
	}
}

// blockingCloser is a Service whose Close blocks until release is closed.
type blockingCloser struct {
	release    chan struct{}
	closeCalls atomic.Int32
}

func (b *blockingCloser) Name() string               { return "blocking-closer" }
func (b *blockingCloser) Init(context.Context) error { return nil }
func (b *blockingCloser) Close(context.Context) error {
	b.closeCalls.Add(1)
	<-b.release
	return nil
}

// An Init that ignores its context and fails late with its own error reports
// both the init deadline and that error.
func TestInitFailingAfterDeadlineReportsBoth(t *testing.T) {
	errInit := errors.New("init failed")
	for _, r := range lcRegistrations {
		t.Run(r.name, func(t *testing.T) {
			p := &lcProbe[lcTag0]{name: "probe", initFn: func(context.Context) error {
				time.Sleep(50 * time.Millisecond)
				return errInit
			}}
			w := gas.NewWorker(gas.WithServiceInitTimeout(10*time.Millisecond), r.reg(p))

			err := w.Start(t.Context())
			if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, errInit) {
				t.Fatalf(
					"Start error = %v, want both context.DeadlineExceeded and the Init error",
					err,
				)
			}
			if got := p.closeCalls.Load(); got != 0 {
				t.Errorf("Close called %d times on an Init that failed, want 0", got)
			}
		})
	}
}

// A caller deadline shorter than the init deadline reaches Init through ctx;
// the error is the caller's, not reported as the container's timeout.
func TestInitCallerDeadlineIsNotReportedAsInitTimeout(t *testing.T) {
	p := &lcProbe[lcTag0]{name: "probe", initFn: waitForCancel}
	w := gas.NewWorker(gas.WithServiceInitTimeout(time.Minute), gas.WithServiceInstance(p))

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	err := w.Start(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start error = %v, want context.DeadlineExceeded", err)
	}
	if strings.Contains(err.Error(), "timed out after") {
		t.Errorf("Start error %q reports the caller's deadline as the init timeout", err)
	}
}

// Init failing fails Start, and that service is never closed by Shutdown.
func TestShutdownAfterFailedStartDoesNotCloseFailedService(t *testing.T) {
	errInit := errors.New("init failed")
	p := &lcProbe[lcTag0]{name: "probe", initFn: func(context.Context) error { return errInit }}
	w := gas.NewWorker(gas.WithServiceInstance(p))

	if err := w.Start(t.Context()); !errors.Is(err, errInit) {
		t.Fatalf("Start error = %v, want the Init error", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := p.closeCalls.Load(); got != 0 {
		t.Errorf("Close called %d times on a service whose Init failed, want 0", got)
	}
}

// scopeProbe is a scoped Service that records the contexts its Init and
// Close receive.
type scopeProbe struct {
	initCtx  context.Context
	closeCtx context.Context
}

func (s *scopeProbe) Name() string { return "scope-probe" }

func (s *scopeProbe) Init(ctx context.Context) error {
	s.initCtx = ctx
	return nil
}

func (s *scopeProbe) Close(ctx context.Context) error {
	s.closeCtx = ctx
	return nil
}

// In an App, a scoped service's Init gets the request context, and its Close
// gets that context without its cancellation, so a client that disconnects
// does not stop scoped services from cleaning up.
func TestRequestScopeContexts(t *testing.T) {
	probe := &scopeProbe{}
	app := gas.NewApp(gas.WithScopedService[*scopeProbe](func() *scopeProbe { return probe }))
	if err := app.InitServices(t.Context()); err != nil {
		t.Fatalf("InitServices: %v", err)
	}
	app.Router().Handle(nil, "GET", "/scoped", func(ctx gas.Context, _ *scopeProbe) error {
		return ctx.NoContent()
	})

	reqCtx, cancel := context.WithCancel(context.WithValue(t.Context(), ctxKey{}, "request"))
	cancel() // the client has gone away
	req := httptest.NewRequestWithContext(reqCtx, "GET", "/scoped", nil)
	app.Router().ServeHTTP(httptest.NewRecorder(), req)

	if probe.initCtx == nil || probe.initCtx.Value(ctxKey{}) != "request" {
		t.Fatal("scoped Init did not get the request context")
	}
	if probe.closeCtx == nil {
		t.Fatal("scoped Close was not called")
	}
	if probe.closeCtx.Value(ctxKey{}) != "request" {
		t.Error("scoped Close context lost the request values")
	}
	if err := probe.closeCtx.Err(); err != nil {
		t.Errorf("scoped Close context is done (%v), want it detached from the client", err)
	}
}

// Services Shutdown closes leave ActiveServices.
func TestShutdownEmptiesActiveServices(t *testing.T) {
	p := &lcProbe[lcTag0]{name: "probe"}
	w := gas.NewWorker(gas.WithServiceInstance(p))
	if err := w.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := w.ActiveServices(); slices.Contains(got, "probe") {
		t.Errorf("ActiveServices after Shutdown = %v, want probe gone", got)
	}
}

// Once Shutdown has begun, the kill switch refuses to restart or close a
// service, so nothing is reopened after the worker has shut down.
func TestKillSwitchAfterShutdownFails(t *testing.T) {
	for name, call := range map[string]func(*gas.Worker) error{
		"RestartService": func(w *gas.Worker) error {
			return w.RestartService[*lcProbe[lcTag0]](context.Background())
		},
		"CloseService": func(w *gas.Worker) error {
			return w.CloseService[*lcProbe[lcTag0]](context.Background())
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := &lcProbe[lcTag0]{name: "probe"}
			w := gas.NewWorker(gas.WithServiceInstance(p))
			if err := w.Start(t.Context()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if err := w.Shutdown(context.Background()); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}

			err := call(w)
			if err == nil || !strings.Contains(err.Error(), "shut down") {
				t.Fatalf(
					"%s after Shutdown = %v, want an error saying the worker is shut down",
					name,
					err,
				)
			}
			if inits, closes := p.initCalls.Load(), p.closeCalls.Load(); inits != 1 || closes != 1 {
				t.Errorf("Init ran %d times and Close %d times, want 1 and 1", inits, closes)
			}
			if got := w.ActiveServices(); slices.Contains(got, "probe") {
				t.Errorf("ActiveServices = %v, want probe gone", got)
			}
		})
	}
}

// Shutdown racing the kill switch never leaves a service open: every Init
// is matched by a Close once all three calls return. One sequential close
// and restart runs first, so the race starts from a restarted service.
func TestShutdownRacingKillSwitchLeavesNothingOpen(t *testing.T) {
	for range 2000 {
		p := &lcProbe[lcTag0]{name: "probe"}
		w := gas.NewWorker(
			gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
			gas.WithServiceInstance(p),
		)
		if err := w.Start(t.Context()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := w.CloseService[*lcProbe[lcTag0]](context.Background()); err != nil {
			t.Fatalf("CloseService: %v", err)
		}
		if err := w.RestartService[*lcProbe[lcTag0]](context.Background()); err != nil {
			t.Fatalf("RestartService: %v", err)
		}

		// The goroutines wait on start so the three calls overlap.
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() { <-start; _ = w.Shutdown(context.Background()) })
		wg.Go(func() { <-start; _ = w.CloseService[*lcProbe[lcTag0]](context.Background()) })
		wg.Go(func() { <-start; _ = w.RestartService[*lcProbe[lcTag0]](context.Background()) })
		close(start)
		wg.Wait()

		if inits, closes := p.initCalls.Load(), p.closeCalls.Load(); inits != closes {
			t.Fatalf("Init ran %d times and Close %d times; a service was left open", inits, closes)
		}
		if got := w.ActiveServices(); slices.Contains(got, "probe") {
			t.Fatalf("ActiveServices = %v after Shutdown, want probe gone", got)
		}
	}
}
