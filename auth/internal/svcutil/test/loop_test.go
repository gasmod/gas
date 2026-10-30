package svcutil_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/internal/svcutil"
	"github.com/gasmod/gas/auth/internal/testutil"
)

// errBoom is the error a failing task returns.
var errBoom = errors.New("boom")

// tick is the interval of loops that should run often.
const tick = 5 * time.Millisecond

// waitFor fails the test unless cond holds within 30 seconds. The bound
// only catches a loop that never runs: a loaded machine, under -race, can
// starve a goroutine for far longer than a few ticks, and the wait ends as
// soon as cond holds.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, cond, 30*time.Second, time.Millisecond, msg)
}

// counting returns a task that counts its runs.
func counting(n *atomic.Int64) svcutil.Task {
	return svcutil.Task{
		Name:     "count",
		Interval: tick,
		Logger:   &gas.NopLogger{},
		Run: func(context.Context) error {
			n.Add(1)

			return nil
		},
	}
}

// A started Loop runs its task every interval until it stops, and no run
// starts after Stop returns.
func TestLoop_RunsUntilStopped(t *testing.T) {
	t.Parallel()

	var (
		l    svcutil.Loop
		runs atomic.Int64
	)

	l.Start(counting(&runs))
	assert.True(t, l.Running())
	waitFor(t, func() bool { return runs.Load() >= 3 }, "the task ran three times")

	l.Stop()
	assert.False(t, l.Running())

	after := runs.Load()

	time.Sleep(10 * tick)
	assert.Equal(t, after, runs.Load())
}

// The first run comes one interval after Start, not at once.
func TestLoop_FirstRunAfterOneInterval(t *testing.T) {
	t.Parallel()

	var (
		l       svcutil.Loop
		started = time.Now()
		first   = make(chan time.Time, 1)
	)

	const interval = 100 * time.Millisecond

	l.Start(svcutil.Task{
		Name:     "first",
		Interval: interval,
		Logger:   &gas.NopLogger{},
		Run: func(context.Context) error {
			select {
			case first <- time.Now():
			default:
			}

			return nil
		},
	})
	defer l.Stop()

	at := <-first
	assert.GreaterOrEqual(t, at.Sub(started), interval)
}

// Stop cancels the context of a run in progress and returns only once the
// run has returned.
func TestLoop_StopWaitsForRun(t *testing.T) {
	t.Parallel()

	var (
		l svcutil.Loop
		// inRun is buffered: the run sends without blocking, and an
		// unbuffered send would be dropped when the test is not yet
		// receiving, leaving the test waiting forever.
		inRun    = make(chan struct{}, 1)
		returned atomic.Bool
	)

	l.Start(svcutil.Task{
		Name:     "slow",
		Interval: tick,
		Logger:   &gas.NopLogger{},
		Run: func(ctx context.Context) error {
			select {
			case inRun <- struct{}{}:
			default:
			}

			<-ctx.Done()
			time.Sleep(20 * time.Millisecond)
			returned.Store(true)

			return ctx.Err()
		},
	})

	<-inRun
	l.Stop()
	assert.True(t, returned.Load(), "Stop returned before the run did")
}

// Stop on a Loop that never started, or that already stopped, does
// nothing.
func TestLoop_StopWhenNotRunning(t *testing.T) {
	t.Parallel()

	var l svcutil.Loop

	l.Stop()
	assert.False(t, l.Running())

	var runs atomic.Int64

	l.Start(counting(&runs))
	l.Stop()
	l.Stop()
	assert.False(t, l.Running())
}

// A Loop starts again after Stop, any number of times, with a fresh
// context each time.
func TestLoop_Restart(t *testing.T) {
	t.Parallel()

	var l svcutil.Loop

	for i := range 3 {
		var runs atomic.Int64

		l.Start(counting(&runs))
		waitFor(
			t,
			func() bool { return runs.Load() >= 2 },
			fmt.Sprintf("the task ran after start number %d", i+1),
		)
		l.Stop()
	}

	assert.False(t, l.Running())
}

// Start on a running Loop stops the running task first, so two tasks never
// run side by side.
func TestLoop_StartReplaces(t *testing.T) {
	t.Parallel()

	var (
		l             svcutil.Loop
		first, second atomic.Int64
	)

	l.Start(counting(&first))
	waitFor(t, func() bool { return first.Load() >= 1 }, "the first task ran")

	l.Start(counting(&second))
	defer l.Stop()

	stopped := first.Load()

	waitFor(t, func() bool { return second.Load() >= 3 }, "the second task ran")
	assert.Equal(t, stopped, first.Load(), "the first task ran after the second started")
}

// A non-positive interval starts nothing, and still stops a running task.
func TestLoop_NonPositiveInterval(t *testing.T) {
	t.Parallel()

	var (
		l    svcutil.Loop
		runs atomic.Int64
	)

	l.Start(counting(&runs))
	waitFor(t, func() bool { return runs.Load() >= 1 }, "the task ran")

	for _, interval := range []time.Duration{0, -time.Second} {
		task := counting(&runs)

		task.Interval = interval

		l.Start(task)
		assert.False(t, l.Running())
	}

	after := runs.Load()

	time.Sleep(10 * tick)
	assert.Equal(t, after, runs.Load())
}

// Start checks the Interval before Run and Logger, so a task that starts
// nothing may leave both nil, and it still stops a running task.
func TestLoop_NonPositiveIntervalNeedsNoRunOrLogger(t *testing.T) {
	t.Parallel()

	var (
		l    svcutil.Loop
		runs atomic.Int64
	)

	l.Start(counting(&runs))
	waitFor(t, func() bool { return runs.Load() >= 1 }, "the task ran")

	for _, interval := range []time.Duration{0, -time.Second} {
		assert.NotPanics(t, func() { l.Start(svcutil.Task{Interval: interval}) })
		assert.False(t, l.Running())
	}
}

// A task with a nil Run or Logger panics before it stops the running
// task.
func TestLoop_StartPanicKeepsRunningTask(t *testing.T) {
	t.Parallel()

	var (
		l    svcutil.Loop
		runs atomic.Int64
	)

	l.Start(counting(&runs))
	defer l.Stop()

	assert.Panics(t, func() { l.Start(svcutil.Task{Interval: tick}) })
	assert.True(t, l.Running())
}

// A positive Timeout puts a deadline on each run's context; without one,
// the context has no deadline.
func TestLoop_Timeout(t *testing.T) {
	t.Parallel()

	deadline := func(timeout time.Duration) (time.Duration, bool) {
		var l svcutil.Loop

		type result struct {
			left time.Duration
			ok   bool
		}

		got := make(chan result, 1)

		l.Start(svcutil.Task{
			Name:     "deadline",
			Interval: tick,
			Timeout:  timeout,
			Logger:   &gas.NopLogger{},
			Run: func(ctx context.Context) error {
				d, ok := ctx.Deadline()
				select {
				case got <- result{left: time.Until(d), ok: ok}:
				default:
				}

				return nil
			},
		})
		defer l.Stop()

		r := <-got

		return r.left, r.ok
	}

	left, ok := deadline(time.Hour)
	assert.True(t, ok)
	assert.InDelta(t, float64(time.Hour), float64(left), float64(time.Minute))

	_, ok = deadline(0)
	assert.False(t, ok)
}

// A run that fails is logged at warn level with the task's name and the
// error, and the loop goes on.
func TestLoop_LogsFailure(t *testing.T) {
	t.Parallel()

	var (
		l      svcutil.Loop
		logger testutil.RecordingLogger
		runs   atomic.Int64
	)

	l.Start(svcutil.Task{
		Name:     "cleanup",
		Interval: tick,
		Logger:   &logger,
		Run: func(context.Context) error {
			runs.Add(1)

			return errBoom
		},
	})

	waitFor(t, func() bool { return runs.Load() >= 2 }, "the task ran twice")
	l.Stop()

	entries := logger.Entries()
	require.NotEmpty(t, entries)

	for _, e := range entries {
		assert.Equal(t, "warn", e.Level)
		assert.Equal(t, "background task failed", e.Msg)
		assert.Equal(t, "cleanup", e.Fields["task"])
		assert.Equal(t, errBoom, e.Fields["error"])
	}
}

// A run that outlives its Timeout fails with the deadline, which is
// logged like any other failure.
func TestLoop_LogsTimeout(t *testing.T) {
	t.Parallel()

	var (
		l      svcutil.Loop
		logger testutil.RecordingLogger
	)

	l.Start(svcutil.Task{
		Name:     "slow",
		Interval: tick,
		Timeout:  time.Millisecond,
		Logger:   &logger,
		Run: func(ctx context.Context) error {
			<-ctx.Done()

			return ctx.Err()
		},
	})

	waitFor(t, func() bool { return len(logger.Entries()) >= 1 }, "a timeout was logged")
	l.Stop()

	err, ok := logger.Entries()[0].Fields["error"].(error)
	require.True(t, ok)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// A run that fails because the Loop is stopping is not logged.
func TestLoop_StopIsNotLogged(t *testing.T) {
	t.Parallel()

	var (
		l      svcutil.Loop
		logger testutil.RecordingLogger
		inRun  = make(chan struct{}, 1)
	)

	l.Start(svcutil.Task{
		Name:     "blocking",
		Interval: tick,
		Logger:   &logger,
		Run: func(ctx context.Context) error {
			select {
			case inRun <- struct{}{}:
			default:
			}

			<-ctx.Done()

			return ctx.Err()
		},
	})

	<-inRun
	l.Stop()
	assert.Empty(t, logger.Entries())
}

// Start panics when the task has no Run or no Logger, a wiring mistake.
func TestLoop_StartPanicsWithoutRunOrLogger(t *testing.T) {
	t.Parallel()

	var l svcutil.Loop

	assert.Panics(t, func() { l.Start(svcutil.Task{Interval: tick, Logger: &gas.NopLogger{}}) })
	assert.Panics(t, func() {
		l.Start(svcutil.Task{Interval: tick, Run: func(context.Context) error { return nil }})
	})
	assert.False(t, l.Running())
}

// Start, Stop and Running may be called from several goroutines at once.
func TestLoop_Concurrent(t *testing.T) {
	t.Parallel()

	var (
		l    svcutil.Loop
		runs atomic.Int64
		wg   sync.WaitGroup
	)

	for i := range 8 {
		wg.Go(func() {
			for range 20 {
				if i%2 == 0 {
					l.Start(counting(&runs))
				} else {
					l.Stop()
				}

				_ = l.Running()
			}
		})
	}

	wg.Wait()
	l.Stop()
	assert.False(t, l.Running())
}
