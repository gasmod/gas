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

// WithServiceInstance registers a pre-built service instance (singleton).
func WithServiceInstance[T any](val T) AppOption {
	return func(a *App) {
		RegisterInstance[T](a.serviceContainer, val)
	}
}

// NewApp creates an App with the given options.
// Router and EventBus are created internally and registered in the container.
func NewApp(opts ...AppOption) *App {
	a := &App{
		cfg:              DefaultConfig(),
		serviceContainer: NewServiceContainer(),
		logger:           slog.With("component", "gas"),
		activeServices:   make(map[string]Service),
	}

	// Create infra and register as instances in the container.
	a.router = NewRouter()
	a.eventBus = NewEventBus()
	RegisterInstance[*Router](a.serviceContainer, a.router)
	RegisterInstance[*EventBus](a.serviceContainer, a.eventBus)

	for _, opt := range opts {
		opt(a)
	}
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

		// Install per-request scope middleware on the router.
		a.router.mux.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				scope := a.serviceContainer.NewScope()
				defer func() { _ = scope.Close() }()
				ctx := context.WithValue(r.Context(), requestScopeKey{}, scope)
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		})

		a.Emit(SystemAllServicesInitialized, NewEventData())
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

	a.Emit(AppConfigUpdated, NewEventData().
		Set("config", a.cfg))

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

	addr := fmt.Sprintf("%s:%d", a.cfg.ServerHost, a.cfg.ServerPort)

	srv := &http.Server{
		Addr:         addr,
		Handler:      a.router,
		ReadTimeout:  a.cfg.ServerReadTimeout,
		WriteTimeout: a.cfg.ServerWriteTimeout,
		IdleTimeout:  a.cfg.ServerIdleTimeout,
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

	a.Emit(SystemServerShuttingDown, NewEventData())

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

// Emit sends an event with the given name and associated data using the event bus.
func (a *App) Emit(event string, data EventData) {
	if a.eventBus != nil {
		a.eventBus.Emit(event, data)
	}
}

// EmitAsync sends an event asynchronously using the event bus.
func (a *App) EmitAsync(event string, data EventData) *sync.WaitGroup {
	if a.eventBus == nil {
		return nil
	}
	return a.eventBus.EmitAsync(event, data)
}

// Subscribe registers a handler function for a specific event name.
func (a *App) Subscribe(event string, handler func(EventData)) {
	if a.eventBus != nil {
		a.eventBus.Subscribe(event, handler)
	}
}

// SubscribeWithOwner registers an event handler under a service's ownership.
func (a *App) SubscribeWithOwner(service, event string, handler func(EventData)) {
	if a.eventBus != nil {
		a.eventBus.SubscribeWithOwner(service, event, handler)
	}
}
