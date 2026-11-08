package gas_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

// S1: with WriteTimeout 0, a handler blocked writing to a client that
// never reads outlived Stop, and its services closed under it. Stop now
// closes the remaining connections after the grace period, so the
// handler returns before the services close.
func TestFindings_S1StopDropsStuckConnections(t *testing.T) {
	t.Parallel()

	rec := &eventRecorder{}
	dep := &fakeService{name: "db", closeFn: func(context.Context) error {
		rec.add("close")

		return nil
	}}

	cfg := gas.DefaultConfig()

	cfg.Server.WriteTimeout = 0
	cfg.Server.ShutdownTimeout = 100 * time.Millisecond

	app := quietApp(gas.WithServiceInstance(dep), gas.WithBaseConfig(*cfg))

	big := make([]byte, 64<<20)
	entered := make(chan struct{})

	app.Router().Get(nil, "/dl", func(c gas.Context, _ *fakeService) error {
		close(entered)

		_, err := c.ResponseWriter().Write(big)

		rec.add("handler returned")

		return err
	})
	require.NoError(t, app.Start(t.Context()))

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go func() { _ = app.Server().Serve(ln) }()

	conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", ln.Addr().String())
	require.NoError(t, err)

	t.Cleanup(func() { _ = conn.Close() })

	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4096)
	}

	_, err = conn.Write([]byte("GET /dl HTTP/1.1\r\nHost: x\r\n\r\n"))
	require.NoError(t, err)

	<-entered

	_ = app.Stop(t.Context())
	assert.Equal(t, []string{"handler returned", "close"}, rec.all())
}

// S2: CacheControl set its header on error responses, so a CDN could
// cache a 404 or a 503 for a year. It applies to 2xx and 3xx only, and
// every error envelope says `no-store`.
func TestFindings_S2CacheControlOnlyOnSuccess(t *testing.T) {
	t.Parallel()

	app := quietApp()
	r := app.Router()
	r.UseMiddlewareFunc(
		gas.CacheControl(gas.WithCacheControlPublic(), gas.WithCacheControlMaxAge(time.Hour)),
	)
	r.UseMiddlewareFunc(gas.BodyLimit(1 << 10))
	r.Group(func(sub *gas.Router) {
		sub.UseMiddlewareFunc(gas.Timeout(20 * time.Millisecond))
		sub.Get(nil, "/app.js", func(c gas.Context) error {
			if c.Query("slow") != "" {
				<-c.Done()
			}

			return c.Blob(http.StatusOK, "text/javascript", []byte("ok"))
		})
	})
	require.NoError(t, app.Start(t.Context()))

	h := app.Handler()

	assert.Equal(
		t,
		"public, max-age=3600",
		serve(h, http.MethodGet, "/app.js", nil).Header().Get("Cache-Control"),
	)

	for _, rr := range []*httptest.ResponseRecorder{
		serve(h, http.MethodGet, "/missing.js", nil),
		serve(h, http.MethodGet, "/app.js?slow=1", nil),
		post(h, "/app.js", "text/plain", strings.Repeat("a", 2048), false),
		serve(h, http.MethodPost, "/app.js", http.Header{"Sec-Fetch-Site": {"cross-site"}}),
	} {
		assert.GreaterOrEqual(t, rr.Code, http.StatusBadRequest)
		assert.Equal(t, "no-store", rr.Header().Get("Cache-Control"), rr.Code)
	}
}

// guardedService is a service whose Close marks it closed.
type guardedService struct {
	closed atomic.Bool
}

// Name returns `auth`.
func (*guardedService) Name() string { return "auth" }

// Init marks it open.
func (s *guardedService) Init(context.Context) error {
	s.closed.Store(false)

	return nil
}

// Close marks it closed.
func (s *guardedService) Close(context.Context) error {
	s.closed.Store(true)

	return nil
}

// S3: a closed service was still injected into other services' handlers.
// Resolving it fails with ErrServiceNotActive, answered 503.
func TestFindings_S3ClosedServiceNotInjected(t *testing.T) {
	t.Parallel()

	s := &guardedService{}
	app := quietApp(gas.WithServiceInstance(s))

	var used atomic.Bool

	app.Router().Get(nil, "/billing", func(c gas.Context, a *guardedService) error {
		used.Store(a.closed.Load())

		return c.NoContent()
	})
	require.NoError(t, app.Start(t.Context()))
	require.NoError(t, app.CloseService[*guardedService](t.Context()))

	rr := serve(app.Handler(), http.MethodGet, "/billing", nil)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	assert.False(t, used.Load())

	_, err := app.ServiceContainer().Resolve[*guardedService]()
	require.ErrorIs(t, err, gas.ErrServiceNotActive)

	require.NoError(t, app.RestartService[*guardedService](t.Context()))
	assert.Equal(
		t,
		http.StatusNoContent,
		serve(app.Handler(), http.MethodGet, "/billing", nil).Code,
	)
}

// S4: ReadTimeout 0 with IdleTimeout 0 held a keep-alive connection open
// forever. Start rejects it.
func TestFindings_S4NoIdleBound(t *testing.T) {
	t.Parallel()

	app := quietApp(gas.WithServiceInstance[gas.ConfigProvider](&mapConfig{values: map[string]any{
		"server": map[string]any{"readtimeout": 0, "idletimeout": 0},
	}}))

	err := app.Start(t.Context())
	require.ErrorIs(t, err, gas.ErrInvalidConfig)
	assert.Contains(t, err.Error(), "server.idletimeout")
}

// S5: Timeout buffered a response of any size. Past its cap the client
// gets a 503 envelope instead.
func TestFindings_S5TimeoutBufferCapped(t *testing.T) {
	t.Parallel()

	app := quietApp()
	chunk := make([]byte, 1<<20)

	app.Router().Get(nil, "/big", func(w http.ResponseWriter, _ *http.Request) {
		for range 9 {
			_, _ = w.Write(chunk)
		}
	}, gas.MiddlewareFunc(gas.Timeout(time.Minute)))
	require.NoError(t, app.Start(t.Context()))

	rr := serve(app.Handler(), http.MethodGet, "/big", nil)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	assert.Contains(t, rr.Body.String(), `"status":503`)
}

// S6, C7: a SystemServerShuttingDown subscriber that blocked held Stop
// past ShutdownTimeout. Stop waits for subscribers only until the
// deadline.
func TestFindings_S6StopBoundsSubscribers(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	t.Cleanup(func() { close(release) })

	cfg := gas.DefaultConfig()

	cfg.Server.ShutdownTimeout = 100 * time.Millisecond

	app := quietApp(gas.WithBaseConfig(*cfg))
	app.EventBus().Subscribe[gas.SystemServerShuttingDown](
		func(gas.SystemServerShuttingDownPayload) { <-release },
	)
	app.EventBus().Subscribe[gas.SystemShuttingDown](
		func(gas.SystemShuttingDownPayload) { <-release },
	)
	require.NoError(t, app.Start(t.Context()))

	start := time.Now()
	err := app.Stop(t.Context())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*gas.ShutdownGracePeriod+time.Second)
}

// C7: a CloseService in progress held Shutdown past its deadline.
func TestFindings_C7ShutdownBoundsKillSwitch(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	entered := make(chan struct{})

	s := &fakeService{name: "slow", closeFn: func(context.Context) error {
		close(entered)
		<-release

		return nil
	}}
	w := gas.NewWorker(gas.WithServiceInstance(s))
	require.NoError(t, w.Start(t.Context()))

	go func() { _ = w.CloseService[*fakeService](context.WithoutCancel(t.Context())) }()

	<-entered

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)

	go func() { done <- w.Shutdown(ctx) }()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown waited for CloseService past its deadline")
	}

	close(release)
}

// S7, C5: with no Logger registered, a returned 5xx error was logged
// nowhere. It is logged through the App's fallback logger.
//
// Not parallel: it sets slog's default logger.
func TestFindings_S7ErrorLoggedWithoutLogger(t *testing.T) {
	var buf lockedBuffer

	old := slog.Default()

	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))

	t.Cleanup(func() { slog.SetDefault(old) })

	app := gas.NewApp()
	app.Router().Get(nil, "/boom", func(gas.Context) error { return errDBExploded })
	require.NoError(t, app.Start(t.Context()))

	serve(app.Handler(), http.MethodGet, "/boom", nil)

	var logged bool

	for _, line := range buf.lines() {
		logged = logged || line["error"] == "db exploded"
	}

	assert.True(t, logged)
	require.NoError(t, app.Stop(t.Context()))
}

// S8: a request cross-origin protection rejected was not logged, and its
// 403 lacked the security headers. It is logged at warn level with the
// method and path, and the 403 carries the headers.
func TestFindings_S8CrossOriginDenied(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	app := gas.NewApp(gas.WithServiceInstance[gas.Logger](newJSONLogger(buf)))
	app.Router().Post(nil, "/admin", func(gas.Context) error { return nil })
	require.NoError(t, app.Start(t.Context()))

	rr := serve(
		app.Handler(),
		http.MethodPost,
		"/admin",
		http.Header{"Sec-Fetch-Site": {"cross-site"}},
	)
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Equal(t, "DENY", rr.Header().Get("X-Frame-Options"))
	assert.Equal(t, "nosniff", rr.Header().Get("X-Content-Type-Options"))

	var found bool

	for _, line := range buf.lines() {
		if line["msg"] == "cross-origin request denied" {
			found = true

			assert.Equal(t, "WARN", line["level"])
			assert.Equal(t, http.MethodPost, line["method"])
			assert.Equal(t, "/admin", line["path"])
		}
	}

	assert.True(t, found)
}

// KI-035: a panic in middleware below RequestLogger was logged without
// the request's fields. It is logged through the request's logger.
func TestFindings_KI035MiddlewarePanicLoggedWithRequestID(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	app := gas.NewApp(gas.WithServiceInstance[gas.Logger](newJSONLogger(buf)))
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(gas.RequestLogger[gas.Logger]())
	app.Router().UseMiddlewareFunc(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("mw boom") })
	})
	app.Router().Get(nil, "/x", func(gas.Context) error { return nil })
	require.NoError(t, app.Start(t.Context()))

	serve(app.Handler(), http.MethodGet, "/x", nil)

	for _, line := range buf.lines() {
		if line["msg"] == "handler panic" {
			assert.NotEmpty(t, line["request_id"])

			return
		}
	}

	t.Fatal("no handler panic line")
}

// BindForm read up to net/http's 10 MB. It has its own limit,
// MaxFormBodyBytes, 1 MiB by default, past which it answers 413.
func TestFindings_FormBodyLimit(t *testing.T) {
	t.Parallel()

	app := startedApp(t)
	app.Router().Post(nil, "/form", func(c gas.Context) error {
		var v struct {
			A string `form:"a"`
		}

		err := c.BindForm(&v)
		if err != nil {
			return err
		}

		return c.NoContent()
	})

	body := url.Values{"a": {strings.Repeat("a", 2<<20)}}.Encode()
	rr := post(app.Handler(), "/form", "application/x-www-form-urlencoded", body, false)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rr.Code)
}

// A MaxJSONBodyBytes of 0 meant no limit. It must be positive.
func TestFindings_JSONBodyLimitPositive(t *testing.T) {
	t.Parallel()

	app := quietApp(gas.WithServiceInstance[gas.ConfigProvider](&mapConfig{values: map[string]any{
		"server": map[string]any{"maxjsonbodybytes": 0},
	}}))
	require.ErrorIs(t, app.Start(t.Context()), gas.ErrInvalidConfig)
}

// C2: a panic in an Init left Start's state unfinished, so Shutdown
// blocked forever, past its ctx, and nothing was closed. Start closes what
// it initialized and panics again; Shutdown returns.
func TestFindings_C2PanicInInit(t *testing.T) {
	t.Parallel()

	a := &fakeService{name: "a"}
	p := &otherService{
		fakeService{name: "p", initFn: func(context.Context) error { panic("init boom") }},
	}
	w := gas.NewWorker(gas.WithServiceInstance(a), gas.WithServiceInstance(p))

	assert.Panics(t, func() { _ = w.Start(t.Context()) })
	require.ErrorIs(t, w.Start(t.Context()), gas.ErrPanic)

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)

	go func() { done <- w.Shutdown(ctx) }()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown blocked after a panic in Init")
	}

	assert.Equal(t, int32(1), a.closes.Load())
	assert.Zero(t, p.closes.Load())
}

// countingLogger is a Logger and a Service that counts its Init and
// Close.
type countingLogger struct {
	gas.NopLogger

	dep    *otherService
	inits  atomic.Int32
	closes atomic.Int32
}

// Name returns `logger`.
func (*countingLogger) Name() string { return "logger" }

// Init counts the call.
func (l *countingLogger) Init(context.Context) error {
	l.inits.Add(1)

	return nil
}

// Close counts the call.
func (l *countingLogger) Close(context.Context) error {
	l.closes.Add(1)

	return nil
}

// C3: the cleanup of a failed Start resolved the Logger to log, building
// and initializing it, and through it its failed dependency again. The
// cleanup builds nothing.
func TestFindings_C3FailedStartBuildsNothing(t *testing.T) {
	t.Parallel()

	a := &fakeService{name: "a"}
	b := &otherService{
		fakeService{name: "b", initFn: func(context.Context) error { return errInitBFailed }},
	}
	lg := &countingLogger{}

	w := gas.NewWorker(
		gas.WithSingletonService[*fakeService](func() *fakeService { return a }),
		gas.WithSingletonService[*otherService](func() *otherService { return b }),
		gas.WithSingletonService[gas.Logger](func(d *otherService) gas.Logger {
			lg.dep = d

			return lg
		}),
	)

	require.ErrorIs(t, w.Start(t.Context()), errInitBFailed)
	assert.Zero(t, lg.inits.Load())
	assert.Equal(t, int32(1), b.inits.Load())
	assert.Equal(t, int32(1), a.closes.Load())
}

// C4: a Scope closed while one of its scoped services was being built
// kept the instance, which no Close ever reached. The build closes it and
// fails with ErrScopeClosed.
func TestFindings_C4ScopeCloseDuringBuild(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	s := &fakeService{name: "scoped", initFn: func(context.Context) error {
		close(started)
		<-release

		return nil
	}}

	c := gas.NewServiceContainer()
	c.RegisterScopedService[*fakeService](func() *fakeService { return s })

	scope := c.NewScope(t.Context())

	var (
		wg   sync.WaitGroup
		rerr error
	)

	wg.Go(func() { _, rerr = scope.Resolve[*fakeService]() })

	<-started
	require.NoError(t, scope.Close(t.Context()))
	close(release)
	wg.Wait()

	require.ErrorIs(t, rerr, gas.ErrScopeClosed)
	assert.Equal(t, int32(1), s.closes.Load())
}

// C6: the 503 of a removed service's route shadowed the same route a
// live service registered after. The live route is served.
func TestFindings_C6LiveRouteBeatsRemoved(t *testing.T) {
	t.Parallel()

	app := quietApp()
	one := &fakeService{name: "one"}

	one.initFn = func(context.Context) error {
		app.Router().
			Get(one, "/x", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("one")) })

		return nil
	}

	two := &otherService{fakeService{name: "two"}}

	app.ServiceContainer().RegisterServiceInstance(one)
	app.ServiceContainer().RegisterServiceInstance(two)
	require.NoError(t, app.Start(t.Context()))
	require.NoError(t, app.CloseService[*fakeService](t.Context()))

	app.Router().
		Get(two, "/x", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("two")) })

	rr := serve(app.Handler(), http.MethodGet, "/x", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "two", rr.Body.String())
}

// recordingDep is a service a Logger depends on, which records its Close.
type recordingDep struct {
	rec *eventRecorder
}

// Name returns `logsink`.
func (*recordingDep) Name() string { return "logsink" }

// Init does nothing.
func (*recordingDep) Init(context.Context) error { return nil }

// Close records `sink close`.
func (d *recordingDep) Close(context.Context) error {
	d.rec.add("sink close")

	return nil
}

// C7: a service the Logger depends on closed before the Logger's last
// lines. The Logger closes first, then its dependencies, last of all.
func TestFindings_C7LoggerDependenciesCloseAfterLogger(t *testing.T) {
	t.Parallel()

	rec := &eventRecorder{}
	sink := &recordingDep{rec: rec}

	w := gas.NewWorker(
		gas.WithServiceInstance(sink),
		gas.WithSingletonService[gas.Logger](
			func(*recordingDep) gas.Logger { return &serviceLogger{rec: rec} },
		),
		gas.WithServiceInstance(&fakeService{name: "other"}),
	)
	require.NoError(t, w.Start(t.Context()))
	require.NoError(t, w.Shutdown(t.Context()))

	events := rec.all()
	require.GreaterOrEqual(t, len(events), 4)
	assert.Equal(
		t,
		[]string{"shutdown complete", "flush", "close", "sink close"},
		events[len(events)-4:],
	)
}

// C7: WithAllowedEnvironments always allows the default environment, so
// an App without a configured environment starts.
func TestFindings_C7DefaultEnvironmentAllowed(t *testing.T) {
	t.Parallel()

	app := startedApp(t, gas.WithAllowedEnvironments("qa"))
	assert.Equal(t, gas.DefaultEnvironment, app.Config().Env)
}

// errDBExploded is a handler's error in the S7 test.
var errDBExploded = errors.New("db exploded")

// Re-check 1: a RestartService whose Init finished after Shutdown gave
// up waiting for it left the service active and never closed. The restart
// closes it and returns ErrShutDown.
func TestFindings_R1RestartAbandonedByShutdown(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	entered := make(chan struct{})

	s := &fakeService{name: "a"}

	s.initFn = func(context.Context) error {
		if s.inits.Load() > 1 {
			close(entered)
			<-release
		}

		return nil
	}

	w := gas.NewWorker(gas.WithServiceInstance(s))
	require.NoError(t, w.Start(t.Context()))
	require.NoError(t, w.CloseService[*fakeService](t.Context()))

	done := make(chan error, 1)

	go func() { done <- w.RestartService[*fakeService](context.WithoutCancel(t.Context())) }()

	<-entered

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	require.ErrorIs(t, w.Shutdown(ctx), context.DeadlineExceeded)
	close(release)

	err := <-done
	require.ErrorIs(t, err, gas.ErrShutDown)
	require.EqualError(t, err, "gas: restart a: gas: shut down")
	assert.Equal(t, int32(2), s.closes.Load())
	assert.Empty(t, w.ActiveServices())
}

// Re-check 2: during Shutdown, a Close that resolved a service closing
// after it got ErrServiceNotActive, as every service turned closed up
// front. Each turns closed just before its own Close.
func TestFindings_R2CloseResolvesOpenDependency(t *testing.T) {
	t.Parallel()

	var (
		w          *gas.Worker
		resolveErr error
	)

	dep := &fakeService{name: "dep"}
	user := &otherService{fakeService{name: "user", closeFn: func(context.Context) error {
		_, resolveErr = w.ServiceContainer().Resolve[*fakeService]()

		return nil
	}}}

	w = gas.NewWorker(
		gas.WithServiceInstance(dep),
		gas.WithSingletonService[*otherService](func(*fakeService) *otherService { return user }),
	)
	require.NoError(t, w.Start(t.Context()))
	require.NoError(t, w.Shutdown(t.Context()))
	require.NoError(t, resolveErr)
	assert.Equal(t, int32(1), dep.closes.Load())
}

// selfHelper is a transient that depends on the service that resolves
// it.
type selfHelper struct{ s *fakeService }

// Re-check 3: a restart's own Init could not resolve its service, even
// through a dependency, as the gate refused it while restarting. The
// Init's goroutine passes; other goroutines still get 503.
func TestFindings_R3RestartInitResolvesItself(t *testing.T) {
	t.Parallel()

	var (
		app      *gas.App
		outsider int
	)

	s := &fakeService{name: "self"}

	s.initFn = func(context.Context) error {
		if s.inits.Load() == 1 {
			return nil
		}

		_, err := app.ServiceContainer().Resolve[selfHelper]()
		if err != nil {
			return err
		}

		// A request on another goroutine still sees the service restarting.
		done := make(chan int)

		//nolint:contextcheck // the request runs on its own, as a client's would
		go func() { done <- serve(app.Handler(), http.MethodGet, "/uses", nil).Code }()

		outsider = <-done

		return nil
	}

	app = quietApp(
		gas.WithServiceInstance(s),
		gas.WithTransientService[selfHelper](
			func(f *fakeService) selfHelper { return selfHelper{f} },
		),
	)
	app.Router().
		Get(nil, "/uses", func(c gas.Context, _ *fakeService) error { return c.NoContent() })
	require.NoError(t, app.Start(t.Context()))
	require.NoError(t, app.CloseService[*fakeService](t.Context()))

	require.NoError(t, app.RestartService[*fakeService](t.Context()))
	assert.Equal(t, http.StatusServiceUnavailable, outsider)
	assert.Equal(t, http.StatusNoContent, serve(app.Handler(), http.MethodGet, "/uses", nil).Code)
}

// Re-check 4: a panic in a singleton constructor left goroutines waiting
// on that build blocked forever. They get an error wrapping ErrPanic, and
// a later resolution builds again.
func TestFindings_R4PanicReleasesWaiters(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	release := make(chan struct{})

	var calls atomic.Int32

	c := gas.NewServiceContainer()
	c.RegisterSingletonService[*scopedCounter](func() *scopedCounter {
		if calls.Add(1) == 1 {
			close(entered)
			<-release

			panic("boom")
		}

		return &scopedCounter{}
	})

	go func() {
		defer func() { _ = recover() }()

		_, _ = c.Resolve[*scopedCounter]()
	}()

	<-entered

	res := make(chan error, 1)

	go func() {
		_, err := c.Resolve[*scopedCounter]()
		res <- err
	}()

	// Let the waiter reach the build before it panics.
	time.Sleep(20 * time.Millisecond)
	close(release)

	select {
	case err := <-res:
		if err != nil {
			require.ErrorIs(t, err, gas.ErrPanic)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a waiter stayed blocked after the build panicked")
	}

	v, err := c.Resolve[*scopedCounter]()
	require.NoError(t, err)
	assert.NotNil(t, v)
}

// Re-check 5: a handler that returned ErrServiceNotActive from
// ResolveFromRequestScope answered 500. It answers 503.
func TestFindings_R5ReturnedNotActiveIs503(t *testing.T) {
	t.Parallel()

	app := quietApp(gas.WithServiceInstance(&fakeService{name: "a"}))
	app.Router().Get(nil, "/r", func(c gas.Context) error {
		_, err := gas.ResolveFromRequestScope[*fakeService](c.Request())

		return err
	})
	require.NoError(t, app.Start(t.Context()))
	require.NoError(t, app.CloseService[*fakeService](t.Context()))

	rr := serve(app.Handler(), http.MethodGet, "/r", nil)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	assert.Contains(t, rr.Body.String(), `"code":"service_unavailable"`)
}

// Re-check 6: Stop's waits for requests ignored its ctx. They end with
// it.
func TestFindings_R6StopWaitsHonorCtx(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	entered := make(chan struct{})

	cfg := gas.DefaultConfig()

	cfg.Server.ShutdownTimeout = time.Minute

	app := quietApp(gas.WithBaseConfig(*cfg))
	app.Router().Get(nil, "/stuck", func(c gas.Context) error {
		close(entered)
		<-release

		return c.NoContent()
	})
	require.NoError(t, app.Start(t.Context()))

	go serve(app.Handler(), http.MethodGet, "/stuck", nil)

	<-entered

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()

	_ = app.Stop(ctx)

	close(release)
	assert.Less(t, time.Since(start), gas.ShutdownGracePeriod)
}
