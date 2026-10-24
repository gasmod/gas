package gas_test

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-playground/validator/v10"

	"github.com/gasmod/gas"
)

// hostConfig returns a ConfigProvider whose Bind sets only Server.Host and
// then validates with a real validator, as the config module does. Core does
// not depend on config, so its tests fake the provider.
func hostConfig(t *testing.T, host string) gas.ConfigProvider {
	t.Helper()
	return &hostProvider{host: host}
}

type hostProvider struct {
	gas.ConfigProvider // methods other than Bind are unused; calling one panics

	host string
}

func (p *hostProvider) Bind(dest any, options ...gas.BindOption) error {
	opts := gas.BindOptions{Validate: true}
	for _, opt := range options {
		opt(&opts)
	}
	if cfg, ok := dest.(*gas.Config); ok {
		cfg.Server.Host = p.host
	}
	if !opts.Validate {
		return nil
	}
	return validator.New().Struct(dest) //nolint:wrapcheck // the test reads it as is
}

// recordingLogger records which level each log call used. Everything it does
// not override is handled by the embedded NopLogger.
type recordingLogger struct {
	gas.Logger

	levels []string
	mu     sync.Mutex
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{Logger: &gas.NopLogger{}}
}

func (l *recordingLogger) record(level string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.levels = append(l.levels, level)
}

func (l *recordingLogger) seen() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.levels...)
}

func (l *recordingLogger) Warn(msg string) gas.LogEvent {
	l.record("warn")
	return l.Logger.Warn(msg)
}

func (l *recordingLogger) Error(msg string) gas.LogEvent {
	l.record("error")
	return l.Logger.Error(msg)
}

// assertPanics fails the test if fn does not panic.
func assertPanics(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for %s", name)
		}
	}()
	fn()
}

// testDep is a simple dependency for DI-aware handler tests.
type testDep struct {
	Value string
}

func newTestDep() *testDep { return &testDep{Value: "resolved"} }

// testDepB is a second dependency for multi-dep tests.
type testDepB struct {
	Count int
}

// Greeter is an interface dependency for interface resolution tests.
type Greeter interface {
	Greet() string
}

type helloGreeter struct{}

func (h *helloGreeter) Greet() string { return "hello" }

func newHelloGreeter() *helloGreeter { return &helloGreeter{} }

func panicking(w http.ResponseWriter, r *http.Request) { panic("plain boom") }

// A type that carries an Init method but does not fully implement gas.Service
// used to register fine and then do nothing: the container's auto-Init only
// fires for a complete Service, so Init was never called, Close was never
// called at shutdown, and the kill switch could not see it. Forgetting Name or
// Close while writing a service is the easy way to land there, so registration
// now rejects the type instead.

// initOnly implements Init and nothing else — the reported failure mode.
type initOnly struct{ inited bool }

func (s *initOnly) Init(context.Context) error { s.inited = true; return nil }

// testService is a minimal Service implementation for testing App lifecycle.
type testService struct {
	initErr   error
	closeErr  error
	name      string
	initCount atomic.Int32
	closed    atomic.Bool
}

func (s *testService) Name() string { return s.name }

func (s *testService) Init(context.Context) error {
	s.initCount.Add(1)
	s.closed.Store(false)
	return s.initErr
}

func (s *testService) Close(context.Context) error {
	s.closed.Store(true)
	return s.closeErr
}

var _ gas.Service = (*testService)(nil)

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

// Worker.Shutdown documents that it "closes all services in reverse
// initialization order", which is what makes it safe for a service to use its
// dependencies during Close.
//
// InitServices builds w.serviceOrder by iterating EachInstance. When the
// container kept its instances in a plain map, that iteration was randomized,
// so serviceOrder was a random permutation of the services and a service
// could be closed after a dependency it uses inside Close. EachInstance now
// yields instances in the order they became available, which is a valid
// initialization order.
//
// The chain below is S0 <- S1 <- ... <- S5 (each depends on the previous), so
// the only correct close order is S5, S4, S3, S2, S1, S0.

type (
	ordTag0 struct{}
	ordTag1 struct{}
	ordTag2 struct{}
	ordTag3 struct{}
	ordTag4 struct{}
	ordTag5 struct{}
)

// ordLog records init and close calls in the order they happen.
type ordLog struct {
	inits  []string
	closes []string
	mu     sync.Mutex
}

func (l *ordLog) init(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inits = append(l.inits, name)
}

func (l *ordLog) close(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closes = append(l.closes, name)
}

func (l *ordLog) snapshot() (inits, closes []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.inits...), append([]string(nil), l.closes...)
}

// ordService is a complete gas.Service. The phantom type parameter K gives each
// link in the chain a distinct type for the container to key on.
type ordService[K any] struct {
	log  *ordLog
	name string
}

func (s *ordService[K]) Name() string { return s.name }

func (s *ordService[K]) Init(context.Context) error { s.log.init(s.name); return nil }

func (s *ordService[K]) Close(context.Context) error { s.log.close(s.name); return nil }
