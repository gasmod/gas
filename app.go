package gas

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// ShutdownGracePeriod is how long App.Stop waits for requests that outlive
// ShutdownTimeout once their contexts are canceled, and the least time it
// leaves services to close, as described under Lifecycle in the package
// documentation.
const ShutdownGracePeriod = time.Second

// inflightPoll is how often Stop checks whether the requests it waits for
// have returned.
const inflightPoll = 5 * time.Millisecond

// App is a Worker with routing and an HTTP server, as described in the
// package documentation. It runs once, like its Worker: Start, Serve,
// Stop. It is safe for concurrent use, except as Server says.
type App struct {
	*Worker

	// srv is the server, built once by Server.
	srv *http.Server
	// handler is the App's handler, built once by Handler.
	handler http.Handler
	// router is the App's router, registered in the container.
	router *Router
	// cfg is the App's configuration: the base config until Start binds
	// and validates a copy of it, then the bound one.
	cfg *Config
	// csrfProtection guards the router against cross-origin requests.
	csrfProtection *http.CrossOriginProtection
	// baseCtx is the base context of the server's requests, and
	// cancelBase cancels it, which Stop does at the shutdown deadline.
	baseCtx    context.Context //nolint:containedctx // the server's base context, canceled by Stop
	cancelBase context.CancelFunc
	// allowedEnvs are the environments Start accepts.
	allowedEnvs []Environment
	// srvCopied holds what Server copied from the Config into srv, so
	// applyBoundServer can tell the fields a caller changed from those it
	// left.
	srvCopied serverFields
	// inflight counts the requests the App's handler is serving.
	inflight atomic.Int64
	// srvOnce and handlerOnce guard the one build of srv and handler.
	srvOnce     sync.Once
	handlerOnce sync.Once
	// srvMu guards srv, srvCopied and srvSettled.
	srvMu sync.Mutex
	// cfgMu guards cfg.
	cfgMu sync.Mutex
	// srvSettled is set once the server's fields are final: the bound
	// config was applied to it, or Serve has begun and net/http reads
	// them.
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
		addr:              net.JoinHostPort(s.Host, strconv.Itoa(s.Port)),
		readTimeout:       s.ReadTimeout,
		readHeaderTimeout: s.ReadHeaderTimeout,
		writeTimeout:      s.WriteTimeout,
		idleTimeout:       s.IdleTimeout,
	}
}

// WithErrorHandler sets the function that turns a handler's error into a
// response; see ErrorHandler. A nil handler keeps the default.
func WithErrorHandler(h ErrorHandler) AppOption {
	return func(a *App) { a.router.SetErrorHandler(h) }
}

// WithTrustedOrigin adds an origin that may make cross-origin requests
// with unsafe methods, such as POST, PUT, PATCH and DELETE. The origin is
// an absolute URL with a scheme and a host, such as
// `https://app.example.com`. An origin that is not makes Start fail with
// an error wrapping ErrInvalidOption.
func WithTrustedOrigin(origin string) AppOption {
	return func(a *App) {
		err := a.csrfProtection.AddTrustedOrigin(origin)
		if err != nil {
			// net/http's error quotes the origin, which is configuration.
			a.recordOptionError(fmt.Errorf(
				"%w: WithTrustedOrigin needs an absolute URL with a scheme and a host",
				ErrInvalidOption,
			))
		}
	}
}

// WithCSRFInsecureBypassPattern adds a path pattern, in the syntax of
// http.ServeMux, whose requests skip cross-origin protection. Use it only
// for endpoints that must take unauthenticated cross-origin requests and
// check them in another way, such as webhook receivers. A pattern
// http.CrossOriginProtection rejects panics.
func WithCSRFInsecureBypassPattern(pattern string) AppOption {
	return func(a *App) { a.csrfProtection.AddInsecureBypassPattern(pattern) }
}

// WithCSRFDenyHandler sets the handler for a request cross-origin
// protection rejects. The default writes a 403 *Error with code
// CodeForbidden in the unified error shape.
func WithCSRFDenyHandler(h http.Handler) AppOption {
	return func(a *App) { a.csrfProtection.SetDenyHandler(h) }
}

// WithBaseConfig sets the Config that Start binds the ConfigProvider's
// values over and validates, in place of DefaultConfig. Start validates
// it even when no ConfigProvider is registered.
//
// Default: DefaultConfig().
func WithBaseConfig(cfg Config) AppOption {
	return func(a *App) { a.cfg = &cfg }
}

// WithAllowedEnvironments sets the environments Start accepts in
// Config.Env, for an app whose config module allows others, as the
// EnvExtension's WithAllowedEnvs does. DefaultEnvironment is always
// allowed, as it is the environment of an App no configuration sets one
// for. An empty list makes Start fail with an error wrapping
// ErrInvalidOption.
//
// Default: EnvDevelopment, EnvTesting, EnvStaging and EnvProduction.
func WithAllowedEnvironments(envs ...Environment) AppOption {
	return func(a *App) {
		if len(envs) == 0 {
			a.recordOptionError(
				fmt.Errorf("%w: WithAllowedEnvironments needs an environment", ErrInvalidOption),
			)

			return
		}

		a.allowedEnvs = slices.Clone(envs)
		if !slices.Contains(envs, DefaultEnvironment) {
			a.allowedEnvs = append(a.allowedEnvs, DefaultEnvironment)
		}
	}
}

// NewApp returns an App with opts applied. It registers the Router in the
// container.
func NewApp(opts ...Option) *App {
	w := NewWorker()

	baseCtx, cancelBase := context.WithCancel(context.Background())

	a := &App{
		Worker:         w,
		cfg:            DefaultConfig(),
		router:         NewRouter(),
		csrfProtection: http.NewCrossOriginProtection(),
		baseCtx:        baseCtx,
		cancelBase:     cancelBase,
		allowedEnvs:    standardEnvironments,
	}

	a.csrfProtection.SetDenyHandler(SecurityHeaders()(http.HandlerFunc(a.denyCrossOrigin)))

	a.router.logger = w.getLogger

	w.serviceContainer.RegisterServiceInstance[*Router](a.router)

	w.afterBuild = a.afterBuild
	w.onServiceClose = func(s Service) { a.router.RemoveByService(s) }
	w.onServiceRestarted = func(s Service) { a.router.settleUse(s) }

	for _, opt := range opts {
		switch o := opt.(type) {
		case WorkerOption:
			o(w)
		case AppOption:
			o(a)
		}
	}

	// Every request gets its own scope.
	a.router.Use(nil, MiddlewareFunc(requestScopeMiddleware(w.serviceContainer, w.getLogger)))

	return a
}

// Config returns a copy of the App's Config: before Start, the base
// config (WithBaseConfig); once Start has bound and validated it, the
// bound one. Changing the copy does not change the App. It is safe to
// call concurrently with Start.
func (a *App) Config() Config {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()

	return *a.cfg
}

// Router returns the App's router.
func (a *App) Router() *Router { return a.router }

// Server returns the App's *http.Server, building it on the first call
// from the Config as it stands, as described under Configuration in the
// package documentation. A server built before Start, such as by a
// service's Init, takes the bound address and timeouts at Start for every
// one of those fields the caller has not changed, unless Serve has begun.
// Do not call Server concurrently with Start, and do not change the
// server's fields once it serves.
func (a *App) Server() *http.Server {
	a.srvOnce.Do(func() {
		a.srvMu.Lock()
		defer a.srvMu.Unlock()

		cfg := a.Config()
		f := serverFieldsOf(&cfg.Server)

		a.srv = &http.Server{
			Addr:              f.addr,
			Handler:           a.Handler(),
			ReadTimeout:       f.readTimeout,
			ReadHeaderTimeout: f.readHeaderTimeout,
			WriteTimeout:      f.writeTimeout,
			IdleTimeout:       f.idleTimeout,
			BaseContext:       func(net.Listener) context.Context { return a.baseCtx },
		}
		a.srvCopied = f
	})

	return a.srv
}

// Handler returns the App's http.Handler: the router behind cross-origin
// protection, counting the requests in flight so Stop can wait for them.
// Use it to serve the App from a listener of your own or in tests. It is
// built once and does not build the server. Before Start, while the
// router is not sealed, every route answers 404.
func (a *App) Handler() http.Handler {
	a.handlerOnce.Do(func() {
		protected := a.csrfProtection.Handler(a.router)

		a.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a.inflight.Add(1)
			defer a.inflight.Add(-1)

			protected.ServeHTTP(w, r)
		})
	})

	return a.handler
}

// Serve starts the HTTP server and blocks until it stops. It returns
// ErrNotStarted before Start succeeds and ErrShutDown once Stop has begun.
// A server closed by Stop returns nil; any other listener error is
// returned, wrapped.
func (a *App) Serve() error {
	a.mu.Lock()

	state := a.state
	a.mu.Unlock()

	switch state {
	case lifeIdle, lifeStarting, lifeFailed:
		return ErrNotStarted
	case lifeShutDown:
		return ErrShutDown
	case lifeStarted:
	}

	srv := a.Server()

	// From here net/http reads the server's fields.
	a.srvMu.Lock()

	a.srvSettled = true
	a.srvMu.Unlock()

	a.getLogger().Info("server started").
		Str("environment", a.Config().Env.String()).
		Str("addr", srv.Addr).
		Send()

	err := srv.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("gas: serve: %w", err)
	}

	return nil
}

// Stop shuts the App down within ShutdownTimeout, as described under
// Lifecycle in the package documentation: it emits
// SystemServerShuttingDown, shuts the server down, cancels the context of
// requests still running at the deadline and waits up to
// ShutdownGracePeriod for them, then shuts the Worker down. ctx bounds it
// too. Every error is returned, joined; a clean Stop returns nil.
func (a *App) Stop(ctx context.Context) error {
	a.mu.Lock()

	started := a.state == lifeStarted
	timeout := a.Config().Server.ShutdownTimeout
	a.mu.Unlock()

	if !started {
		a.cancelBase()

		return a.Shutdown(ctx)
	}

	deadline := time.Now().Add(timeout)

	srvCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	var errs []error

	err := emitAndWait[SystemServerShuttingDown](
		srvCtx,
		a.eventBus,
		SystemServerShuttingDownPayload{},
	)
	if err != nil {
		errs = append(
			errs,
			fmt.Errorf("gas: stop: wait for SystemServerShuttingDown subscribers: %w", err),
		)
	}

	errs = append(errs, a.drainServer(ctx, srvCtx)...)

	closeDeadline := deadline
	if least := time.Now().Add(ShutdownGracePeriod); least.After(closeDeadline) {
		closeDeadline = least
	}

	closeCtx, cancelClose := context.WithDeadline(ctx, closeDeadline)
	defer cancelClose()

	err = a.Shutdown(closeCtx)
	if err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// Run starts the App, logs the route map, serves, and blocks until SIGINT,
// SIGTERM or a server error, then stops the App and returns Stop's error,
// joined with the server error. Start runs under a context the first
// signal cancels, so a signal during a slow startup stops it. Once the
// first signal arrives the App stops handling them, so a second one ends
// the process. Stop gets context.Background(); ShutdownTimeout bounds it.
func (a *App) Run() error {
	ctx, sigs, stop := watchSignals()
	defer stop()

	err := a.Start(ctx)
	if err != nil {
		return err
	}

	a.logRouteMap(a.Config().Env.IsDevelopment())

	srvErr := make(chan error, 1)

	go func() { srvErr <- a.Serve() }()

	stopCtx := context.Background()

	select {
	case sig := <-sigs:
		a.getLogger().Info("shutdown signal received").Str("signal", sig.String()).Send()

		return a.Stop(stopCtx)
	case err = <-srvErr:
		// Stop anyway, so the services Start initialized are closed.
		return errors.Join(err, a.Stop(stopCtx))
	}
}

// denyCrossOrigin answers a request cross-origin protection rejects with a
// 403 in the unified error shape, and logs it at warn level with its
// method and path.
func (a *App) denyCrossOrigin(w http.ResponseWriter, r *http.Request) {
	a.getLogger().Warn("cross-origin request denied").
		Str("method", r.Method).
		Str("path", r.URL.Path).
		Send()

	_ = WriteError(w, r, Forbidden("cross-origin request denied"))
}

// bindConfig binds a copy of the base Config from the ConfigProvider,
// when one is registered, validates it, and makes it the App's Config.
// Every singleton is built, so resolving the provider runs no Init.
func (a *App) bindConfig(ctx context.Context) error {
	cfg := a.Config()

	if a.serviceContainer.CanResolve[ConfigProvider]() {
		p, err := resolveFrom[ConfigProvider](ctx, a.serviceContainer)
		if err != nil {
			return fmt.Errorf("%w: bind: %w", ErrInvalidConfig, err)
		}

		err = p.Bind(&cfg)
		if err != nil {
			return fmt.Errorf("%w: bind: %w", ErrInvalidConfig, err)
		}
	}

	err := cfg.validate(a.allowedEnvs)
	if err != nil {
		return err
	}

	a.router.maxJSONBody.Store(cfg.Server.MaxJSONBodyBytes)
	a.router.maxFormBody.Store(cfg.Server.MaxFormBodyBytes)

	a.cfgMu.Lock()

	a.cfg = &cfg
	a.cfgMu.Unlock()

	return nil
}

// afterBuild binds and validates the Config, seals the router, checks that
// every DI-aware handler's dependencies are registered, applies the bound
// Config to a server built early, and emits AppConfigUpdated.
func (a *App) afterBuild(ctx context.Context) error {
	err := a.bindConfig(ctx)
	if err != nil {
		return err
	}

	a.router.Seal()

	for _, ph := range a.router.handlersToCheck() {
		for _, depType := range ph.depTypes {
			if !a.serviceContainer.canResolveType(depType) {
				return newDIError(
					ErrNotRegistered,
					"gas: handler %s %s (service %q): dependency %v is not registered",
					ph.method,
					ph.path,
					ph.service,
					depType,
				)
			}
		}
	}

	// Before the event, so a subscriber that sets a server field wins.
	a.applyBoundServer()

	a.eventBus.Emit[AppConfigUpdated](AppConfigUpdatedPayload{Config: a.Config()}).Wait()

	return nil
}

// applyBoundServer gives a server built before the config was bound the
// bound value of each field the caller left as Server set it. It applies
// at most once, and never to a server Serve has begun with.
func (a *App) applyBoundServer() {
	a.srvMu.Lock()
	defer a.srvMu.Unlock()

	if a.srv == nil || a.srvSettled {
		return
	}

	a.srvSettled = true

	cfg := a.Config()
	bound, was := serverFieldsOf(&cfg.Server), a.srvCopied

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

// drainServer shuts the server down within srvCtx, cancels the context of
// the requests still running, and gives them ShutdownGracePeriod to
// return; then it closes their connections, which ends their blocked
// reads and writes, and gives them ShutdownGracePeriod again, logging how
// many are left when it gives up. The waits also end with ctx, Stop's
// own. It returns the server's errors.
func (a *App) drainServer(ctx, srvCtx context.Context) []error {
	var errs []error

	srv := a.Server()

	err := srv.Shutdown(srvCtx)
	if err != nil {
		errs = append(errs, fmt.Errorf("gas: server shutdown: %w", err))
	}

	a.cancelBase()

	if a.waitInflight(ctx, ShutdownGracePeriod) {
		return errs
	}

	err = srv.Close()
	if err != nil {
		errs = append(errs, fmt.Errorf("gas: server close: %w", err))
	}

	if !a.waitInflight(ctx, ShutdownGracePeriod) {
		a.getLogger().Warn("requests still running after the shutdown grace period").
			Int64("requests", a.inflight.Load()).
			Send()
	}

	return errs
}

// waitInflight waits up to grace, and no longer than ctx allows, for the
// App's handler to serve no request, and reports whether it does.
func (a *App) waitInflight(ctx context.Context, grace time.Duration) bool {
	giveUp := time.Now().Add(grace)

	for a.inflight.Load() > 0 {
		if time.Now().After(giveUp) || ctx.Err() != nil {
			return false
		}

		time.Sleep(inflightPoll)
	}

	return true
}
