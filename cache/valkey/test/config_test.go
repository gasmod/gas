package valkey_test

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/cache"
	"github.com/gasmod/gas/cache/valkey"
	"github.com/gasmod/gas/config/configtest"
)

// Config returns the zero Config before the first successful Init, and
// after a failed one.
func TestConfig_ZeroBeforeInit(t *testing.T) {
	t.Parallel()

	s, _ := build(t)
	assert.Equal(t, valkey.Config{}, s.Config())

	require.ErrorIs(t, s.Init(t.Context()), cache.ErrNoConfigProvider)
	assert.Equal(t, valkey.Config{}, s.Config())
}

// Config returns the configuration the last successful Init bound,
// defaults included, with the password a Secret that prints as
// `[redacted]`. Close and a failed Init keep it, and a restart binds it
// afresh.
func TestConfig_Bound(t *testing.T) {
	t.Parallel()

	addr := fakeServer(t, "_\r\n")

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"cache": map[string]any{"addr": addr, "password": "zqxsecret", "keyprefix": "p:"},
	})
	require.NoError(t, err)

	s, err := valkey.New()(p, nop)
	require.NoError(t, err)

	ctx := t.Context()
	require.NoError(t, s.Init(ctx))

	want := valkey.DefaultConfig()

	want.Cache.Addr = addr
	want.Cache.Password = "zqxsecret"
	want.Cache.KeyPrefix = "p:"

	got := s.Config()
	assert.Equal(t, *want, got)
	assert.NotContains(t, fmt.Sprintf("%+v", got), "zqxsecret")
	assert.Contains(t, fmt.Sprintf("%+v", got), "[redacted]")

	require.NoError(t, s.Close(ctx))
	assert.Equal(t, *want, s.Config(), "after Close")

	require.NoError(t, p.Set("cache.db", -1))
	require.ErrorIs(t, s.Init(ctx), cache.ErrInvalidConfig)
	assert.Equal(t, *want, s.Config(), "after a failed Init")

	require.NoError(t, p.Set("cache.db", 0))
	require.NoError(t, p.Set("cache.optimeout", "2s"))
	require.NoError(t, s.Init(ctx))
	t.Cleanup(func() { require.NoError(t, s.Close(ctx)) })

	want.Cache.OpTimeout = 2 * time.Second

	assert.Equal(t, *want, s.Config(), "after a restart")
}

// The Config returned shares nothing with the store: changing it does
// not change what the next call returns, and Config holds no slice, map
// or pointer that a copy would share.
func TestConfig_Copy(t *testing.T) {
	t.Parallel()

	cfg := config(fakeServer(t, "_\r\n"))
	s := open(t, cfg)

	c := s.Config()

	c.Cache.Addr = "elsewhere:6379"
	c.Cache.Password = "changed"

	assert.Equal(t, *cfg, s.Config())
	assertNoReferences(t, reflect.TypeFor[valkey.Config](), "Config")
}

// Config may run while the store initializes and restarts, which -race
// checks: it returns the zero Config until the first Init, and the bound
// configuration from then on, Close keeping it.
func TestConfig_ConcurrentRestart(t *testing.T) {
	t.Parallel()

	cfg := config(fakeServer(t, "_\r\n"))
	s, _ := build(t, valkey.WithConfig(cfg))
	want := *cfg

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
				case got != valkey.Config{} || bound:
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
