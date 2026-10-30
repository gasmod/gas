package svcutil

import (
	"context"
	"sync"
	"time"

	"github.com/gasmod/gas"
)

// Task is the work a Loop runs, as described in the package
// documentation.
type Task struct {
	// Run does one run of the work. Its context ends when the run times
	// out or the Loop stops. It must not call the Loop's Start or Stop.
	Run func(ctx context.Context) error
	// Logger receives a warning for each run that fails.
	Logger gas.Logger
	// Name names the task in the warnings.
	Name string
	// Interval is the time between runs. A non-positive Interval starts no
	// runs.
	Interval time.Duration
	// Timeout bounds each run. A non-positive Timeout leaves a run
	// unbounded until the Loop stops.
	Timeout time.Duration
}

// Loop runs a Task in the background, as described in the package
// documentation. The zero Loop is stopped and ready to Start. A Loop is
// safe for concurrent use.
type Loop struct {
	// cancel ends the running goroutine's context, or is nil when no
	// goroutine runs.
	cancel context.CancelFunc
	// done is closed when the running goroutine returns.
	done chan struct{}
	// mu guards cancel and done, and makes Start and Stop one at a time,
	// so a Start never overlaps a Stop that is still waiting.
	mu sync.Mutex
}

// Start stops the running task, if any, waiting for it as Stop does, then
// starts t, unless t.Interval is not positive. The Interval is checked
// first: a task that starts nothing may have a nil Run and Logger.
// Otherwise Start panics when t.Run or t.Logger is nil, before it stops
// anything, since that is a wiring mistake.
func (l *Loop) Start(t Task) {
	if t.Interval <= 0 {
		l.Stop()

		return
	}

	if t.Run == nil || t.Logger == nil {
		panic("gas: auth: svcutil.Loop.Start: Task.Run and Task.Logger must not be nil")
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.stopLocked()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	l.cancel = cancel
	l.done = done

	go run(ctx, done, t)
}

// Stop ends the running task and waits for its goroutine to return,
// including a run in progress, whose context it cancels. Stop on a Loop
// that is not running does nothing.
func (l *Loop) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.stopLocked()
}

// Running reports whether a task's goroutine is running.
func (l *Loop) Running() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.cancel != nil
}

// stopLocked is Stop with l.mu held.
func (l *Loop) stopLocked() {
	if l.cancel == nil {
		return
	}

	l.cancel()
	<-l.done

	l.cancel = nil
	l.done = nil
}

// run calls t.Run every t.Interval until ctx ends, then closes done.
func run(ctx context.Context, done chan<- struct{}, t Task) {
	defer close(done)

	ticker := time.NewTicker(t.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// select picks at random when both are ready, so a tick that
			// arrives with the stop must not start a run.
			if ctx.Err() != nil {
				return
			}

			runOnce(ctx, t)
		}
	}
}

// runOnce does one run of t, bounded by t.Timeout, and logs its error at
// warn level unless the run failed because the Loop is stopping.
func runOnce(ctx context.Context, t Task) {
	runCtx := ctx

	if t.Timeout > 0 {
		var cancel context.CancelFunc

		runCtx, cancel = context.WithTimeout(ctx, t.Timeout)
		defer cancel()
	}

	err := t.Run(runCtx)
	if err != nil && ctx.Err() == nil {
		t.Logger.Warn("background task failed").Str("task", t.Name).Err("error", err).Send()
	}
}
