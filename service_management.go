package gas

import (
	"context"
	"fmt"
	"reflect"
	"sync"
)

// ActiveServices returns the names of all currently active services.
func (w *Worker) ActiveServices() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	names := make([]string, 0, len(w.activeServices))
	for name := range w.activeServices {
		names = append(names, name)
	}
	return names
}

// CheckHealth runs CheckHealth concurrently on every active service that
// implements HealthReporter and returns a map of service name to result
// (nil if healthy). Services that do not implement HealthReporter are omitted.
func (w *Worker) CheckHealth(ctx context.Context) map[string]error {
	return w.runReporters(ctx, func(svc Service) (func(context.Context) error, bool) {
		r, ok := svc.(HealthReporter)
		if !ok {
			return nil, false
		}
		return r.CheckHealth, true
	})
}

// CheckReady runs CheckReady concurrently on every active service that
// implements ReadyReporter and returns a map of service name to result
// (nil if ready). Services that do not implement ReadyReporter are omitted.
func (w *Worker) CheckReady(ctx context.Context) map[string]error {
	return w.runReporters(ctx, func(svc Service) (func(context.Context) error, bool) {
		r, ok := svc.(ReadyReporter)
		if !ok {
			return nil, false
		}
		return r.CheckReady, true
	})
}

func (w *Worker) runReporters(
	ctx context.Context,
	pick func(Service) (func(context.Context) error, bool),
) map[string]error {
	w.mu.Lock()
	targets := make(map[string]func(context.Context) error, len(w.activeServices))
	for name, svc := range w.activeServices {
		if fn, ok := pick(svc); ok {
			targets[name] = fn
		}
	}
	w.mu.Unlock()

	results := make(map[string]error, len(targets))
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for name, fn := range targets {
		wg.Go(func() {
			err := safeCall(ctx, fn)
			mu.Lock()
			results[name] = err
			mu.Unlock()
		})
	}
	wg.Wait()
	return results
}

// safeCall runs fn with panic recovery so a misbehaving reporter cannot
// crash the health/ready aggregation.
func safeCall(ctx context.Context, fn func(context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return fn(ctx)
}

// CloseService performs the kill-switch sequence for the service registered
// under T at runtime:
//
//	w.CloseService[*auth.Service]()
//
// Infrastructure is cleaned up first, so even if Close panics or fails, the
// service's routes and event subscriptions are already gone. Its routes then
// answer 503, and named middleware it owns is disabled wherever it is used.
//
// The service is looked up among the instances the container has already
// built, so a registration that was never initialized is reported as an error
// rather than constructed here. Returns an error if T has no built instance or
// is not currently active; a failure from the service's own Close is logged,
// not returned, because the teardown above it has already happened.
func (w *Worker) CloseService[T Service]() error {
	w.mu.Lock()

	s, ok := w.serviceContainer.resolveBuilt[T]()
	if !ok {
		w.mu.Unlock()
		return fmt.Errorf(
			"gas: service %v has no built instance; it was never initialized, or is registered under a different type (lookup is by exact registration type)",
			reflect.TypeFor[T](),
		)
	}

	name := s.Name()

	svc, ok := w.activeServices[name]
	if !ok {
		w.mu.Unlock()
		return fmt.Errorf("gas: service %q is not active", name)
	}

	// 1. App sets this hook to remove routes and middleware.
	if w.onServiceClose != nil {
		w.onServiceClose(s)
	}

	// 2. Remove event subscriptions.
	if w.eventBus != nil {
		w.eventBus.RemoveByService(s)
	}

	// 3. Close the service (internal cleanup).
	if err := svc.Close(); err != nil {
		w.getLogger().Error("service close failed").Str("service", name).Err("error", err).Send()
	}

	// 4. Remove from active services.
	delete(w.activeServices, name)

	w.mu.Unlock()

	// 5. Notify all other services.
	w.eventBus.Emit[SystemServiceClosed](SystemServiceClosedPayload{ServiceName: name}).Wait()

	w.getLogger().Info("service closed").Str("service", name).Send()
	return nil
}

// RestartService re-initializes the previously closed service registered
// under T, re-running Init on the same instance so it registers its routes,
// middleware and subscriptions again:
//
//	w.RestartService[*auth.Service]()
//
// The service must have been registered with the Worker at construction time
// and built during InitServices (that is, it must be a singleton in
// serviceOrder). Returns an error if T has no built instance, is already
// active, or its Init fails.
func (w *Worker) RestartService[T Service]() error {
	w.mu.Lock()

	s, ok := w.serviceContainer.resolveBuilt[T]()
	if !ok {
		w.mu.Unlock()
		return fmt.Errorf(
			"gas: service %v has no built instance; it was never initialized, or is registered under a different type (lookup is by exact registration type)",
			reflect.TypeFor[T](),
		)
	}

	name := s.Name()

	if _, ok := w.activeServices[name]; ok {
		w.mu.Unlock()
		return fmt.Errorf("gas: service %q is already active", name)
	}

	// Find the service in the init order (singleton instance still exists).
	var svc Service
	for _, s := range w.serviceOrder {
		if s.Name() == name {
			svc = s
			break
		}
	}
	if svc == nil {
		w.mu.Unlock()
		return fmt.Errorf("gas: service %q not found", name)
	}

	// Re-initialize.
	if err := svc.Init(); err != nil {
		w.mu.Unlock()
		return fmt.Errorf("gas: re-init %s: %w", name, err)
	}

	w.activeServices[name] = svc

	w.mu.Unlock()

	w.eventBus.Emit[SystemServiceInitialized](SystemServiceInitializedPayload{ServiceName: name}).Wait()

	w.getLogger().Info("service restarted").Str("service", name).Send()
	return nil
}
