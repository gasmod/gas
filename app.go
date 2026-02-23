package gas

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"sync"
	"syscall"
	"time"
)

type requestScopeKey struct{}

// RequestScope returns the per-request Scope from the request context.
// Panics if called outside the scope middleware (i.e. before InitServices
// installs it, or on a non-App-managed handler).
func RequestScope(r *http.Request) *Scope {
	s, ok := r.Context().Value(requestScopeKey{}).(*Scope)
	if !ok {
		panic("gas: no request scope in context — is the request served by an App-managed router?")
	}
	return s
}

// App manages service lifecycle, the HTTP server, and graceful shutdown.
type App struct {
	serviceContainer *ServiceContainer
	router           *Router
	eventBus         *EventBus
	cfg              *Config
	logger           *slog.Logger

	activeServices map[string]Service // runtime kill-switch tracking
	serviceOrder   []Service          // init order for reverse-close at shutdown

	customConfigProvided bool
	mu                   sync.Mutex
	initOnce             sync.Once
}

// AppOption configures an App.
type AppOption func(*App)

// WithService registers a constructor-based service with the given lifetime.
func WithService[T any](ctor any, lifetime ServiceLifetime) AppOption {
	return func(a *App) {
		RegisterCtor[T](a.serviceContainer, ctor, lifetime)
	}
}

// WithAppModule registers a constructor-based app module as a singleton.
// This is the equivalent to WithService(ctor, ServiceLifetimeSingleton).
func WithAppModule[T any](ctor any) AppOption {
	return func(a *App) {
		RegisterCtor[T](a.serviceContainer, ctor, ServiceLifetimeSingleton)
	}
}

// WithServiceInstance registers a pre-built service instance (singleton).
func WithServiceInstance[T any](val T) AppOption {
	return func(a *App) {
		RegisterInstance[T](a.serviceContainer, val)
	}
}

// WithErrorHandler configures the function that converts DI-aware handler
// errors into HTTP responses.
func WithErrorHandler(h ErrorHandler) AppOption {
	return func(a *App) {
		a.router.SetErrorHandler(h)
	}
}

// NewApp creates an App with the given options.
// Router and EventBus are created internally and registered in the container.
func NewApp(opts ...AppOption) *App {
	a := &App{
		cfg:              DefaultConfig(),
		serviceContainer: NewServiceContainer(),
		router:           NewRouter(),
		eventBus:         NewEventBus(),
		logger:           slog.With("component", "gas"),
		activeServices:   make(map[string]Service),
	}

	// Register infra as instances in the container.
	RegisterInstance[*Router](a.serviceContainer, a.router)
	RegisterInstance[*EventBus](a.serviceContainer, a.eventBus)

	for _, opt := range opts {
		opt(a)
	}

	// Install per-request scope middleware on the router at app initialization time, before services are registered.
	a.router.Use(MiddlewareFunc(requestScopeMiddleware(a.serviceContainer)))

	return a
}

// Config retrieves the application's configuration.
func (a *App) Config() *Config {
	return a.cfg
}

// Router returns the App's router.
func (a *App) Router() *Router { return a.router }

// EventBus returns the App's event bus.
func (a *App) EventBus() *EventBus { return a.eventBus }

// MigrationManager resolves the MigrationManager from the DI container.
// Returns nil if no MigrationManager is registered.
func (a *App) MigrationManager() MigrationManager {
	mgr, ok := Resolve[MigrationManager](a.serviceContainer)
	if !ok {
		return nil
	}
	return mgr
}

// ConfigProvider resolves the ConfigProvider from the DI container.
// Returns nil if no ConfigProvider is registered.
func (a *App) ConfigProvider() ConfigProvider {
	cfg, ok := Resolve[ConfigProvider](a.serviceContainer)
	if !ok {
		return nil
	}
	return cfg
}

// InitServices builds all singletons via the DI container (which calls
// Init() on each Service automatically), then collects Service instances
// for runtime management.
// DO NOT CALL THIS METHOD DIRECTLY. Use Run() instead.
func (a *App) InitServices() (err error) {
	a.initOnce.Do(func() {
		if err = a.serviceContainer.BuildAll(); err != nil {
			return
		}

		// Collect all singleton Service instances for tracking.
		a.serviceContainer.EachInstance(func(v reflect.Value) {
			if svc, ok := v.Interface().(Service); ok {
				a.mu.Lock()
				a.activeServices[svc.Name()] = svc
				a.serviceOrder = append(a.serviceOrder, svc)
				a.mu.Unlock()
			}
		})

		// Seal the router: flush all pending middleware first, then routes.
		// This ensures middleware registered during Init() is applied before
		// any routes, regardless of service initialization order.
		a.router.Seal()

		// Validate all DI-aware handler dependencies. This runs after Seal()
		// so handlers registered inside Group/Route callbacks are included.
		for _, ph := range *a.router.pendingHandlers {
			for _, depType := range ph.depTypes {
				if !a.serviceContainer.CanResolve(depType) {
					err = fmt.Errorf(
						"gas: handler %s %s (service %q): dependency %v is not registered in the container",
						ph.method, ph.path, ph.service, depType,
					)
					return
				}
			}
		}

		Emit(a.eventBus, SystemAllServicesInitialized, SystemAllServicesInitializedPayload{}).Wait()
	})
	return
}

func (a *App) bindConfig() error {
	if !a.customConfigProvided {
		if cfgProvider := a.ConfigProvider(); cfgProvider != nil {
			a.logger.Info("using config provider", "name", "ConfigProvider")
			if err := cfgProvider.Bind(a.cfg); err != nil {
				return fmt.Errorf("gas: config binding: %w", err)
			}
		}
	}

	if err := a.cfg.Validate(); err != nil {
		return fmt.Errorf("gas: config validation: %w", err)
	}

	Emit(a.eventBus, AppConfigUpdated, AppConfigUpdatedPayload{Config: *a.cfg}).Wait()

	return nil
}

// Run initializes all services, runs pending migrations, starts the HTTP
// server, and blocks until a shutdown signal is received.
func (a *App) Run() error {
	if err := a.InitServices(); err != nil {
		return err
	}

	if err := a.bindConfig(); err != nil {
		return err
	}

	a.logger = slog.With("component", "gas")

	// Run pending migrations.
	if migrationMgr := a.MigrationManager(); migrationMgr != nil {
		a.logger.Info("running pending migrations")
		if err := migrationMgr.RunPending(); err != nil {
			return fmt.Errorf("gas: migrations: %w", err)
		}
	}

	addr := fmt.Sprintf("%s:%d", a.cfg.Server.Host, a.cfg.Server.Port)

	srv := &http.Server{
		Addr:         addr,
		Handler:      a.router,
		ReadTimeout:  a.cfg.Server.ReadTimeout,
		WriteTimeout: a.cfg.Server.WriteTimeout,
		IdleTimeout:  a.cfg.Server.IdleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		a.logger.Info("server listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		a.logger.Info("shutdown signal received", "signal", sig)
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("gas: server error: %w", err)
		}
	}

	Emit(a.eventBus, SystemServerShuttingDown, SystemServerShuttingDownPayload{}).Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		a.logger.Error("server shutdown error", "error", err)
	}

	// Close all services in reverse init order.
	for i := len(a.serviceOrder) - 1; i >= 0; i-- {
		svc := a.serviceOrder[i]
		a.logger.Info("closing service", "service", svc.Name())
		if err := svc.Close(); err != nil {
			a.logger.Error("service close error", "service", svc.Name(), "error", err)
		}
	}

	a.logger.Info("shutdown complete")
	return nil
}
