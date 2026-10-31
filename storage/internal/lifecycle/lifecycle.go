package lifecycle

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/gasmod/gas/storage"
)

// Lifecycle is the lifecycle of a backend whose Init opens a T, as
// described in the package documentation. The zero Lifecycle is not
// initialized. It is safe for concurrent use.
type Lifecycle[T any] struct {
	// cur is the open Session, or nil before Init succeeds and after
	// Close. It is swapped whole.
	cur atomic.Pointer[Session[T]]
	// closed reports whether Close has ever run after a successful
	// Init. Close sets it before it clears cur, and Init never clears it.
	closed atomic.Bool
	// mu makes Init and Close run one at a time. Calls do not take it.
	mu sync.Mutex
}

// Session is what one Init opened, and the calls in flight on it.
type Session[T any] struct {
	// Value is what Init's open returned.
	Value T
	// ctx ends when a Close whose context ended cancels the calls in
	// flight.
	ctx context.Context //nolint:containedctx // the cancellation of one Init's calls.
	// cancel ends ctx.
	cancel context.CancelFunc
	// idle is closed once Close has begun and no call is in flight.
	idle chan struct{}
	// calls counts the calls in flight.
	calls int
	// mu guards calls and closing.
	mu sync.Mutex
	// closing reports whether Close began.
	closing bool
}

// Init opens the lifecycle with what open returns, as described in the
// package documentation.
func (l *Lifecycle[T]) Init(ctx context.Context, open func() (T, error)) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.cur.Load() != nil {
		return storage.ErrAlreadyInitialized
	}

	err := ctx.Err()
	if err != nil {
		return err //nolint:wrapcheck // the backend adds its name and the operation.
	}

	v, err := open()
	if err != nil {
		return err
	}

	//nolint:gosec // G118: Close calls cancel, which the Session keeps.
	sessCtx, cancel := context.WithCancel(context.Background())

	l.cur.Store(&Session[T]{
		Value:  v,
		ctx:    sessCtx,
		cancel: cancel,
		idle:   make(chan struct{}),
	})

	return nil
}

// Begin returns the open Session, counted as a call in flight until its
// End, or the error the package documentation lists.
func (l *Lifecycle[T]) Begin(ctx context.Context) (*Session[T], error) {
	s := l.cur.Load()

	switch {
	case s == nil && l.closed.Load():
		return nil, storage.ErrClosed
	case s == nil:
		return nil, storage.ErrNotInitialized
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closing {
		return nil, storage.ErrClosed
	}

	err := ctx.Err()
	if err != nil {
		return nil, err //nolint:wrapcheck // the backend adds its name and the operation.
	}

	s.calls++

	return s, nil
}

// Close closes the lifecycle, as described in the package
// documentation, and returns the Session it closed, or nil when it was
// not open.
func (l *Lifecycle[T]) Close(ctx context.Context) (*Session[T], error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	s := l.cur.Load()
	if s == nil {
		return nil, nil
	}

	s.mu.Lock()

	s.closing = true
	if s.calls == 0 {
		close(s.idle)
	}

	s.mu.Unlock()

	l.closed.Store(true)
	l.cur.Store(nil)

	var err error

	select {
	case <-s.idle:
	case <-ctx.Done():
		select {
		case <-s.idle:
		default:
			err = ctx.Err()
		}
	}

	// Cancels the calls still in flight when ctx ended, and else frees
	// the context.
	s.cancel()

	return s, err //nolint:wrapcheck // the backend adds its name and the operation.
}

// Context returns the Session's context, which a Close whose context
// ended cancels. Every call runs within it.
func (s *Session[T]) Context() context.Context { return s.ctx }

// End counts a call Begin returned as no longer in flight.
func (s *Session[T]) End() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls--
	if s.closing && s.calls == 0 {
		close(s.idle)
	}
}
