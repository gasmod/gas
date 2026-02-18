package gas

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// App manages module lifecycle, the HTTP server, and graceful shutdown.
// It is constructed with functional options and wired together in main.go.
type App struct {
	migrationManagerModuleName string
	router                     *Router
	eventBus                   *EventBus
	activeModules              map[string]Module
	cfg                        *Config
	modules                    []Module
	mu                         sync.Mutex
}

// AppOption configures an App.
type AppOption func(*App)

// WithConfig sets the server configuration. If not provided,
// DefaultConfig() is used.
func WithConfig(cfg *Config) AppOption {
	return func(a *App) { a.cfg = cfg }
}

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
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
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
	// 1. Init all modules.
	for _, m := range a.modules {
		a.cfg.Logger.Info("initializing module", "module", m.Name())
		if err := m.Init(); err != nil {
			return fmt.Errorf("gas: init %s: %w", m.Name(), err)
		}
		a.mu.Lock()
		a.activeModules[m.Name()] = m
		a.mu.Unlock()
	}

	// 2. Run pending migrations.
	if migrationMgr := a.GetMigrationManager(); migrationMgr != nil {
		a.cfg.Logger.Info("running pending migrations")
		if err := migrationMgr.RunPending(); err != nil {
			return fmt.Errorf("gas: migrations: %w", err)
		}
	}

	// 3. Start HTTP server.
	if a.router == nil {
		return fmt.Errorf("gas: router is required")
	}

	srv := &http.Server{
		Addr:         a.cfg.Addr,
		Handler:      a.router,
		ReadTimeout:  a.cfg.ReadTimeout,
		WriteTimeout: a.cfg.WriteTimeout,
		IdleTimeout:  a.cfg.IdleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		a.cfg.Logger.Info("server listening", "addr", a.cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	// 4. Wait for shutdown signal.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		a.cfg.Logger.Info("shutdown signal received", "signal", sig)
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("gas: server error: %w", err)
		}
	}

	// 5. Emit shutdown event.
	if a.eventBus != nil {
		a.eventBus.Emit(SystemServerShuttingDown, NewEventData())
	}

	// 6. Gracefully shut down the HTTP server.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		a.cfg.Logger.Error("server shutdown error", "error", err)
	}

	// 7. Close all modules in reverse order.
	for i := len(a.modules) - 1; i >= 0; i-- {
		m := a.modules[i]
		a.cfg.Logger.Info("closing module", "module", m.Name())
		if err := m.Close(); err != nil {
			a.cfg.Logger.Error("module close error", "module", m.Name(), "error", err)
		}
	}

	a.cfg.Logger.Info("shutdown complete")
	return nil
}
