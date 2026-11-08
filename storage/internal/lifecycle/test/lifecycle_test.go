package lifecycle_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal/lifecycle"
)

// errOpen is the error of an open that fails.
var errOpen = errors.New("open failed")

// open returns an open func that returns v.
func open(v int) func() (int, error) { return func() (int, error) { return v, nil } }

// canceled returns a context that has ended.
func canceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	return ctx
}

// The sentinels before Init, after Init and after Close, a failed open,
// an ended context, and a restart.
func TestLifecycle(t *testing.T) {
	t.Parallel()

	var lc lifecycle.Lifecycle[int]

	ctx := t.Context()

	_, err := lc.Begin(ctx)
	require.ErrorIs(t, err, storage.ErrNotInitialized)

	sess, err := lc.Close(ctx)
	require.NoError(t, err)
	assert.Nil(t, sess)

	require.ErrorIs(t, lc.Init(canceled(), open(1)), context.Canceled)
	require.ErrorIs(t, lc.Init(ctx, func() (int, error) { return 0, errOpen }), errOpen)

	_, err = lc.Begin(ctx)
	require.ErrorIs(t, err, storage.ErrNotInitialized)

	require.NoError(t, lc.Init(ctx, open(1)))
	require.ErrorIs(t, lc.Init(ctx, open(2)), storage.ErrAlreadyInitialized)

	sess, err = lc.Begin(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, sess.Value)
	require.NoError(t, sess.Context().Err())
	sess.End()

	_, err = lc.Begin(canceled())
	require.ErrorIs(t, err, context.Canceled)

	closed, err := lc.Close(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, closed.Value)
	require.Error(t, closed.Context().Err())

	_, err = lc.Begin(ctx)
	require.ErrorIs(t, err, storage.ErrClosed)

	require.NoError(t, lc.Init(ctx, open(2)))

	sess, err = lc.Begin(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, sess.Value)
	sess.End()
}

// Close waits for the calls in flight; when its context ends first, it
// cancels them and returns the context's error, and refuses new calls.
func TestLifecycle_CloseWaits(t *testing.T) {
	t.Parallel()

	var lc lifecycle.Lifecycle[int]

	require.NoError(t, lc.Init(t.Context(), open(1)))

	sess, err := lc.Begin(t.Context())
	require.NoError(t, err)

	done := make(chan error, 1)

	go func() {
		_, closeErr := lc.Close(context.Background())
		done <- closeErr
	}()

	select {
	case closeErr := <-done:
		require.Failf(t, "Close returned with a call in flight", "%v", closeErr)
	case <-time.After(50 * time.Millisecond):
	}

	sess.End()
	require.NoError(t, <-done)

	require.NoError(t, lc.Init(t.Context(), open(2)))

	sess, err = lc.Begin(t.Context())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	_, err = lc.Close(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorIs(t, sess.Context().Err(), context.Canceled)

	_, err = lc.Begin(t.Context())
	require.ErrorIs(t, err, storage.ErrClosed)

	sess.End()
}

// A Session that Close began closing refuses new calls, even when it is
// reached before Close cleared it.
func TestLifecycle_ClosingSession(t *testing.T) {
	t.Parallel()

	var lc lifecycle.Lifecycle[int]

	require.NoError(t, lc.Init(t.Context(), open(1)))

	sess, err := lc.Begin(t.Context())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = lc.Close(ctx)
	require.ErrorIs(t, err, context.Canceled)
	sess.End()
}
