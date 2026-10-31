package gas_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

// fakeService is a Service that counts its Init and Close calls and runs
// initFn and closeFn, when set.
type fakeService struct {
	initFn  func(ctx context.Context) error
	closeFn func(ctx context.Context) error
	name    string
	inits   atomic.Int32
	closes  atomic.Int32
}

var _ gas.Service = (*fakeService)(nil)

// Name returns s.name.
func (s *fakeService) Name() string { return s.name }

// Init counts the call and runs initFn.
func (s *fakeService) Init(ctx context.Context) error {
	s.inits.Add(1)

	if s.initFn != nil {
		return s.initFn(ctx)
	}

	return nil
}

// Close counts the call and runs closeFn.
func (s *fakeService) Close(ctx context.Context) error {
	s.closes.Add(1)

	if s.closeFn != nil {
		return s.closeFn(ctx)
	}

	return nil
}

// otherService is a second Service type, so two services can be
// registered under different types.
type otherService struct{ fakeService }

// mapConfig is a ConfigProvider whose Bind fills dest from values through
// JSON, so keys match fields case-insensitively and durations are
// nanoseconds, or returns err. Its other methods are not used.
type mapConfig struct {
	gas.ConfigProvider

	err    error
	values map[string]any
}

// Bind fills dest from p.values, or returns p.err.
func (p *mapConfig) Bind(dest any) error {
	if p.err != nil {
		return p.err
	}

	b, err := json.Marshal(p.values)
	if err != nil {
		return err
	}

	return json.Unmarshal(b, dest)
}

// eventRecorder records events in order, safe for concurrent use.
type eventRecorder struct {
	events []string
	mu     sync.Mutex
}

// add records ev.
func (r *eventRecorder) add(ev string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.events = append(r.events, ev)
}

// all returns a copy of the events.
func (r *eventRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.events)
}

// serviceLogger is a Logger that is also a Service, and records its Info
// messages, Flush and Close in order.
type serviceLogger struct {
	gas.NopLogger

	rec *eventRecorder
}

// Name returns `logger`.
func (*serviceLogger) Name() string { return "logger" }

// Init does nothing.
func (*serviceLogger) Init(context.Context) error { return nil }

// Close records `close`.
func (l *serviceLogger) Close(context.Context) error {
	l.rec.add("close")

	return nil
}

// Flush records `flush`.
func (l *serviceLogger) Flush() { l.rec.add("flush") }

// Info records msg.
func (l *serviceLogger) Info(msg string) gas.LogEvent {
	l.rec.add(msg)

	return l.NopLogger.Info(msg)
}

// startedWorker returns a started Worker with opts, shut down when the
// test ends.
func startedWorker(t *testing.T, opts ...gas.Option) *gas.Worker {
	t.Helper()

	opts = append([]gas.Option{gas.WithServiceInstance[gas.Logger](&gas.NopLogger{})}, opts...)

	w := gas.NewWorker(opts...)
	require.NoError(t, w.Start(t.Context()))
	t.Cleanup(func() { _ = w.Shutdown(context.WithoutCancel(t.Context())) })

	return w
}

// startedApp returns a started App with opts, stopped when the test ends.
func startedApp(t *testing.T, opts ...gas.Option) *gas.App {
	t.Helper()

	app := quietApp(opts...)
	require.NoError(t, app.Start(t.Context()))
	t.Cleanup(func() { _ = app.Stop(context.WithoutCancel(t.Context())) })

	return app
}

// quietApp returns an App with opts and a NopLogger.
func quietApp(opts ...gas.Option) *gas.App {
	opts = append([]gas.Option{gas.WithServiceInstance[gas.Logger](&gas.NopLogger{})}, opts...)

	return gas.NewApp(opts...)
}

// serve serves a request through h and returns the recorder.
func serve(h http.Handler, method, path string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	maps.Copy(req.Header, header)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	return rr
}

// Errors the tests return from fakes, as values so tests can match them.
var (
	errBFailed                        = errors.New("b failed")
	errRelationMissing                = errors.New(`pq: relation "internal_billing" does not exist`)
	errAFailed                        = errors.New("a failed")
	errAfterWrite                     = errors.New("after write")
	errBadInput                       = errors.New("bad input")
	errBadKey                         = errors.New("bad key")
	errBoom                           = errors.New("boom")
	errCacheCold                      = errors.New("cache cold")
	errCloseFailed2                   = errors.New("close failed")
	errConnectionRefused              = errors.New("connection refused")
	errCtorFailed                     = errors.New("ctor failed")
	errDialFailed                     = errors.New("dial failed")
	errDirtyMigration                 = errors.New("dirty migration")
	errDown                           = errors.New("down")
	errDropped                        = errors.New("dropped")
	errFlushFailed                    = errors.New("flush failed")
	errHookFailed                     = errors.New("hook failed")
	errInitBFailed                    = errors.New("init b failed")
	errInitContextWasNever            = errors.New("init context was never canceled")
	errInitFailed2                    = errors.New("init failed")
	errLate                           = errors.New("late")
	errNo                             = errors.New("no")
	errPlain                          = errors.New("plain")
	errPqPasswordAuthenticationFailed = errors.New(
		`pq: password authentication failed for user "admin"`,
	)
	errPqPasswordAuthenticationFailed2 = errors.New(
		"pq: password authentication failed for user admin at 10.0.0.5",
	)
	errRaw                = errors.New("raw")
	errScopedCloseBoom    = errors.New("scoped close boom")
	errSomethingWentWrong = errors.New("something went wrong")
	errSQLNoRowsIn        = errors.New("sql: no rows in result set")
	errWarming            = errors.New("warming")
	errX                  = errors.New("x")
)
