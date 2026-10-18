package gas

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"reflect"
	"slices"
	"sync"
	"syscall"
	"time"
)

// Worker manages service lifecycle, dependency injection, and the event bus
// without an HTTP server. Use it for non-HTTP environments such as AWS Lambda,
// background job processors, or CLI tools. For HTTP servers, use App which
// embeds Worker and adds routing, CSRF protection, and an HTTP listener.
type Worker struct {
	logger           Logger
	serviceContainer *ServiceContainer
	eventBus         *EventBus

	// initErr is the result of the one run of InitServices, and startErr the
	// error of a failed Start, which every later Start returns; startErr is
	// guarded by mu.
	initErr  error
	startErr error

	// postBuildHook is called by InitServices after BuildAll and service
	// collection but before emitting SystemAllServicesInitialized. App sets
	// this to seal the router and validate DI handler dependencies.
	postBuildHook func() error

	// onServiceClose is called by CloseService before removing event
	// subscriptions and closing the service. App sets this to call
	// router.RemoveByService.
	onServiceClose func(s Service)

	activeServices map[string]Service // runtime kill-switch tracking
	serviceOrder   []Service          // init order for reverse-close at shutdown

	readyFuncs []func(*ServiceContainer) error
	readyRan   bool // set once Start reaches the ready hooks; guarded by mu

	// shutDown is set when Shutdown begins, after which CloseService and
	// RestartService refuse to run; guarded by mu.
	shutDown bool

	mu           sync.Mutex
	initOnce     sync.Once
	shutdownOnce sync.Once
	loggerOnce   sync.Once // guards the one-time resolution of logger
}

var (
	_ HealthProvider = (*Worker)(nil)
	_ ReadyProvider  = (*Worker)(nil)
)

// NewWorker creates a Worker with the given options. Only WorkerOption values
// are applied; passing an AppOption panics.
func NewWorker(opts ...Option) *Worker {
	w := &Worker{
		serviceContainer: NewServiceContainer(),
		eventBus:         NewEventBus(),
		activeServices:   make(map[string]Service),
	}

	// Recovered subscriber panics are logged through the Worker's logger.
	w.eventBus.logger = w.getLogger
	w.serviceContainer.RegisterServiceInstance[*EventBus](w.eventBus)
	w.serviceContainer.RegisterServiceInstance[HealthProvider](w)
	w.serviceContainer.RegisterServiceInstance[ReadyProvider](w)

	for _, opt := range opts {
		switch o := opt.(type) {
		case WorkerOption:
			o(w)
		case AppOption:
			panic("gas: AppOption passed to NewWorker — use NewApp for HTTP options")
		}
	}

	return w
}

// EventBus returns the Worker's event bus.
func (w *Worker) EventBus() *EventBus { return w.eventBus }

// ServiceContainer returns the Worker's dependency injection container.
func (w *Worker) ServiceContainer() *ServiceContainer { return w.serviceContainer }

// MigrationManager resolves the MigrationManager from the DI container.
// Returns nil if no MigrationManager is registered.
func (w *Worker) MigrationManager() MigrationManager {
	mgr, err := w.serviceContainer.Resolve[MigrationManager]()
	if err != nil {
		return nil
	}
	return mgr
}

// ConfigProvider resolves the ConfigProvider from the DI container.
// Returns nil if no ConfigProvider is registered.
func (w *Worker) ConfigProvider() ConfigProvider {
	cfg, err := w.serviceContainer.Resolve[ConfigProvider]()
	if err != nil {
		return nil
	}
	return cfg
}

// InitServices builds all singletons via the DI container (which calls
// Init(ctx) on each Service automatically), then collects Service instances
// for runtime management. Collection follows the container's instance order,
// so serviceOrder is a genuine initialization order and Shutdown can reverse
// it safely. ctx is passed to the Init of every singleton, each under its
// init deadline (see WithServiceInitTimeout).
//
// InitServices runs once. If it fails, it closes, in reverse order, every
// service it had initialized, with ctx stripped of its cancellation, and
// returns the error joined with any Close errors; later calls return that
// same error.
func (w *Worker) InitServices(ctx context.Context) error {
	w.initOnce.Do(func() { w.initErr = w.initServices(ctx) })
	return w.initErr
}

// initServices is the one run of InitServices.
func (w *Worker) initServices(ctx context.Context) error {
	if err := w.serviceContainer.BuildAll(ctx); err != nil {
		// Nothing is tracked yet, so close what the container had
		// initialized before the failure.
		built := w.serviceContainer.initializedServices()
		slices.Reverse(built)
		//nolint:contextcheck // cleanup must run even when ctx is what failed
		return errors.Join(err, w.closeServices(context.WithoutCancel(ctx), built))
	}

	// Collect all singleton Service instances for tracking.
	w.serviceContainer.EachInstance(func(v reflect.Value) {
		if svc, ok := v.Interface().(Service); ok {
			w.mu.Lock()
			// An instance registered under several types (the Worker is both
			// HealthProvider and ReadyProvider) is tracked once, so shutdown
			// does not close it twice.
			if _, tracked := w.activeServices[svc.Name()]; tracked {
				w.mu.Unlock()
				return
			}
			w.activeServices[svc.Name()] = svc
			w.serviceOrder = append(w.serviceOrder, svc)
			w.mu.Unlock()
		}
	})

	// App injects router seal + handler validation via this hook.
	if w.postBuildHook != nil {
		if err := w.postBuildHook(); err != nil {
			//nolint:contextcheck // cleanup must run even when ctx is what failed
			return errors.Join(err, w.closeActive(context.WithoutCancel(ctx)))
		}
	}

	w.eventBus.Emit[SystemAllServicesInitialized](SystemAllServicesInitializedPayload{}).Wait()
	return nil
}

// Start initializes all services, runs pending migrations, and executes
// ready hooks. It does NOT block — use it when you need explicit lifecycle
// control (e.g. AWS Lambda). Call Shutdown when done. ctx is passed to the
// Init of every service built during startup; canceling it aborts startup.
//
// A Start that fails leaves nothing running: it closes, in reverse order,
// every service it had initialized, with ctx stripped of its cancellation,
// and returns the startup error joined with any Close errors. The Worker
// cannot be started again: every later Start returns the same error, and a
// later Shutdown has nothing to close.
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	failed := w.startErr
	w.mu.Unlock()
	if failed != nil {
		return failed
	}

	err := w.start(ctx)
	if err != nil {
		w.failStart(err)
	}
	return err
}

// failStart records err as the result of a failed Start, which every later
// Start returns.
func (w *Worker) failStart(err error) {
	w.mu.Lock()
	w.startErr = err
	w.mu.Unlock()
}

// start is one attempt of Start.
func (w *Worker) start(ctx context.Context) error {
	if err := w.InitServices(ctx); err != nil {
		return err
	}

	// Run pending migrations. Every singleton is built by now, so resolving
	// the manager runs no Init and needs no ctx.
	//nolint:contextcheck // see above
	if migrationMgr := w.MigrationManager(); migrationMgr != nil {
		w.getLogger().Info("applying pending migrations").Send()
		if mErr := migrationMgr.RunPending(); mErr != nil {
			//nolint:contextcheck // cleanup must run even when ctx is what failed
			return errors.Join(
				fmt.Errorf("gas: migrations: %w", mErr),
				w.closeActive(context.WithoutCancel(ctx)),
			)
		}
	}

	// Run ready hooks. Snapshot under the lock so a concurrent ReadyFunc
	// either lands before this point or panics; it is never silently dropped.
	w.mu.Lock()
	w.readyRan = true
	readyFuncs := slices.Clone(w.readyFuncs)
	w.mu.Unlock()

	for _, fn := range readyFuncs {
		if fnErr := fn(w.serviceContainer); fnErr != nil {
			//nolint:contextcheck // getLogger resolves the logger, which needs no caller ctx
			w.getLogger().Error("ready hook failed").Err("error", fnErr).Send()
			//nolint:contextcheck // cleanup must run even when ctx is what failed
			return errors.Join(
				fmt.Errorf("gas: ready hook: %w", fnErr),
				w.closeActive(context.WithoutCancel(ctx)),
			)
		}
	}

	return nil
}

// Shutdown emits SystemShuttingDown and closes every active service in
// reverse initialization order, passing ctx to each Close. A service already
// closed by CloseService is skipped, and each service Shutdown closes leaves
// ActiveServices. A failing Close does not stop the rest; every Close error
// is returned, joined, and not logged.
//
// Shutdown runs once. Later calls, including concurrent ones, wait for the
// first to finish and return nil. Once Shutdown has begun, CloseService and
// RestartService return an error.
func (w *Worker) Shutdown(ctx context.Context) (err error) {
	w.shutdownOnce.Do(func() {
		w.mu.Lock()
		w.shutDown = true
		w.mu.Unlock()

		w.eventBus.Emit[SystemShuttingDown](SystemShuttingDownPayload{}).Wait()

		err = w.closeActive(ctx)

		//nolint:contextcheck // getLogger resolves the logger, which needs no caller ctx
		w.getLogger().Info("shutdown complete").Send()
	})
	return
}

// closeActive closes every service still active, newest first, and removes
// each from ActiveServices first, so a CloseService racing with it cannot
// close one again. Every Close error is returned, joined.
func (w *Worker) closeActive(ctx context.Context) error {
	w.mu.Lock()
	closing := make([]Service, 0, len(w.activeServices))
	for _, svc := range slices.Backward(w.serviceOrder) {
		if _, active := w.activeServices[svc.Name()]; active {
			closing = append(closing, svc)
			delete(w.activeServices, svc.Name())
		}
	}
	w.mu.Unlock()

	return w.closeServices(ctx, closing)
}

// closeServices closes each service in order, passing ctx to every Close.
// A failing Close does not stop the rest; every Close error is returned,
// joined, and not logged.
func (w *Worker) closeServices(ctx context.Context, services []Service) error {
	var err error
	for _, svc := range services {
		//nolint:contextcheck // getLogger resolves the logger, which needs no caller ctx
		w.getLogger().Info("closing service").Str("service", svc.Name()).Send()
		if svcErr := svc.Close(ctx); svcErr != nil {
			err = errors.Join(err, fmt.Errorf("gas: close %s: %w", svc.Name(), svcErr))
		}
	}
	return err
}

// Run initializes services, runs migrations and ready hooks, then blocks
// until a SIGINT or SIGTERM signal is received before shutting down.
// For non-blocking lifecycle control, use Start and Shutdown directly.
// Startup and shutdown use context.Background().
func (w *Worker) Run() error {
	ctx := context.Background()
	if err := w.Start(ctx); err != nil {
		return err
	}

	w.getLogger().Info("worker started").Send()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	sig := <-quit
	w.getLogger().Info("shutdown signal received").Str("signal", sig.String()).Send()

	return w.Shutdown(ctx)
}

// ReadyFunc registers a function that runs after all services are initialized
// and migrations are applied, but before Run blocks or Start returns. It is
// the imperative form of WithReadyFunc and may be called any number of times
// before startup; the funcs run in registration order and the first error
// aborts startup.
//
// Safe for concurrent use. Panics if called once Start has reached the ready
// hooks, because the func would never run.
func (w *Worker) ReadyFunc(fn func(*ServiceContainer) error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.readyRan {
		panic("gas: ReadyFunc called after ready hooks have run")
	}
	w.readyFuncs = append(w.readyFuncs, fn)
}

// getLogger returns the Worker's logger, resolving it from the container
// the first time it is called. If no Logger is registered, it uses a logger
// over slog.Default and warns once; that fallback is not registered in the
// container. It is safe for concurrent use.
func (w *Worker) getLogger() Logger {
	w.loggerOnce.Do(func() {
		// See if we have a logger registered
		logger, err := w.serviceContainer.Resolve[Logger]()
		if err != nil {
			// Fall back to slog. The fallback stays private to the Worker:
			// registering it would write the container's instance map while
			// other goroutines read it.
			logger = newSlogLogger(slog.Default())
			logger.Warn("no logger registered").Err("reason", err).Send()
		}
		w.logger = logger
	})
	return w.logger
}

// --- ServiceContainer helpers ---

// RegisterService registers a constructor for type T with the given lifetime
// on the worker's service container. It is the imperative form of WithService,
// for wiring done after the Worker is built. See
// ServiceContainer.RegisterService for the accepted constructor signatures and
// the panics on a bad one.
func (w *Worker) RegisterService[T any](ctor any, lifetime ServiceLifetime) {
	w.serviceContainer.RegisterService[T](ctor, lifetime)
}

// RegisterTransientService registers a constructor for type T with the
// Transient lifetime on the worker's service container, so a fresh T is built
// on every resolution. T must not implement Service.
func (w *Worker) RegisterTransientService[T any](ctor any) {
	w.serviceContainer.RegisterTransientService[T](ctor)
}

// RegisterScopedService registers a constructor for type T with the Scoped
// lifetime on the worker's service container, so one T is built per Scope. In
// an App, each request gets its own scope; see ResolveFromRequestScope.
func (w *Worker) RegisterScopedService[T any](ctor any) {
	w.serviceContainer.RegisterScopedService[T](ctor)
}

// RegisterSingletonService registers a constructor for type T with the
// Singleton lifetime on the worker's service container, so one T is built and
// shared by every consumer.
func (w *Worker) RegisterSingletonService[T any](ctor any) {
	w.serviceContainer.RegisterSingletonService[T](ctor)
}

// RegisterServiceInstance registers an already-built value on the worker's
// service container under T, the static type at the call site, not the dynamic
// type of val. Treated as a singleton; see
// ServiceContainer.RegisterServiceInstance for the lifecycle it takes on.
func (w *Worker) RegisterServiceInstance[T any](val T) {
	w.serviceContainer.RegisterServiceInstance[T](val)
}

// --- WorkerOption functions ---

// WithService registers a constructor-based service with the given lifetime.
func WithService[T any](ctor any, lifetime ServiceLifetime) WorkerOption {
	return func(w *Worker) { w.serviceContainer.RegisterService[T](ctor, lifetime) }
}

// WithServiceInstance registers a pre-built service instance (singleton).
//
// Pre-built means pre-constructed, not pre-initialized: if the value
// implements Service, the container calls Init(ctx) on it during
// InitServices like any other service, and Close(ctx) at shutdown.
// Implementing Service is what hands the lifecycle to the container, however
// the value got there. Do not call Init yourself before registering, or it
// runs twice.
func WithServiceInstance[T any](val T) WorkerOption {
	return func(w *Worker) { w.serviceContainer.RegisterServiceInstance[T](val) }
}

// WithTransientService registers a transient service constructor.
func WithTransientService[T any](ctor any) WorkerOption {
	return func(w *Worker) { w.serviceContainer.RegisterTransientService[T](ctor) }
}

// WithScopedService registers a service constructor with a scoped lifetime.
func WithScopedService[T any](ctor any) WorkerOption {
	return func(w *Worker) { w.serviceContainer.RegisterScopedService[T](ctor) }
}

// WithSingletonService registers a singleton service constructor.
func WithSingletonService[T any](ctor any) WorkerOption {
	return func(w *Worker) { w.serviceContainer.RegisterSingletonService[T](ctor) }
}

// WithReadyFunc registers a function that runs after all services are
// initialized and migrations are applied but before Run blocks or Start
// returns. Multiple funcs are called in registration order; any error
// aborts startup.
func WithReadyFunc(fn func(*ServiceContainer) error) WorkerOption {
	return func(w *Worker) { w.ReadyFunc(fn) }
}

// WithServiceInitTimeout sets the deadline the Worker's service container
// gives each singleton service's Init, at startup and on RestartService. When
// it passes, Init's context is canceled and startup fails once Init returns.
// The default is 15 seconds, and 0 disables the deadline so only the caller's
// context applies. A service that implements ServiceInitTimeout overrides it
// for itself.
//
// Panics if timeout is negative.
func WithServiceInitTimeout(timeout time.Duration) WorkerOption {
	mustNotBeNegativeInitTimeout("WithServiceInitTimeout", timeout)
	return func(w *Worker) { w.serviceContainer.opt.serviceInitTimeout = timeout }
}
