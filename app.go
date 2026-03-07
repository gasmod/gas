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
	csrfProtection   *http.CrossOriginProtection
	logger           Logger

	activeServices map[string]Service // runtime kill-switch tracking
	serviceOrder   []Service          // init order for reverse-close at shutdown

	readyFuncs           []func(*ServiceContainer) error
	customConfigProvided bool
	mu                   sync.Mutex
	initOnce             sync.Once
}

// AppOption configures an App.
type AppOption func(*App)

// WithService registers a constructor-based service with the given lifetime.
func WithService[T any](ctor any, lifetime ServiceLifetime) AppOption {
	return func(a *App) { RegisterCtor[T](a.serviceContainer, ctor, lifetime) }
}

// WithAppModule registers a constructor-based app module as a singleton.
// This is the equivalent to WithService(ctor, ServiceLifetimeSingleton).
func WithAppModule[T any](ctor any) AppOption {
	return func(a *App) { RegisterCtor[T](a.serviceContainer, ctor, ServiceLifetimeSingleton) }
}

// WithServiceInstance registers a pre-built service instance (singleton).
func WithServiceInstance[T any](val T) AppOption {
	return func(a *App) { RegisterInstance[T](a.serviceContainer, val) }
}

// WithTransientService registers a transient service constructor for a specified type in the application's service container.
func WithTransientService[T any](ctor any) AppOption {
	return func(a *App) { RegisterCtor[T](a.serviceContainer, ctor, ServiceLifetimeTransient) }
}

// WithScopedService registers a service constructor with a scoped lifetime for the application.
func WithScopedService[T any](ctor any) AppOption {
	return func(a *App) { RegisterCtor[T](a.serviceContainer, ctor, ServiceLifetimeScoped) }
}

// WithSingletonService registers a singleton service constructor with the App, ensuring a single instance is shared.
func WithSingletonService[T any](ctor any) AppOption {
	return func(a *App) { RegisterCtor[T](a.serviceContainer, ctor, ServiceLifetimeSingleton) }
}

// WithReadyFunc registers a function that runs after all services are
// initialized but before the HTTP server starts. Use it for data seeding
// or other startup tasks that require a live DI container.
// Multiple funcs are called in registration order and any error aborts startup.
func WithReadyFunc(fn func(*ServiceContainer) error) AppOption {
	return func(a *App) { a.readyFuncs = append(a.readyFuncs, fn) }
}

// WithErrorHandler configures the function that converts DI-aware handler
// errors into HTTP responses.
func WithErrorHandler(h ErrorHandler) AppOption {
	return func(a *App) { a.router.SetErrorHandler(h) }
}

// WithTrustedOrigin adds an origin that is permitted to make cross-origin
// non-safe requests (POST, PUT, PATCH, DELETE, etc.). The origin must be an
// absolute URL with a scheme and host, e.g. "https://app.example.com".
// Panics if the origin is not a valid absolute URL.
func WithTrustedOrigin(origin string) AppOption {
	return func(a *App) {
		if err := a.csrfProtection.AddTrustedOrigin(origin); err != nil {
			panic(fmt.Errorf("failed to add trusted origin: %w", err))
		}
	}
}

// WithCSRFInsecureBypassPattern adds a URL path pattern that bypasses CSRF
// cross-origin protection. Use only for endpoints that require unauthenticated
// cross-origin access and implement their own request validation (e.g. webhook
// receivers).
func WithCSRFInsecureBypassPattern(pattern string) AppOption {
	return func(a *App) { a.csrfProtection.AddInsecureBypassPattern(pattern) }
}

// WithCSRFDenyHandler sets the handler invoked when a cross-origin request is
// rejected by CSRF protection. The default handler returns 403 Forbidden.
func WithCSRFDenyHandler(h http.Handler) AppOption {
	return func(a *App) { a.csrfProtection.SetDenyHandler(h) }
}

// NewApp creates an App with the given options.
// Router and EventBus are created internally and registered in the container.
func NewApp(opts ...AppOption) *App {
	a := &App{
		cfg:              DefaultConfig(),
		serviceContainer: NewServiceContainer(),
		router:           NewRouter(),
		eventBus:         NewEventBus(),
		csrfProtection:   http.NewCrossOriginProtection(),
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

// ServiceContainer returns the application's dependency injection container.
func (a *App) ServiceContainer() *ServiceContainer {
	return a.serviceContainer
}

// MigrationManager resolves the MigrationManager from the DI container.
// Returns nil if no MigrationManager is registered.
func (a *App) MigrationManager() MigrationManager {
	mgr, err := Resolve[MigrationManager](a.serviceContainer)
	if err != nil {
		return nil
	}
	return mgr
}

// ConfigProvider resolves the ConfigProvider from the DI container.
// Returns nil if no ConfigProvider is registered.
func (a *App) ConfigProvider() ConfigProvider {
	cfg, err := Resolve[ConfigProvider](a.serviceContainer)
	if err != nil {
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

	// Run pending migrations.
	if migrationMgr := a.MigrationManager(); migrationMgr != nil {
		a.getLogger().Info("applying pending migrations").Send()
		if mErr := migrationMgr.RunPending(); mErr != nil {
			return fmt.Errorf("gas: migrations: %w", mErr)
		}
	}

	// Run ready hooks (e.g. data seeding) after migrations but before the server accepts traffic.
	for _, fn := range a.readyFuncs {
		if fnErr := fn(a.serviceContainer); fnErr != nil {
			a.getLogger().Error("ready hook failed").Err("error", fnErr).Send()
			return fmt.Errorf("gas: ready hook: %w", fnErr)
		}
	}

	// log route map
	a.logRouteMap(a.cfg.GasEnv.IsDevelopment())

	addr := fmt.Sprintf("%s:%d", a.cfg.Server.Host, a.cfg.Server.Port)

	srv := &http.Server{
		Addr:         addr,
		Handler:      a.csrfProtection.Handler(a.router),
		ReadTimeout:  a.cfg.Server.ReadTimeout,
		WriteTimeout: a.cfg.Server.WriteTimeout,
		IdleTimeout:  a.cfg.Server.IdleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		a.getLogger().Info("server started").
			Str("environment", a.cfg.GasEnv.String()).
			Str("addr", addr).
			Send()
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		a.getLogger().Info("shutdown signal received").Str("signal", sig.String()).Send()
	case chnErr := <-errCh:
		if chnErr != nil {
			return fmt.Errorf("gas: server error: %w", chnErr)
		}
	}

	Emit(a.eventBus, SystemServerShuttingDown, SystemServerShuttingDownPayload{}).Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if srvErr := srv.Shutdown(ctx); srvErr != nil {
		a.getLogger().Error("server shutdown error").Err("error", srvErr).Send()
	}

	// Close all services in reverse init order.
	for i := len(a.serviceOrder) - 1; i >= 0; i-- {
		svc := a.serviceOrder[i]
		a.getLogger().Info("closing service").Str("service", svc.Name()).Send()
		if svcErr := svc.Close(); svcErr != nil {
			a.getLogger().Error("service close error").
				Str("service", svc.Name()).
				Err("error", svcErr).
				Send()
		}
	}

	a.getLogger().Info("shutdown complete").Send()
	return nil
}

func (a *App) getLogger() Logger {
	if a.logger == nil {
		// See if we have a logger registered
		logger, err := Resolve[Logger](a.serviceContainer)
		if err != nil {
			// fallback to slog
			logger = newSlogLogger(slog.Default())
			RegisterInstance[Logger](a.serviceContainer, logger)
			logger.Warn("no logger registered").Err("reason", err).Send()
		}
		a.logger = logger
	}
	return a.logger
}
