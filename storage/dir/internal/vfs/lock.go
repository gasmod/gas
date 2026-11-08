package vfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// slot serializes waiters for one name inside this process, so at most one
// goroutine per name is ever parked in the blocking lock syscall.
type slot struct {
	ch   chan struct{} // cap 1; holding the token means owning the name in-process
	refs int           // holder + waiters; guarded by slotTable.mu
}

// slotTable holds the slots of the names of a set of ObjectsLocks. The
// locks of every Bucket handle of a VFS share one, so a key that many
// handles wait for still has at most one goroutine parked in the blocking
// lock syscall.
type slotTable struct {
	slots map[string]*slot // guarded by mu
	mu    sync.Mutex
}

// newSlotTable returns an empty slotTable.
func newSlotTable() *slotTable {
	return &slotTable{slots: make(map[string]*slot)}
}

// ObjectsLock is an exclusive lock per name, held as an OS file lock on a
// file named after it in locksRoot. It excludes holders in other processes
// and, through per-name slots, other goroutines of this process.
type ObjectsLock struct {
	locksRoot *os.Root
	closed    chan struct{}
	// table holds the slots, which other locks may share.
	table *slotTable
	held  map[string]*os.File // nil after Close; guarded by mu
	// scope sets the names of this lock apart in table: the path of
	// locksRoot below the VFS root.
	scope string
	mu    sync.Mutex
}

var _ io.Closer = (*ObjectsLock)(nil)

// NewLock returns an ObjectsLock that keeps its lock files in root, with
// slots of its own: it waits for a name that another ObjectsLock holds in
// the lock syscall, as a lock of another process does. Close closes root.
func NewLock(root *os.Root) *ObjectsLock {
	return newSharedLock(root, newSlotTable(), "")
}

// newSharedLock returns an ObjectsLock that keeps its lock files in root,
// the locks directory scope, and waits for its names in table, which it
// shares with the other locks of its VFS. Close closes root.
func newSharedLock(root *os.Root, table *slotTable, scope string) *ObjectsLock {
	return &ObjectsLock{
		locksRoot: root,
		closed:    make(chan struct{}),
		table:     table,
		held:      make(map[string]*os.File),
		scope:     scope,
	}
}

// Lock blocks until it holds name, ctx is done, or the lock is closed. A
// canceled wait returns an error wrapping ctx.Err(); a closed lock, one
// wrapping os.ErrClosed.
func (l *ObjectsLock) Lock(ctx context.Context, name string) error {
	sErr := l.acquireSlot(ctx, name)
	if sErr != nil {
		return fmt.Errorf("wait for lock: %w", sErr)
	}

	f, fErr := l.lockFile(ctx, name)
	if fErr != nil {
		return fErr
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.held == nil {
		cErr := closeAll(joinErrs(os.ErrClosed, unlock(f)), f)

		l.dropSlot(name)

		return fmt.Errorf("wait for lock: %w", cErr)
	}

	l.held[name] = f

	return nil
}

// Unlock gives up name and keeps its lock file for the next holder. A name
// this lock does not hold returns an error wrapping errNotLocked.
func (l *ObjectsLock) Unlock(name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	f, ok := l.held[name]
	if !ok {
		return errNotLocked
	}

	delete(l.held, name)

	cErr := closeAll(unlock(f), f)
	// Drop the slot even if close failed, or the name is stuck in-process.
	l.dropSlot(name)

	return cErr
}

// Close wakes every waiter with os.ErrClosed, gives up every held name and
// closes the locks root. Close after Close returns nil.
func (l *ObjectsLock) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var err error

	if l.held != nil {
		close(l.closed) // wakes in-process waiters with os.ErrClosed

		for name, f := range l.held {
			err = joinErrs(err, unlock(f), closeOne(f))
			// The slots are shared with the other locks of the VFS, so
			// they are given back; a waiter of this lock that gets one
			// still fails with os.ErrClosed (see acquireSlot).
			l.dropSlot(name)
		}

		l.held = nil
	}

	// os.Root.Close is idempotent.
	return joinErrs(err, closeOne(l.locksRoot))
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
		return errNotLocked
	}

	delete(l.held, name)

	// Remove before unlocking: a waiter that then gets the old file sees it
	// is gone and retries, instead of holding it next to a newcomer.
	err := joinErrs(rmInRoot(l.locksRoot, name, "lock file"), closeAll(unlock(f), f))

	l.dropSlot(name)

	return err
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
		current, sErr := isCurrent(l.locksRoot, f, name)
		if sErr != nil {
			l.dropSlot(name)

			cErr := closeAll(joinErrs(sErr, unlock(f)), f)

			return nil, fmt.Errorf("check lock file: %w", cErr)
		}

		if current {
			return f, nil
		}

		cErr := closeAll(unlock(f), f)
		if cErr != nil {
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

		return nil, fmt.Errorf("acquire lock file: %w", closeAll(lErr, f))
	}

	if !ok {
		done := make(chan error, 1)
		go func() { done <- lockBlocking(f) }()

		select {
		case lErr = <-done:
			if lErr != nil {
				l.dropSlot(name)

				return nil, fmt.Errorf("acquire lock file: %w", closeAll(lErr, f))
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
	deadline := time.Now().Add(deletePendingTimeout)

	for {
		f, err := openInRoot(l.locksRoot, name, "lock file")
		if err == nil || !isDeletePending(err) || !time.Now().Before(deadline) {
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

// isCurrent reports whether f is still the file at name in locksRoot.
func isCurrent(locksRoot *os.Root, f *os.File, name string) (bool, error) {
	held, err := statFile(f)
	if err != nil {
		return false, ioErr("stat held lock file", err)
	}

	current, err := lstatIn(locksRoot, name)
	// A name being removed is not f's to keep: only a holder removes, so it
	// is f, which its last holder released, or a newer file. Do not wait for
	// the removal to end: where it ends only once every handle is closed (see
	// isDeletePending), f's handle is one of those.
	if errors.Is(err, os.ErrNotExist) || isDeletePending(err) {
		return false, nil
	}

	if err != nil {
		return false, ioErr("stat lock file", err)
	}

	return os.SameFile(held, current), nil
}

// acquireSlot takes the in-process slot of name, waiting for the current
// owner to drop it. It fails when ctx is done or the lock is closed.
func (l *ObjectsLock) acquireSlot(ctx context.Context, name string) error {
	l.mu.Lock()

	if l.held == nil {
		l.mu.Unlock()

		return os.ErrClosed
	}

	l.mu.Unlock()

	key := l.slotKey(name)
	s := l.table.ref(key)

	var err error

	select {
	case s.ch <- struct{}{}:
		select {
		case <-l.closed:
			// Close gave the slot back to the other locks of the VFS and
			// this one took it; it is closed all the same.
			l.table.drop(key, true)

			return os.ErrClosed
		default:
			return nil
		}
	case <-ctx.Done():
		err = ctx.Err()
	case <-l.closed:
		err = os.ErrClosed
	}

	l.table.drop(key, false)

	return err
}

// slotKey returns the key of name in the slot table.
func (l *ObjectsLock) slotKey(name string) string {
	return join(l.scope, name)
}

// dropSlot gives back the slot of name, which the caller owns.
func (l *ObjectsLock) dropSlot(name string) {
	l.table.drop(l.slotKey(name), true)
}

// ref adds one reference to the slot of key, making it when there is none,
// and returns it.
func (t *slotTable) ref(key string) *slot {
	t.mu.Lock()
	defer t.mu.Unlock()

	s := t.slots[key]
	if s == nil {
		s = &slot{ch: make(chan struct{}, 1)}
		t.slots[key] = s
	}

	s.refs++

	return s
}

// drop removes one reference to the slot of key. owned means the caller
// holds the slot token and gives it back.
func (t *slotTable) drop(key string, owned bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	s := t.slots[key]
	if owned {
		<-s.ch
	}

	if s.refs--; s.refs == 0 {
		delete(t.slots, key)
	}
}

// abandon stops waiting for the blocking lock of f and returns an error
// wrapping why. The blocking syscall cannot be canceled, so the slot is
// kept until it returns and this name never has more than one parked thread.
func (l *ObjectsLock) abandon(name string, f *os.File, done chan error, why error) error {
	go func() {
		// No caller is left to report these errors to.
		bErr := <-done
		if bErr == nil {
			_ = unlock(f)
		}

		_ = f.Close()

		l.dropSlot(name)
	}()

	return fmt.Errorf("wait for lock: %w", why)
}
