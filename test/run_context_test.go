package gas_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

// runProbe is a Service that records what the contexts of its Init and
// Close carry.
type runProbe struct {
	initValue  any
	closeValue any
	closeErr   error
	inits      atomic.Int32
	closed     atomic.Bool
	mu         sync.Mutex
}

func (*runProbe) Name() string { return "run-probe" }

func (p *runProbe) Init(ctx context.Context) error {
	p.mu.Lock()

	p.initValue = ctx.Value(ctxKey{})
	p.mu.Unlock()

	p.inits.Add(1)

	return nil
}

func (p *runProbe) Close(ctx context.Context) error {
	p.mu.Lock()

	p.closeValue = ctx.Value(ctxKey{})
	p.closeErr = ctx.Err()
	p.mu.Unlock()

	p.closed.Store(true)

	return nil
}

// runUntilCanceled runs run in a goroutine with a context that carries a
// value, cancels it once probe is initialized, and returns run's error.
func runUntilCanceled(t *testing.T, probe *runProbe, run func(context.Context) error) error {
	t.Helper()

	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), ctxKey{}, "v"))
	defer cancel()

	done := make(chan error, 1)

	go func() { done <- run(ctx) }()

	require.Eventually(
		t,
		func() bool { return probe.inits.Load() == 1 },
		5*time.Second,
		time.Millisecond,
	)
	cancel()

	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		require.FailNow(t, "Run did not return after its context ended")

		return nil
	}
}

// requireCleanStop fails t unless probe saw ctx's value in Init and in
// Close, and Close ran under a context the end of Run's did not cancel.
func requireCleanStop(t *testing.T, probe *runProbe) {
	t.Helper()

	require.True(t, probe.closed.Load(), "the service was not closed")

	probe.mu.Lock()
	defer probe.mu.Unlock()

	assert.Equal(t, "v", probe.initValue)
	assert.Equal(t, "v", probe.closeValue)
	assert.NoError(t, probe.closeErr)
}

// The end of the context a Worker runs under stops it cleanly, and its
// services see the context's values at Init and at Close.
func TestWorker_Run_StopsWhenContextEnds(t *testing.T) {
	probe := &runProbe{}
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[*runProbe](probe),
	)

	require.NoError(t, runUntilCanceled(t, probe, w.Run))
	requireCleanStop(t, probe)
}

// The end of the context an App runs under stops it cleanly, server
// included, and its services see the context's values at Init and Close.
func TestApp_Run_StopsWhenContextEnds(t *testing.T) {
	cfg := gas.DefaultConfig()

	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 0

	probe := &runProbe{}
	app := quietApp(gas.WithBaseConfig(*cfg), gas.WithServiceInstance[*runProbe](probe))

	require.NoError(t, runUntilCanceled(t, probe, app.Run))
	requireCleanStop(t, probe)
}

// ctxInit is a Service whose Init honors its context, as a slow one does.
type ctxInit struct{}

func (ctxInit) Name() string { return "ctx-init" }

func (ctxInit) Init(ctx context.Context) error { return ctx.Err() }

func (ctxInit) Close(context.Context) error { return nil }

// The context Run is given ends the context every Init gets, so an Init
// that honors it fails Start when the context has ended.
func TestRun_ContextEndedDuringStart(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	t.Run("worker", func(t *testing.T) {
		w := gas.NewWorker(
			gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
			gas.WithServiceInstance[ctxInit](ctxInit{}),
		)
		require.ErrorIs(t, w.Run(ctx), context.Canceled)
	})

	t.Run("app", func(t *testing.T) {
		require.ErrorIs(
			t,
			quietApp(gas.WithServiceInstance[ctxInit](ctxInit{})).Run(ctx),
			context.Canceled,
		)
	})
}
