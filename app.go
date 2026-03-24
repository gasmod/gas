package gas

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
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

// WithRequestScope adds a Scope instance to the context using a custom key for managing scoped service lifetimes.
// Useful for testing and managing scoped service lifetimes within request contexts.
func WithRequestScope(ctx context.Context, scope *Scope) context.Context {
	return context.WithValue(ctx, requestScopeKey{}, scope)
}

// App manages service lifecycle, the HTTP server, and graceful shutdown.
// It embeds a Worker for DI, events, migrations, and service management,
// and adds routing, CSRF protection, and an HTTP listener on top.
type App struct {
	*Worker

	router         *Router
	cfg            *Config
	csrfProtection *http.CrossOriginProtection
}

// --- AppOption functions (HTTP-specific) ---

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
func NewApp(opts ...Option) *App {
	w := &Worker{
		serviceContainer: NewServiceContainer(),
		eventBus:         NewEventBus(),
		activeServices:   make(map[string]Service),
	}

	a := &App{
		Worker:         w,
		cfg:            DefaultConfig(),
		router:         NewRouter(),
		csrfProtection: http.NewCrossOriginProtection(),
	}

	// Register infra as instances in the container.
	RegisterInstance[*Router](w.serviceContainer, a.router)
	RegisterInstance[*EventBus](w.serviceContainer, w.eventBus)

	// Set hooks so Worker delegates HTTP-specific work back to App.
	w.postBuildHook = func() error {
		// Seal the router: flush all pending middleware first, then routes.
		a.router.Seal()

		// Validate all DI-aware handler dependencies.
		for _, ph := range *a.router.pendingHandlers {
			for _, depType := range ph.depTypes {
				if !w.serviceContainer.CanResolve(depType) {
					return fmt.Errorf(
						"gas: handler %s %s (service %q): dependency %v is not registered in the container",
						ph.method, ph.path, ph.service, depType,
					)
				}
			}
		}
		return nil
	}
	w.onServiceClose = func(name string) {
		a.router.RemoveByModule(name)
	}

	for _, opt := range opts {
		switch o := opt.(type) {
		case WorkerOption:
			o(w)
		case AppOption:
			o(a)
		}
	}

	// Install per-request scope middleware on the router at app initialization time.
	a.router.Use(MiddlewareFunc(requestScopeMiddleware(w.serviceContainer)))

	return a
}

// Config retrieves the application's configuration.
func (a *App) Config() *Config {
	return a.cfg
}

// Router returns the App's router.
func (a *App) Router() *Router { return a.router }

// Run initializes all services, runs pending migrations, starts the HTTP
// server, and blocks until a shutdown signal is received.
func (a *App) Run() error {
	if err := a.Start(); err != nil {
		return err
	}

	if err := a.bindConfig(); err != nil {
		return err
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

	Emit(a.Worker.eventBus, SystemServerShuttingDown, SystemServerShuttingDownPayload{}).Wait()

	ctx, cancel := context.WithTimeout(context.Background(), a.cfg.Server.ShutdownTimeout)
	defer cancel()
	if srvErr := srv.Shutdown(ctx); srvErr != nil {
		a.getLogger().Error("server shutdown error").Err("error", srvErr).Send()
	}

	return a.Shutdown()
}

func (a *App) bindConfig() error {
	if cfgProvider := a.ConfigProvider(); cfgProvider != nil {
		if err := cfgProvider.Bind(a.cfg); err != nil {
			return fmt.Errorf("gas: config binding: %w", err)
		}
	}

	if err := a.cfg.Validate(); err != nil {
		return fmt.Errorf("gas: config validation: %w", err)
	}

	Emit(a.Worker.eventBus, AppConfigUpdated, AppConfigUpdatedPayload{Config: *a.cfg}).Wait()

	return nil
}
