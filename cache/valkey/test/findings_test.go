package valkey_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/cache/valkey"
)

// KI-052: Init's context did not bound the dial. valkey-go dials and
// handshakes inside NewClient, which takes no context, so a server that
// accepted and never answered held Init for the whole dial timeout, 3s
// with a 300ms context. The store now dials through a function that ends
// the attempt's dial and closes its connections when Init's context ends,
// so Init returns with the context.
func TestKI052_InitContextBoundsDial(t *testing.T) {
	t.Parallel()

	cfg := config(blackhole(t))

	cfg.Cache.DialTimeout = 10 * time.Second

	s, _ := build(t, valkey.WithConfig(cfg))

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()

	err := s.Init(ctx)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 2*time.Second)
	require.ErrorIs(t, err, valkey.ErrConnect)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualError(t, err, "gas/cache/valkey: connect: timed out: context deadline exceeded")
	assert.Nil(t, s.Client())
}

// KI-052, after Init: the connection an attempt made stays open once Init
// has returned, though Init's context ends afterwards: the server gives
// the same CLIENT ID before and after the context ends.
func TestKI052_ContextEndAfterInitKeepsConnection(t *testing.T) {
	t.Parallel()

	s, _ := build(t, valkey.WithConfig(config(valkeyServer(t).addr)))

	ctx, cancel := context.WithCancel(t.Context())
	require.NoError(t, s.Init(ctx))

	c := s.Client()
	before, err := c.Do(t.Context(), c.B().ClientId().Build()).AsInt64()
	require.NoError(t, err)

	cancel()

	time.Sleep(50 * time.Millisecond)

	require.NoError(t, s.Set(t.Context(), "k", []byte("v"), 0))

	after, err := c.Do(t.Context(), c.B().ClientId().Build()).AsInt64()
	require.NoError(t, err)
	assert.Equal(t, before, after, "the connection was replaced")
}
