package gas

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Lifecycle errors of a Worker or App, as described under Starting and
// Shutting down in the package documentation.
var (
	// ErrAlreadyStarted indicates that Start was called on a Worker that
	// has started or is starting, or ReadyFunc once Start has run the
	// ready hooks.
	ErrAlreadyStarted = errors.New("gas: already started")

	// ErrNotStarted indicates a call that needs a started Worker, such as
	// CloseService, RestartService or App.Serve, before Start succeeded.
	ErrNotStarted = errors.New("gas: not started")

	// ErrShutDown indicates a call made once Shutdown has begun: Start,
	// CloseService, RestartService or App.Serve.
	ErrShutDown = errors.New("gas: shut down")

	// ErrInvalidOption indicates an option given a value it cannot take.
	// Start returns it before it initializes anything. NewWorker panics
	// with it when it is given an AppOption.
	ErrInvalidOption = errors.New("gas: invalid option")
)

// DefaultServiceInitTimeout is the deadline a service's Init gets unless
// WithServiceInitTimeout, WithServiceContainerServiceTimeout or the
// service's own ServiceInitTimeout sets another.
const DefaultServiceInitTimeout = 15 * time.Second

// lifecycle is where a Worker is in its one run.
type lifecycle uint8

const (
	// lifeIdle is a Worker that Start has not been called on.
	lifeIdle lifecycle = iota
	// lifeStarting is a Worker whose Start is running.
	lifeStarting
	// lifeStarted is a Worker whose Start succeeded.
	lifeStarted
	// lifeFailed is a Worker whose Start failed.
	lifeFailed
	// lifeShutDown is a Worker whose Shutdown has begun.
	lifeShutDown
)

// Worker runs services without an HTTP server, as described in the
// package documentation. Use it for non-HTTP environments such as AWS
// Lambda, background job processors or CLI tools; App embeds one and adds
// routing and an HTTP server. A Worker runs once: Start, then Shutdown. It
// is safe for concurrent use.
type Worker struct {
	logger           Logger
	startErr         error
	optErr           error
	serviceContainer *ServiceContainer
	eventBus         *EventBus
	afterBuild       func(ctx context.Context) error
	onServiceClose   func(s Service)
	// onServiceRestarted runs once a restart's Init succeeds.
	onServiceRestarted func(s Service)
	startDone          chan struct{}
	shutdownDone       chan struct{}
	services           []*trackedService
	readyFuncs         []func(*ServiceContainer) error
	ops                sync.WaitGroup
	shutdownTimeout    time.Duration
	mu                 sync.Mutex
	loggerMu           sync.Mutex
	opsN               atomic.Int32
	state              lifecycle
	readyRan           bool
	shutdownGaveUp     bool
}

var (
	_ HealthProvider = (*Worker)(nil)
	_ ReadyProvider  = (*Worker)(nil)
)

// NewWorker returns a Worker with opts applied. Only WorkerOption values
// apply; an AppOption panics with an error wrapping ErrInvalidOption.
func NewWorker(opts ...Option) *Worker {
	w := &Worker{
		serviceContainer: NewServiceContainer(),
		eventBus:         NewEventBus(),
		startDone:        make(chan struct{}),
		shutdownDone:     make(chan struct{}),
		shutdownTimeout:  DefaultShutdownTimeout,
	}

	// Recovered subscriber panics are logged through the Worker's logger.
	w.eventBus.logger = w.getLogger
	w.serviceContainer.gate = w.checkActive
	w.serviceContainer.fallbackLogger = w.getLogger
	w.serviceContainer.RegisterServiceInstance[*EventBus](w.eventBus)
	w.serviceContainer.RegisterServiceInstance[HealthProvider](w)
	w.serviceContainer.RegisterServiceInstance[ReadyProvider](w)

	for _, opt := range opts {
		switch o := opt.(type) {
		case WorkerOption:
			o(w)
		case AppOption:
			panic(fmt.Errorf("%w: an AppOption needs NewApp", ErrInvalidOption))
		}
	}

	return w
}

// EventBus returns the Worker's event bus.
func (w *Worker) EventBus() *EventBus { return w.eventBus }

// ServiceContainer returns the Worker's dependency injection container.
func (w *Worker) ServiceContainer() *ServiceContainer { return w.serviceContainer }

// MigrationManager returns the MigrationManager registered in the
// container, building it if it is not built yet, or nil when none is
// registered or it cannot be built; Start reports a build failure.
func (w *Worker) MigrationManager() MigrationManager {
	return resolveOptional[MigrationManager](context.Background(), w.serviceContainer)
}

// ConfigProvider returns the ConfigProvider registered in the container,
// building it if it is not built yet, or nil when none is registered or
// it cannot be built; Start reports a build failure.
func (w *Worker) ConfigProvider() ConfigProvider {
	return resolveOptional[ConfigProvider](context.Background(), w.serviceContainer)
}

// resolveOptional resolves T from c with ctx, or returns the zero T when
// nothing is registered for T. A T that is registered but fails to build
// is also the zero T: the build error belongs to Start, which reports it.
func resolveOptional[T any](ctx context.Context, c *ServiceContainer) T {
	if !c.CanResolve[T]() {
		var zero T

		return zero
	}

	v, err := resolveFrom[T](ctx, c)
	if err != nil {
		var zero T

		return zero
	}

	return v
}

// Start initializes every service, runs pending migrations and runs the
// ready hooks, as described under Starting in the package documentation.
// It does not block; call Shutdown when done. ctx is passed to the Init of
// every service built during startup and to the migrations; canceling it
// aborts startup.
//
// A Worker starts once. A second Start returns ErrAlreadyStarted, a Start
// once Shutdown has begun returns ErrShutDown, and a Start after a failed
// one returns that failure again. A Start that fails leaves nothing
// running: it closes, newest first, every service it had initialized,
// with ctx stripped of its cancellation, and returns the error joined with
// any Close errors.
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()

	switch w.state {
	case lifeStarting, lifeStarted:
		w.mu.Unlock()

		return ErrAlreadyStarted
	case lifeFailed:
		err := w.startErr
		w.mu.Unlock()

		return err
	case lifeShutDown:
		w.mu.Unlock()

		return ErrShutDown
	case lifeIdle:
	}

	w.state = lifeStarting

	optErr := w.optErr
	w.mu.Unlock()

	defer func() {
		rec := recover()
		if rec == nil {
			return
		}

		// A panic in an Init or a constructor fails Start like an error,
		// and is panicked again once nothing is left running.

		w.finishStart(
			context.WithoutCancel(ctx),
			w.cleanupAfterPanic(context.WithoutCancel(ctx), rec),
		)

		panic(rec)
	}()

	err := optErr
	if err == nil {
		err = w.start(ctx)
	}

	w.finishStart(context.WithoutCancel(ctx), err)

	return err
}

// Shutdown emits SystemShuttingDown, waits for its subscribers, and closes
// every active service newest first, passing ctx to each Close, as
// described under Shutting down in the package documentation. The Logger,
// when it is a service, is flushed and closed last, after the final log
// line. A failing Close does not stop the rest; every Close error is
// returned, joined.
//
// Shutdown runs once. It waits for a Start or a CloseService or
// RestartService in progress to return first. A later call waits for the
// first to finish and returns nil. A Shutdown before Start closes nothing
// and makes Start return ErrShutDown.
func (w *Worker) Shutdown(ctx context.Context) error {
	proceed, err := w.beginShutdown(ctx)
	if !proceed {
		return err
	}

	defer close(w.shutdownDone)

	var errs []error

	if w.opsN.Load() > 0 {
		err = waitFor(ctx, w.ops.Wait)
		if err != nil {
			errs = append(errs, fmt.Errorf("gas: shutdown: wait for the kill switch: %w", err))
		}
	}

	w.mu.Lock()

	failed := w.startErr != nil
	w.mu.Unlock()

	if failed {
		// The Start that was running failed and closed what it had.
		return errors.Join(errs...)
	}

	err = emitAndWait[SystemShuttingDown](ctx, w.eventBus, SystemShuttingDownPayload{})
	if err != nil {
		errs = append(
			errs,
			fmt.Errorf("gas: shutdown: wait for SystemShuttingDown subscribers: %w", err),
		)
	}

	return errors.Join(append(errs, w.closeAll(ctx))...)
}

// emitAndWait emits data as E on bus and waits for its subscribers, or
// for ctx to end, when it returns ctx's error.
func emitAndWait[E event[D], D any](ctx context.Context, bus *EventBus, data D) error {
	wg, subscribers := bus.emit[E](data)
	if subscribers == 0 {
		return nil
	}

	return waitFor(ctx, wg.Wait)
}

// waitFor runs wait and returns once it returns, or with ctx's error when
// ctx ends first.
func waitFor(ctx context.Context, wait func()) error {
	done := make(chan struct{})

	go func() {
		wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		// A wait that ended as ctx did counts as done.
		select {
		case <-done:
			return nil
		default:
			//nolint:wrapcheck // the caller wraps it with what it waited for
			return ctx.Err()
		}
	}
}

// closeService closes svc and names it in the error.
func closeService(ctx context.Context, svc Service) error {
	err := svc.Close(ctx)
	if err != nil {
		return fmt.Errorf("gas: close %s: %w", svc.Name(), err)
	}

	return nil
}

// Run starts the Worker, blocks until SIGINT, SIGTERM or the end of ctx,
// and then shuts it down within the shutdown timeout, as described under
// Running in the package documentation. Start runs under a context that
// ctx and the first signal both end, so either during startup fails Start
// through an Init that honors its ctx. Once the first signal arrives the Worker stops handling them, so
// a second one ends the process. Shutdown gets ctx's values but not its
// cancellation, under the timeout. Ending ctx is a normal stop: Run then
// returns Shutdown's error.
func (w *Worker) Run(ctx context.Context) error {
	runCtx, sigs, stop := watchSignals(ctx)
	defer stop()

	err := w.Start(runCtx)
	if err != nil {
		return err
	}

	w.getLogger().Info("worker started").Send()

	awaitStop(ctx, sigs, w.getLogger())

	w.mu.Lock()

	timeout := w.shutdownTimeout
	w.mu.Unlock()

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	return w.Shutdown(shutdownCtx)
}

// awaitStop blocks until the first signal on sigs or the end of ctx, and
// logs which to logger.
func awaitStop(ctx context.Context, sigs <-chan os.Signal, logger Logger) {
	select {
	case sig := <-sigs:
		logger.Info("shutdown signal received").Str("signal", sig.String()).Send()
	case <-ctx.Done():
		logger.Info("shutdown requested").Err("cause", context.Cause(ctx)).Send()
	}
}

// watchSignals handles SIGINT and SIGTERM until the first one arrives,
// which it delivers on the channel after it cancels the returned context,
// a child of parent, and stops handling signals, so a second one ends the
// process. stop stops handling them without a signal.
func watchSignals(parent context.Context) (context.Context, <-chan os.Signal, func()) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(parent)
	got := make(chan os.Signal, 1)
	done := make(chan struct{})

	go func() {
		select {
		case sig := <-quit:
			signal.Stop(quit)

			got <- sig

			cancel()
		case <-done:
			signal.Stop(quit)
		}
	}()

	var once sync.Once

	return ctx, got, func() {
		once.Do(func() {
			close(done)
			cancel()
		})
	}
}

// ReadyFunc registers a function that runs once every service is
// initialized and the migrations are applied, before Start returns. It is
// the imperative form of WithReadyFunc and may be called any number of
// times before Start runs the ready hooks; the funcs run in registration
// order and the first error fails Start.
//
// Safe for concurrent use. It panics with ErrAlreadyStarted once Start has
// run the ready hooks, because the func would never run.
func (w *Worker) ReadyFunc(fn func(*ServiceContainer) error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.readyRan {
		panic(fmt.Errorf("%w: ReadyFunc after the ready hooks ran", ErrAlreadyStarted))
	}

	w.readyFuncs = append(w.readyFuncs, fn)
}

// RegisterService registers a constructor for type T with the given
// lifetime on the Worker's container. It is the imperative form of
// WithService; see ServiceContainer.RegisterService.
func (w *Worker) RegisterService[T any](ctor any, lifetime ServiceLifetime) {
	w.serviceContainer.RegisterService[T](ctor, lifetime)
}

// RegisterTransientService registers a transient constructor for type T
// on the Worker's container; see ServiceContainer.RegisterTransientService.
func (w *Worker) RegisterTransientService[T any](ctor any) {
	w.serviceContainer.RegisterTransientService[T](ctor)
}

// RegisterScopedService registers a scoped constructor for type T on the
// Worker's container; see ServiceContainer.RegisterScopedService.
func (w *Worker) RegisterScopedService[T any](ctor any) {
	w.serviceContainer.RegisterScopedService[T](ctor)
}

// RegisterSingletonService registers a singleton constructor for type T on
// the Worker's container; see ServiceContainer.RegisterSingletonService.
func (w *Worker) RegisterSingletonService[T any](ctor any) {
	w.serviceContainer.RegisterSingletonService[T](ctor)
}

// RegisterServiceInstance registers val under T on the Worker's container;
// see ServiceContainer.RegisterServiceInstance.
func (w *Worker) RegisterServiceInstance[T any](val T) {
	w.serviceContainer.RegisterServiceInstance[T](val)
}

// RegisterServiceAlias makes the service registered under Target
// resolvable as Alias on the Worker's container; see
// ServiceContainer.RegisterServiceAlias.
func (w *Worker) RegisterServiceAlias[Target, Alias any]() {
	w.serviceContainer.RegisterServiceAlias[Target, Alias]()
}

// finishStart records the result of Start and lets a waiting Shutdown go
// on. When a Shutdown gave up waiting for Start, it closes what Start
// initialized itself, with ctx.
func (w *Worker) finishStart(ctx context.Context, err error) {
	w.mu.Lock()

	// A Shutdown that began during Start has moved the state on already.
	if err != nil {
		w.startErr = err
	}

	if w.state == lifeStarting {
		w.state = lifeStarted
		if err != nil {
			w.state = lifeFailed
		}
	}

	abandoned := w.shutdownGaveUp && err == nil

	close(w.startDone)
	w.mu.Unlock()

	if abandoned {
		// The Shutdown that gave up has returned; the error has no one
		// else to go to.
		closeErr := w.closeActive(ctx, nil)
		if closeErr != nil {
			w.getLogger().
				Error("close after an abandoned shutdown failed").
				Err("error", closeErr).
				Send()
		}
	}
}

// cleanupAfterPanic closes what a Start that panicked with rec had
// initialized, and returns the error Start records: one wrapping ErrPanic,
// joined with any Close error.
func (w *Worker) cleanupAfterPanic(ctx context.Context, rec any) error {
	err := fmt.Errorf("%w in Start: %v", ErrPanic, rec)

	w.mu.Lock()

	tracked := len(w.services) > 0
	w.mu.Unlock()

	if tracked {
		return errors.Join(err, w.closeActive(ctx, nil))
	}

	built := w.serviceContainer.initializedServices()
	slices.Reverse(built)

	return errors.Join(err, w.closeServices(ctx, built))
}

// beginShutdown moves the Worker to shut down and reports whether this
// Shutdown closes the services. One that does not returns at once: a
// later Shutdown once the first is done, one before Start or after a
// failed Start with nil, and one whose ctx ended while Start ran with
// that error, leaving Start to close what it initialized.
func (w *Worker) beginShutdown(ctx context.Context) (bool, error) {
	w.mu.Lock()

	prev := w.state

	w.state = lifeShutDown
	w.mu.Unlock()

	switch prev {
	case lifeShutDown:
		<-w.shutdownDone

		return false, nil
	case lifeIdle, lifeFailed:
		close(w.shutdownDone)

		return false, nil
	case lifeStarting:
		select {
		case <-w.startDone:
		case <-ctx.Done():
			w.mu.Lock()
			defer w.mu.Unlock()

			// finishStart closes startDone under mu, so Start either has
			// returned already or will see the flag and close.
			select {
			case <-w.startDone:
				return true, nil
			default:
			}

			w.shutdownGaveUp = true

			close(w.shutdownDone)

			return false, fmt.Errorf("gas: shutdown: wait for Start: %w", ctx.Err())
		}
	case lifeStarted:
	}

	return true, nil
}

// closeAll closes every active service: first all but the Logger and the
// services it depends on, newest first; then, after the final log line
// and a flush, the Logger, and then its dependencies, newest first.
func (w *Worker) closeAll(ctx context.Context) error {
	logger := w.getLogger()

	var last []Service

	if svc, ok := logger.(Service); ok {
		last = append(last, svc)
	}

	deps := w.serviceContainer.dependencyServices(reflect.TypeFor[Logger]())

	err := w.closeActive(ctx, append(slices.Clone(last), deps...))

	logger.Info("shutdown complete").Send()
	logger.Flush()

	for _, svc := range append(last, w.newestFirst(deps)...) {
		err = errors.Join(err, w.closeActiveService(ctx, svc))
	}

	return err
}

// newestFirst returns the services of svcs the Worker tracks, newest
// first.
func (w *Worker) newestFirst(svcs []Service) []Service {
	w.mu.Lock()
	defer w.mu.Unlock()

	out := make([]Service, 0, len(svcs))

	for _, ts := range slices.Backward(w.services) {
		if slices.ContainsFunc(svcs, func(s Service) bool { return sameInstance(s, ts.svc) }) {
			out = append(out, ts.svc)
		}
	}

	return out
}

// recordOptionError keeps err as the first option error, which Start
// returns.
func (w *Worker) recordOptionError(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.optErr == nil {
		w.optErr = err
	}
}

// start is the work of Start.
func (w *Worker) start(ctx context.Context) error {
	err := w.serviceContainer.BuildAll(ctx)
	if err != nil {
		// Nothing is tracked yet, so close what the container had
		// initialized before the failure.
		built := w.serviceContainer.initializedServices()
		slices.Reverse(built)

		return errors.Join(err, w.closeServices(context.WithoutCancel(ctx), built))
	}

	w.mu.Lock()

	for _, svc := range w.serviceContainer.initializedServices() {
		w.services = append(w.services, &trackedService{svc: svc})
	}

	w.mu.Unlock()

	cleanup := func(err error) error {
		return errors.Join(err, w.closeActive(context.WithoutCancel(ctx), nil))
	}

	if w.afterBuild != nil {
		err = w.afterBuild(ctx)
		if err != nil {
			return cleanup(err)
		}
	}

	w.eventBus.Emit[SystemAllServicesInitialized](SystemAllServicesInitializedPayload{}).Wait()

	err = w.runMigrations(ctx)
	if err != nil {
		return cleanup(err)
	}

	err = w.runReadyHooks()
	if err != nil {
		return cleanup(err)
	}

	return nil
}

// runMigrations runs the pending migrations of the MigrationManager, when
// one is registered, with ctx.
func (w *Worker) runMigrations(ctx context.Context) error {
	if !w.serviceContainer.CanResolve[MigrationManager]() {
		return nil
	}

	// Every singleton is built, so resolving the manager runs no Init.
	mgr, err := resolveFrom[MigrationManager](ctx, w.serviceContainer)
	if err != nil {
		return fmt.Errorf("gas: run migrations: %w", err)
	}

	w.getLogger().Info("applying pending migrations").Send()

	err = mgr.RunPendingContext(ctx)
	if err != nil {
		return fmt.Errorf("gas: run migrations: %w", err)
	}

	return nil
}

// runReadyHooks runs the ready hooks in order and returns the first
// error, wrapped.
func (w *Worker) runReadyHooks() error {
	// Snapshot under the lock so a concurrent ReadyFunc either lands
	// before this point or panics; it is never silently dropped.
	w.mu.Lock()

	w.readyRan = true

	readyFuncs := slices.Clone(w.readyFuncs)

	w.mu.Unlock()

	for _, fn := range readyFuncs {
		err := fn(w.serviceContainer)
		if err != nil {
			return fmt.Errorf("gas: ready hook: %w", err)
		}
	}

	return nil
}

// closeActive closes every active service but those in skip, newest
// first, and marks each closed just before its Close runs, so nothing
// closes one twice. Every Close error is returned, joined.
func (w *Worker) closeActive(ctx context.Context, skip []Service) error {
	w.mu.Lock()

	closing := make([]Service, 0, len(w.services))
	for _, ts := range slices.Backward(w.services) {
		if ts.state != serviceActive ||
			slices.ContainsFunc(skip, func(s Service) bool { return sameInstance(s, ts.svc) }) {
			continue
		}

		closing = append(closing, ts.svc)
	}

	w.mu.Unlock()

	// Each service turns closed just before its own Close, so a Close may
	// still resolve the services that close after it.
	var err error

	for _, svc := range closing {
		w.getLogger().Info("closing service").Str("service", svc.Name()).Send()

		err = errors.Join(err, w.closeActiveService(ctx, svc))
	}

	return err
}

// closeActiveService closes svc when it is tracked and active.
func (w *Worker) closeActiveService(ctx context.Context, svc Service) error {
	w.mu.Lock()

	ts := w.trackedLocked(svc)
	if ts == nil || ts.state != serviceActive {
		w.mu.Unlock()

		return nil
	}

	ts.state = serviceClosed
	w.mu.Unlock()

	return closeService(ctx, svc)
}

// closeServices closes each service in order, passing ctx to every Close
// and logging each first. A failing Close does not stop the rest; every
// Close error is returned, joined.
func (w *Worker) closeServices(ctx context.Context, services []Service) error {
	var err error

	for _, svc := range services {
		w.getLogger().Info("closing service").Str("service", svc.Name()).Send()

		err = errors.Join(err, closeService(ctx, svc))
	}

	return err
}

// getLogger returns the Worker's logger: the registered Logger once it is
// built, or a logger over slog.Default when none is registered, which
// warns once and is not registered in the container. It never builds a
// service: while a registered Logger is not built yet, such as during the
// cleanup of a failed Start, it returns the slog logger without keeping
// it. It is safe for concurrent use.
func (w *Worker) getLogger() Logger {
	w.loggerMu.Lock()
	defer w.loggerMu.Unlock()

	if w.logger != nil {
		return w.logger
	}

	c := w.serviceContainer

	v, built := c.lookupInstance(c.aliasTarget(reflect.TypeFor[Logger]()))
	if built {
		if logger, ok := reflect.TypeAssert[Logger](v); ok {
			w.logger = logger

			return logger
		}
	}

	fallback := newSlogLogger(slog.Default())

	if !c.CanResolve[Logger]() {
		w.logger = fallback
		fallback.Warn("no logger registered").Send()
	}

	return fallback
}

// WithService registers a constructor for type T with the given lifetime,
// as ServiceContainer.RegisterService does.
func WithService[T any](ctor any, lifetime ServiceLifetime) WorkerOption {
	return func(w *Worker) { w.serviceContainer.RegisterService[T](ctor, lifetime) }
}

// WithServiceInstance registers val under T, as
// ServiceContainer.RegisterServiceInstance does.
//
// Pre-built means pre-constructed, not pre-initialized: if the value
// implements Service, the container calls Init(ctx) on it during Start
// like any other service, and Close(ctx) at shutdown. Do not call Init
// yourself before registering, or it runs twice.
func WithServiceInstance[T any](val T) WorkerOption {
	return func(w *Worker) { w.serviceContainer.RegisterServiceInstance[T](val) }
}

// WithTransientService registers a transient constructor for type T.
func WithTransientService[T any](ctor any) WorkerOption {
	return func(w *Worker) { w.serviceContainer.RegisterTransientService[T](ctor) }
}

// WithScopedService registers a scoped constructor for type T.
func WithScopedService[T any](ctor any) WorkerOption {
	return func(w *Worker) { w.serviceContainer.RegisterScopedService[T](ctor) }
}

// WithSingletonService registers a singleton constructor for type T.
func WithSingletonService[T any](ctor any) WorkerOption {
	return func(w *Worker) { w.serviceContainer.RegisterSingletonService[T](ctor) }
}

// WithServiceAlias makes the service registered under Target resolvable
// as the interface Alias too, sharing Target's instance and lifetime.
// Target may be registered by an earlier or a later option. See
// ServiceContainer.RegisterServiceAlias.
func WithServiceAlias[Target, Alias any]() WorkerOption {
	return func(w *Worker) { w.serviceContainer.RegisterServiceAlias[Target, Alias]() }
}

// WithReadyFunc registers a ready hook, as ReadyFunc does.
func WithReadyFunc(fn func(*ServiceContainer) error) WorkerOption {
	return func(w *Worker) { w.ReadyFunc(fn) }
}

// WithServiceInitTimeout sets the deadline the Worker's container gives
// each service's Init: a singleton's at startup and on RestartService,
// and a scoped service's on its first resolution in a scope. When it
// passes, Init's context is canceled and startup, or the resolution,
// fails once Init returns. 0 disables the deadline, so only the caller's
// context applies. A service that implements ServiceInitTimeout overrides
// it for itself. A negative timeout makes Start fail with an error
// wrapping ErrInvalidOption.
//
// Default: DefaultServiceInitTimeout.
func WithServiceInitTimeout(timeout time.Duration) WorkerOption {
	return func(w *Worker) {
		if timeout < 0 {
			w.recordOptionError(
				fmt.Errorf("%w: WithServiceInitTimeout must not be negative", ErrInvalidOption),
			)

			return
		}

		w.serviceContainer.opt.serviceInitTimeout = timeout
	}
}

// WithShutdownTimeout sets the deadline of the Shutdown that Worker.Run
// starts after a signal or the end of its ctx. In an App, Stop and App.Run use the Config's
// ShutdownTimeout instead, and this option has no effect. A timeout that
// is not positive makes Start fail with an error wrapping
// ErrInvalidOption.
//
// Default: DefaultShutdownTimeout.
func WithShutdownTimeout(timeout time.Duration) WorkerOption {
	return func(w *Worker) {
		if timeout <= 0 {
			w.recordOptionError(
				fmt.Errorf("%w: WithShutdownTimeout must be positive", ErrInvalidOption),
			)

			return
		}

		w.mu.Lock()

		w.shutdownTimeout = timeout
		w.mu.Unlock()
	}
}
