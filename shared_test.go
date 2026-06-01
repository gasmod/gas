package gas_test

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/gasmod/gas"
)

// testService is a minimal Service implementation for testing App lifecycle.
type testService struct {
	initErr   error
	closeErr  error
	name      string
	initCount atomic.Int32
	closed    atomic.Bool
}

func (s *testService) Name() string { return s.name }

func (s *testService) Init() error {
	s.initCount.Add(1)
	s.closed.Store(false)
	return s.initErr
}

func (s *testService) Close() error {
	s.closed.Store(true)
	return s.closeErr
}

var _ gas.Service = (*testService)(nil)

// reporterService is a Service that optionally reports health and readiness.
// The phantom type parameter K lets tests register several instances under
// distinct container types (e.g. *reporterService[tagA], *reporterService[tagB]).
type reporterService[K any] struct {
	healthErr error
	readyErr  error
	name      string
}

func (s *reporterService[K]) Name() string                        { return s.name }
func (s *reporterService[K]) Init() error                         { return nil }
func (s *reporterService[K]) Close() error                        { return nil }
func (s *reporterService[K]) CheckHealth(_ context.Context) error { return s.healthErr }
func (s *reporterService[K]) CheckReady(_ context.Context) error  { return s.readyErr }

type tagA struct{}
type tagB struct{}
type tagC struct{}

// healthOnlyService implements HealthReporter but not ReadyReporter.
type healthOnlyService[K any] struct {
	err  error
	name string
}

func (s *healthOnlyService[K]) Name() string                        { return s.name }
func (s *healthOnlyService[K]) Init() error                         { return nil }
func (s *healthOnlyService[K]) Close() error                        { return nil }
func (s *healthOnlyService[K]) CheckHealth(_ context.Context) error { return s.err }

// readyOnlyService implements ReadyReporter but not HealthReporter.
type readyOnlyService[K any] struct {
	err  error
	name string
}

func (s *readyOnlyService[K]) Name() string                       { return s.name }
func (s *readyOnlyService[K]) Init() error                        { return nil }
func (s *readyOnlyService[K]) Close() error                       { return nil }
func (s *readyOnlyService[K]) CheckReady(_ context.Context) error { return s.err }

// slowReporter blocks inside CheckHealth for the given delay. Used to verify
// that CheckHealth runs reporters concurrently.
type slowReporter[K any] struct {
	started chan struct{}
	name    string
	delay   time.Duration
}

func (s *slowReporter[K]) Name() string { return s.name }
func (s *slowReporter[K]) Init() error  { return nil }
func (s *slowReporter[K]) Close() error { return nil }
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

func (s *panicReporter[K]) Name() string                        { return s.name }
func (s *panicReporter[K]) Init() error                         { return nil }
func (s *panicReporter[K]) Close() error                        { return nil }
func (s *panicReporter[K]) CheckHealth(_ context.Context) error { panic(s.message) }

// ctxObservingReporter records the context it was called with.
type ctxObservingReporter[K any] struct {
	seen chan context.Context
	name string
}

func (s *ctxObservingReporter[K]) Name() string { return s.name }
func (s *ctxObservingReporter[K]) Init() error  { return nil }
func (s *ctxObservingReporter[K]) Close() error { return nil }
func (s *ctxObservingReporter[K]) CheckHealth(ctx context.Context) error {
	s.seen <- ctx
	return nil
}

var (
	_ gas.Service        = (*reporterService[tagA])(nil)
	_ gas.HealthReporter = (*reporterService[tagA])(nil)
	_ gas.ReadyReporter  = (*reporterService[tagA])(nil)
)
