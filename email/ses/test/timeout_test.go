package ses_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/email"
	"github.com/gasmod/gas/email/ses"
)

// A server that stops answering cannot hold a send past OpTimeout,
// retries included, even with a context that never ends: the send fails
// with `timed out`, wrapping ErrConnect and context.DeadlineExceeded.
func TestTimeout_Stalled(t *testing.T) {
	t.Parallel()

	cfg := config("http://" + blackhole(t))

	cfg.Email.OpTimeout = 300 * time.Millisecond

	s := open(t, cfg)

	start := time.Now()
	err := s.Send(context.Background(), message())
	require.ErrorIs(t, err, ses.ErrConnect)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualError(t, err, "gas/email/ses: send: timed out")
	assert.Less(t, time.Since(start), 2*time.Second)
	assertNoSecrets(t, err.Error())
}

// A refused connection is `connection refused`, and a host that does not
// resolve `unreachable`, both wrapping ErrConnect, and neither naming the
// endpoint.
func TestErrors_Network(t *testing.T) {
	t.Parallel()

	for endpoint, text := range map[string]string{
		"http://" + closedAddr(t): "connection refused",
		"http://zqx.invalid:4566": "unreachable",
	} {
		cfg := config(endpoint)

		cfg.Email.MaxRetries = 0

		err := open(t, cfg).Send(t.Context(), message())
		require.ErrorIs(t, err, ses.ErrConnect, endpoint)
		require.EqualError(t, err, "gas/email/ses: send: "+text)
		assertNoSecrets(t, err.Error(), "zqx")
	}
}

// A send whose own context ends returns the context's error, without
// ErrConnect, even when the context's deadline passes on the network:
// the deadline in force decides.
func TestTimeout_CallerContext(t *testing.T) {
	t.Parallel()

	s := open(t, config("http://"+blackhole(t)))

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	err := s.Send(ctx, message())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, ses.ErrConnect)
	require.EqualError(t, err, "gas/email/ses: send: context deadline exceeded")

	ctx, cancelNow := context.WithCancel(t.Context())

	time.AfterFunc(200*time.Millisecond, cancelNow)

	err = s.Send(ctx, message())
	require.ErrorIs(t, err, context.Canceled)
	require.EqualError(t, err, "gas/email/ses: send: context canceled")
	require.NoError(t, s.CheckReady(t.Context()))
}

// Close waits for a send in flight on a server that does not answer, and
// when its context ends, cancels it: the send returns ErrClosed, Close
// the context's error, and the warning is logged.
func TestClose_CancelsSend(t *testing.T) {
	t.Parallel()

	cfg := config("http://" + blackhole(t))

	cfg.Email.OpTimeout = time.Minute

	s, log := build(t, ses.WithConfig(cfg))
	require.NoError(t, s.Init(t.Context()))

	sendErr := make(chan error, 1)

	go func() { sendErr <- s.Send(context.Background(), message()) }()

	time.Sleep(200 * time.Millisecond)

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	err := s.Close(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualError(t, err, "gas/email/ses: close: context deadline exceeded")

	select {
	case err := <-sendErr:
		require.ErrorIs(t, err, email.ErrClosed)
		require.EqualError(t, err, "gas/email/ses: send: service closed")
	case <-time.After(5 * time.Second):
		t.Fatal("the send in flight did not return")
	}

	entries := log.entries()
	assert.Equal(t, "warn", entries[len(entries)-1].level)
	assert.Equal(t, "service closed, sends in flight canceled", entries[len(entries)-1].msg)
}
