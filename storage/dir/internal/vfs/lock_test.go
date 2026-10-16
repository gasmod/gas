package vfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestLock opens its own root on dir, as each provider call opens its own
// Bucket and so its own ObjectsLock.
func newTestLock(t *testing.T, dir string) *ObjectsLock {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	l := NewLock(root)
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// lockAsync locks "n" in a goroutine and returns its result channel.
func lockAsync(ctx context.Context, l *ObjectsLock) <-chan error {
	done := make(chan error, 1)
	go func() { done <- l.Lock(ctx, "n") }()
	return done
}

// recv waits up to timeout for a Lock result.
func recv(t *testing.T, ch <-chan error, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(timeout):
		t.Fatalf("Lock did not return within %v", timeout)
		return nil
	}
}

// assertBlocked fails if a Lock result arrives within 50ms.
func assertBlocked(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatalf("Lock returned %v while the name was held elsewhere", err)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestLock_MutualExclusionAcrossLocks covers bug 1: separate ObjectsLocks on
// one dir, mixing Unlock and Release, must never have two holders at once.
func TestLock_MutualExclusionAcrossLocks(t *testing.T) {
	dir := t.TempDir()
	const name = "obj"
	deadline := time.Now().Add(2 * time.Second)

	var inside, violations, acquisitions atomic.Int64
	locks := make([]*ObjectsLock, 16)
	for i := range locks {
		locks[i] = newTestLock(t, dir)
	}

	var wg sync.WaitGroup
	for w, l := range locks {
		wg.Go(func() {
			for i := 0; time.Now().Before(deadline); i++ {
				if err := l.Lock(context.Background(), name); err != nil {
					t.Errorf("Lock: %v", err)
					return
				}
				acquisitions.Add(1)
				if inside.Add(1) > 1 {
					violations.Add(1)
				}
				time.Sleep(50 * time.Microsecond)
				inside.Add(-1)

				var rErr error
				if (w+i)%3 == 0 {
					rErr = l.Release(name)
				} else {
					rErr = l.Unlock(name)
				}
				if rErr != nil {
					t.Errorf("unlock: %v", rErr)
					return
				}
			}
		})
	}
	wg.Wait()

	if v := violations.Load(); v > 0 {
		t.Fatalf("%d of %d acquisitions overlapped with another holder", v, acquisitions.Load())
	}
	for _, l := range locks {
		if n := slotCount(l); n != 0 {
			t.Fatalf("%d slots left, want 0", n)
		}
	}
}

// TestLock_WaiterRetriesAfterRelease covers bug 1, deterministic: a waiter
// parked on a lock file that the holder then releases must not end up holding
// the unlinked file.
func TestLock_WaiterRetriesAfterRelease(t *testing.T) {
	dir := t.TempDir()
	holder, waiter := newTestLock(t, dir), newTestLock(t, dir)

	if err := holder.Lock(t.Context(), "n"); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	done := lockAsync(t.Context(), waiter)
	assertBlocked(t, done)

	if err := holder.Release("n"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := recv(t, done, 5*time.Second); err != nil {
		t.Fatalf("waiter Lock: %v", err)
	}

	waiter.mu.Lock()
	f := waiter.held["n"]
	waiter.mu.Unlock()
	held, err := f.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	current, err := os.Lstat(filepath.Join(dir, "n"))
	if err != nil {
		t.Fatalf("lock file missing while held: %v", err)
	}
	if !os.SameFile(held, current) {
		t.Fatal("waiter holds a lock file that is no longer at its name")
	}

	// A newcomer must contend with the waiter.
	newcomer := newTestLock(t, dir)
	done = lockAsync(t.Context(), newcomer)
	assertBlocked(t, done)
	if err := waiter.Unlock("n"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if err := recv(t, done, 5*time.Second); err != nil {
		t.Fatalf("newcomer Lock: %v", err)
	}
}

// TestLock_UnlockAndReleaseFiles checks that Unlock keeps the lock file and
// Release removes it.
func TestLock_UnlockAndReleaseFiles(t *testing.T) {
	dir := t.TempDir()
	l := newTestLock(t, dir)
	path := filepath.Join(dir, "n")

	if err := l.Lock(t.Context(), "n"); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := l.Unlock("n"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Unlock removed the lock file: %v", err)
	}

	if err := l.Lock(t.Context(), "n"); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := l.Release("n"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat after Release = %v, want ErrNotExist", err)
	}
	if n := slotCount(l); n != 0 {
		t.Fatalf("%d slots left, want 0", n)
	}
}

// TestLock_NotHeld checks that Unlock and Release of a name this lock does
// not hold are errors, and Release must not unlink a lock file that another
// lock holds.
func TestLock_NotHeld(t *testing.T) {
	dir := t.TempDir()
	holder, other := newTestLock(t, dir), newTestLock(t, dir)
	if err := holder.Lock(t.Context(), "n"); err != nil {
		t.Fatalf("Lock: %v", err)
	}

	if err := other.Unlock("n"); !errors.Is(err, errNotLocked) {
		t.Errorf("Unlock of a name that is not held = %v, want errNotLocked", err)
	}
	if err := other.Release("n"); !errors.Is(err, errNotLocked) {
		t.Errorf("Release of a name that is not held = %v, want errNotLocked", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "n")); err != nil {
		t.Fatalf("Release of a name that is not held removed another holder's lock file: %v", err)
	}

	if err := holder.Unlock("n"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if err := holder.Unlock("n"); !errors.Is(err, errNotLocked) {
		t.Errorf("second Unlock = %v, want errNotLocked", err)
	}
}

// TestLock_DistinctNamesDoNotContend checks that locks on different names do
// not wait on each other.
func TestLock_DistinctNamesDoNotContend(t *testing.T) {
	dir := t.TempDir()
	a, b := newTestLock(t, dir), newTestLock(t, dir)
	if err := a.Lock(t.Context(), "x"); err != nil {
		t.Fatalf("Lock x: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := b.Lock(ctx, "y"); err != nil {
		t.Fatalf("Lock y: %v", err)
	}
	if err := a.Lock(ctx, "z"); err != nil {
		t.Fatalf("Lock z: %v", err)
	}
}

// TestLock_SlotWait checks that a wait for the in-process slot ends on cancel
// and on Close.
func TestLock_SlotWait(t *testing.T) {
	t.Run("canceled", func(t *testing.T) {
		l := newTestLock(t, t.TempDir())
		if err := l.Lock(t.Context(), "n"); err != nil {
			t.Fatalf("Lock: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := lockAsync(ctx, l)
		assertBlocked(t, done)
		cancel()
		if err := recv(t, done, 2*time.Second); !errors.Is(err, context.Canceled) {
			t.Fatalf("Lock = %v, want Canceled", err)
		}
		if err := l.Unlock("n"); err != nil {
			t.Fatalf("Unlock: %v", err)
		}
		if n := slotCount(l); n != 0 {
			t.Fatalf("%d slots left, want 0", n)
		}
		if err := l.Lock(t.Context(), "n"); err != nil {
			t.Fatalf("Lock after canceled waiter: %v", err)
		}
	})

	t.Run("closed", func(t *testing.T) {
		l := newTestLock(t, t.TempDir())
		if err := l.Lock(t.Context(), "n"); err != nil {
			t.Fatalf("Lock: %v", err)
		}
		done := lockAsync(t.Context(), l)
		assertBlocked(t, done)
		if err := l.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := recv(t, done, 2*time.Second); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("Lock = %v, want ErrClosed", err)
		}
	})
}

// TestLock_FlockWait checks that the blocking flock wait is abandoned on
// cancel or Close; the abandoned goroutine must still release the flock once
// it gets it.
func TestLock_FlockWait(t *testing.T) {
	cancelWait := func(cancel context.CancelFunc, _ *ObjectsLock) error { cancel(); return nil }
	closeLock := func(_ context.CancelFunc, l *ObjectsLock) error { return l.Close() }
	tests := []struct {
		abort func(cancel context.CancelFunc, l *ObjectsLock) error
		want  error
		name  string
	}{
		{cancelWait, context.Canceled, "canceled"},
		{closeLock, os.ErrClosed, "closed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			holder, waiter := newTestLock(t, dir), newTestLock(t, dir)
			if err := holder.Lock(t.Context(), "n"); err != nil {
				t.Fatalf("Lock: %v", err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := lockAsync(ctx, waiter)
			assertBlocked(t, done)
			if err := tt.abort(cancel, waiter); err != nil {
				t.Fatalf("abort: %v", err)
			}
			if err := recv(t, done, 2*time.Second); !errors.Is(err, tt.want) {
				t.Fatalf("Lock = %v, want %v", err, tt.want)
			}

			if err := holder.Unlock("n"); err != nil {
				t.Fatalf("Unlock: %v", err)
			}
			// The abandoned goroutine now takes the flock and must give it back.
			late := newTestLock(t, dir)
			lctx, lcancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer lcancel()
			if err := late.Lock(lctx, "n"); err != nil {
				t.Fatalf("Lock after abandoned wait: %v", err)
			}
			if err := late.Unlock("n"); err != nil {
				t.Fatalf("Unlock: %v", err)
			}

			if errors.Is(tt.want, context.Canceled) {
				assertReusable(t, waiter)
			}
		})
	}
}

// assertReusable checks that l can lock "n" again once its abandoned
// goroutine has given the slot back.
func assertReusable(t *testing.T, l *ObjectsLock) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := l.Lock(ctx, "n"); err != nil {
		t.Fatalf("Lock after canceled wait: %v", err)
	}
	if err := l.Unlock("n"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for slotCount(l) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := slotCount(l); n != 0 {
		t.Fatalf("%d slots left, want 0", n)
	}
}

// TestLock_Close checks that Close releases held locks and rejects later use.
func TestLock_Close(t *testing.T) {
	dir := t.TempDir()
	l := newTestLock(t, dir)
	if err := l.Lock(t.Context(), "a"); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := l.Lock(t.Context(), "b"); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Close released the flocks.
	other := newTestLock(t, dir)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	for _, name := range []string{"a", "b"} {
		if err := other.Lock(ctx, name); err != nil {
			t.Fatalf("Lock(%s) after holder Close: %v", name, err)
		}
	}

	if err := l.Lock(t.Context(), "c"); !errors.Is(err, os.ErrClosed) {
		t.Errorf("Lock after Close = %v, want ErrClosed", err)
	}
	if err := l.Unlock("a"); !errors.Is(err, errNotLocked) {
		t.Errorf("Unlock after Close = %v, want errNotLocked", err)
	}
	if err := l.Release("a"); !errors.Is(err, errNotLocked) {
		t.Errorf("Release after Close = %v, want errNotLocked", err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// TestLock_OpenFails checks that a lock file that cannot be created fails
// Lock and frees the slot.
func TestLock_OpenFails(t *testing.T) {
	l := newTestLock(t, t.TempDir())
	// The parent dir does not exist, so the lock file cannot be created.
	if err := l.Lock(t.Context(), filepath.Join("missing", "n")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Lock = %v, want ErrNotExist", err)
	}
	if n := slotCount(l); n != 0 {
		t.Fatalf("%d slots left, want 0", n)
	}
}

// TestLock_UnopenableLockFile covers bug 2: a lock file that exists but
// cannot be opened makes Lock fail promptly instead of spinning in
// openInRoot.
func TestLock_UnopenableLockFile(t *testing.T) {
	skipIfNoPermissions(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "n"), nil, 0o000); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	l := newTestLock(t, dir)

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	err := recv(t, lockAsync(ctx, l), 2*time.Second)
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("Lock = %v, want ErrPermission", err)
	}
	if n := slotCount(l); n != 0 {
		t.Fatalf("%d slots left, want 0", n)
	}
}

// TestLock_DanglingSymlinkLockFile checks that a lock name that exists but
// cannot be opened as a file, here a symlink to a missing path, makes Lock
// fail promptly with an error wrapping os.ErrNotExist and frees the slot. On
// darwin the exclusive create sees the link (EEXIST) and the follow-up open
// sees its missing target (ENOENT), which once retried forever.
func TestLock_DanglingSymlinkLockFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join("missingdir", "x"), filepath.Join(dir, "n")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	l := newTestLock(t, dir)

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	err := recv(t, lockAsync(ctx, l), 2*time.Second)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Lock = %v, want ErrNotExist", err)
	}
	if n := slotCount(l); n != 0 {
		t.Fatalf("%d slots left, want 0", n)
	}
}

// TestOpenInRoot_Concurrent checks that concurrent first opens of one new
// name all succeed (the darwin os.Root ENOENT workaround). It covers first
// creation only; the retry after a lock file is removed between the two
// opens is covered by TestBucket_ConcurrentSameKey and
// TestLock_MutualExclusionAcrossLocks.
func TestOpenInRoot_Concurrent(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer func() { _ = root.Close() }()

	for round := range 50 {
		name := fmt.Sprint(round)
		var wg sync.WaitGroup
		errs := make(chan error, 50)
		for range 50 {
			wg.Go(func() {
				f, err := openInRoot(root, name, "lock file")
				if err != nil {
					errs <- err
					return
				}
				errs <- f.Close()
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("openInRoot: %v", err)
			}
		}
	}
}
