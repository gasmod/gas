package gas_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

var (
	errLifetimeInit  = errors.New("init failed")
	errLifetimeReady = errors.New("not ready")
	errLifetimeDirty = errors.New("dirty migration")
)

// lifeSeen records what a lifetime context looked like at one moment: its
// error and its cause.
type lifeSeen struct {
	err   error
	cause error
}

// seeLife returns what lp's lifetime context looks like now.
func seeLife(lp gas.LifetimeProvider) lifeSeen {
	ctx := lp.Context()

	return lifeSeen{err: ctx.Err(), cause: context.Cause(ctx)}
}

// lifeProbe is a service that records, in its Close, what the lifetime
// context looked like.
type lifeProbe struct {
	closed lifeSeen
	life   gas.LifetimeProvider
	name   string
	mu     sync.Mutex
	closes atomic.Int32
}

var _ gas.Service = (*lifeProbe)(nil)

// newLifeProbe returns a lifeProbe named "probe" that records lp's context
// in its Close.
func newLifeProbe(lp gas.LifetimeProvider) *lifeProbe {
	return &lifeProbe{life: lp, name: "probe"}
}

// Name returns p.name.
func (p *lifeProbe) Name() string { return p.name }

// Init does nothing.
func (*lifeProbe) Init(context.Context) error { return nil }

// Close counts the call and records what the lifetime context looks like.
func (p *lifeProbe) Close(context.Context) error {
	p.closes.Add(1)

	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = seeLife(p.life) //nolint:contextcheck // the lifetime context is what it records

	return nil
}

// seenAtClose returns what the lifetime context looked like in Close.
func (p *lifeProbe) seenAtClose() lifeSeen {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.closed
}

// The lifetime context exists from NewWorker on, is the same value before
// Start, during an Init, in a ready hook and after Start, is what the
// container hands out as LifetimeProvider, and stays alive until Shutdown.
func TestLifetime_StableUntilShutdown(t *testing.T) {
	t.Parallel()

	var initLP, readyLP gas.LifetimeProvider

	s := &fakeService{name: "s"}

	w := gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[*fakeService](func(lp gas.LifetimeProvider) *fakeService {
			s.initFn = func(context.Context) error {
				initLP = lp

				return nil
			}

			return s
		}),
		gas.WithReadyFunc(func(_ context.Context, c *gas.ServiceContainer) error {
			lp, err := c.Resolve[gas.LifetimeProvider]()

			readyLP = lp

			return err
		}),
	)

	life := w.Context()
	require.NotNil(t, life)
	require.NoError(t, life.Err())

	require.NoError(t, w.Start(t.Context()))

	assert.Equal(t, life, initLP.Context())
	assert.Equal(t, life, readyLP.Context())
	assert.Equal(t, life, w.Context())
	require.NoError(t, life.Err())

	require.NoError(t, w.Shutdown(t.Context()))

	require.ErrorIs(t, life.Err(), context.Canceled)
	require.ErrorIs(t, context.Cause(life), gas.ErrShutDown)
}

// Shutdown ends the lifetime context before it emits SystemShuttingDown
// and before the first Close.
func TestLifetime_EndsBeforeFirstClose(t *testing.T) {
	t.Parallel()

	var atEvent lifeSeen

	w := gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[*lifeProbe](newLifeProbe),
	)
	w.EventBus().Subscribe[gas.SystemShuttingDown](func(gas.SystemShuttingDownPayload) {
		atEvent = seeLife(w)
	})

	require.NoError(t, w.Start(t.Context()))

	probe, err := w.ServiceContainer().Resolve[*lifeProbe]()
	require.NoError(t, err)

	require.NoError(t, w.Shutdown(t.Context()))

	for name, seen := range map[string]lifeSeen{"event": atEvent, "close": probe.seenAtClose()} {
		require.ErrorIs(t, seen.err, context.Canceled, name)
		require.ErrorIs(t, seen.cause, gas.ErrShutDown, name)
	}
}

// A Shutdown before Start ends the lifetime context.
func TestLifetime_ShutdownBeforeStart(t *testing.T) {
	t.Parallel()

	w := gas.NewWorker(gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}))

	require.NoError(t, w.Shutdown(t.Context()))

	require.ErrorIs(t, w.Context().Err(), context.Canceled)
	require.ErrorIs(t, context.Cause(w.Context()), gas.ErrShutDown)
}

// A Shutdown during Start ends the lifetime context at once, before it
// waits for Start, and so does one that gives up waiting.
func TestLifetime_ShutdownDuringStart(t *testing.T) {
	t.Parallel()

	for name, shutdownCtx := range map[string]func(*testing.T) context.Context{
		"waits":    testContext,
		"gives up": expired,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			entered := make(chan struct{})
			release := make(chan struct{})

			s := &fakeService{name: "slow", initFn: func(context.Context) error {
				close(entered)
				<-release

				return nil
			}}

			w := gas.NewWorker(
				gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
				gas.WithServiceInstance(s),
			)

			started := make(chan error, 1)

			go func() { started <- w.Start(t.Context()) }()

			<-entered

			shut := make(chan error, 1)

			go func() { shut <- w.Shutdown(shutdownCtx(t)) }()

			select {
			case <-w.Context().Done():
			case <-time.After(5 * time.Second):
				t.Fatal("the lifetime context did not end while Start ran")
			}

			require.ErrorIs(t, context.Cause(w.Context()), gas.ErrShutDown)

			close(release)
			require.NoError(t, receive(t, started))

			_ = receive(t, shut)
		})
	}
}

// startFailure is one way to make Start fail, and the error it fails
// with.
type startFailure struct {
	cause error
	opts  []gas.Option
	app   bool
}

// startFailures returns every way Start can fail after building a
// service, by name.
func startFailures() map[string]startFailure {
	badPort := gas.DefaultConfig()

	badPort.Server.Port = -1

	failing := func(initFn func(context.Context) error) gas.Option {
		return gas.WithSingletonService[*otherService](func(*lifeProbe) *otherService {
			return &otherService{fakeService{name: "failing", initFn: initFn}}
		})
	}

	return map[string]startFailure{
		"init error": {
			cause: errLifetimeInit,
			opts:  []gas.Option{failing(func(context.Context) error { return errLifetimeInit })},
		},
		"init panic": {
			cause: gas.ErrPanic,
			opts:  []gas.Option{failing(func(context.Context) error { panic("boom") })},
		},
		"invalid config": {
			cause: gas.ErrInvalidConfig,
			opts:  []gas.Option{gas.WithBaseConfig(*badPort)},
			app:   true,
		},
		"migration error": {
			cause: errLifetimeDirty,
			opts: []gas.Option{gas.WithServiceInstance[gas.MigrationManager](
				&fakeMigrations{name: "migrations", err: errLifetimeDirty},
			)},
		},
		"ready hook error": {
			cause: errLifetimeReady,
			opts: []gas.Option{
				gas.WithReadyFunc(func(context.Context, *gas.ServiceContainer) error {
					return errLifetimeReady
				}),
			},
		},
	}
}

// Each way Start fails ends the lifetime context before Start closes a
// service, with the error that failed Start as its cause.
func TestLifetime_StartFailures(t *testing.T) {
	t.Parallel()

	for name, tc := range startFailures() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var probe *lifeProbe

			opts := append([]gas.Option{
				gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
				gas.WithSingletonService[*lifeProbe](func(lp gas.LifetimeProvider) *lifeProbe {
					probe = newLifeProbe(lp)

					return probe
				}),
			}, tc.opts...)

			var w *gas.Worker

			if tc.app {
				w = gas.NewApp(opts...).Worker
			} else {
				w = gas.NewWorker(opts...)
			}

			_ = panicErr(func() { _ = w.Start(t.Context()) })

			require.ErrorIs(t, w.Context().Err(), context.Canceled)
			require.ErrorIs(t, context.Cause(w.Context()), tc.cause)

			require.NotNil(t, probe)
			require.EqualValues(t, 1, probe.closes.Load())

			seen := probe.seenAtClose()
			require.ErrorIs(t, seen.err, context.Canceled)
			require.ErrorIs(t, seen.cause, tc.cause)
		})
	}
}

// An option error fails Start before anything is initialized, and ends the
// lifetime context with it.
func TestLifetime_OptionError(t *testing.T) {
	t.Parallel()

	w := gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithShutdownTimeout(0),
	)

	require.ErrorIs(t, w.Start(t.Context()), gas.ErrInvalidOption)
	require.ErrorIs(t, context.Cause(w.Context()), gas.ErrInvalidOption)
}

// The kill switch leaves the lifetime context alone.
func TestLifetime_KillSwitch(t *testing.T) {
	t.Parallel()

	w := startedWorker(t, gas.WithServiceInstance(&fakeService{name: "s"}))

	require.NoError(t, w.CloseService[*fakeService](t.Context()))
	require.NoError(t, w.Context().Err())

	require.NoError(t, w.RestartService[*fakeService](t.Context()))
	require.NoError(t, w.Context().Err())
}

// Stop drains an App's requests while the lifetime context is still
// alive, and ends it before the first Close.
func TestLifetime_AliveDuringDrain(t *testing.T) {
	t.Parallel()

	cfg := gas.DefaultConfig()

	cfg.Server.ShutdownTimeout = 100 * time.Millisecond

	app := quietApp(gas.WithBaseConfig(*cfg))
	atCancel := make(chan lifeSeen, 1)

	entered := hangRoute(app, func(gas.Context) { atCancel <- seeLife(app) })

	probe := newLifeProbe(app)
	app.RegisterServiceInstance(probe)

	url := serveApp(t, app)
	go getQuietly(t, url+"/hang")

	<-entered

	// The request holds the drain until the deadline, which Stop reports.
	require.ErrorIs(t, app.Stop(t.Context()), context.DeadlineExceeded)

	require.NoError(t, receive(t, atCancel).err)

	seen := probe.seenAtClose()
	require.ErrorIs(t, seen.err, context.Canceled)
	require.ErrorIs(t, seen.cause, gas.ErrShutDown)
}

// An App's requests descend from the lifetime context, so a Shutdown that
// bypasses Stop cancels a request in flight by ending it.
func TestLifetime_DirectShutdownCancelsRequests(t *testing.T) {
	t.Parallel()

	app := quietApp()
	canceled := make(chan error, 1)

	entered := hangRoute(app, func(c gas.Context) { canceled <- context.Cause(c) })

	url := serveApp(t, app)
	go getQuietly(t, url+"/hang")

	<-entered

	require.NoError(t, app.Shutdown(t.Context()))
	require.ErrorIs(t, receive(t, canceled), gas.ErrShutDown)

	// Stop after Shutdown leaves the server alone, so close it here.
	require.NoError(t, app.Server().Close())
}

// Context is safe to call while Shutdown runs.
func TestLifetime_ConcurrentShutdown(t *testing.T) {
	t.Parallel()

	w := startedWorker(t)

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			<-w.Context().Done()
		})
	}

	require.NoError(t, w.Shutdown(t.Context()))
	wg.Wait()
}

// testContext returns t's own context.
func testContext(t *testing.T) context.Context {
	t.Helper()

	return t.Context()
}

// hangRoute registers GET /hang on app: it waits for its request context
// to end, then calls done with it. The returned channel is closed once a
// request has entered.
func hangRoute(app *gas.App, done func(gas.Context)) <-chan struct{} {
	entered := make(chan struct{})

	app.Router().Get(nil, "/hang", func(c gas.Context) error {
		close(entered)
		<-c.Done()

		done(c)

		return nil
	})

	return entered
}

// getQuietly sends a GET of url and drops the response, for a request the
// test only needs in flight.
func getQuietly(t *testing.T, url string) {
	t.Helper()

	req, err := http.NewRequestWithContext(
		context.WithoutCancel(t.Context()),
		http.MethodGet,
		url,
		nil,
	)
	if err != nil {
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

// serveApp starts app, serves it on a free local port until the test ends,
// and returns its base URL.
func serveApp(t *testing.T, app *gas.App) string {
	t.Helper()

	events := listening(app)

	require.NoError(t, app.Start(t.Context()))

	l := listenLocal(t)
	served := make(chan error, 1)

	go func() { served <- app.ServeListener(l) }()

	t.Cleanup(func() {
		_ = app.Stop(context.WithoutCancel(t.Context()))
		_ = receive(t, served)
	})

	return "http://" + receive(t, events).Addr.String()
}
