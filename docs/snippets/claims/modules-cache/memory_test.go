package modulescache_test

import (
	"bytes"
	"context"
	"errors"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache"
	cachemem "github.com/gasmod/gas/cache/memory"
)

// cleanupGoroutines counts running memory cleanup goroutines.
func cleanupGoroutines() int {
	var buf bytes.Buffer
	_ = pprof.Lookup("goroutine").WriteTo(&buf, 2)
	return strings.Count(buf.String(), "cache/memory.(*Service).cleanup(")
}

// waitGoroutines waits up to a second for the count to reach want.
func waitGoroutines(want int) int {
	got := cleanupGoroutines()
	for deadline := time.Now().Add(time.Second); got != want && time.Now().Before(deadline); got = cleanupGoroutines() {
		time.Sleep(10 * time.Millisecond)
	}
	return got
}

// Docs: modules/cache.mdx#the-in-memory-backend
// "Expired entries are invisible to `Get` and `Exists`. ... the rest are removed by a background goroutine
// every `cache.cleanupinterval`, which `Init` starts when the interval is
// positive and `Close` stops."
// Not parallel: it counts goroutines process-wide, so it runs before the
// parallel tests start.
func TestMemoryCleanupGoroutine(t *testing.T) {
	ctx := context.Background()
	if n := cleanupGoroutines(); n != 0 {
		t.Fatalf("cleanup goroutines before the test = %d, want 0", n)
	}

	// Bound from config: only maxentries is set, so cleanupinterval keeps its
	// 1m default and the goroutine starts.
	bound, _ := cachemem.New()(mustConfig(t, map[string]any{"cache": map[string]any{"maxentries": 10}}), &gas.NopLogger{})
	if err := bound.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if n := waitGoroutines(1); n != 1 {
		t.Errorf("after Init with the default interval: %d cleanup goroutines, want 1", n)
	}
	_ = bound.Close(ctx)
	if n := waitGoroutines(0); n != 0 {
		t.Errorf("after Close: %d cleanup goroutines, want 0", n)
	}

	for _, interval := range []time.Duration{0, -time.Second} {
		cfg := cachemem.DefaultConfig()
		cfg.Cache.CleanupInterval = interval
		s, _ := cachemem.New(cachemem.WithConfig(cfg))(nil, &gas.NopLogger{})
		if err := s.Init(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
		if n := cleanupGoroutines(); n != 0 {
			t.Errorf("interval %v: %d cleanup goroutines, want 0", interval, n)
		}
		_ = s.Close(ctx)
	}

	// Expired entries are invisible while still stored.
	cfg := cachemem.DefaultConfig()
	cfg.Cache.CleanupInterval = 0
	s := newMemory(t, cfg)
	if err := s.Set(ctx, "k", []byte("v"), 30*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)
	if ok, err := s.Exists(ctx, "k"); ok || err != nil {
		t.Errorf("Exists expired = %v, %v; want false, nil", ok, err)
	}
	if _, err := s.Get(ctx, "k"); !errors.Is(err, cache.ErrKeyNotFound) {
		t.Errorf("Get expired = %v, want ErrKeyNotFound", err)
	}
}

// Docs: modules/cache.mdx#the-in-memory-backend
// "When `cache.maxentries` is reached, a `Set` of a new key first removes one
// expired entry, picked in map iteration order rather than by age. When none
// has expired, the `Set` fails with
// `gas/cache/memory: max entries (<n>) reached`. Overwriting an existing key
// always succeeds. There is no LRU eviction."
func TestMemoryMaxEntries(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	cfg := cachemem.DefaultConfig()
	cfg.Cache.MaxEntries = 2
	cfg.Cache.CleanupInterval = 0
	s := newMemory(t, cfg)

	if err := s.Set(ctx, "a", []byte("1"), 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "b", []byte("2"), 0); err != nil {
		t.Fatal(err)
	}
	// "a" is the most recently used; LRU would evict "b". Nothing is evicted.
	_, _ = s.Get(ctx, "a")
	err := s.Set(ctx, "c", []byte("3"), 0)
	if err == nil || err.Error() != "gas/cache/memory: max entries (2) reached" {
		t.Errorf("Set over the limit = %v, want gas/cache/memory: max entries (2) reached", err)
	}
	for _, k := range []string{"a", "b"} {
		if _, err := s.Get(ctx, k); err != nil {
			t.Errorf("Get %s after a rejected Set = %v, want it kept", k, err)
		}
	}
	if err := s.Set(ctx, "a", []byte("1b"), 0); err != nil {
		t.Errorf("overwrite at the limit = %v, want nil", err)
	}

	// With one entry expired, a new key takes its place.
	if err := s.Set(ctx, "b", []byte("2"), 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if err := s.Set(ctx, "c", []byte("3"), 0); err != nil {
		t.Errorf("Set with an expired entry to evict = %v, want nil", err)
	}
	if got, err := s.Get(ctx, "a"); err != nil || string(got) != "1b" {
		t.Errorf("Get a = %q, %v; the live entry must survive", got, err)
	}
}

// Docs: modules/cache.mdx#the-in-memory-backend
// "Values are stored and returned as the same slice, not copies. Modifying a
// slice you passed to `Set` or got from `Get` changes the cached value."
func TestMemorySharesSlices(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s := newMemory(t, cachemem.DefaultConfig())
	in := []byte("abc")
	if err := s.Set(ctx, "k", in, 0); err != nil {
		t.Fatal(err)
	}
	in[0] = 'X'
	got, _ := s.Get(ctx, "k")
	if string(got) != "Xbc" {
		t.Fatalf("Get after modifying the Set slice = %q, want Xbc", got)
	}
	got[1] = 'Y'
	if again, _ := s.Get(ctx, "k"); string(again) != "XYc" {
		t.Fatalf("Get after modifying the Get slice = %q, want XYc", again)
	}
}

// Docs: modules/cache.mdx#the-in-memory-backend
// "`Close` drops every entry. A service started again begins with an empty
// cache."
func TestMemoryCloseDropsEntries(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](mustConfig(t, nil)),
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithSingletonService[*cachemem.Service](cachemem.New()),
	)
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Shutdown(context.Background()) }()
	s, err := app.ServiceContainer().Resolve[*cachemem.Service]()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "k", []byte("v"), 0); err != nil {
		t.Fatal(err)
	}
	if err := app.CloseService[*cachemem.Service](ctx); err != nil {
		t.Fatal(err)
	}
	if err := app.RestartService[*cachemem.Service](ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "k"); !errors.Is(err, cache.ErrKeyNotFound) {
		t.Fatalf("Get after restart = %v, want ErrKeyNotFound (empty cache)", err)
	}
}

// Docs: modules/cache.mdx#health-and-readiness
// "The in-memory backend implements neither."
func TestMemoryHasNoReporters(t *testing.T) {
	t.Parallel()
	var s any = &cachemem.Service{}
	if _, ok := s.(gas.HealthReporter); ok {
		t.Error("*memory.Service implements gas.HealthReporter")
	}
	if _, ok := s.(gas.ReadyReporter); ok {
		t.Error("*memory.Service implements gas.ReadyReporter")
	}
}
