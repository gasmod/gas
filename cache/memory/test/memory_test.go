package memory_test

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache"
	"github.com/gasmod/gas/cache/internal/conformance"
	"github.com/gasmod/gas/cache/memory"
	"github.com/gasmod/gas/config/configtest"
)

// The memory store keeps the contract of the cache package.
func TestConformance(t *testing.T) {
	t.Parallel()

	conformance.Run(t, conformance.Subject{
		Name: memory.ServiceName,
		New: func(t *testing.T, defaultTTL time.Duration) conformance.Store {
			t.Helper()

			cfg := memory.DefaultConfig()

			cfg.Cache.DefaultTTL = defaultTTL

			s, _ := build(t, memory.WithConfig(cfg))

			return s
		},
	})
}

// requireHeld checks which of keys the store holds.
func requireHeld(t *testing.T, s *memory.Service, held map[string]bool) {
	t.Helper()

	for k, want := range held {
		ok, err := s.Exists(t.Context(), k)
		require.NoError(t, err)
		assert.Equal(t, want, ok, k)
	}
}

// A new key on a full store evicts the least recently used entry. Get and
// Set make an entry the most recently used; Exists does not, and
// replacing a key never evicts.
func TestEviction_LeastRecentlyUsed(t *testing.T) {
	t.Parallel()

	s := initialized(t, func(set *memory.Settings) { set.MaxEntries = 3 })
	ctx := t.Context()
	set := func(k string) { require.NoError(t, s.Set(ctx, k, []byte(k), 0)) }

	set("a")
	set("b")
	set("c")

	_, err := s.Get(ctx, "a")
	require.NoError(t, err)

	set("d") // evicts b
	requireHeld(t, s, map[string]bool{"a": true, "b": false, "c": true, "d": true})

	set("c") // replaces, and touches c
	assert.Equal(t, 3, s.Len())

	set("e") // evicts a
	requireHeld(t, s, map[string]bool{"a": false, "c": true, "d": true, "e": true})

	set("f") // evicts d: the Exists calls above did not touch it
	requireHeld(t, s, map[string]bool{"c": true, "d": false, "e": true, "f": true})
	assert.Equal(t, 3, s.Len())
}

// On a full store an expired entry is evicted before the least recently
// used one.
func TestEviction_ExpiredFirst(t *testing.T) {
	t.Parallel()

	s := initialized(t, func(set *memory.Settings) { set.MaxEntries = 3 })
	ctx := t.Context()

	require.NoError(t, s.Set(ctx, "old", []byte("v"), 0))
	require.NoError(t, s.Set(ctx, "late", []byte("v"), 30*time.Millisecond))
	require.NoError(t, s.Set(ctx, "soon", []byte("v"), 10*time.Millisecond))

	time.Sleep(60 * time.Millisecond)

	assert.Equal(t, 3, s.Len())
	require.NoError(t, s.Set(ctx, "new", []byte("v"), 0))
	require.NoError(t, s.Set(ctx, "newer", []byte("v"), 0))
	assert.Equal(t, 3, s.Len())
	requireHeld(t, s, map[string]bool{"old": true, "new": true, "newer": true})
}

// Replacing a value moves its expiry: from one TTL to another, from a TTL
// to none and from none to a TTL.
func TestExpiry_Replaced(t *testing.T) {
	t.Parallel()

	s := initialized(t, func(*memory.Settings) {})
	ctx := t.Context()

	require.NoError(t, s.Set(ctx, "longer", []byte("v"), 20*time.Millisecond))
	require.NoError(t, s.Set(ctx, "longer", []byte("v"), time.Hour))
	require.NoError(t, s.Set(ctx, "shorter", []byte("v"), time.Hour))
	require.NoError(t, s.Set(ctx, "shorter", []byte("v"), 20*time.Millisecond))
	require.NoError(t, s.Set(ctx, "never", []byte("v"), 0))
	require.NoError(t, s.Set(ctx, "never", []byte("v"), 20*time.Millisecond))

	time.Sleep(80 * time.Millisecond)

	requireHeld(t, s, map[string]bool{"longer": true, "shorter": false, "never": false})
}

// A TTL under a millisecond is rounded up to one, so the value is there
// right after the Set.
func TestExpiry_SubMillisecondRoundsUp(t *testing.T) {
	t.Parallel()

	s := initialized(t, func(*memory.Settings) {})

	require.NoError(t, s.Set(t.Context(), "k", []byte("v"), time.Nanosecond))
	requireHeld(t, s, map[string]bool{"k": true})
}

// The janitor removes expired entries that nothing reads, each
// CleanupInterval.
func TestJanitor_RemovesExpired(t *testing.T) {
	t.Parallel()

	s := initialized(
		t,
		func(set *memory.Settings) { set.CleanupInterval = memory.MinCleanupInterval },
	)
	ctx := t.Context()

	for i := range 5 {
		require.NoError(t, s.Set(ctx, strconv.Itoa(i), []byte("v"), 10*time.Millisecond))
	}

	require.NoError(t, s.Set(ctx, "keep", []byte("v"), 0))
	assert.Equal(t, 6, s.Len())

	require.Eventually(t, func() bool { return s.Len() == 1 }, 3*time.Second, 50*time.Millisecond)
	requireHeld(t, s, map[string]bool{"keep": true})
}

// Len is 0 before Init and after Close.
func TestLen_NotOpen(t *testing.T) {
	t.Parallel()

	s, _ := build(t, memory.WithConfig(memory.DefaultConfig()))
	assert.Equal(t, 0, s.Len())

	require.NoError(t, s.Init(t.Context()))
	require.NoError(t, s.Set(t.Context(), "k", nil, 0))
	assert.Equal(t, 1, s.Len())

	require.NoError(t, s.Close(t.Context()))
	assert.Equal(t, 0, s.Len())
}

// Init after Close starts with an empty store.
func TestRestart_Empty(t *testing.T) {
	t.Parallel()

	s, _ := build(t, memory.WithConfig(memory.DefaultConfig()))
	ctx := t.Context()

	require.NoError(t, s.Init(ctx))
	require.NoError(t, s.Set(ctx, "k", []byte("v"), 0))
	require.NoError(t, s.Close(ctx))
	require.NoError(t, s.Init(ctx))

	defer func() { require.NoError(t, s.Close(ctx)) }()

	_, err := s.Get(ctx, "k")
	require.ErrorIs(t, err, cache.ErrKeyNotFound)
}

// The store binds the `cache` section from the ConfigProvider, afresh on
// each Init, and a restart while calls run is free of data races: the
// calls read the settings of the entries they hold under the lock.
func TestConfig_ProviderAndRestart(t *testing.T) {
	t.Parallel()

	p, err := configtest.NewMockConfigWithValues(map[string]any{
		"cache": map[string]any{"maxentries": 1, "defaultttl": "1h", "cleanupinterval": "2s"},
	})
	require.NoError(t, err)

	s, err := memory.New()(p, newRecorder())
	require.NoError(t, err)

	ctx := t.Context()
	require.NoError(t, s.Init(ctx))
	require.NoError(t, s.Set(ctx, "a", []byte("v"), 0))
	require.NoError(t, s.Set(ctx, "b", []byte("v"), 0))
	assert.Equal(t, 1, s.Len(), "maxentries 1")

	var wg sync.WaitGroup

	stop := make(chan struct{})

	for g := range 4 {
		wg.Go(func() {
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}

				k := strconv.Itoa(g) + ":" + strconv.Itoa(i)
				requireLifecycleOrNil(t, s.Set(ctx, k, []byte("v"), 0))

				_, err := s.Get(ctx, k)
				if !errors.Is(err, cache.ErrKeyNotFound) {
					requireLifecycleOrNil(t, err)
				}
			}
		})
	}

	for range 20 {
		require.NoError(t, s.Close(ctx))
		require.NoError(t, p.Set("cache.maxentries", 2))
		require.NoError(t, s.Init(ctx))
	}

	close(stop)
	wg.Wait()

	require.NoError(t, s.Set(ctx, "a", []byte("v"), 0))
	require.NoError(t, s.Set(ctx, "b", []byte("v"), 0))
	require.NoError(t, s.Set(ctx, "c", []byte("v"), 0))
	assert.Equal(t, 2, s.Len(), "maxentries 2 after the restart")
	require.NoError(t, s.Close(ctx))
}

// requireLifecycleOrNil checks that err, from a call racing Close and
// Init, is nil or cache.ErrClosed.
func requireLifecycleOrNil(t *testing.T, err error) {
	t.Helper()

	if err != nil && !errors.Is(err, cache.ErrClosed) {
		t.Errorf("unexpected error: %v", err)
	}
}

// Calls that race Close complete before it or return cache.ErrClosed;
// none touches the dropped entries, which -race checks.
func TestClose_RacesCalls(t *testing.T) {
	t.Parallel()

	for range 20 {
		s, _ := build(t, memory.WithConfig(memory.DefaultConfig()))
		ctx := t.Context()
		require.NoError(t, s.Init(ctx))

		var wg sync.WaitGroup

		for g := range 4 {
			wg.Go(func() {
				k := strconv.Itoa(g)
				requireLifecycleOrNil(t, s.Set(ctx, k, []byte("v"), time.Minute))

				_, err := s.Exists(ctx, k)
				requireLifecycleOrNil(t, err)
				requireLifecycleOrNil(t, s.Delete(ctx, k))
			})
		}

		require.NoError(t, s.Close(ctx))
		wg.Wait()
	}
}

// Close waits for the janitor to return: after Close no goroutine of the
// store is left.
//
// Not parallel: it counts the process's goroutines.
func TestClose_WaitsForJanitor(t *testing.T) {
	before := runtime.NumGoroutine()

	for range 10 {
		s, _ := build(t, memory.WithConfig(memory.DefaultConfig()))
		require.NoError(t, s.Init(t.Context()))
		assert.Greater(t, runtime.NumGoroutine(), before)
		require.NoError(t, s.Close(t.Context()))
		require.NoError(t, s.Close(t.Context()))
	}

	goroutinesSettle(t, before)
}

// Init with a configuration given to WithConfig checks the validate tags
// and copies it, so changing the caller's struct afterwards changes
// nothing.
func TestConfig_WithConfig(t *testing.T) {
	t.Parallel()

	for name, edit := range map[string]func(*memory.Settings){
		"maxentries":      func(s *memory.Settings) { s.MaxEntries = -1 },
		"cleanupinterval": func(s *memory.Settings) { s.CleanupInterval = 999 * time.Millisecond },
		"zerocleanup":     func(s *memory.Settings) { s.CleanupInterval = 0 },
		"defaultttl":      func(s *memory.Settings) { s.DefaultTTL = -time.Second },
	} {
		cfg := memory.DefaultConfig()
		edit(&cfg.Cache)

		err := initErr(t, memory.WithConfig(cfg))
		require.ErrorIs(t, err, cache.ErrInvalidConfig, name)
	}

	cfg := memory.DefaultConfig()

	cfg.Cache.MaxEntries = 1

	s, _ := build(t, memory.WithConfig(cfg))
	require.NoError(t, s.Init(t.Context()))

	defer func() { require.NoError(t, s.Close(context.Background())) }()

	cfg.Cache.MaxEntries = 5

	require.NoError(t, s.Set(t.Context(), "a", nil, 0))
	require.NoError(t, s.Set(t.Context(), "b", nil, 0))
	assert.Equal(t, 1, s.Len())
	assert.Equal(t, 5, cfg.Cache.MaxEntries)
}

// A bound configuration that fails its tags or does not bind is
// cache.ErrInvalidConfig.
func TestConfig_ProviderInvalid(t *testing.T) {
	t.Parallel()

	for _, values := range []map[string]any{
		{"cache": map[string]any{"maxentries": -1}},
		{"cache": map[string]any{"cleanupinterval": "10ms"}},
		{"cache": map[string]any{"defaultttl": "soon"}},
	} {
		p, err := configtest.NewMockConfigWithValues(values)
		require.NoError(t, err)

		s, err := memory.New()(p, newRecorder())
		require.NoError(t, err)

		err = s.Init(t.Context())
		require.ErrorIs(t, err, cache.ErrInvalidConfig, "%v", values)
		require.ErrorContains(t, err, "gas/cache/memory: init: invalid config: ")
	}
}

// Init and Close log their events, with the `service` field, and Close on
// a store that is not initialized logs nothing.
func TestLog(t *testing.T) {
	t.Parallel()

	cfg := memory.DefaultConfig()

	cfg.Cache.MaxEntries = 7

	s, log := build(t, memory.WithConfig(cfg))
	require.NoError(t, s.Close(t.Context()))
	assert.Empty(t, log.entries())

	require.NoError(t, s.Init(t.Context()))
	require.NoError(t, s.Close(t.Context()))

	got := log.entries()
	require.Len(t, got, 2)
	assert.Equal(t, entry{level: "info", msg: "service initialized", fields: map[string]any{
		"service":          memory.ServiceName,
		"max_entries":      7,
		"cleanup_interval": memory.DefaultCleanupInterval,
		"default_ttl":      time.Duration(0),
	}}, got[0])
	assert.Equal(t, entry{level: "info", msg: "service closed", fields: map[string]any{
		"service": memory.ServiceName,
	}}, got[1])
}

// Name returns ServiceName, and the store is a gas.Service and a
// gas.CacheProvider.
func TestName(t *testing.T) {
	t.Parallel()

	s, _ := build(t)

	var (
		_ gas.Service       = s
		_ gas.CacheProvider = s
	)

	assert.Equal(t, "gas/cache/memory", s.Name())
}
