package gas

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"reflect"
	"slices"
	"sync"
	"syscall"
)

// Worker manages service lifecycle, dependency injection, and the event bus
// without an HTTP server. Use it for non-HTTP environments such as AWS Lambda,
// background job processors, or CLI tools. For HTTP servers, use App which
// embeds Worker and adds routing, CSRF protection, and an HTTP listener.
type Worker struct {
	logger           Logger
	serviceContainer *ServiceContainer
	eventBus         *EventBus

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

	mu       sync.Mutex
	initOnce sync.Once
}

var _ HealthProvider = (*Worker)(nil)
var _ ReadyProvider = (*Worker)(nil)
var _ Service = (*Worker)(nil)

// NewWorker creates a Worker with the given options. Only WorkerOption values
// are applied; passing an AppOption panics.
func NewWorker(opts ...Option) *Worker {
	w := &Worker{
		serviceContainer: NewServiceContainer(),
		eventBus:         NewEventBus(),
		activeServices:   make(map[string]Service),
	}

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

// Name returns the service name of the Worker.
func (w *Worker) Name() string { return "gas/worker" }

// Init is a no-op. It exists so the Worker satisfies Service and can be passed
// as an owner; use InitServices or Start to bring the Worker up.
func (w *Worker) Init() error { return nil }

// Close is a no-op; use Shutdown to stop the Worker and close its services.
func (w *Worker) Close() error { return nil }

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
// Init() on each Service automatically), then collects Service instances
// for runtime management. Collection follows the container's instance order,
// so serviceOrder is a genuine initialization order and Shutdown can reverse
// it safely.
func (w *Worker) InitServices() (err error) {
	w.initOnce.Do(func() {
		if err = w.serviceContainer.BuildAll(); err != nil {
			return
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
			if err = w.postBuildHook(); err != nil {
				return
			}
		}

		w.eventBus.Emit[SystemAllServicesInitialized](SystemAllServicesInitializedPayload{}).Wait()
	})
	return
}

// Start initializes all services, runs pending migrations, and executes
// ready hooks. It does NOT block — use it when you need explicit lifecycle
// control (e.g. AWS Lambda). Call Shutdown when done.
func (w *Worker) Start() error {
	if err := w.InitServices(); err != nil {
		return err
	}

	// Run pending migrations.
	if migrationMgr := w.MigrationManager(); migrationMgr != nil {
		w.getLogger().Info("applying pending migrations").Send()
		if mErr := migrationMgr.RunPending(); mErr != nil {
			return fmt.Errorf("gas: migrations: %w", mErr)
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
			w.getLogger().Error("ready hook failed").Err("error", fnErr).Send()
			return fmt.Errorf("gas: ready hook: %w", fnErr)
		}
	}

	return nil
}

// Shutdown emits SystemShuttingDown and closes all services in reverse
// initialization order. Safe to call multiple times (subsequent calls are
// no-ops once services are closed).
func (w *Worker) Shutdown() error {
	w.eventBus.Emit[SystemShuttingDown](SystemShuttingDownPayload{}).Wait()

	// Close all services in reverse init order.
	for _, svc := range slices.Backward(w.serviceOrder) {
		w.getLogger().Info("closing service").Str("service", svc.Name()).Send()
		if svcErr := svc.Close(); svcErr != nil {
			w.getLogger().Error("service close error").
				Str("service", svc.Name()).
				Err("error", svcErr).
				Send()
		}
	}

	w.getLogger().Info("shutdown complete").Send()
	return nil
}

// Run initializes services, runs migrations and ready hooks, then blocks
// until a SIGINT or SIGTERM signal is received before shutting down.
// For non-blocking lifecycle control, use Start and Shutdown directly.
func (w *Worker) Run() error {
	if err := w.Start(); err != nil {
		return err
	}

	w.getLogger().Info("worker started").Send()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	sig := <-quit
	w.getLogger().Info("shutdown signal received").Str("signal", sig.String()).Send()

	return w.Shutdown()
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

func (w *Worker) getLogger() Logger {
	if w.logger == nil {
		// See if we have a logger registered
		logger, err := w.serviceContainer.Resolve[Logger]()
		if err != nil {
			// fallback to slog
			logger = newSlogLogger(slog.Default())
			w.serviceContainer.RegisterServiceInstance[Logger](logger)
			logger.Warn("no logger registered").Err("reason", err).Send()
		}
		w.logger = logger
	}
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
// implements Service, the container calls Init() on it during InitServices
// like any other service, and Close() at shutdown. Implementing Service is
// what hands the lifecycle to the container, however the value got there. Do
// not call Init() yourself before registering, or it runs twice.
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
