package gas_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

var errTaskFailed = errors.New("render failed")

// lingeringTask returns a task that, once its context ends, works for a
// moment longer and then marks done, so a test can tell whether a wait
// covered it.
func lingeringTask(done *atomic.Bool) func(context.Context) error {
	return func(ctx context.Context) error {
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		done.Store(true)

		return ctx.Err()
	}
}

// doneAtClose returns a service named name whose Close records whether
// done was set by then.
func doneAtClose(name string, done *atomic.Bool, seen *atomic.Bool) *fakeService {
	return &fakeService{name: name, closeFn: func(context.Context) error {
		seen.Store(done.Load())

		return nil
	}}
}

// Go refuses a task before Start and once the lifetime context has ended,
// without running it.
func TestGo_Refused(t *testing.T) {
	t.Parallel()

	var ran atomic.Bool

	task := func(context.Context) error {
		ran.Store(true)

		return nil
	}

	w := gas.NewWorker(gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}))
	require.ErrorIs(t, w.Go("early", task), gas.ErrNotStarted)

	require.NoError(t, w.Start(t.Context()))
	require.NoError(t, w.Shutdown(t.Context()))
	require.ErrorIs(t, w.Go("late", task), gas.ErrShutDown)

	failed := gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithReadyFunc(
			func(context.Context, *gas.ServiceContainer) error { return errLifetimeReady },
		),
	)
	require.Error(t, failed.Start(t.Context()))
	require.ErrorIs(t, failed.Go("after failure", task), gas.ErrShutDown)

	assert.False(t, ran.Load())
}

// Shutdown waits for a task, started during Start or after it, to return
// before it closes any service.
func TestGo_ShutdownWaits(t *testing.T) {
	t.Parallel()

	var inInit, afterStart, seenInit, seenAfter atomic.Bool

	var w *gas.Worker

	starter := &fakeService{name: "starter", initFn: func(context.Context) error {
		return w.Go("from init", lingeringTask(&inInit))
	}}

	w = gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance(starter),
		gas.WithServiceInstance(&otherService{fakeService{
			name: "watcher",
			closeFn: func(context.Context) error {
				seenAfter.Store(afterStart.Load())

				return nil
			},
		}}),
	)

	starter.closeFn = func(context.Context) error {
		seenInit.Store(inInit.Load())

		return nil
	}

	require.NoError(t, w.Start(t.Context()))
	require.NoError(t, w.Go("after start", lingeringTask(&afterStart)))

	require.NoError(t, w.Shutdown(t.Context()))

	assert.True(t, seenInit.Load(), "the task started in Init ran to its end before Close")
	assert.True(t, seenAfter.Load(), "the task started after Start ran to its end before Close")
}

// A Start that fails waits for the tasks before it closes its services.
func TestGo_FailedStartWaits(t *testing.T) {
	t.Parallel()

	var done, seen atomic.Bool

	var w *gas.Worker

	starter := doneAtClose("starter", &done, &seen)

	starter.initFn = func(context.Context) error {
		return w.Go("from init", lingeringTask(&done))
	}

	w = gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance(starter),
		gas.WithReadyFunc(
			func(context.Context, *gas.ServiceContainer) error { return errLifetimeReady },
		),
	)

	require.ErrorIs(t, w.Start(t.Context()), errLifetimeReady)
	assert.True(t, seen.Load())
}

// A failed Start's wait is bounded by the shutdown timeout, less
// ShutdownGracePeriod, and its error joins Start's.
func TestGo_FailedStartWaitIsBounded(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	defer close(release)

	var w *gas.Worker

	starter := &fakeService{name: "starter", initFn: func(context.Context) error {
		return w.Go("stuck", func(context.Context) error {
			<-release

			return nil
		})
	}}

	w = gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithShutdownTimeout(gas.ShutdownGracePeriod+100*time.Millisecond),
		gas.WithServiceInstance(starter),
		gas.WithReadyFunc(
			func(context.Context, *gas.ServiceContainer) error { return errLifetimeReady },
		),
	)

	began := time.Now()
	err := w.Start(t.Context())

	require.ErrorIs(t, err, errLifetimeReady)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "gas: start: wait for background tasks")
	assert.Less(t, time.Since(began), gas.ShutdownGracePeriod)
	assert.EqualValues(t, 1, starter.closes.Load())
}

// Shutdown's wait ends ShutdownGracePeriod before its deadline, logs how
// many tasks are still running, and the services close anyway.
func TestGo_ShutdownWaitIsBounded(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	defer close(release)

	var logs lockedBuffer

	s := &fakeService{name: "s"}
	w := gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](newJSONLogger(&logs)),
		gas.WithServiceInstance(s),
	)

	require.NoError(t, w.Start(t.Context()))
	require.NoError(t, w.Go("stuck", func(context.Context) error {
		<-release

		return nil
	}))

	ctx, cancel := context.WithTimeout(t.Context(), gas.ShutdownGracePeriod+100*time.Millisecond)
	defer cancel()

	began := time.Now()
	err := w.Shutdown(ctx)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "gas: shutdown: wait for background tasks")
	assert.Less(t, time.Since(began), gas.ShutdownGracePeriod)
	assert.EqualValues(t, 1, s.closes.Load())

	var warned bool

	for _, line := range logs.lines() {
		if line["msg"] == "background tasks still running" {
			warned = true

			assert.InDelta(t, 1, line["tasks"], 0)
		}
	}

	assert.True(t, warned)
}

// A task's error is logged with its name unless it is the lifetime
// context's own cancellation, and a panic is recovered and logged.
func TestGo_LogsFailures(t *testing.T) {
	t.Parallel()

	var logs lockedBuffer

	w := gas.NewWorker(gas.WithServiceInstance[gas.Logger](newJSONLogger(&logs)))
	require.NoError(t, w.Start(t.Context()))

	require.NoError(t, w.Go("fails", func(context.Context) error { return errTaskFailed }))
	require.NoError(t, w.Go("panics", func(context.Context) error { panic("boom") }))
	require.NoError(t, w.Go("stops", func(ctx context.Context) error {
		<-ctx.Done()

		return ctx.Err()
	}))

	require.NoError(t, w.Shutdown(t.Context()))

	byTask := map[string]map[string]any{}

	for _, line := range logs.lines() {
		if task, ok := line["task"].(string); ok {
			byTask[task] = line
		}
	}

	require.Contains(t, byTask, "fails")
	assert.Equal(t, "background task failed", byTask["fails"]["msg"])
	assert.Equal(t, errTaskFailed.Error(), byTask["fails"]["error"])

	require.Contains(t, byTask, "panics")
	assert.Equal(t, "background task panic", byTask["panics"]["msg"])
	assert.Equal(t, "gas: panic in background task: boom", byTask["panics"]["error"])

	assert.NotContains(t, byTask, "stops")
}

// Go racing Shutdown either refuses a task or has it waited for: no task
// is still running when the first service closes.
func TestGo_RacesShutdown(t *testing.T) {
	t.Parallel()

	var running atomic.Int32

	var leftAtClose atomic.Int32

	s := &fakeService{name: "s", closeFn: func(context.Context) error {
		leftAtClose.Store(running.Load())

		return nil
	}}

	w := startedWorker(t, gas.WithServiceInstance(s))

	var wg sync.WaitGroup

	for range 32 {
		wg.Go(func() {
			err := w.Go("racer", func(ctx context.Context) error {
				running.Add(1)
				defer running.Add(-1)

				<-ctx.Done()

				return nil
			})
			if err != nil {
				assert.ErrorIs(t, err, gas.ErrShutDown)
			}
		})
	}

	require.NoError(t, w.Shutdown(t.Context()))
	wg.Wait()

	assert.Zero(t, leftAtClose.Load())
}

// When a Shutdown gives up waiting for Start, the Start that then succeeds
// closes its services itself, after waiting for the tasks.
func TestGo_AbandonedShutdownWaits(t *testing.T) {
	t.Parallel()

	var done, seen atomic.Bool

	entered := make(chan struct{})
	release := make(chan struct{})

	var w *gas.Worker

	starter := doneAtClose("starter", &done, &seen)

	starter.initFn = func(context.Context) error {
		err := w.Go("from init", lingeringTask(&done))

		close(entered)
		<-release

		return err
	}

	w = gas.NewWorker(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance(starter),
	)

	started := make(chan error, 1)

	go func() { started <- w.Start(t.Context()) }()

	<-entered

	require.ErrorIs(t, w.Shutdown(expired(t)), context.DeadlineExceeded)

	close(release)
	require.NoError(t, receive(t, started))

	assert.True(t, seen.Load())
}
