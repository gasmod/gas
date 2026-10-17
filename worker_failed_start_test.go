package gas_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gasmod/gas"
)

// startProbe records its lifecycle calls into a shared log.
type startProbe struct {
	initErr error
	log     *startLog
	name    string
}

type startLog struct {
	calls []string
	ctxs  []error // ctx.Err() seen by each Close
	mu    sync.Mutex
}

func (l *startLog) add(call string) {
	l.mu.Lock()
	l.calls = append(l.calls, call)
	l.mu.Unlock()
}

func (l *startLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

func (p *startProbe) Name() string { return p.name }

func (p *startProbe) Init(context.Context) error {
	p.log.add("init " + p.name)
	return p.initErr
}

func (p *startProbe) Close(ctx context.Context) error {
	p.log.add("close " + p.name)
	p.log.mu.Lock()
	p.log.ctxs = append(p.log.ctxs, ctx.Err())
	p.log.mu.Unlock()
	return nil
}

type (
	probeA struct{ *startProbe }
	probeB struct{ *startProbe }
	probeC struct{ *startProbe }
)

var errInitFailed = errors.New("init failed")

// newFailingWorker registers a, b, and c, with c depending on b and b on a,
// so they build in that order, and c's Init failing.
func newFailingWorker(log *startLog, opts ...gas.Option) *gas.Worker {
	all := append([]gas.Option{
		gas.WithServiceInstance[gas.Logger](gas.NewNopLogger()()),
		gas.WithSingletonService[*probeA](func() *probeA {
			return &probeA{&startProbe{name: "a", log: log}}
		}),
		gas.WithSingletonService[*probeB](func(*probeA) *probeB {
			return &probeB{&startProbe{name: "b", log: log}}
		}),
		gas.WithSingletonService[*probeC](func(*probeB) *probeC {
			return &probeC{&startProbe{name: "c", log: log, initErr: errInitFailed}}
		}),
	}, opts...)
	return gas.NewWorker(all...)
}

// A Start that fails while initializing services closes, in reverse order,
// the services it had initialized, and not the one whose Init failed.
func TestWorker_FailedInitClosesInitialized(t *testing.T) {
	t.Parallel()

	log := &startLog{}
	w := newFailingWorker(log)

	err := w.Start(context.Background())
	if !errors.Is(err, errInitFailed) {
		t.Fatalf("Start() = %v, want %v", err, errInitFailed)
	}

	want := []string{"init a", "init b", "init c", "close b", "close a"}
	if got := log.snapshot(); !equalStrings(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}

	// Nothing is left for Shutdown to close.
	if err := w.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown() after a failed Start = %v, want nil", err)
	}
	if got := log.snapshot(); len(got) != len(want) {
		t.Errorf("Shutdown closed again: calls = %v", got)
	}
}

// A second Start after a failed one returns the original error and runs
// nothing again.
func TestWorker_StartAfterFailureReturnsOriginalError(t *testing.T) {
	t.Parallel()

	log := &startLog{}
	w := newFailingWorker(log)

	first := w.Start(context.Background())
	if first == nil {
		t.Fatal("first Start() = nil, want an error")
	}
	calls := len(log.snapshot())

	second := w.Start(context.Background())
	if !errors.Is(second, errInitFailed) {
		t.Errorf("second Start() = %v, want %v", second, errInitFailed)
	}
	if got := log.snapshot(); len(got) != calls {
		t.Errorf("second Start ran more lifecycle calls: %v", got[calls:])
	}
}

// A Start that fails after every service is initialized, in a ready hook,
// closes every service in reverse order.
func TestWorker_FailedReadyHookClosesAll(t *testing.T) {
	t.Parallel()

	log := &startLog{}
	hookErr := errors.New("hook failed")
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](gas.NewNopLogger()()),
		gas.WithSingletonService[*probeA](func() *probeA {
			return &probeA{&startProbe{name: "a", log: log}}
		}),
		gas.WithSingletonService[*probeB](func(*probeA) *probeB {
			return &probeB{&startProbe{name: "b", log: log}}
		}),
		gas.WithReadyFunc(func(*gas.ServiceContainer) error { return hookErr }),
	)

	if err := w.Start(context.Background()); !errors.Is(err, hookErr) {
		t.Fatalf("Start() = %v, want %v", err, hookErr)
	}
	want := []string{"init a", "init b", "close b", "close a"}
	if got := log.snapshot(); !equalStrings(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	if got := w.ActiveServices(); len(got) != 0 {
		t.Errorf("ActiveServices() after a failed Start = %v, want none", got)
	}
}

// Cleanup closes with a context that is not canceled, even when the
// caller's context was.
func TestWorker_FailedStartClosesWithoutCancel(t *testing.T) {
	t.Parallel()

	log := &startLog{}
	w := newFailingWorker(log)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_ = w.Start(ctx)

	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.ctxs) == 0 {
		t.Fatal("no service was closed")
	}
	for i, err := range log.ctxs {
		if err != nil {
			t.Errorf("Close %d got a context with error %v, want none", i, err)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// An App whose config fails validation after the Worker started closes
// every service, and a second Start returns the same error.
func TestApp_FailedConfigBindClosesAll(t *testing.T) {
	t.Parallel()

	log := &startLog{}
	app := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](gas.NewNopLogger()()),
		gas.WithSingletonService[*probeA](func() *probeA {
			return &probeA{&startProbe{name: "a", log: log}}
		}),
	)
	app.Config().Server.Host = ""

	first := app.Start(context.Background())
	if first == nil {
		t.Fatal("Start() = nil, want a config validation error")
	}
	want := []string{"init a", "close a"}
	if got := log.snapshot(); !equalStrings(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}

	second := app.Start(context.Background())
	if second == nil || second.Error() != first.Error() {
		t.Errorf("second Start() = %v, want %v", second, first)
	}
	if got := log.snapshot(); len(got) != len(want) {
		t.Errorf("second Start ran more lifecycle calls: %v", got)
	}
}
