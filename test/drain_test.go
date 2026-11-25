package gas_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

// seeDrain returns what dp's drain context looks like now.
func seeDrain(dp gas.DrainProvider) lifeSeen {
	ctx := dp.DrainContext()

	return lifeSeen{err: ctx.Err(), cause: context.Cause(ctx)}
}

// drainProbe is a service that records, in its Close, what the drain and
// lifetime contexts looked like.
type drainProbe struct {
	drain gas.DrainProvider
	life  gas.LifetimeProvider

	drainAt lifeSeen
	lifeAt  lifeSeen
	mu      sync.Mutex
}

var _ gas.Service = (*drainProbe)(nil)

// Name returns "drain-probe".
func (*drainProbe) Name() string { return "drain-probe" }

// Init does nothing.
func (*drainProbe) Init(context.Context) error { return nil }

// Close records what both contexts look like.
func (p *drainProbe) Close(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.drainAt = seeDrain(p.drain) //nolint:contextcheck // the drain context is what it records
	p.lifeAt = seeLife(p.life)    //nolint:contextcheck // the lifetime context is what it records

	return nil
}

// seen returns what the drain and lifetime contexts looked like in Close.
func (p *drainProbe) seen() (lifeSeen, lifeSeen) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.drainAt, p.lifeAt
}

// The drain context is alive until Stop, ends before the
// SystemServerShuttingDown subscribers run while the lifetime context is
// still alive, and both have ended before the first Close.
func TestDrain_EndsFirstInStop(t *testing.T) {
	t.Parallel()

	app := quietApp()
	probe := &drainProbe{drain: app, life: app}
	app.RegisterServiceInstance(probe)

	var drainAtEvent, lifeAtEvent lifeSeen

	app.EventBus().Subscribe[gas.SystemServerShuttingDown](
		func(gas.SystemServerShuttingDownPayload) {
			drainAtEvent = seeDrain(app)
			lifeAtEvent = seeLife(app)
		},
	)

	require.NoError(t, app.Start(t.Context()))
	require.NoError(t, app.DrainContext().Err())

	resolved, err := app.ServiceContainer().Resolve[gas.DrainProvider]()
	require.NoError(t, err)
	require.Equal(t, app.DrainContext(), resolved.DrainContext())

	require.NoError(t, app.Stop(t.Context()))

	require.ErrorIs(t, drainAtEvent.err, context.Canceled)
	require.NoError(t, lifeAtEvent.err)

	drainAtClose, lifeAtClose := probe.seen()
	require.ErrorIs(t, drainAtClose.err, context.Canceled)
	require.ErrorIs(t, lifeAtClose.err, context.Canceled)
}

// The drain context is a child of the lifetime context, so every path
// that ends the lifetime ends it too.
func TestDrain_EndsWithLifetime(t *testing.T) {
	t.Parallel()

	badPort := gas.DefaultConfig()

	badPort.Server.Port = -1

	cases := map[string]func(*testing.T) *gas.App{
		"direct shutdown": func(t *testing.T) *gas.App {
			t.Helper()

			app := quietApp()
			require.NoError(t, app.Start(t.Context()))
			require.NoError(t, app.Shutdown(t.Context()))

			return app
		},
		"failed start": func(t *testing.T) *gas.App {
			t.Helper()

			app := quietApp(gas.WithBaseConfig(*badPort))
			require.ErrorIs(t, app.Start(t.Context()), gas.ErrInvalidConfig)

			return app
		},
		"stop before start": func(t *testing.T) *gas.App {
			t.Helper()

			app := quietApp()
			require.NoError(t, app.Stop(t.Context()))

			return app
		},
	}

	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			app := run(t)

			require.ErrorIs(t, app.DrainContext().Err(), context.Canceled)
			require.ErrorIs(t, app.Context().Err(), context.Canceled)
		})
	}
}
