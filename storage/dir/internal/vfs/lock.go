package vfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gasmod/gas/storage/dir/internal/util"
)

// slot serializes waiters for one name inside this process, so at most one
// goroutine per name is ever parked in the blocking lock syscall.
type slot struct {
	ch   chan struct{} // cap 1; holding the token means owning the name in-process
	refs int           // holder + waiters; guarded by ObjectsLock.mu
}

// ObjectsLock is an exclusive lock per name, held as an OS file lock on a
// file named after it in locksRoot. It excludes holders in other processes
// and, through per-name slots, other goroutines of this process.
type ObjectsLock struct {
	locksRoot *os.Root
	closed    chan struct{}

	held  map[string]*os.File // nil after Close; guarded by mu
	slots map[string]*slot    // guarded by mu
	mu    sync.Mutex
}

var _ io.Closer = (*ObjectsLock)(nil)

// NewLock returns an ObjectsLock that keeps its lock files in root. Close
// closes root.
func NewLock(root *os.Root) *ObjectsLock {
	return &ObjectsLock{
		locksRoot: root,
		closed:    make(chan struct{}),
		held:      make(map[string]*os.File),
		slots:     make(map[string]*slot),
	}
}

// Lock blocks until it holds name, ctx is done, or the lock is closed. A
// canceled wait returns an error wrapping ctx.Err(); a closed lock, one
// wrapping os.ErrClosed.
func (l *ObjectsLock) Lock(ctx context.Context, name string) error {
	if sErr := l.acquireSlot(ctx, name); sErr != nil {
		return fmt.Errorf("wait for lock: %w", sErr)
	}

	f, fErr := l.lockFile(ctx, name)
	if fErr != nil {
		return fErr
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.held == nil {
		cErr := util.CloseWithErr(errors.Join(os.ErrClosed, unlock(f)), f)
		l.dropSlotLocked(name, true)
		return fmt.Errorf("wait for lock: %w", cErr)
	}
	l.held[name] = f
	return nil
}

// lockFile locks the file currently at name. The caller owns the slot; on
// error lockFile has given it back (or handed it to abandon).
func (l *ObjectsLock) lockFile(ctx context.Context, name string) (*os.File, error) {
	for {
		f, fErr := l.acquireFile(ctx, name)
		if fErr != nil {
			return nil, fErr
		}

		// Release unlinks the file while holding it, so a file that is no
		// longer at name was released while we waited, and a newcomer may
		// already hold the one that replaced it.
		current, sErr := l.isCurrent(f, name)
		if sErr != nil {
			l.dropSlot(name)
			cErr := util.CloseWithErr(errors.Join(sErr, unlock(f)), f)
			return nil, fmt.Errorf("check lock file: %w", cErr)
		}
		if current {
			return f, nil
		}

		if cErr := util.CloseWithErr(unlock(f), f); cErr != nil {
			l.dropSlot(name)
			return nil, fmt.Errorf("unlock stale lock file: %w", cErr)
		}

		select {
		case <-ctx.Done():
			l.dropSlot(name)
			return nil, fmt.Errorf("wait for lock: %w", ctx.Err())
		case <-l.closed:
			l.dropSlot(name)
			return nil, fmt.Errorf("wait for lock: %w", os.ErrClosed)
		default:
		}
	}
}

// acquireFile opens and locks the file at name. On error it has given the
// slot back (or handed it to abandon).
func (l *ObjectsLock) acquireFile(ctx context.Context, name string) (*os.File, error) {
	f, fErr := l.openFile(ctx, name)
	if fErr != nil {
		l.dropSlot(name)
		return nil, fErr
	}

	// Fast path: no goroutine when uncontended across processes.
	ok, lErr := tryLock(f)
	if lErr != nil {
		l.dropSlot(name)
		return nil, fmt.Errorf("acquire lock file: %w", util.CloseWithErr(lErr, f))
	}

	if !ok {
		if hook := lockWaitHook.Load(); hook != nil {
			(*hook)(name)
		}
		done := make(chan error, 1)
		go func() { done <- lockBlocking(f) }()

		select {
		case lErr = <-done:
			if lErr != nil {
				l.dropSlot(name)
				return nil, fmt.Errorf("acquire lock file: %w", util.CloseWithErr(lErr, f))
			}
		case <-ctx.Done():
			return nil, l.abandon(name, f, done, ctx.Err())
		case <-l.closed:
			return nil, l.abandon(name, f, done, os.ErrClosed)
		}
	}

	return f, nil
}

// openFile opens the file at name, retrying while a holder is removing it,
// like retryPending but giving up when ctx is done or the lock is closed.
func (l *ObjectsLock) openFile(ctx context.Context, name string) (*os.File, error) {
	for i := 0; ; i++ {
		f, err := openInRoot(l.locksRoot, name, "lock file")
		if err == nil || i == deletePendingRetries || !isDeletePending(err) {
			return f, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for lock: %w", ctx.Err())
		case <-l.closed:
			return nil, fmt.Errorf("wait for lock: %w", os.ErrClosed)
		case <-time.After(deletePendingBackoff):
		}
	}
}

// lockWaitHook, when set, is called with the name of every lock an
// ObjectsLock finds held by another holder, just before it waits for it.
// Only tests set it, to know that a caller has reached that wait.
var lockWaitHook atomic.Pointer[func(name string)]

// SetLockWaitHook sets the function called when a lock has to wait for
// another holder, or clears it when hook is nil. It is for tests of the
// packages that use this one, which cannot see when a call they started
// blocks on a lock.
func SetLockWaitHook(hook func(name string)) {
	if hook == nil {
		lockWaitHook.Store(nil)
		return
	}
	lockWaitHook.Store(&hook)
}

// isCurrent reports whether f is still the file at name.
func (l *ObjectsLock) isCurrent(f *os.File, name string) (bool, error) {
	held, err := f.Stat()
	if err != nil {
		return false, fmt.Errorf("stat held lock file: %w", err)
	}
	current, err := retryPending(func() (os.FileInfo, error) { return l.locksRoot.Lstat(name) })
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat lock file: %w", err)
	}
	return os.SameFile(held, current), nil
}

// Unlock gives up name and keeps its lock file for the next holder. A name
// this lock does not hold returns an error wrapping errNotLocked.
func (l *ObjectsLock) Unlock(name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	f, ok := l.held[name]
	if !ok {
		return fmt.Errorf("%s: %w", name, errNotLocked)
	}
	delete(l.held, name)

	cErr := util.CloseWithErr(unlock(f), f)
	// Drop the slot even if close failed, or the name is stuck in-process.
	l.dropSlotLocked(name, true)

	if cErr != nil {
		return fmt.Errorf("%s: %w", name, cErr)
	}
	return nil
}

// Close wakes every waiter with os.ErrClosed, gives up every held name and
// closes the locks root. Close after Close returns nil.
func (l *ObjectsLock) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var err error
	if l.held != nil {
		close(l.closed) // wakes in-process waiters with os.ErrClosed
		for _, f := range l.held {
			err = errors.Join(err, unlock(f), f.Close())
		}
		l.held = nil
		// Slots of held names are left owned on purpose, so waiters fail
		// with os.ErrClosed instead of racing for a freed slot.
	}

	// os.Root.Close is idempotent.
	return errors.Join(err, l.locksRoot.Close())
}

// Release gives up name and removes its lock file. A name this lock does not
// hold returns an error wrapping errNotLocked.
func (l *ObjectsLock) Release(name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Only the holder may remove the file, or it would unlink a file that
	// another holder still has locked.
	f, ok := l.held[name]
	if !ok {
		return fmt.Errorf("%s: %w", name, errNotLocked)
	}
	delete(l.held, name)

	// Remove before unlocking: a waiter that then gets the old file sees it
	// is gone and retries, instead of holding it next to a newcomer.
	err := rmInRoot(l.locksRoot, name, "lock file")
	if cErr := util.CloseWithErr(unlock(f), f); cErr != nil {
		err = errors.Join(err, fmt.Errorf("%s: %w", name, cErr))
	}
	l.dropSlotLocked(name, true)

	return err
}

// acquireSlot takes the in-process slot of name, waiting for the current
// owner to drop it. It fails when ctx is done or the lock is closed.
func (l *ObjectsLock) acquireSlot(ctx context.Context, name string) error {
	l.mu.Lock()
	if l.held == nil {
		l.mu.Unlock()
		return os.ErrClosed
	}
	s := l.slots[name]
	if s == nil {
		s = &slot{ch: make(chan struct{}, 1)}
		l.slots[name] = s
	}
	s.refs++
	l.mu.Unlock()

	var err error
	select {
	case s.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		err = ctx.Err()
	case <-l.closed:
		err = os.ErrClosed
	}

	l.mu.Lock()
	l.dropSlotLocked(name, false)
	l.mu.Unlock()
	return err
}

// dropSlot gives back the slot of name, which the caller owns.
func (l *ObjectsLock) dropSlot(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.dropSlotLocked(name, true)
}

// dropSlotLocked removes one reference to name's slot. owned means the caller
// holds the slot token and gives it back.
func (l *ObjectsLock) dropSlotLocked(name string, owned bool) {
	s := l.slots[name]
	if owned {
		<-s.ch
	}
	if s.refs--; s.refs == 0 {
		delete(l.slots, name)
	}
}

// abandon stops waiting for the blocking lock of f and returns an error
// wrapping cause. The blocking syscall cannot be canceled, so the slot is
// kept until it returns and this name never has more than one parked thread.
func (l *ObjectsLock) abandon(name string, f *os.File, done chan error, cause error) error {
	go func() {
		// No caller is left to report these errors to.
		if bErr := <-done; bErr == nil {
			_ = unlock(f)
		}
		_ = f.Close()
		l.dropSlot(name)
	}()
	return fmt.Errorf("wait for lock: %w", cause)
}
