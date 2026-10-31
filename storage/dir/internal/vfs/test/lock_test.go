package vfs_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage/dir/internal/vfs"
)

// Locks on one directory, each on its own root, mixing Unlock and Release,
// never have two holders of a name at once.
func TestLock_MutualExclusionAcrossLocks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	deadline := time.Now().Add(time.Second)

	var inside, overlaps atomic.Int64

	var wg sync.WaitGroup

	for w := range 8 {
		l := newLock(t, dir)

		wg.Go(func() {
			for i := 0; time.Now().Before(deadline); i++ {
				err := l.Lock(context.Background(), "obj")
				if err != nil {
					t.Errorf("unexpected error: %v", err)

					return
				}

				if inside.Add(1) > 1 {
					overlaps.Add(1)
				}

				time.Sleep(50 * time.Microsecond)
				inside.Add(-1)

				if (w+i)%3 == 0 {
					err = l.Release("obj")
				} else {
					err = l.Unlock("obj")
				}

				if err != nil {
					t.Errorf("unexpected error: %v", err)

					return
				}
			}
		})
	}

	wg.Wait()
	assert.Zero(t, overlaps.Load())
}

// A waiter on a lock file that its holder then releases does not hold the
// removed file: it gets the new one, so a newcomer waits for it.
func TestLock_WaiterRetriesAfterRelease(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	holder, waiter, newcomer := newLock(t, dir), newLock(t, dir), newLock(t, dir)

	require.NoError(t, holder.Lock(t.Context(), "n"))

	done := lockAsync(t.Context(), waiter, "n")
	requireBlocked(t, done)
	require.NoError(t, holder.Release("n"))
	require.NoError(t, recv(t, done))

	done = lockAsync(t.Context(), newcomer, "n")
	requireBlocked(t, done)
	require.NoError(t, waiter.Unlock("n"))
	require.NoError(t, recv(t, done))
	require.NoError(t, newcomer.Unlock("n"))
}

// Unlock keeps the lock file and Release removes it; neither works on a
// name the lock does not hold, and Release then leaves the file.
func TestLock_UnlockAndRelease(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	l, other := newLock(t, dir), newLock(t, dir)
	path := filepath.Join(dir, "n")

	require.NoError(t, l.Lock(t.Context(), "n"))
	require.Error(t, other.Unlock("n"))
	require.Error(t, other.Release("n"))
	assert.FileExists(t, path)
	require.NoError(t, l.Unlock("n"))
	assert.FileExists(t, path)
	require.Error(t, l.Unlock("n"))

	require.NoError(t, l.Lock(t.Context(), "n"))
	require.NoError(t, l.Release("n"))
	assert.NoFileExists(t, path)
}

// Locks of distinct names do not wait on each other.
func TestLock_DistinctNamesDoNotContend(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	a, b := newLock(t, dir), newLock(t, dir)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)

	defer cancel()

	require.NoError(t, a.Lock(ctx, "x"))
	require.NoError(t, b.Lock(ctx, "y"))
	require.NoError(t, a.Lock(ctx, "z"))
}

// A wait for the in-process slot ends with the context's error when the
// context ends, and with os.ErrClosed when the lock is closed.
func TestLock_SlotWait(t *testing.T) {
	t.Parallel()

	t.Run("canceled", func(t *testing.T) {
		t.Parallel()

		l := newLock(t, t.TempDir())
		require.NoError(t, l.Lock(t.Context(), "n"))

		ctx, cancel := context.WithCancel(t.Context())
		done := lockAsync(ctx, l, "n")
		requireBlocked(t, done)
		cancel()
		require.ErrorIs(t, recv(t, done), context.Canceled)
		require.NoError(t, l.Unlock("n"))
		require.NoError(t, l.Lock(t.Context(), "n"))
	})

	t.Run("closed", func(t *testing.T) {
		t.Parallel()

		l := newLock(t, t.TempDir())
		require.NoError(t, l.Lock(t.Context(), "n"))

		done := lockAsync(t.Context(), l, "n")
		requireBlocked(t, done)
		require.NoError(t, l.Close())
		require.ErrorIs(t, recv(t, done), os.ErrClosed)
	})
}

// A wait in the blocking file lock call is given up when the context ends
// or the lock is closed; the goroutine left in the call gives the file
// lock back once it gets it, so a later Lock succeeds.
func TestLock_FlockWait(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		abort func(context.CancelFunc, *vfs.ObjectsLock) error
		want  error
	}{
		"canceled": {
			func(cancel context.CancelFunc, _ *vfs.ObjectsLock) error {
				cancel()

				return nil
			},
			context.Canceled,
		},
		"closed": {
			func(_ context.CancelFunc, l *vfs.ObjectsLock) error { return l.Close() },
			os.ErrClosed,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			holder, waiter := newLock(t, dir), newLock(t, dir)

			require.NoError(t, holder.Lock(t.Context(), "n"))

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			done := lockAsync(ctx, waiter, "n")
			requireBlocked(t, done)
			require.NoError(t, tc.abort(cancel, waiter))
			require.ErrorIs(t, recv(t, done), tc.want)
			require.NoError(t, holder.Unlock("n"))

			late := newLock(t, dir)
			lctx, lcancel := context.WithTimeout(t.Context(), 5*time.Second)

			defer lcancel()

			require.NoError(t, late.Lock(lctx, "n"))
			require.NoError(t, late.Unlock("n"))

			if errors.Is(
				tc.want,
				context.Canceled,
			) {
				require.NoError(t, waiter.Lock(lctx, "n"))
				require.NoError(t, waiter.Unlock("n"))
			}
		})
	}
}

// Close gives up every name held, and the lock then refuses Lock with
// os.ErrClosed; a second Close returns nil.
func TestLock_Close(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	l, other := newLock(t, dir), newLock(t, dir)

	require.NoError(t, l.Lock(t.Context(), "a"))
	require.NoError(t, l.Lock(t.Context(), "b"))
	require.NoError(t, l.Close())

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)

	defer cancel()

	require.NoError(t, other.Lock(ctx, "a"))
	require.NoError(t, other.Lock(ctx, "b"))
	require.ErrorIs(t, l.Lock(t.Context(), "c"), os.ErrClosed)
	require.NoError(t, l.Close())
}

// A lock file that cannot be created, opened, or that is a dangling
// symlink, fails Lock at once, and the name can be locked again.
func TestLock_OpenFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	l := newLock(t, dir)

	err := l.Lock(t.Context(), filepath.Join("missing", "n"))
	require.ErrorIs(t, err, os.ErrNotExist)
	requireNoPath(t, err, dir)

	require.NoError(t, os.Symlink(filepath.Join("missingdir", "x"), filepath.Join(dir, "link")))

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)

	defer cancel()

	require.ErrorIs(t, recv(t, lockAsync(ctx, l, "link")), os.ErrNotExist)
}

// A lock file that exists but cannot be opened fails Lock at once with
// os.ErrPermission.
func TestLock_UnopenableLockFile(t *testing.T) {
	t.Parallel()
	skipIfRoot(t)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "n"), nil, 0o000))

	err := recv(t, lockAsync(t.Context(), newLock(t, dir), "n"))
	require.ErrorIs(t, err, os.ErrPermission)
}

// gatedReader is a Body that, at its first read, closes entered and then
// waits for gate to close before it ends.
type gatedReader struct {
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (g *gatedReader) Read([]byte) (int, error) {
	g.once.Do(func() { close(g.entered) })
	<-g.gate

	return 0, io.EOF
}

// The Bucket handles of a VFS share the in-process slot of each key, so
// waves of waiters from handles of their own that give up while a write
// holds the key leave no thread parked in the blocking lock call.
//
// Not parallel: it counts the threads the process creates.
func TestLock_HandlesShareSlots(t *testing.T) {
	v, _ := newVFS(t)
	writer := newBucket(t, v)
	body := &gatedReader{gate: make(chan struct{}), entered: make(chan struct{})}

	written := make(chan error, 1)

	go func() {
		_, err := writer.CreateObject(context.Background(), &vfs.Write{
			Key: "k", Body: body, Done: func() error { return nil },
		})
		written <- err
	}()

	<-body.entered

	threads := pprof.Lookup("threadcreate")
	before := threads.Count()

	for range 4 {
		var wg sync.WaitGroup

		for range 250 {
			wg.Go(func() {
				bk, err := v.OpenBucket(testBucket)
				if err != nil {
					t.Errorf("unexpected error: %v", err)

					return
				}

				ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
				defer cancel()

				_, err = bk.OpenObject(ctx, "k")
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("want a timed-out wait, got %v", err)
				}

				cErr := bk.Close()
				if cErr != nil {
					t.Errorf("unexpected error: %v", cErr)
				}
			})
		}

		wg.Wait()
	}

	grown := threads.Count() - before

	close(body.gate)
	require.NoError(t, <-written)
	assert.Less(t, grown, 50, "threads created while 1000 waiters gave up")
}

// A waiter on the in-process slot of a lock that Close then closes fails
// with os.ErrClosed, whether it sees the close first or the slot Close
// gave back to the other locks of the VFS. Each round takes either path
// at random.
func TestLock_SlotWaitClosedRounds(t *testing.T) {
	t.Parallel()

	for range 20 {
		l := newLock(t, t.TempDir())
		require.NoError(t, l.Lock(t.Context(), "n"))

		done := lockAsync(t.Context(), l, "n")
		requireBlocked(t, done)
		require.NoError(t, l.Close())
		require.ErrorIs(t, recv(t, done), os.ErrClosed)
	}
}
