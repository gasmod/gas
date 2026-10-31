package valkey_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/cache"
	"github.com/gasmod/gas/cache/valkey"
)

// Close cancels an Init in progress, whether in a connect attempt or in
// the wait before a retry, and waits for it to return; Init then fails
// with cache.ErrClosed and leaves the store as it was. Alone, each Init
// would take minutes.
func TestClose_CancelsInit(t *testing.T) {
	t.Parallel()

	cases := map[string]func(t *testing.T) *valkey.Config{
		"attempt": func(t *testing.T) *valkey.Config {
			t.Helper()

			cfg := config(blackhole(t))

			cfg.Cache.DialTimeout = time.Minute
			cfg.Cache.OpTimeout = time.Minute

			return cfg
		},
		"retry wait": func(t *testing.T) *valkey.Config {
			t.Helper()

			return config(closedAddr(t))
		},
	}

	for name, cfgFn := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := cfgFn(t)

			cfg.Cache.ConnRetries = 10
			cfg.Cache.ConnRetryInterval = valkey.MaxConnRetryInterval

			s, log := build(t, valkey.WithConfig(cfg))
			done := make(chan error, 1)

			go func() { done <- s.Init(context.Background()) }()

			// Let Init reach its attempt, or its wait after the first.
			time.Sleep(200 * time.Millisecond)

			start := time.Now()

			require.NoError(t, s.Close(t.Context()))
			assert.Less(t, time.Since(start), 2*time.Second)

			var err error

			select {
			case err = <-done:
			case <-time.After(time.Second):
				t.Fatal("Init still runs after Close returned")
			}

			require.ErrorIs(t, err, cache.ErrClosed)
			require.EqualError(t, err, "gas/cache/valkey: init: service closed")
			assert.Nil(t, s.Client())

			_, err = s.Get(t.Context(), "k")
			require.ErrorIs(t, err, cache.ErrNotInitialized)
			assertNoSecrets(t, log.text())
		})
	}
}

// A Close that runs while another closes the client waits for it too, so
// that, when either returns, the client's goroutines have stopped. A
// command in progress, on a stalled server, holds the first Close until
// OpTimeout ends it.
//
// Not parallel: it counts the process's goroutines.
func TestClose_ConcurrentCloseWaits(t *testing.T) {
	addr := valkeyServer(t).addr
	before := runtime.NumGoroutine()

	p := newProxy(t, addr)
	cfg := config(p.addr())

	cfg.Cache.OpTimeout = time.Second

	s, err := valkey.New(valkey.WithConfig(cfg))(nil, nop)
	require.NoError(t, err)
	require.NoError(t, s.Init(t.Context()))

	p.stall()

	got := make(chan error, 1)

	go func() {
		_, err := s.Get(context.Background(), "k")
		got <- err
	}()

	// Let the Get reach the server.
	time.Sleep(100 * time.Millisecond)

	first := make(chan time.Time, 1)

	go func() {
		assert.NoError(t, s.Close(context.Background()))

		first <- time.Now()
	}()

	// Let the first Close take the client.
	time.Sleep(100 * time.Millisecond)

	start := time.Now()

	require.NoError(t, s.Close(t.Context()))

	waited := time.Since(start)
	assert.Greater(t, waited, 400*time.Millisecond, "the second Close did not wait")

	require.ErrorIs(t, <-got, cache.ErrClosed)
	<-first

	// Cut, the proxy's goroutines end too.
	p.cut()
	goroutinesSettle(t, before)
}
