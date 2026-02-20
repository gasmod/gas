package gas

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	config "github.com/gasmod/gas-config"
)

// App manages module lifecycle, the HTTP server, and graceful shutdown.
// It is constructed with functional options and wired together in main.go.
type App struct {
	migrationManagerModuleName string
	configModuleName           string
	router                     *Router
	eventBus                   *EventBus
	activeModules              map[string]Module
	cfg                        *Config
	logger                     *slog.Logger
	modules                    []Module
	customConfigProvided       bool
	mu                         sync.Mutex
	initModsOnce               sync.Once
}

// AppOption configures an App.
type AppOption func(*App)

// WithModule registers a module with the App. Modules are initialized
// in registration order and closed in reverse order.
func WithModule(m Module) AppOption {
	return func(a *App) {
		a.modules = append(a.modules, m)
	}
}

// WithRouter sets the smart router for the App.
func WithRouter(r *Router) AppOption {
	return func(a *App) { a.router = r }
}

// WithEventBus sets the event bus for the App.
func WithEventBus(bus *EventBus) AppOption {
	return func(a *App) { a.eventBus = bus }
}

// WithConfig sets the server configuration. If not provided,
// DefaultConfig() is used.
func WithConfig(cfg *Config) AppOption {
	return func(a *App) {
		a.cfg = cfg
		a.customConfigProvided = true
	}
}

// WithConfigProvider sets a configuration provider for the application and ensures it is registered as the first module.
func WithConfigProvider(cfg *config.Config) AppOption {
	return func(a *App) {
		a.configModuleName = cfg.Name()
		// config module must be registered first
		a.modules = append([]Module{cfg}, a.modules...)
	}
}

// WithMigrationManager sets the migration manager for the App.
func WithMigrationManager(mgr MigrationManager) AppOption {
	return func(a *App) {
		a.migrationManagerModuleName = mgr.Name()
		a.modules = append(a.modules, mgr)
	}
}

// NewApp creates an App with the given options.
func NewApp(opts ...AppOption) *App {
	a := &App{
		cfg:           DefaultConfig(),
		activeModules: make(map[string]Module),
		logger:        slog.With("module", "gas"),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// InitModules initializes all registered modules by calling their Init() method and stores them in the activeModules map.
// DO NOT CALL THIS METHOD DIRECTLY. Use Run() instead.
func (a *App) InitModules() (err error) {
	a.initModsOnce.Do(func() {
		for _, m := range a.modules {
			a.logger.Info("initializing module", "module", m.Name())

			if initErr := m.Init(); initErr != nil {
				err = fmt.Errorf("gas: init %s: %w", m.Name(), initErr)
				return
			}

			a.mu.Lock()
			a.activeModules[m.Name()] = m
			a.mu.Unlock()

			a.Emit(SystemModuleInitialized, NewEventData().
				Set("module_name", m.Name()))
		}

		a.Emit(SystemAllModulesInitialized, NewEventData())
	})
	return
}

func (a *App) bindConfig() error {
	if !a.customConfigProvided {
		// no custom config provided, try to bind from config-module
		if cfgProvider := a.GetConfigProvider(); cfgProvider != nil {
			a.logger.Info("using config provider", "name", cfgProvider.Name())
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

// Run initializes all modules, runs pending migrations, starts the HTTP
// server, and blocks until a shutdown signal is received.
//
// Sequence:
//  1. Call Init() on all modules in registration order
//  2. Run pending migrations via MigrationManager (if set)
//  3. Start HTTP server using the Router
//  4. Wait for SIGINT/SIGTERM
//  5. Emit gas:server-shutting-down
//  6. Gracefully shut down the HTTP server
//  7. Close all modules in reverse registration order
func (a *App) Run() error {
	// Init all modules.
	if err := a.InitModules(); err != nil {
		return err
	}

	// Bind config from config-module.
	if err := a.bindConfig(); err != nil {
		return err
	}

	// Reset the logger after binding config.
	a.logger = slog.With("module", "gas")

	// Run pending migrations.
	if migrationMgr := a.GetMigrationManager(); migrationMgr != nil {
		a.logger.Info("running pending migrations")
		if err := migrationMgr.RunPending(); err != nil {
			return fmt.Errorf("gas: migrations: %w", err)
		}
	}

	// Start HTTP server.
	if a.router == nil {
		return fmt.Errorf("gas: router is required")
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

	// Wait for shutdown signal.
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

	// Emit shutdown event.
	a.Emit(SystemServerShuttingDown, NewEventData())

	// Gracefully shut down the HTTP server.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		a.logger.Error("server shutdown error", "error", err)
	}

	// Close all modules in reverse order.
	for i := len(a.modules) - 1; i >= 0; i-- {
		m := a.modules[i]
		a.logger.Info("closing module", "module", m.Name())
		if err := m.Close(); err != nil {
			a.logger.Error("module close error", "module", m.Name(), "error", err)
		}
	}

	a.logger.Info("shutdown complete")
	return nil
}

// Emit sends an event with the given name and associated data using the event bus if it is initialized.
func (a *App) Emit(event string, data EventData) {
	if a.eventBus != nil {
		a.eventBus.Emit(event, data)
	}
}

// EmitAsync sends an event asynchronously using the event bus if it is initialized.
func (a *App) EmitAsync(event string, data EventData) *sync.WaitGroup {
	if a.eventBus == nil {
		return nil
	}
	return a.eventBus.EmitAsync(event, data)
}

// Subscribe registers a handler function for a specific event name using the App's event bus.
func (a *App) Subscribe(event string, handler func(EventData)) {
	if a.eventBus != nil {
		a.eventBus.Subscribe(event, handler)
	}
}

// SubscribeWithOwner registers an event handler for a specific event under a module's ownership via the event bus.
func (a *App) SubscribeWithOwner(module, event string, handler func(EventData)) {
	if a.eventBus != nil {
		a.eventBus.SubscribeWithOwner(module, event, handler)
	}
}

// GetConfig retrieves the application's configuration and returns it as a pointer to a Config object.
func (a *App) GetConfig() *Config {
	return a.cfg
}
