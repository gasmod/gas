package gas_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
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

// P1: a second Start ran the migrations and ready hooks again. It now
// returns ErrAlreadyStarted and runs nothing.
func TestFindings_StartTwice(t *testing.T) {
	t.Parallel()

	var hooks atomic.Int32

	w := startedWorker(t, gas.WithReadyFunc(func(*gas.ServiceContainer) error {
		hooks.Add(1)

		return nil
	}))

	err := w.Start(t.Context())
	require.ErrorIs(t, err, gas.ErrAlreadyStarted)
	require.EqualError(t, err, "gas: already started")
	assert.Equal(t, int32(1), hooks.Load())
}

// P2: Start after Shutdown returned nil on a dead Worker. It now returns
// ErrShutDown, and the services stay closed.
func TestFindings_StartAfterShutdown(t *testing.T) {
	t.Parallel()

	s := &fakeService{name: "p2"}
	w := gas.NewWorker(gas.WithServiceInstance(s))

	require.NoError(t, w.Start(t.Context()))
	require.NoError(t, w.Shutdown(t.Context()))

	err := w.Start(t.Context())
	require.ErrorIs(t, err, gas.ErrShutDown)
	require.EqualError(t, err, "gas: shut down")
	assert.Equal(t, int32(1), s.inits.Load())
	assert.Equal(t, int32(1), s.closes.Load())
	assert.Empty(t, w.ActiveServices())
}

// P3: Shutdown before Start spent the shutdown, and a Start after it
// initialized services nothing closed. Start now fails with ErrShutDown
// and initializes nothing.
func TestFindings_ShutdownBeforeStart(t *testing.T) {
	t.Parallel()

	s := &fakeService{name: "p3"}
	w := gas.NewWorker(gas.WithServiceInstance(s))

	require.NoError(t, w.Shutdown(t.Context()))
	require.ErrorIs(t, w.Start(t.Context()), gas.ErrShutDown)
	require.NoError(t, w.Shutdown(t.Context()))
	assert.Zero(t, s.inits.Load())
}

// P4: two services with one name were both initialized, and only the
// first was closed. Services are tracked by instance, so both close.
func TestFindings_DuplicateNamesBothClosed(t *testing.T) {
	t.Parallel()

	a := &fakeService{name: "same"}
	b := &otherService{fakeService{name: "same"}}

	w := gas.NewWorker(gas.WithServiceInstance(a), gas.WithServiceInstance(b))
	require.NoError(t, w.Start(t.Context()))
	assert.Equal(t, []string{"gas/eventbus", "same", "same"}, w.ActiveServices())
	require.NoError(t, w.Shutdown(t.Context()))

	assert.Equal(t, int32(1), a.closes.Load())
	assert.Equal(t, int32(1), b.closes.Load())
}

// P4: the kill switch owns routes by name, so it refuses a service whose
// name another started service shares.
func TestFindings_DuplicateNameKillSwitch(t *testing.T) {
	t.Parallel()

	w := startedWorker(t,
		gas.WithServiceInstance(&fakeService{name: "billing"}),
		gas.WithServiceInstance(&otherService{fakeService{name: "billing"}}),
	)

	err := w.CloseService[*fakeService](t.Context())
	require.ErrorIs(t, err, gas.ErrDuplicateServiceName)
	require.EqualError(t, err, `gas: duplicate service name "billing"`)

	err = w.RestartService[*otherService](t.Context())
	require.ErrorIs(t, err, gas.ErrDuplicateServiceName)
}

// P5: a Close that waits on its context blocked Shutdown forever under
// Run. Shutdown passes its ctx to Close, and Run gives it a deadline.
func TestFindings_CloseHonorsShutdownDeadline(t *testing.T) {
	t.Parallel()

	s := &fakeService{name: "p5", closeFn: func(ctx context.Context) error {
		<-ctx.Done()

		return ctx.Err()
	}}

	w := gas.NewWorker(gas.WithServiceInstance(s), gas.WithShutdownTimeout(time.Second))
	require.NoError(t, w.Start(t.Context()))

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	err := w.Shutdown(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualError(t, err, "gas: close p5: context deadline exceeded")
}

// P6: CloseService held the Worker's lock across Close, so a Close that
// called back into the Worker deadlocked. No lock is held now.
func TestFindings_CloseServiceReentrant(t *testing.T) {
	t.Parallel()

	var w *gas.Worker

	s := &fakeService{name: "p6"}

	s.closeFn = func(ctx context.Context) error {
		_ = w.ActiveServices()
		_ = w.CheckHealth(ctx)

		return nil
	}

	w = startedWorker(t, gas.WithServiceInstance(s))

	done := make(chan error, 1)

	go func() { done <- w.CloseService[*fakeService](t.Context()) }()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("CloseService deadlocked")
	}
}

// P6: RestartService held the Worker's lock across Init, so health
// checks waited for a slow restart. They now run during it.
func TestFindings_HealthDuringRestart(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	entered := make(chan struct{}, 1)

	s := &fakeService{name: "p6b"}
	w := startedWorker(t, gas.WithServiceInstance(s))
	require.NoError(t, w.CloseService[*fakeService](t.Context()))

	s.initFn = func(context.Context) error {
		entered <- struct{}{}

		<-release

		return nil
	}

	done := make(chan error, 1)

	go func() { done <- w.RestartService[*fakeService](t.Context()) }()

	<-entered

	health := make(chan map[string]error, 1)

	go func() { health <- w.CheckHealth(t.Context()) }()

	select {
	case <-health:
	case <-time.After(5 * time.Second):
		t.Fatal("CheckHealth waited for the restart")
	}

	close(release)
	require.NoError(t, <-done)
	assert.Contains(t, w.ActiveServices(), "p6b")
}

// P7: CloseService logged a Close error and returned nil. It returns it,
// wrapped with the service's name.
func TestFindings_CloseServiceReturnsCloseError(t *testing.T) {
	t.Parallel()

	s := &fakeService{name: "billing", closeFn: func(context.Context) error {
		return errFlushFailed
	}}
	w := startedWorker(t, gas.WithServiceInstance(s))

	require.EqualError(
		t,
		w.CloseService[*fakeService](t.Context()),
		"gas: close billing: flush failed",
	)
	assert.NotContains(t, w.ActiveServices(), "billing")
}

// P8: SystemShuttingDown runs, and its subscribers return, before any
// service closes, which is where a queue consumer stops.
func TestFindings_ShuttingDownBeforeClose(t *testing.T) {
	t.Parallel()

	rec := &eventRecorder{}
	s := &fakeService{name: "p8", closeFn: func(context.Context) error {
		rec.add("close")

		return nil
	}}

	w := gas.NewWorker(gas.WithServiceInstance(s))
	w.EventBus().Subscribe[gas.SystemShuttingDown](func(gas.SystemShuttingDownPayload) {
		rec.add("event")
	})

	require.NoError(t, w.Start(t.Context()))
	require.NoError(t, w.Shutdown(t.Context()))
	assert.Equal(t, []string{"event", "close"}, rec.all())
}

// P9: a failed Start closes what it had initialized, and not the service
// whose Init failed.
func TestFindings_FailedStartCleansUp(t *testing.T) {
	t.Parallel()

	a := &fakeService{name: "a"}
	b := &otherService{fakeService{name: "b", initFn: func(context.Context) error {
		return errInitBFailed
	}}}

	w := gas.NewWorker(gas.WithServiceInstance(a), gas.WithServiceInstance(b))

	err := w.Start(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "init b failed")
	assert.Equal(t, int32(1), a.closes.Load())
	assert.Zero(t, b.closes.Load())

	// A later Start returns the same error.
	assert.Equal(t, err, w.Start(t.Context()))
}

// P10: the default timeouts bound a client that trickles its headers, and
// no configuration can remove the header deadline.
func TestFindings_HeaderDeadline(t *testing.T) {
	t.Parallel()

	cfg := gas.DefaultConfig()
	assert.Equal(t, gas.DefaultReadHeaderTimeout, cfg.Server.ReadHeaderTimeout)

	app := quietApp(gas.WithServiceInstance[gas.ConfigProvider](&mapConfig{values: map[string]any{
		"server": map[string]any{"readtimeout": 0, "readheadertimeout": 0},
	}}))

	err := app.Start(t.Context())
	require.ErrorIs(t, err, gas.ErrInvalidConfig)
	require.EqualError(t, err,
		"building *gas.Config: gas: invalid config: server.readheadertimeout must be positive")
}

// P11: Stop closed services while handlers that outlived ShutdownTimeout
// still ran on them. Their contexts are now canceled at the deadline, and
// the services close once they return.
func TestFindings_StopCancelsRequestsBeforeClose(t *testing.T) {
	t.Parallel()

	rec := &eventRecorder{}
	dep := &fakeService{name: "dep", closeFn: func(context.Context) error {
		rec.add("close")

		return nil
	}}

	cfg := gas.DefaultConfig()

	cfg.Server.ShutdownTimeout = 100 * time.Millisecond

	app := quietApp(gas.WithServiceInstance(dep), gas.WithBaseConfig(*cfg))

	entered := make(chan struct{})

	app.Router().Get(nil, "/slow", func(c gas.Context) error {
		close(entered)
		<-c.Done()
		rec.add("handler returned")

		return c.NoContent()
	})

	require.NoError(t, app.Start(t.Context()))

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go func() { _ = app.Server().Serve(ln) }()

	go func() {
		req, _ := http.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"http://"+ln.Addr().String()+"/slow",
			nil,
		)

		resp, doErr := http.DefaultClient.Do(req)
		if doErr == nil {
			_ = resp.Body.Close()
		}
	}()

	<-entered

	err = app.Stop(t.Context())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, []string{"handler returned", "close"}, rec.all())
}

// P12, KI-054: RestartService revived routes the restarted Init did not
// register again, and the replayed registrations grew without bound. A
// removed service's registrations are dropped, so after a restart only
// what that Init registered is served, and Routes reports it.
func TestFindings_KI054RestartServesOnlyNewRoutes(t *testing.T) {
	t.Parallel()

	app := quietApp()
	s := &routedService{name: "rsvc", router: app.Router(), gens: [][]string{{"/a", "/b"}, {"/b"}}}
	app.ServiceContainer().RegisterServiceInstance[*routedService](s)
	require.NoError(t, app.Start(t.Context()))

	h := app.Handler()
	assert.Equal(t, http.StatusOK, serve(h, http.MethodGet, "/a", nil).Code)

	require.NoError(t, app.CloseService[*routedService](t.Context()))
	assert.Equal(t, http.StatusServiceUnavailable, serve(h, http.MethodGet, "/a", nil).Code)
	assert.Equal(t, http.StatusServiceUnavailable, serve(h, http.MethodGet, "/b", nil).Code)

	for range 3 {
		require.NoError(t, app.RestartService[*routedService](t.Context()))

		assert.Equal(t, http.StatusNotFound, serve(h, http.MethodGet, "/a", nil).Code)
		assert.Equal(t, http.StatusOK, serve(h, http.MethodGet, "/b", nil).Code)
		assert.Equal(t, map[string][]gas.RegisteredRoute{
			"rsvc": {{Method: http.MethodGet, Path: "/b", Middleware: []string{}}},
		}, app.Router().Routes())

		require.NoError(t, app.CloseService[*routedService](t.Context()))
	}
}

// KI-054: grouped routes of a removed service are dropped too.
func TestFindings_KI054GroupedRoutes(t *testing.T) {
	t.Parallel()

	app := quietApp()
	s := &routedService{
		name:    "grouped",
		router:  app.Router(),
		gens:    [][]string{{"/a", "/b"}, {"/b"}},
		grouped: true,
	}
	app.ServiceContainer().RegisterServiceInstance[*routedService](s)
	require.NoError(t, app.Start(t.Context()))

	h := app.Handler()
	assert.Equal(t, http.StatusOK, serve(h, http.MethodGet, "/api/a", nil).Code)

	require.NoError(t, app.CloseService[*routedService](t.Context()))
	assert.Equal(t, http.StatusServiceUnavailable, serve(h, http.MethodGet, "/api/a", nil).Code)

	require.NoError(t, app.RestartService[*routedService](t.Context()))
	assert.Equal(t, http.StatusNotFound, serve(h, http.MethodGet, "/api/a", nil).Code)
	assert.Equal(t, http.StatusOK, serve(h, http.MethodGet, "/api/b", nil).Code)
}

// routedService registers, on each Init, the GET routes of its next
// generation of paths, under `/api` when grouped.
type routedService struct {
	router  *gas.Router
	name    string
	gens    [][]string
	gen     int
	grouped bool
}

// Name returns s.name.
func (s *routedService) Name() string { return s.name }

// Init registers the next generation's routes.
func (s *routedService) Init(context.Context) error {
	paths := s.gens[min(s.gen, len(s.gens)-1)]
	s.gen++

	handler := func(c gas.Context) error { return c.Text(http.StatusOK, c.Request().URL.Path) }

	if !s.grouped {
		for _, p := range paths {
			s.router.Get(s, p, handler)
		}

		return nil
	}

	s.router.Route("/api", func(sub *gas.Router) {
		for _, p := range paths {
			sub.Get(s, p, handler)
		}
	})

	return nil
}

// Close does nothing.
func (s *routedService) Close(context.Context) error { return nil }

// P13: a panic in middleware aborted the connection with no response. The
// router recovers it into a 500 in the unified error shape.
func TestFindings_MiddlewarePanicRecovered(t *testing.T) {
	t.Parallel()

	app := quietApp()
	app.Router().UseMiddlewareFunc(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("mw boom") })
	})
	app.Router().Get(nil, "/x", func(gas.Context) error { return nil })
	require.NoError(t, app.Start(t.Context()))

	rr := serve(app.Handler(), http.MethodGet, "/x", nil)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.JSONEq(t,
		`{"error":{"status":500,"code":"internal_error","message":"internal server error"}}`,
		rr.Body.String())
}

// P14: an error or a panic after the handler wrote appended an error body
// to the response. Nothing is appended, and the error is logged.
func TestFindings_ErrorAfterWriteNotAppended(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	app := gas.NewApp(gas.WithServiceInstance[gas.Logger](newJSONLogger(buf)))
	app.Router().Get(nil, "/ew", func(c gas.Context) error {
		_ = c.JSON(http.StatusOK, map[string]int{"ok": 1})

		return errAfterWrite
	})
	app.Router().Get(nil, "/pw", func(c gas.Context) error {
		_ = c.Text(http.StatusOK, "partial;")

		panic("late")
	})
	require.NoError(t, app.Start(t.Context()))

	rr := serve(app.Handler(), http.MethodGet, "/ew", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.JSONEq(t, `{"ok":1}`, rr.Body.String())

	rr = serve(app.Handler(), http.MethodGet, "/pw", nil)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "partial;", rr.Body.String())

	var committed int

	for _, line := range buf.lines() {
		if line["msg"] == "error after the response was committed" {
			committed++
		}
	}

	assert.Equal(t, 2, committed)
}

// P15: 404, 405 and the cross-origin 403 were plain text. They are in the
// unified error shape, and 405 carries Allow.
func TestFindings_ErrorShapes(t *testing.T) {
	t.Parallel()

	app := quietApp()
	app.Router().Post(nil, "/only-post", func(gas.Context) error { return nil })
	app.Router().Put(nil, "/only-post", func(gas.Context) error { return nil })
	require.NoError(t, app.Start(t.Context()))

	h := app.Handler()

	rr := serve(h, http.MethodGet, "/nope", nil)
	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.JSONEq(
		t,
		`{"error":{"status":404,"code":"not_found","message":"not found"}}`,
		rr.Body.String(),
	)

	rr = serve(h, http.MethodGet, "/only-post", nil)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	assert.Equal(t, []string{http.MethodPost, http.MethodPut}, rr.Header().Values("Allow"))
	assert.JSONEq(t,
		`{"error":{"status":405,"code":"method_not_allowed","message":"method not allowed"}}`,
		rr.Body.String())

	rr = serve(h, http.MethodPost, "/only-post", http.Header{"Sec-Fetch-Site": {"cross-site"}})
	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.JSONEq(t,
		`{"error":{"status":403,"code":"forbidden","message":"cross-origin request denied"}}`,
		rr.Body.String())
}

// P15: a cause never reaches the client.
func TestFindings_CauseNotWritten(t *testing.T) {
	t.Parallel()

	app := quietApp()
	app.Router().Get(nil, "/secret", func(gas.Context) error {
		return errPqPasswordAuthenticationFailed2
	})
	require.NoError(t, app.Start(t.Context()))

	rr := serve(app.Handler(), http.MethodGet, "/secret", nil)
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.NotContains(t, rr.Body.String(), "password")
}

// P16: a Scope used by two goroutines of one request raced, and both
// could build their own instance, one never closed. One build is shared.
func TestFindings_ScopeConcurrentResolve(t *testing.T) {
	t.Parallel()

	var builds atomic.Int32

	c := gas.NewServiceContainer()
	c.RegisterScopedService[*scopedCounter](func() *scopedCounter {
		builds.Add(1)
		time.Sleep(5 * time.Millisecond)

		return &scopedCounter{}
	})

	scope := c.NewScope(t.Context())

	var (
		wg   sync.WaitGroup
		seen sync.Map
	)

	for range 8 {
		wg.Go(func() {
			v, err := scope.Resolve[*scopedCounter]()
			if assert.NoError(t, err) {
				seen.Store(v, true)
			}
		})
	}

	wg.Wait()

	assert.Equal(t, int32(1), builds.Load())

	n := 0

	seen.Range(func(any, any) bool {
		n++

		return true
	})
	assert.Equal(t, 1, n)
	require.NoError(t, scope.Close(t.Context()))

	_, err := scope.Resolve[*scopedCounter]()
	require.NoError(t, err, "a built instance is still returned after Close")
}

// scopedCounter is a plain scoped type.
type scopedCounter struct{ _ int }

// P17: full NewApp, Start, Serve and Stop cycles leave no goroutine
// behind.
func TestFindings_NoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()

	for range 3 {
		app := quietApp()
		app.Router().Get(nil, "/", func(c gas.Context) error { return c.Text(http.StatusOK, "x") })
		require.NoError(t, app.Start(t.Context()))

		ts := httptest.NewServer(app.Handler())
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/", nil)
		require.NoError(t, err)

		resp, err := ts.Client().Do(req)
		require.NoError(t, err)

		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		ts.Close()
		require.NoError(t, app.Stop(t.Context()))
	}

	assert.Eventually(t, func() bool { return runtime.NumGoroutine() <= before+2 },
		5*time.Second, 10*time.Millisecond)
}

// P18: CacheControl wrote `max-age=-5` and truncated sub-second durations
// to 0. A negative duration panics, and durations round up.
func TestFindings_CacheControlDurations(t *testing.T) {
	t.Parallel()

	h := gas.CacheControl(
		gas.WithCacheControlMaxAge(1500*time.Millisecond),
		gas.WithCacheControlSMaxAge(500*time.Millisecond),
	)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	rr := serve(h, http.MethodGet, "/", nil)
	assert.Equal(t, "max-age=2, s-maxage=1", rr.Header().Get("Cache-Control"))

	assert.PanicsWithError(
		t,
		"gas: invalid option: WithCacheControlMaxAge must not be negative",
		func() {
			gas.CacheControl(gas.WithCacheControlMaxAge(-5 * time.Second))
		},
	)
}

// P19: GAS_ENV never reached App.Config().Env, which bound from `env`,
// not `gasenv`, where the config module's EnvExtension stores it.
func TestFindings_EnvBindsFromGasEnv(t *testing.T) {
	t.Parallel()

	app := startedApp(
		t,
		gas.WithServiceInstance[gas.ConfigProvider](&mapConfig{values: map[string]any{
			"gasenv": "production", "env": "staging",
		}}),
	)

	assert.Equal(t, gas.EnvProduction, app.Config().Env)
}

// P19: nothing validated the bound config: an unknown environment, port
// -1 and a negative shutdown timeout bound. Start now fails, naming the
// key, not the value.
func TestFindings_ConfigValidated(t *testing.T) {
	t.Parallel()

	app := quietApp(gas.WithServiceInstance[gas.ConfigProvider](&mapConfig{values: map[string]any{
		"gasenv": "prod-typo",
	}}))

	err := app.Start(t.Context())
	require.ErrorIs(t, err, gas.ErrInvalidConfig)
	assert.NotContains(t, err.Error(), "prod-typo")
}

// P20: a Logger that is a service was closed before the Worker's last log
// lines and never flushed. It is flushed and closed last.
func TestFindings_LoggerClosedLast(t *testing.T) {
	t.Parallel()

	rec := &eventRecorder{}
	logger := &serviceLogger{rec: rec}

	w := gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](logger),
		gas.WithServiceInstance(&fakeService{name: "other"}),
	)
	require.NoError(t, w.Start(t.Context()))
	require.NoError(t, w.Shutdown(t.Context()))

	events := rec.all()
	require.GreaterOrEqual(t, len(events), 3)
	assert.Equal(t, []string{"shutdown complete", "flush", "close"}, events[len(events)-3:])
}

// P21: a ctx canceled during startup fails Start and closes what was
// initialized.
func TestFindings_StartCtxCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	a := &fakeService{name: "a"}
	b := &otherService{fakeService{name: "b", initFn: func(ictx context.Context) error {
		cancel()
		<-ictx.Done()

		return ictx.Err()
	}}}

	w := gas.NewWorker(gas.WithServiceInstance(a), gas.WithServiceInstance(b))
	require.ErrorIs(t, w.Start(ctx), context.Canceled)
	assert.Equal(t, int32(1), a.closes.Load())
}

// P22: before Start the router is not sealed and every path answers 404;
// Serve refuses to run.
func TestFindings_BeforeStart(t *testing.T) {
	t.Parallel()

	app := quietApp()
	app.Router().Get(nil, "/x", func(c gas.Context) error { return c.Text(http.StatusOK, "x") })

	assert.Equal(t, http.StatusNotFound, serve(app.Handler(), http.MethodGet, "/x", nil).Code)
	require.ErrorIs(t, app.Serve(), gas.ErrNotStarted)
}

// P23: a DI handler whose dependency is not registered fails Start with
// the method, the path, the owner and the type.
func TestFindings_MissingHandlerDependency(t *testing.T) {
	t.Parallel()

	app := quietApp()
	app.Router().Get(nil, "/x", func(gas.Context, *bytes.Buffer) error { return nil })

	err := app.Start(t.Context())
	require.ErrorIs(t, err, gas.ErrNotRegistered)
	require.EqualError(t, err,
		`gas: handler GET /x (service "gas/router"): dependency *bytes.Buffer is not registered`)
}

// P24: a 404 logged twice, the second at error level. With RequestLogger
// it logs one line, at warn level, carrying the error.
func TestFindings_OneLinePerFailedRequest(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	app := gas.NewApp(gas.WithServiceInstance[gas.Logger](newJSONLogger(buf)))
	app.Router().
		UseMiddlewareFunc(gas.RequestLogger[gas.Logger](gas.WithRequestLoggerAppendRequestID(false)))
	app.Router().Get(nil, "/missing", func(gas.Context) error { return gas.NotFound("nope") })
	require.NoError(t, app.Start(t.Context()))

	before := len(buf.lines())

	serve(app.Handler(), http.MethodGet, "/missing", nil)

	lines := buf.lines()[before:]
	require.Len(t, lines, 1)
	assert.Equal(t, "request", lines[0]["msg"])
	assert.Equal(t, "WARN", lines[0]["level"])
	assert.Equal(t, "not_found: nope", lines[0]["error"])
}

// KI-035: a recovered handler panic was logged through the scope's
// gas.Logger, without the request ID. It is logged through the
// per-request logger RequestLogger put on the context.
func TestFindings_KI035PanicLoggedWithRequestID(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	app := gas.NewApp(gas.WithServiceInstance[gas.Logger](newJSONLogger(buf)))
	app.Router().UseMiddlewareFunc(middleware.RequestID)
	app.Router().UseMiddlewareFunc(gas.RequestLogger[gas.Logger]())
	app.Router().Get(nil, "/boom", func(gas.Context) error { panic("boom") })
	require.NoError(t, app.Start(t.Context()))

	serve(app.Handler(), http.MethodGet, "/boom", nil)

	for _, line := range buf.lines() {
		if line["msg"] == "handler panic" {
			assert.NotEmpty(t, line["request_id"])

			return
		}
	}

	t.Fatal("no handler panic line")
}

// KI-039: a nil interface registered as an instance panicked at Start
// inside reflect. Registration panics with ErrNilInstance.
func TestFindings_KI039NilInstance(t *testing.T) {
	t.Parallel()

	var p gas.ConfigProvider

	assert.PanicsWithError(t, "gas: nil instance: gas.ConfigProvider", func() {
		gas.NewWorker(gas.WithServiceInstance[gas.ConfigProvider](p))
	})
	assert.Panics(t, func() {
		gas.NewWorker(gas.WithServiceInstance[*fakeService](nil))
	})
}

// KI-045: sub-routers dropped the parent's validator and form decoder,
// so a form field's `form` tag and unknown keys failed under Route.
func TestFindings_KI045SubRouterDecoder(t *testing.T) {
	t.Parallel()

	type form struct {
		UserName string `form:"user_name"`
	}

	app := quietApp()
	app.Router().Route("/api", func(sub *gas.Router) {
		sub.Post(nil, "/form", func(c gas.Context) error {
			var f form

			err := c.BindForm(&f)
			if err != nil {
				return err
			}

			return c.Text(http.StatusOK, f.UserName)
		})
	})
	require.NoError(t, app.Start(t.Context()))

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/api/form",
		strings.NewReader("user_name=bob&extra=1"),
	)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "bob", rr.Body.String())
}

// KI-050: RequestLogger kept a concrete scoped T the per-request logger
// cannot take, silently. It warns, once.
func TestFindings_KI050ScopedTypeNotAssignable(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	base := newJSONLogger(buf)

	app := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](base),
		gas.WithScopedService[*wrappedLogger](
			func(l gas.Logger) *wrappedLogger { return &wrappedLogger{l} },
		),
	)
	app.Router().UseMiddlewareFunc(gas.RequestLogger[*wrappedLogger]())
	app.Router().
		Get(nil, "/x", func(c gas.Context, _ *wrappedLogger) error { return c.NoContent() })
	require.NoError(t, app.Start(t.Context()))

	for range 2 {
		serve(app.Handler(), http.MethodGet, "/x", nil)
	}

	var warnings int

	for _, line := range buf.lines() {
		if line["msg"] == "request logger cannot replace the scoped logger type" {
			warnings++

			assert.Equal(t, "*gas_test.wrappedLogger", line["type"])
		}
	}

	assert.Equal(t, 1, warnings)
}

// wrappedLogger is a concrete Logger type.
type wrappedLogger struct{ gas.Logger }

// KI-056: Routes() left a top-level named middleware off the routes
// registered before its Use. Every route reports it.
func TestFindings_KI056RoutesReportGlobalMiddleware(t *testing.T) {
	t.Parallel()

	app := quietApp()
	r := app.Router()
	r.Register(nil, "audit", func(next http.Handler) http.Handler { return next })
	r.Get(nil, "/before", func(gas.Context) error { return nil })
	r.Group(
		func(sub *gas.Router) { sub.Get(nil, "/group-before", func(gas.Context) error { return nil }) },
	)
	r.UseMiddlewareByName("audit")
	r.Get(nil, "/after", func(gas.Context) error { return nil })
	require.NoError(t, app.Start(t.Context()))

	routes := r.Routes()["gas/router"]
	require.Len(t, routes, 3)

	for _, rt := range routes {
		assert.Equal(t, []string{"audit"}, rt.Middleware, rt.Path)
	}
}

// KI-058: the FieldError doc named only the json tag. The name follows
// the json tag, then the form tag, then the Go name.
func TestFindings_KI058FieldNames(t *testing.T) {
	t.Parallel()

	type body struct {
		A string `json:"a_json" validate:"required"`

		B string `form:"b_form" validate:"required"`

		C string `json:"-" validate:"required"`
	}

	app := quietApp()
	app.Router().Post(nil, "/x", func(c gas.Context) error {
		var b body

		return c.BindJSON(&b)
	})
	require.NoError(t, app.Start(t.Context()))

	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/x",
		strings.NewReader(`{}`),
	)
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnprocessableEntity, rr.Code)
	assert.Contains(t, rr.Body.String(), `"field":"a_json"`)
	assert.Contains(t, rr.Body.String(), `"field":"b_form"`)
	assert.Contains(t, rr.Body.String(), `"field":"C"`)
}
