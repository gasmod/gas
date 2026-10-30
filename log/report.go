package log

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// reportClasses are the classes of error reports, in the order a due
// report of each is delivered.
var reportClasses = []error{
	ErrQueueFull,
	ErrLocalHandler,
	ErrMarshal,
	ErrMarshalPanic,
	ErrPost,
	ErrStatus,
	ErrNotDelivered,
	ErrNotRunning,
}

// reporter delivers error reports to the error handler on a goroutine of
// its own, at most one per class per interval. The reports of a class made
// meanwhile are coalesced into one, the last error with the count. The
// goroutine runs only while a report is waiting.
type reporter struct {
	// fn is the error handler.
	fn func(error)
	// stderr is standard error as it was when the reporter was made.
	stderr io.Writer
	// classes holds the state of each class.
	classes map[error]*reportClass
	// wake tells a waiting goroutine that a report arrived.
	wake chan struct{}
	// interval is the shortest time between two reports of a class.
	interval time.Duration
	// mu guards classes and active.
	mu sync.Mutex
	// active is set while the goroutine runs.
	active bool
}

// reportClass is the state of one class of reports.
type reportClass struct {
	// last is the last error reported.
	last error
	// sent is when a report of the class was last delivered.
	sent time.Time
	// pending counts the errors reported since.
	pending uint64
}

// newReporter returns a reporter delivering to fn.
func newReporter(fn func(error), interval time.Duration) *reporter {
	classes := make(map[error]*reportClass, len(reportClasses))
	for _, c := range reportClasses {
		classes[c] = &reportClass{}
	}

	return &reporter{
		fn:       fn,
		stderr:   os.Stderr,
		classes:  classes,
		wake:     make(chan struct{}, 1),
		interval: interval,
	}
}

// report records err in class and makes sure the goroutine runs. It never
// blocks on the error handler.
func (r *reporter) report(class, err error) {
	r.mu.Lock()

	c := r.classes[class]

	c.last = err
	c.pending++

	start := !r.active

	r.active = true

	r.mu.Unlock()

	if start {
		go r.run()

		return
	}

	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// run delivers the due reports until none is waiting.
func (r *reporter) run() {
	for {
		due, wait, idle := r.take(time.Now())
		for _, err := range due {
			r.deliver(err)
		}

		if idle {
			return
		}

		if len(due) > 0 {
			continue
		}

		timer := time.NewTimer(wait)

		select {
		case <-timer.C:
		case <-r.wake:
		}

		timer.Stop()
	}
}

// take returns the reports due at now, the time until the next one is
// due, and whether none is waiting, in which case the goroutine is marked
// stopped.
func (r *reporter) take(now time.Time) ([]error, time.Duration, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var (
		due     []error
		wait    time.Duration
		waiting bool
	)

	for _, class := range reportClasses {
		c := r.classes[class]
		if c.pending == 0 {
			continue
		}

		if left := r.interval - now.Sub(c.sent); !c.sent.IsZero() && left > 0 {
			if !waiting || left < wait {
				wait = left
			}

			waiting = true

			continue
		}

		due = append(due, counted(c.last, c.pending))
		c.pending, c.sent = 0, now
	}

	if len(due) == 0 && !waiting {
		r.active = false

		return nil, 0, true
	}

	return due, wait, false
}

// deliver calls the error handler. A panic of the handler is recovered and
// noted on standard error, without its value; reporting it to the handler
// would panic again.
func (r *reporter) deliver(err error) {
	defer func() {
		if recover() != nil {
			// Standard error is the last resort; a failed write has nowhere to go.
			_, _ = fmt.Fprintln(r.stderr, errPrefix+": error handler panicked")
		}
	}()

	r.fn(err)
}

// counted returns err, with the count when it stands for more than one.
func counted(err error, n uint64) error {
	if n == 1 {
		return err
	}

	return fmt.Errorf("%w (%d times)", err, n)
}
