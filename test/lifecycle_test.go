package gas_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

// Of concurrent Starts, one starts the Worker and the others return
// ErrAlreadyStarted.
func TestWorker_ConcurrentStart(t *testing.T) {
	t.Parallel()

	s := &fakeService{name: "s"}
	w := gas.NewWorker(gas.WithServiceInstance(s))

	var (
		wg      sync.WaitGroup
		ok, dup atomic.Int32
	)

	for range 8 {
		wg.Go(func() {
			err := w.Start(t.Context())

			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, gas.ErrAlreadyStarted):
				dup.Add(1)
			}
		})
	}

	wg.Wait()
	require.NoError(t, w.Shutdown(t.Context()))

	assert.Equal(t, int32(1), ok.Load())
	assert.Equal(t, int32(7), dup.Load())
	assert.Equal(t, int32(1), s.inits.Load())
	assert.Equal(t, int32(1), s.closes.Load())
}

// A Shutdown during Start waits for Start to return, then closes what it
// initialized; concurrent Shutdowns close each service once.
func TestWorker_ShutdownDuringStart(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	release := make(chan struct{})

	s := &fakeService{name: "slow", initFn: func(context.Context) error {
		close(entered)
		<-release

		return nil
	}}
	w := gas.NewWorker(gas.WithServiceInstance(s))

	started := make(chan error, 1)

	go func() { started <- w.Start(t.Context()) }()

	<-entered

	var wg sync.WaitGroup

	for range 3 {
		wg.Go(func() { assert.NoError(t, w.Shutdown(t.Context())) })
	}

	close(release)
	require.NoError(t, <-started)
	wg.Wait()

	assert.Equal(t, int32(1), s.closes.Load())
	require.ErrorIs(t, w.Start(t.Context()), gas.ErrShutDown)
}

// A Shutdown during a Start that fails closes nothing more.
func TestWorker_ShutdownDuringFailedStart(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	release := make(chan struct{})

	s := &fakeService{name: "failing", initFn: func(context.Context) error {
		close(entered)
		<-release

		return errInitFailed2
	}}
	w := gas.NewWorker(gas.WithServiceInstance(s))

	started := make(chan error, 1)

	go func() { started <- w.Start(t.Context()) }()

	<-entered

	done := make(chan error, 1)

	go func() { done <- w.Shutdown(t.Context()) }()

	close(release)
	require.Error(t, <-started)
	require.NoError(t, <-done)
	assert.Zero(t, s.closes.Load())
}

// A Shutdown after a failed Start closes nothing and returns nil.
func TestWorker_ShutdownAfterFailedStart(t *testing.T) {
	t.Parallel()

	w := gas.NewWorker(
		gas.WithReadyFunc(func(*gas.ServiceContainer) error { return errNo }),
	)
	require.Error(t, w.Start(t.Context()))
	require.NoError(t, w.Shutdown(t.Context()))
}

// The kill switch needs a started Worker, and refuses once Shutdown has
// begun.
func TestWorker_KillSwitchLifecycle(t *testing.T) {
	t.Parallel()

	s := &fakeService{name: "s"}
	w := gas.NewWorker(gas.WithServiceInstance(s))

	require.ErrorIs(t, w.CloseService[*fakeService](t.Context()), gas.ErrNotStarted)
	require.ErrorIs(t, w.RestartService[*fakeService](t.Context()), gas.ErrNotStarted)

	require.NoError(t, w.Start(t.Context()))
	require.NoError(t, w.Shutdown(t.Context()))

	require.ErrorIs(t, w.CloseService[*fakeService](t.Context()), gas.ErrShutDown)
	require.ErrorIs(t, w.RestartService[*fakeService](t.Context()), gas.ErrShutDown)
}

// A restart whose Init fails removes what that Init registered and leaves
// the service closed, so a later restart can succeed.
func TestRestartService_InitFailureTearsDown(t *testing.T) {
	t.Parallel()

	app := quietApp()
	fail := atomic.Bool{}
	s := &fakeService{name: "flaky"}

	s.initFn = func(context.Context) error {
		app.Router().Get(s, "/flaky", func(c gas.Context) error { return c.NoContent() })

		if fail.Load() {
			return errInitFailed2
		}

		return nil
	}

	app.ServiceContainer().RegisterServiceInstance[*fakeService](s)
	require.NoError(t, app.Start(t.Context()))
	require.NoError(t, app.CloseService[*fakeService](t.Context()))

	fail.Store(true)

	err := app.RestartService[*fakeService](t.Context())
	require.EqualError(t, err, "gas: restart flaky: init *gas_test.fakeService: init failed")
	assert.Equal(
		t,
		http.StatusServiceUnavailable,
		serve(app.Handler(), http.MethodGet, "/flaky", nil).Code,
	)
	assert.NotContains(t, app.ActiveServices(), "flaky")

	fail.Store(false)
	require.NoError(t, app.RestartService[*fakeService](t.Context()))
	assert.Equal(t, http.StatusNoContent, serve(app.Handler(), http.MethodGet, "/flaky", nil).Code)
}

// CloseService and RestartService running beside each other, health
// checks and Shutdown never race or deadlock.
func TestKillSwitch_Race(t *testing.T) {
	t.Parallel()

	w := gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance(&fakeService{name: "s"}),
	)
	require.NoError(t, w.Start(t.Context()))

	var wg sync.WaitGroup

	for range 4 {
		wg.Go(func() {
			for range 20 {
				_ = w.CloseService[*fakeService](t.Context())
				_ = w.RestartService[*fakeService](t.Context())
				_ = w.CheckHealth(t.Context())
				_ = w.ActiveServices()
			}
		})
	}

	wg.Go(func() {
		time.Sleep(time.Millisecond)
		assert.NoError(t, w.Shutdown(t.Context()))
	})

	wg.Wait()
}

// Start passes its ctx to the migrations.
func TestStart_MigrationsGetCtx(t *testing.T) {
	t.Parallel()

	type key struct{}

	var got any

	m := &ctxMigrations{fakeMigrations: &fakeMigrations{name: "migrate"}}

	m.run = func(ctx context.Context) { got = ctx.Value(key{}) }

	ctx := context.WithValue(t.Context(), key{}, "start")
	w := gas.NewWorker(gas.WithServiceInstance[gas.MigrationManager](m))
	require.NoError(t, w.Start(ctx))
	assert.Same(t, m, w.MigrationManager())
	require.NoError(t, w.Shutdown(t.Context()))

	assert.Equal(t, "start", got)
	assert.Nil(t, w.MigrationManager(), "a closed manager is not handed out")
}

// ctxMigrations calls run from RunPendingContext.
type ctxMigrations struct {
	*fakeMigrations

	run func(ctx context.Context)
}

// RunPendingContext calls run.
func (m *ctxMigrations) RunPendingContext(ctx context.Context) error {
	m.run(ctx)

	return nil
}

// A Worker without a MigrationManager or a ConfigProvider returns nil for
// them.
func TestWorker_OptionalProviders(t *testing.T) {
	t.Parallel()

	w := startedWorker(t)
	assert.Nil(t, w.MigrationManager())
	assert.Nil(t, w.ConfigProvider())

	p := &mapConfig{}

	w = startedWorker(t, gas.WithServiceInstance[gas.ConfigProvider](p))
	assert.Same(t, p, w.ConfigProvider())
}

// The App does not register its Config in the container: a service that
// takes *gas.Config fails Start, and the bound Config is read with
// App.Config.
func TestApp_ConfigNotInjected(t *testing.T) {
	t.Parallel()

	app := quietApp(
		gas.WithServiceInstance[gas.ConfigProvider](&mapConfig{values: map[string]any{
			"gasenv": "staging", "server": map[string]any{"port": 9090},
		}}),
	)
	require.NoError(t, app.Start(t.Context()))
	t.Cleanup(func() { _ = app.Stop(context.WithoutCancel(t.Context())) })

	assert.False(t, app.ServiceContainer().CanResolve[*gas.Config]())
	assert.Equal(t, gas.EnvStaging, app.Config().Env)
	assert.Equal(t, 9090, app.Config().Server.Port)
	assert.Equal(t, gas.DefaultHost, app.Config().Server.Host)

	app = quietApp(gas.WithSingletonService[*fakeService](func(*gas.Config) *fakeService {
		return &fakeService{name: "uses-config"}
	}))
	require.ErrorIs(t, app.Start(t.Context()), gas.ErrNotRegistered)
}

// Config returns a copy: changing it changes the App's Config neither
// before Start nor after.
func TestApp_ConfigReturnsCopy(t *testing.T) {
	t.Parallel()

	app := quietApp()

	before := app.Config()

	before.Server.Port = 1

	require.Equal(t, gas.DefaultPort, app.Config().Server.Port)
	require.NoError(t, app.Start(t.Context()))
	t.Cleanup(func() { _ = app.Stop(context.WithoutCancel(t.Context())) })

	after := app.Config()

	after.Server.Host = "example.com"

	assert.Equal(t, gas.DefaultHost, app.Config().Server.Host)
	assert.Equal(t, gas.DefaultPort, app.Config().Server.Port)
}

// WithBaseConfig sets the Config Start binds over: its values reach the
// bound Config, and the ConfigProvider's override them.
func TestApp_WithBaseConfig(t *testing.T) {
	t.Parallel()

	base := gas.DefaultConfig()

	base.Server.Host = "127.0.0.1"
	base.Server.Port = 1234
	base.Server.ShutdownTimeout = 3 * time.Second

	app := quietApp(
		gas.WithBaseConfig(*base),
		gas.WithServiceInstance[gas.ConfigProvider](&mapConfig{values: map[string]any{
			"server": map[string]any{"port": 9090},
		}}),
	)

	require.Equal(t, *base, app.Config())
	require.NoError(t, app.Start(t.Context()))
	t.Cleanup(func() { _ = app.Stop(context.WithoutCancel(t.Context())) })

	got := app.Config()
	assert.Equal(t, "127.0.0.1", got.Server.Host)
	assert.Equal(t, 9090, got.Server.Port)
	assert.Equal(t, 3*time.Second, got.Server.ShutdownTimeout)
	assert.Equal(t, 1234, base.Server.Port, "Start changed the caller's base config")
}

// Start validates the base config even without a ConfigProvider.
func TestApp_WithBaseConfigValidated(t *testing.T) {
	t.Parallel()

	base := gas.DefaultConfig()

	base.Server.Port = -1

	err := quietApp(gas.WithBaseConfig(*base)).Start(t.Context())
	require.EqualError(
		t,
		err,
		"gas: invalid config: server.port must be 0 to 65535",
	)
}

// gatedConfig is a ConfigProvider whose Bind closes binding and waits for
// read before it binds, so a test can read the Config while Start binds.
type gatedConfig struct {
	mapConfig

	binding chan struct{}
	read    chan struct{}
}

// Bind closes p.binding, waits for p.read, then binds like mapConfig.
func (p *gatedConfig) Bind(dest any) error {
	close(p.binding)
	<-p.read

	return p.mapConfig.Bind(dest)
}

// Config may be called while Start binds the Config; under -race the
// reads and the bind must not race.
func TestApp_ConfigDuringStart(t *testing.T) {
	t.Parallel()

	p := &gatedConfig{
		values:  map[string]any{"server": map[string]any{"port": 9090}},
		binding: make(chan struct{}),
		read:    make(chan struct{}),
	}
	app := quietApp(gas.WithServiceInstance[gas.ConfigProvider](p))

	done := make(chan struct{})

	var wg sync.WaitGroup

	for range 4 {
		wg.Go(func() {
			<-p.binding

			for {
				_ = app.Config()

				select {
				case p.read <- struct{}{}:
				case <-done:
					return
				default:
				}
			}
		})
	}

	err := app.Start(t.Context())

	close(done)
	wg.Wait()
	require.NoError(t, err)
	t.Cleanup(func() { _ = app.Stop(context.WithoutCancel(t.Context())) })

	assert.Equal(t, 9090, app.Config().Server.Port)
}

// A ConfigProvider whose Bind fails Start with ErrInvalidConfig.
func TestApp_BindFails(t *testing.T) {
	t.Parallel()

	app := quietApp(
		gas.WithServiceInstance[gas.ConfigProvider](&mapConfig{err: errBadKey}),
	)

	err := app.Start(t.Context())
	require.ErrorIs(t, err, gas.ErrInvalidConfig)
	require.EqualError(t, err, "gas: invalid config: bind: bad key")
}

// WithAllowedEnvironments replaces the environments Start accepts.
func TestApp_AllowedEnvironments(t *testing.T) {
	t.Parallel()

	cfg := func() gas.Option {
		return gas.WithServiceInstance[gas.ConfigProvider](
			&mapConfig{values: map[string]any{"gasenv": "qa"}},
		)
	}

	require.ErrorIs(t, quietApp(cfg()).Start(t.Context()), gas.ErrInvalidConfig)

	app := startedApp(t, cfg(), gas.WithAllowedEnvironments("qa"))
	assert.Equal(t, gas.Environment("qa"), app.Config().Env)

	err := quietApp(gas.WithAllowedEnvironments("qa"), gas.WithServiceInstance[gas.ConfigProvider](
		&mapConfig{values: map[string]any{"gasenv": "staging"}},
	)).Start(t.Context())
	require.EqualError(
		t,
		err,
		"gas: invalid config: gasenv must be one of qa, development",
	)
}

// The Config's rules hold for every field; a valid Config starts.
func TestConfig_Rules(t *testing.T) {
	t.Parallel()

	valid := map[string]any{"server": map[string]any{
		"host":         "::1",
		"port":         0,
		"readtimeout":  0,
		"writetimeout": 0,
		"idletimeout":  int64(time.Minute),
	}}
	startedApp(t, gas.WithServiceInstance[gas.ConfigProvider](&mapConfig{values: valid}))

	for _, host := range []string{"", "bad host", "-x.example"} {
		err := quietApp(
			gas.WithServiceInstance[gas.ConfigProvider](&mapConfig{values: map[string]any{
				"server": map[string]any{"host": host},
			}}),
		).Start(t.Context())
		require.ErrorIs(t, err, gas.ErrInvalidConfig, host)
	}
}

// The server built before Start takes the bound address and timeouts for
// the fields the caller left, and keeps those it changed.
func TestApp_ServerBuiltBeforeStart(t *testing.T) {
	t.Parallel()

	app := quietApp(gas.WithServiceInstance[gas.ConfigProvider](&mapConfig{values: map[string]any{
		"server": map[string]any{
			"host":         "127.0.0.1",
			"port":         9091,
			"writetimeout": int64(time.Minute),
		},
	}}))

	srv := app.Server()

	srv.ReadTimeout = 0

	require.NoError(t, app.Start(t.Context()))
	t.Cleanup(func() { _ = app.Stop(context.WithoutCancel(t.Context())) })

	assert.Equal(t, "127.0.0.1:9091", srv.Addr)
	assert.Equal(t, time.Minute, srv.WriteTimeout)
	assert.Zero(t, srv.ReadTimeout)
	assert.Same(t, srv, app.Server())
}

// Serve serves until Stop, then returns nil; a second Serve after Stop
// returns ErrShutDown.
func TestApp_ServeAndStop(t *testing.T) {
	t.Parallel()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	app := quietApp(gas.WithServiceInstance[gas.ConfigProvider](&mapConfig{values: map[string]any{
		"server": map[string]any{"host": "127.0.0.1", "port": port},
	}}))
	app.Router().Get(nil, "/", func(c gas.Context) error { return c.Text(http.StatusOK, "up") })
	require.NoError(t, app.Start(t.Context()))

	served := make(chan error, 1)

	go func() { served <- app.Serve() }()

	assert.Eventually(t, func() bool {
		req, _ := http.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"http://"+app.Server().Addr+"/",
			nil,
		)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}

		_ = resp.Body.Close()

		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 10*time.Millisecond)

	require.NoError(t, app.Stop(t.Context()))
	require.NoError(t, <-served)
	require.ErrorIs(t, app.Serve(), gas.ErrShutDown)
	require.NoError(t, app.Stop(t.Context()))
}

// Serve returns a listener error, wrapped.
func TestApp_ServeListenError(t *testing.T) {
	t.Parallel()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	t.Cleanup(func() { _ = ln.Close() })

	app := startedApp(
		t,
		gas.WithServiceInstance[gas.ConfigProvider](&mapConfig{values: map[string]any{
			"server": map[string]any{"host": "127.0.0.1", "port": ln.Addr().(*net.TCPAddr).Port},
		}}),
	)

	err = app.Serve()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gas: serve: ")
}

// A request that ignores its canceled context past ShutdownGracePeriod
// is logged and left; the services close anyway.
func TestApp_StopGraceExpires(t *testing.T) {
	t.Parallel()

	buf := &lockedBuffer{}
	dep := &fakeService{name: "dep"}
	cfg := gas.DefaultConfig()

	cfg.Server.ShutdownTimeout = 20 * time.Millisecond

	app := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](newJSONLogger(buf)),
		gas.WithServiceInstance(dep),
		gas.WithBaseConfig(*cfg),
	)

	entered := make(chan struct{})
	release := make(chan struct{})

	app.Router().Get(nil, "/stuck", func(c gas.Context) error {
		close(entered)
		<-release

		return c.NoContent()
	})
	require.NoError(t, app.Start(t.Context()))

	go serve(app.Handler(), http.MethodGet, "/stuck", nil)

	<-entered
	require.NoError(t, app.Stop(t.Context()))
	close(release)

	assert.Equal(t, int32(1), dep.closes.Load())

	var warned bool

	for _, line := range buf.lines() {
		warned = warned || line["msg"] == "requests still running after the shutdown grace period"
	}

	assert.True(t, warned)
}

// A MigrationManager registered with a lifetime Start cannot resolve it
// with fails Start rather than skipping the migrations.
func TestStart_MigrationManagerUnresolvable(t *testing.T) {
	t.Parallel()

	w := gas.NewWorker(gas.WithScopedService[gas.MigrationManager](func() gas.MigrationManager {
		return &fakeMigrations{name: "migrate"}
	}))

	err := w.Start(t.Context())
	require.ErrorIs(t, err, gas.ErrScopeRequired)
	assert.Contains(t, err.Error(), "gas: run migrations: ")
	assert.Nil(t, w.MigrationManager())
}
