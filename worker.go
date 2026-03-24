package gas

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"reflect"
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
	// router.RemoveByModule.
	onServiceClose func(name string)

	activeServices map[string]Service // runtime kill-switch tracking
	serviceOrder   []Service          // init order for reverse-close at shutdown

	readyFuncs []func(*ServiceContainer) error

	mu       sync.Mutex
	initOnce sync.Once
}

// NewWorker creates a Worker with the given options. Only WorkerOption values
// are applied; passing an AppOption panics.
func NewWorker(opts ...Option) *Worker {
	w := &Worker{
		serviceContainer: NewServiceContainer(),
		eventBus:         NewEventBus(),
		activeServices:   make(map[string]Service),
	}

	RegisterInstance[*EventBus](w.serviceContainer, w.eventBus)

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
	mgr, err := Resolve[MigrationManager](w.serviceContainer)
	if err != nil {
		return nil
	}
	return mgr
}

// ConfigProvider resolves the ConfigProvider from the DI container.
// Returns nil if no ConfigProvider is registered.
func (w *Worker) ConfigProvider() ConfigProvider {
	cfg, err := Resolve[ConfigProvider](w.serviceContainer)
	if err != nil {
		return nil
	}
	return cfg
}

// InitServices builds all singletons via the DI container (which calls
// Init() on each Service automatically), then collects Service instances
// for runtime management.
func (w *Worker) InitServices() (err error) {
	w.initOnce.Do(func() {
		if err = w.serviceContainer.BuildAll(); err != nil {
			return
		}

		// Collect all singleton Service instances for tracking.
		w.serviceContainer.EachInstance(func(v reflect.Value) {
			if svc, ok := v.Interface().(Service); ok {
				w.mu.Lock()
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

		Emit(w.eventBus, SystemAllServicesInitialized, SystemAllServicesInitializedPayload{}).Wait()
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

	// Run ready hooks.
	for _, fn := range w.readyFuncs {
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
	Emit(w.eventBus, SystemShuttingDown, SystemShuttingDownPayload{}).Wait()

	// Close all services in reverse init order.
	for i := len(w.serviceOrder) - 1; i >= 0; i-- {
		svc := w.serviceOrder[i]
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

func (w *Worker) getLogger() Logger {
	if w.logger == nil {
		// See if we have a logger registered
		logger, err := Resolve[Logger](w.serviceContainer)
		if err != nil {
			// fallback to slog
			logger = newSlogLogger(slog.Default())
			RegisterInstance[Logger](w.serviceContainer, logger)
			logger.Warn("no logger registered").Err("reason", err).Send()
		}
		w.logger = logger
	}
	return w.logger
}

// --- WorkerOption functions ---

// WithService registers a constructor-based service with the given lifetime.
func WithService[T any](ctor any, lifetime ServiceLifetime) WorkerOption {
	return func(w *Worker) { RegisterCtor[T](w.serviceContainer, ctor, lifetime) }
}

// WithServiceInstance registers a pre-built service instance (singleton).
func WithServiceInstance[T any](val T) WorkerOption {
	return func(w *Worker) { RegisterInstance[T](w.serviceContainer, val) }
}

// WithTransientService registers a transient service constructor.
func WithTransientService[T any](ctor any) WorkerOption {
	return func(w *Worker) { RegisterCtor[T](w.serviceContainer, ctor, ServiceLifetimeTransient) }
}

// WithScopedService registers a service constructor with a scoped lifetime.
func WithScopedService[T any](ctor any) WorkerOption {
	return func(w *Worker) { RegisterCtor[T](w.serviceContainer, ctor, ServiceLifetimeScoped) }
}

// WithSingletonService registers a singleton service constructor.
func WithSingletonService[T any](ctor any) WorkerOption {
	return func(w *Worker) { RegisterCtor[T](w.serviceContainer, ctor, ServiceLifetimeSingleton) }
}

// WithReadyFunc registers a function that runs after all services are
// initialized and migrations are applied but before Run blocks or Start
// returns. Multiple funcs are called in registration order; any error
// aborts startup.
func WithReadyFunc(fn func(*ServiceContainer) error) WorkerOption {
	return func(w *Worker) { w.readyFuncs = append(w.readyFuncs, fn) }
}
