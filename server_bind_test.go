package gas_test

import (
	"context"
	"testing"
	"time"

	"github.com/gasmod/gas"
)

// serverProvider is a ConfigProvider whose Bind applies fn to the Config.
type serverProvider struct {
	gas.ConfigProvider // methods other than Bind are unused; calling one panics

	fn func(*gas.Config)
}

func (p *serverProvider) Bind(dest any, _ ...gas.BindOption) error {
	if cfg, ok := dest.(*gas.Config); ok {
		p.fn(cfg)
	}
	return nil
}

// boundApp returns an App whose ConfigProvider binds fn, after early has
// run on the App before Start, as a service's Init would.
func boundApp(t *testing.T, fn func(*gas.Config), early func(*gas.App)) *gas.App {
	t.Helper()
	app := gas.NewApp(gas.WithServiceInstance[gas.ConfigProvider](&serverProvider{fn: fn}))
	early(app)
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	return app
}

// bindServer is the bound config the tests use: every server field the App
// copies, changed from its default.
func bindServer(c *gas.Config) {
	c.Server.Port = 9091
	c.Server.ReadTimeout = 7 * time.Second
	c.Server.ReadHeaderTimeout = 4 * time.Second
	c.Server.WriteTimeout = 11 * time.Second
	c.Server.IdleTimeout = 3 * time.Minute
}

// A server built before Start, from the defaults, gets every bound value
// once Start binds the config, instead of keeping the defaults for the
// life of the process.
func TestServer_BuiltBeforeStartGetsTheBoundConfig(t *testing.T) {
	t.Parallel()
	app := boundApp(t, bindServer, func(a *gas.App) { _ = a.Server() })

	srv := app.Server()
	if srv.Addr != "0.0.0.0:9091" || srv.ReadTimeout != 7*time.Second ||
		srv.ReadHeaderTimeout != 4*time.Second || srv.WriteTimeout != 11*time.Second ||
		srv.IdleTimeout != 3*time.Minute {
		t.Errorf("Server() = Addr %s, timeouts %v %v %v %v; want the bound config",
			srv.Addr, srv.ReadTimeout, srv.ReadHeaderTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
}

// A field the caller set on a server built before Start is kept: the bound
// config only fills the fields left as the App set them.
func TestServer_BuiltBeforeStartKeepsWhatTheCallerSet(t *testing.T) {
	t.Parallel()
	app := boundApp(t, bindServer, func(a *gas.App) {
		a.Server().ReadTimeout = 0 // stream request bodies
	})

	srv := app.Server()
	if srv.ReadTimeout != 0 {
		t.Errorf("Server().ReadTimeout = %v, want the caller's 0", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 11*time.Second {
		t.Errorf("Server().WriteTimeout = %v, want the bound 11s", srv.WriteTimeout)
	}
}

// An AppConfigUpdated subscriber runs after the bound config is applied, so
// what it sets on the server stays.
func TestServer_AppConfigUpdatedHasTheLastWord(t *testing.T) {
	t.Parallel()
	app := boundApp(t, bindServer, func(a *gas.App) {
		a.EventBus().Subscribe[gas.AppConfigUpdated](func(gas.AppConfigUpdatedPayload) {
			a.Server().ReadHeaderTimeout = 2 * time.Second
		})
	})
	if got := app.Server().ReadHeaderTimeout; got != 2*time.Second {
		t.Errorf("Server().ReadHeaderTimeout = %v, want the subscriber's 2s", got)
	}
}

// A field the caller set to the value the App had copied is
// indistinguishable from one left alone, so it takes the bound value. This
// pins the documented edge.
func TestServer_SetToTheSameValueCountsAsUnchanged(t *testing.T) {
	t.Parallel()
	app := boundApp(t, bindServer, func(a *gas.App) {
		srv := a.Server()
		srv.ReadTimeout = gas.DefaultConfig().Server.ReadTimeout
	})
	if got := app.Server().ReadTimeout; got != 7*time.Second {
		t.Errorf("Server().ReadTimeout = %v, want the bound 7s", got)
	}
}

// The subscriber's last word holds even when it sets the very value Server
// copied: were the bound config applied after AppConfigUpdated, that field
// would look untouched and be overwritten.
func TestServer_AppConfigUpdatedRunsAfterTheBoundConfig(t *testing.T) {
	t.Parallel()
	copied := gas.DefaultConfig().Server.ReadHeaderTimeout
	app := boundApp(t, bindServer, func(a *gas.App) {
		_ = a.Server()
		a.EventBus().Subscribe[gas.AppConfigUpdated](func(gas.AppConfigUpdatedPayload) {
			a.Server().ReadHeaderTimeout = copied
		})
	})
	if got := app.Server().ReadHeaderTimeout; got != copied {
		t.Errorf("Server().ReadHeaderTimeout = %v, want the subscriber's %v", got, copied)
	}
}
