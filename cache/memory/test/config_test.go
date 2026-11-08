package memory_test

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/cache"
	"github.com/gasmod/gas/cache/memory"
	"github.com/gasmod/gas/config/configtest"
)

// Config returns the zero Config before the first successful Init, and
// after a failed one.
func TestConfig_ZeroBeforeInit(t *testing.T) {
	t.Parallel()

	s, _ := build(t)
	assert.Equal(t, memory.Config{}, s.Config())

	require.ErrorIs(t, s.Init(t.Context()), cache.ErrNoConfigProvider)
	assert.Equal(t, memory.Config{}, s.Config())
}

// Config returns the configuration the last successful Init bound,
// defaults included. Close and a failed Init keep it, and a restart
// binds it afresh.
func TestConfig_Bound(t *testing.T) {
	t.Parallel()

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"cache": map[string]any{"maxentries": 5},
	})
	require.NoError(t, err)

	s, err := memory.New()(p, newRecorder())
	require.NoError(t, err)

	ctx := t.Context()
	require.NoError(t, s.Init(ctx))

	want := memory.DefaultConfig()

	want.Cache.MaxEntries = 5

	assert.Equal(t, *want, s.Config())

	require.NoError(t, s.Close(ctx))
	assert.Equal(t, *want, s.Config(), "after Close")

	require.NoError(t, p.Set("cache.cleanupinterval", "1ms"))
	require.ErrorIs(t, s.Init(ctx), cache.ErrInvalidConfig)
	assert.Equal(t, *want, s.Config(), "after a failed Init")

	require.NoError(t, p.Set("cache.cleanupinterval", "2s"))
	require.NoError(t, s.Init(ctx))
	t.Cleanup(func() { require.NoError(t, s.Close(context.Background())) })

	want.Cache.CleanupInterval = 2 * time.Second

	assert.Equal(t, *want, s.Config(), "after a restart")
}

// The Config returned shares nothing with the store: changing it does
// not change what the next call returns, and Config holds no slice, map
// or pointer that a copy would share.
func TestConfig_Copy(t *testing.T) {
	t.Parallel()

	s := initialized(t, func(*memory.Settings) {})

	c := s.Config()

	c.Cache.MaxEntries = 99

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
