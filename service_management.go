package gas

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// Errors of the kill switch, as described under Kill switch in the package
// documentation.
var (
	// ErrNoBuiltInstance indicates that CloseService or RestartService
	// found no service the Worker started under the type: it was never
	// built, is not a singleton service, or is registered under another
	// type. Lookup is by exact registration type.
	ErrNoBuiltInstance = errors.New("gas: no built instance")

	// ErrServiceNotActive indicates a CloseService of a service that is
	// closed, or being closed or restarted, and a resolution of such a
	// service from the container.
	ErrServiceNotActive = errors.New("gas: service not active")

	// ErrServiceAlreadyActive indicates a RestartService of a service that
	// is active, or being closed or restarted.
	ErrServiceAlreadyActive = errors.New("gas: service already active")

	// ErrDuplicateServiceName indicates a CloseService or RestartService of
	// a service whose Name another service the Worker started shares.
	// Routes, middleware and subscriptions are owned by name, so the kill
	// switch cannot tell the two apart.
	ErrDuplicateServiceName = errors.New("gas: duplicate service name")
)

// serviceState is where a service the Worker started is.
type serviceState uint8

const (
	// serviceActive is a service that is initialized and not closed.
	serviceActive serviceState = iota
	// serviceClosing is a service CloseService is closing.
	serviceClosing
	// serviceClosed is a service that was closed.
	serviceClosed
	// serviceRestarting is a service RestartService is initializing.
	serviceRestarting
)

// trackedService is a service the Worker started, tracked by instance.
type trackedService struct {
	// svc is the instance.
	svc Service
	// state is where it is, and initGID the goroutine running its restart
	// Init while it is restarting, both guarded by the Worker's mu.
	state   serviceState
	initGID uint64
}

// sameInstance reports whether a and b are the same value: the same
// pointer, or equal comparable values. Values of a type that is not
// comparable are never the same, so the check never panics.
func sameInstance(a, b any) bool {
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	if ta != tb || ta == nil || !ta.Comparable() {
		return false
	}

	return a == b
}

// trackedLocked returns the tracked entry of svc, or nil. Must hold w.mu.
func (w *Worker) trackedLocked(svc Service) *trackedService {
	for _, ts := range w.services {
		if sameInstance(ts.svc, svc) {
			return ts
		}
	}

	return nil
}

// checkActive returns an error wrapping ErrServiceNotActive when v is a
// service the Worker started that is not active: closed, or being closed
// or restarted, but for its restart Init's own goroutine. The container
// calls it before it hands a singleton out.
func (w *Worker) checkActive(v reflect.Value) error {
	svc, ok := reflect.TypeAssert[Service](v)
	if !ok {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	ts := w.trackedLocked(svc)
	if ts == nil || ts.state == serviceActive {
		return nil
	}

	// A restarting service's own Init, on the goroutine running it, may
	// resolve the service, such as through a dependency of its own.
	if ts.state == serviceRestarting && ts.initGID == curGoroutineID() {
		return nil
	}

	return fmt.Errorf("%w: %q", ErrServiceNotActive, svc.Name())
}

// ActiveServices returns the names of the active services, in the order
// they were initialized. Two active services with one name both appear.
func (w *Worker) ActiveServices() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	names := make([]string, 0, len(w.services))
	for _, ts := range w.services {
		if ts.state == serviceActive {
			names = append(names, ts.svc.Name())
		}
	}

	return names
}

// CheckHealth runs CheckHealth concurrently on every active service that
// implements HealthReporter and returns each result by service name, nil
// when healthy. Services that do not implement HealthReporter are left
// out; the results of two services with one name are joined. A reporter
// that panics reports an error wrapping ErrPanic.
func (w *Worker) CheckHealth(ctx context.Context) map[string]error {
	return w.runReporters(
		ctx,
		"health check",
		func(svc Service) (func(context.Context) error, bool) {
			r, ok := svc.(HealthReporter)
			if !ok {
				return nil, false
			}

			return r.CheckHealth, true
		},
	)
}

// CheckReady runs CheckReady concurrently on every active service that
// implements ReadyReporter and returns each result by service name, nil
// when ready, as CheckHealth does for health.
func (w *Worker) CheckReady(ctx context.Context) map[string]error {
	return w.runReporters(
		ctx,
		"readiness check",
		func(svc Service) (func(context.Context) error, bool) {
			r, ok := svc.(ReadyReporter)
			if !ok {
				return nil, false
			}

			return r.CheckReady, true
		},
	)
}

// reporter is one check runReporters runs.
type reporter struct {
	// fn is the check.
	fn func(context.Context) error
	// name is the service's name.
	name string
}

// runReporters runs the check pick finds on each active service, all at
// once, without holding w.mu, and collects the results by name.
func (w *Worker) runReporters(
	ctx context.Context,
	what string,
	pick func(Service) (func(context.Context) error, bool),
) map[string]error {
	w.mu.Lock()

	targets := make([]reporter, 0, len(w.services))
	for _, ts := range w.services {
		if ts.state != serviceActive {
			continue
		}

		if fn, ok := pick(ts.svc); ok {
			targets = append(targets, reporter{name: ts.svc.Name(), fn: fn})
		}
	}

	w.mu.Unlock()

	results := make(map[string]error, len(targets))

	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)

	for _, t := range targets {
		wg.Go(func() {
			err := safeCall(ctx, what, t.fn)

			mu.Lock()
			defer mu.Unlock()

			if prev, seen := results[t.name]; seen {
				err = errors.Join(prev, err)
			}

			results[t.name] = err
		})
	}

	wg.Wait()

	return results
}

// safeCall runs fn and turns a panic into an error wrapping ErrPanic, so a
// misbehaving reporter cannot crash the aggregation.
func safeCall(ctx context.Context, what string, fn func(context.Context) error) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("%w in %s: %v", ErrPanic, what, rec)
		}
	}()

	return fn(ctx)
}

// beginOp finds the service the Worker started under t, the instance s,
// checks that a kill-switch operation may run on it, and moves it to
// next. want is the state it must be in, and wrong the error when it is
// not. On success the operation is counted in w.ops.
func (w *Worker) beginOp(
	t reflect.Type,
	s Service,
	built bool,
	want, next serviceState,
	wrong error,
) (*trackedService, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	switch w.state {
	case lifeIdle, lifeStarting, lifeFailed:
		return nil, ErrNotStarted
	case lifeShutDown:
		return nil, ErrShutDown
	case lifeStarted:
	}

	var ts *trackedService

	if built {
		ts = w.trackedLocked(s)
	}

	if ts == nil {
		return nil, fmt.Errorf("%w for %v", ErrNoBuiltInstance, t)
	}

	name := s.Name()

	for _, other := range w.services {
		if other != ts && other.svc.Name() == name {
			return nil, fmt.Errorf("%w %q", ErrDuplicateServiceName, name)
		}
	}

	if ts.state != want {
		return nil, fmt.Errorf("%w: %q", wrong, name)
	}

	ts.state = next

	w.ops.Add(1)
	w.opsN.Add(1)

	return ts, nil
}

// opDone ends an operation beginOp counted.
func (w *Worker) opDone() {
	w.opsN.Add(-1)
	w.ops.Done()
}

// closeAfterShutdown closes s, which a restart initialized after Shutdown
// began, logs any Close error, as Shutdown may have returned already, and
// returns an error wrapping ErrShutDown.
func (w *Worker) closeAfterShutdown(ctx context.Context, s Service) error {
	w.teardown(s)

	closeErr := closeService(ctx, s)
	if closeErr != nil {
		w.getLogger().
			Error("close after an abandoned shutdown failed").
			Err("error", closeErr).
			Send()
	}

	return fmt.Errorf("gas: restart %s: %w", s.Name(), ErrShutDown)
}

// setState moves ts to state.
func (w *Worker) setState(ts *trackedService, state serviceState) {
	w.mu.Lock()

	ts.state = state
	w.mu.Unlock()
}

// teardown removes what s registered with the router and the event bus.
func (w *Worker) teardown(s Service) {
	if w.onServiceClose != nil {
		w.onServiceClose(s)
	}

	w.eventBus.RemoveByService(s)
}

// CloseService runs the kill switch on the service registered under T, as
// described under Kill switch in the package documentation:
//
//	err := w.CloseService[*auth.Service](ctx)
//
// Its routes and named middleware answer 503 and its event subscriptions
// are removed before its Close runs, so even a Close that fails leaves
// nothing of it reachable. ctx is passed to Close, and a Close error is
// returned, wrapped; the service counts as closed either way.
// SystemServiceClosed is emitted once Close returns.
//
// It returns ErrNotStarted before Start succeeds, ErrShutDown once
// Shutdown begins, ErrNoBuiltInstance when the Worker started no service
// under T, ErrDuplicateServiceName when another started service has its
// name, and ErrServiceNotActive when it is not active. No lock is held
// while the service's code runs.
func (w *Worker) CloseService[T Service](ctx context.Context) error {
	s, built := w.serviceContainer.resolveBuilt[T]()

	ts, err := w.beginOp(
		reflect.TypeFor[T](),
		s,
		built,
		serviceActive,
		serviceClosing,
		ErrServiceNotActive,
	)
	if err != nil {
		return err
	}
	defer w.opDone()

	name := s.Name()

	w.teardown(s)

	err = closeService(ctx, s)

	w.setState(ts, serviceClosed)

	w.eventBus.Emit[SystemServiceClosed](SystemServiceClosedPayload{ServiceName: name}).Wait()

	w.getLogger().Info("service closed").Str("service", name).Send()

	return err
}

// RestartService runs Init again on the closed service registered under
// T, so it registers its routes, middleware and subscriptions anew, as
// described under Kill switch in the package documentation:
//
//	err := w.RestartService[*auth.Service](ctx)
//
// Init runs under the same deadline as at startup, with ctx. When it
// fails, what it had registered is removed again, the service stays
// closed, and the error is returned, wrapped. When it returns once
// Shutdown has begun, the restart closes the service, logs any Close
// error, and returns an error wrapping ErrShutDown. SystemServiceInitialized is
// emitted once it succeeds.
//
// It returns the errors CloseService does, with ErrServiceAlreadyActive in
// place of ErrServiceNotActive, for a service that is not closed.
func (w *Worker) RestartService[T Service](ctx context.Context) error {
	s, built := w.serviceContainer.resolveBuilt[T]()

	ts, err := w.beginOp(
		reflect.TypeFor[T](),
		s,
		built,
		serviceClosed,
		serviceRestarting,
		ErrServiceAlreadyActive,
	)
	if err != nil {
		return err
	}
	defer w.opDone()

	name := s.Name()

	w.mu.Lock()

	ts.initGID = curGoroutineID()
	w.mu.Unlock()

	err = w.serviceContainer.initService(ctx, reflect.TypeFor[T](), s)
	if err != nil {
		w.teardown(s)
		w.setState(ts, serviceClosed)

		return fmt.Errorf("gas: restart %s: %w", name, err)
	}

	if w.onServiceRestarted != nil {
		w.onServiceRestarted(s)
	}

	w.mu.Lock()

	if w.state == lifeShutDown {
		// Shutdown began during the Init, and closes no service that is
		// restarting, so the restart closes it.
		ts.state = serviceClosed
		w.mu.Unlock()

		return w.closeAfterShutdown(ctx, s)
	}

	ts.state = serviceActive
	w.mu.Unlock()

	w.eventBus.Emit[SystemServiceInitialized](
		SystemServiceInitializedPayload{ServiceName: name},
	).Wait()

	w.getLogger().Info("service restarted").Str("service", name).Send()

	return nil
}
