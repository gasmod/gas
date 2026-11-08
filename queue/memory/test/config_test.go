package memory_test

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config/configtest"
	"github.com/gasmod/gas/queue"
	"github.com/gasmod/gas/queue/memory"
)

// Config returns the zero Config before the first successful Init, and
// after a failed one.
func TestConfig_ZeroBeforeInit(t *testing.T) {
	t.Parallel()

	s, _ := build(t)
	assert.Equal(t, memory.Config{}, s.Config())

	require.ErrorIs(t, s.Init(t.Context()), queue.ErrNoConfigProvider)
	assert.Equal(t, memory.Config{}, s.Config())
}

// Config returns the configuration the last successful Init bound,
// defaults included, with the visibility timeout as bound, before Init
// rounds it up. Close and a failed Init keep it, and a restart binds it
// afresh.
func TestConfig_Bound(t *testing.T) {
	t.Parallel()

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"queue": map[string]any{"maxmessages": 5, "visibilitytimeout": "1500ms"},
	})
	require.NoError(t, err)

	s, err := memory.New()(p, newRecorder())
	require.NoError(t, err)

	ctx := t.Context()
	require.NoError(t, s.Init(ctx))

	want := memory.DefaultConfig()

	want.Queue.MaxMessages = 5
	want.Queue.VisibilityTimeout = 1500 * time.Millisecond

	assert.Equal(t, *want, s.Config())

	require.NoError(t, s.Close(ctx))
	assert.Equal(t, *want, s.Config(), "after Close")

	require.NoError(t, p.Set("queue.maxmessages", 0))
	require.ErrorIs(t, s.Init(ctx), queue.ErrInvalidConfig)
	assert.Equal(t, *want, s.Config(), "after a failed Init")

	require.NoError(t, p.Set("queue.maxmessages", 6))
	require.NoError(t, s.Init(ctx))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	want.Queue.MaxMessages = 6

	assert.Equal(t, *want, s.Config(), "after a restart")
}

// The Config returned shares nothing with the store: changing it does
// not change what the next call returns, and Config holds no slice, map
// or pointer that a copy would share.
func TestConfig_Copy(t *testing.T) {
	t.Parallel()

	s := initialized(t, nil)

	c := s.Config()

	c.Queue.MaxMessages = 99

	assert.Equal(t, *memory.DefaultConfig(), s.Config())
	assertNoReferences(t, reflect.TypeFor[memory.Config](), "Config")
}

// Config may run while the store initializes and restarts, which -race
// checks: it returns the zero Config until the first Init, and the bound
// configuration from then on, Close keeping it.
func TestConfig_ConcurrentRestart(t *testing.T) {
	t.Parallel()

	s, _ := build(t, memory.WithConfig(memory.DefaultConfig()))
	want := *memory.DefaultConfig()

	var wg sync.WaitGroup

	stop := make(chan struct{})

	for range 4 {
		wg.Go(func() {
			bound := false

			for {
				select {
				case <-stop:
					return
				default:
				}

				switch got := s.Config(); {
				case got == want:
					bound = true
				case got != memory.Config{} || bound:
					t.Errorf("config: %+v", got)

					return
				}
			}
		})
	}

	for range 20 {
		require.NoError(t, s.Init(t.Context()))
		require.NoError(t, s.Close(t.Context()))
	}

	close(stop)
	wg.Wait()
	assert.Equal(t, want, s.Config())
}
