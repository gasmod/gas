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

// App manages service lifecycle, the HTTP server, and graceful shutdown.
// It embeds a Worker for DI, events, migrations, and service management,
// and adds routing, CSRF protection, and an HTTP listener on top.
type App struct {
	*Worker

	srv            *http.Server
	handler        http.Handler
	router         *Router
	cfg            *Config
	csrfProtection *http.CrossOriginProtection
	// srvCopied holds what Server copied from the Config into srv, so
	// bindConfig can tell the fields a caller changed from those it left.
	srvCopied   serverFields
	srvOnce     sync.Once
	handlerOnce sync.Once
	srvMu       sync.Mutex // guards srv, srvCopied and srvSettled
	// srvSettled is set once the server's fields are final: the bound
	// config was applied to it, or Serve has begun and net/http reads them.
	srvSettled bool
}

// serverFields are the fields of the *http.Server that Server sets from the
// Config.
type serverFields struct {
	addr              string
	readTimeout       time.Duration
	readHeaderTimeout time.Duration
	writeTimeout      time.Duration
	idleTimeout       time.Duration
}

// serverFieldsOf returns the server fields s calls for.
func serverFieldsOf(s *ServerSettings) serverFields {
	return serverFields{
		addr:              fmt.Sprintf("%s:%d", s.Host, s.Port),
		readTimeout:       s.ReadTimeout,
		readHeaderTimeout: s.ReadHeaderTimeout,
		writeTimeout:      s.WriteTimeout,
		idleTimeout:       s.IdleTimeout,
	}
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
			panic(fmt.Errorf("gas: failed to add trusted origin: %w", err))
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
	w := NewWorker()

	a := &App{
		Worker:         w,
		cfg:            DefaultConfig(),
		router:         NewRouter(),
		csrfProtection: http.NewCrossOriginProtection(),
	}

	// Register infra as instances in the container.
	w.serviceContainer.RegisterServiceInstance[*Router](a.router)

	// Set hooks so Worker delegates HTTP-specific work back to App.
	w.postBuildHook = func() error {
		// Seal the router: flush all pending middleware first, then routes.
		a.router.Seal()

		// Validate all DI-aware handler dependencies.
		for _, ph := range *a.router.pendingHandlers {
			for _, depType := range ph.depTypes {
				if !w.serviceContainer.canResolveType(depType) {
					return fmt.Errorf(
						"gas: handler %s %s (service %q): dependency %v is not registered in the container",
						ph.method,
						ph.path,
						ph.service.Name(),
						depType,
					)
				}
			}
		}
		return nil
	}
	w.onServiceClose = func(s Service) {
		a.router.RemoveByService(s)
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
	a.router.Use(MiddlewareFunc(requestScopeMiddleware(w.serviceContainer, w.getLogger)))

	return a
}

// Config retrieves the application's configuration.
func (a *App) Config() *Config { return a.cfg }

// Router returns the App's router.
func (a *App) Router() *Router { return a.router }

// Start initializes the underlying Worker (services, migrations, hooks) and
// binds configuration. It does not start the HTTP server; call Serve for that,
// or use Run to do both and block until shutdown. ctx is passed to the Init
// of every service built during startup.
//
// Like Worker.Start, a Start that fails, including when the ConfigProvider
// cannot bind the config (Bind also checks its validate tags), closes every service it had initialized and returns the
// error; every later Start returns the same error.
func (a *App) Start(ctx context.Context) error {
	if err := a.Worker.Start(ctx); err != nil {
		return err
	}

	//nolint:contextcheck // every singleton is built by now, so resolving the provider runs no Init
	if err := a.bindConfig(); err != nil {
		// Like a failed Worker.Start, leave nothing running and make every
		// later Start return this error.
		//nolint:contextcheck // cleanup must run even when ctx is what failed
		err = errors.Join(
			fmt.Errorf("gas: failed to bind config: %w", err),
			a.closeActive(context.WithoutCancel(ctx)),
		)
		a.failStart(err)
		return err
	}

	return nil
}

// Server returns the App's *http.Server, constructing it on first call from
// the current Config. The instance is cached and safe to call concurrently.
//
// A server built before Start, such as by a service's Init, starts from the
// config as it stood then. When Start binds the config, the bound Addr and
// timeouts are applied to every one of those fields the caller has not
// changed; a field the caller set, such as a ReadTimeout of zero to stream
// request bodies, is kept. "Changed" is a comparison of values, so a field
// set to the very value the server was built with counts as unchanged and
// takes the bound value. An AppConfigUpdated subscriber runs after this,
// so what it sets on the server stays. The bound config is applied once:
// not again on a later Start, and not at all once Serve has begun, so a
// Start after Serve leaves the server's fields alone. Only App.Serve marks
// the server as serving: an embedder that serves Server() itself, with
// Server().Serve or ListenAndServe, must call Start before it begins, or
// a later Start can write onto the live server. Do not read or write
// the server's fields from another goroutine while Start runs, and do not
// call Server concurrently with Start: building the server reads the
// Config that Start's bind writes, as it always has.
func (a *App) Server() *http.Server {
	a.srvOnce.Do(func() {
		a.srvMu.Lock()
		defer a.srvMu.Unlock()
		f := serverFieldsOf(&a.cfg.Server)
		a.srv = &http.Server{
			Addr:              f.addr,
			Handler:           a.Handler(),
			ReadTimeout:       f.readTimeout,
			ReadHeaderTimeout: f.readHeaderTimeout,
			WriteTimeout:      f.writeTimeout,
			IdleTimeout:       f.idleTimeout,
		}
		a.srvCopied = f
	})
	return a.srv
}

// applyBoundServer gives a server built before the config was bound the
// bound value of each field the caller left as Server set it. A server
// built later is built from the bound config and needs nothing. It applies
// at most once, and never to a server Serve has begun with: a second Start
// must not write fields net/http is reading.
func (a *App) applyBoundServer() {
	a.srvMu.Lock()
	defer a.srvMu.Unlock()
	if a.srv == nil || a.srvSettled {
		return
	}
	a.srvSettled = true
	bound, was := serverFieldsOf(&a.cfg.Server), a.srvCopied
	if a.srv.Addr == was.addr {
		a.srv.Addr = bound.addr
	}
	if a.srv.ReadTimeout == was.readTimeout {
		a.srv.ReadTimeout = bound.readTimeout
	}
	if a.srv.ReadHeaderTimeout == was.readHeaderTimeout {
		a.srv.ReadHeaderTimeout = bound.readHeaderTimeout
	}
	if a.srv.WriteTimeout == was.writeTimeout {
		a.srv.WriteTimeout = bound.writeTimeout
	}
	if a.srv.IdleTimeout == was.idleTimeout {
		a.srv.IdleTimeout = bound.idleTimeout
	}
	a.srvCopied = bound
}

// Handler returns the fully wrapped http.Handler used by the App's server
// (router behind CSRF protection). Useful for embedding the App in tests or
// in an externally managed listener. It is built once and does not build
// the server, so calling it before Start leaves the server to be built from
// the bound config. It is the handler the App built: a replacement assigned
// to Server().Handler is not returned here.
func (a *App) Handler() http.Handler {
	a.handlerOnce.Do(func() {
		a.handler = a.csrfProtection.Handler(a.router)
	})
	return a.handler
}

// Serve starts the HTTP server and blocks until it stops. A clean shutdown
// (http.ErrServerClosed) returns nil; any other listener error is returned.
func (a *App) Serve() error {
	srv := a.Server()
	// From here net/http reads the server's fields, so Start must no longer
	// write them; see applyBoundServer.
	a.srvMu.Lock()
	a.srvSettled = true
	a.srvMu.Unlock()

	a.getLogger().Info("server started").
		Str("environment", a.cfg.GasEnv.String()).
		Str("addr", srv.Addr).
		Send()

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("gas: server error: %w", err)
	}

	return nil
}

// Stop emits the server-shutting-down event, gracefully shuts down the HTTP
// server within ShutdownTimeout (or sooner, if ctx ends first), and then
// shuts down the underlying Worker, passing ctx to each service's Close.
//
// The Worker is shut down, and the services closed, even when the server
// shutdown fails, for example because in-flight requests outlive
// ShutdownTimeout. That error is logged as "server shutdown error" and
// returned, joined with any error from the Worker shutdown. A clean Stop
// returns nil.
func (a *App) Stop(ctx context.Context) error {
	srv := a.Server()

	a.Worker.eventBus.Emit[SystemServerShuttingDown](SystemServerShuttingDownPayload{}).Wait()

	srvCtx, cancel := context.WithTimeout(ctx, a.cfg.Server.ShutdownTimeout)
	defer cancel()

	var shErr error
	if srvErr := srv.Shutdown(srvCtx); srvErr != nil {
		//nolint:contextcheck // getLogger resolves the logger, which needs no caller ctx
		a.getLogger().Error("server shutdown error").Err("error", srvErr).Send()
		shErr = fmt.Errorf("server shutdown error: %w", srvErr)
	}

	if err := a.Shutdown(ctx); err != nil {
		shErr = errors.Join(shErr, fmt.Errorf("worker shutdown error: %w", err))
	}

	if shErr == nil {
		return nil
	}
	return fmt.Errorf("gas: shutdown error: %w", shErr)
}

// Run initializes all services, runs pending migrations, starts the HTTP
// server, and blocks until a shutdown signal is received, then stops the App
// and returns Stop's error. Startup and shutdown use context.Background(). If
// the server fails, Run stops the App and returns the server error joined
// with any error from Stop.
func (a *App) Run() error {
	ctx := context.Background()
	if err := a.Start(ctx); err != nil {
		return err
	}

	// log route map
	a.logRouteMap(a.cfg.GasEnv.IsDevelopment())

	srvErr := make(chan error, 1)
	go func() {
		if err := a.Serve(); err != nil {
			srvErr <- err
		}
		close(srvErr)
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-quit:
		a.getLogger().Info("shutdown signal received").Str("signal", sig.String()).Send()
	case chnErr := <-srvErr:
		if chnErr != nil {
			// Stop anyway, so the services Start initialized are closed.
			return errors.Join(chnErr, a.Stop(ctx))
		}
	}

	return a.Stop(ctx)
}

func (a *App) bindConfig() error {
	if cfgProvider := a.ConfigProvider(); cfgProvider != nil {
		if err := cfgProvider.Bind(a.cfg); err != nil {
			return fmt.Errorf("gas: config binding: %w", err)
		}
	}

	a.router.maxJSONBody.Store(a.cfg.Server.MaxJSONBodyBytes)
	// Before the event, so a subscriber that sets a server field wins.
	a.applyBoundServer()

	a.Worker.eventBus.Emit[AppConfigUpdated](AppConfigUpdatedPayload{Config: *a.cfg}).Wait()

	return nil
}
