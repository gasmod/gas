package guidescaching_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache"
	"github.com/gasmod/gas/cache/cachetest"
	cachemem "github.com/gasmod/gas/cache/memory"
	cachevk "github.com/gasmod/gas/cache/valkey"
)

// Docs: guides/caching.mdx
// "`gas/cache` implements `gas.CacheProvider` twice: in-memory for development
// and single-instance deployments, and Valkey (Redis-compatible) for anything
// running more than one process."
var (
	_ gas.CacheProvider = (*cachemem.Service)(nil)
	_ gas.CacheProvider = (*cachevk.Service)(nil)
)

// newMemory builds and starts an in-memory cache from cfg, closing it at the
// end of the test.
func newMemory(t *testing.T, cfg *cachemem.Config) *cachemem.Service {
	t.Helper()
	svc, err := cachemem.New(cachemem.WithConfig(cfg))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("memory Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

// valkeyContainer starts a Valkey container and returns it with its address.
func valkeyContainer(t *testing.T) (testcontainers.Container, string) {
	t.Helper()
	requireDocker(t)
	ctx := context.Background()

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "valkey/valkey:8",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForAll(
				wait.ForLog("Ready to accept connections"),
				wait.ForListeningPort("6379/tcp"),
			).WithDeadline(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start valkey container: %v", err)
	}
	t.Cleanup(func() {
		if err := ctr.Terminate(ctx); err != nil {
			t.Logf("terminate valkey container: %v", err)
		}
	})

	host, err := ctr.Host(ctx)
	if err != nil {
		t.Fatalf("valkey host: %v", err)
	}
	port, err := ctr.MappedPort(ctx, "6379")
	if err != nil {
		t.Fatalf("valkey port: %v", err)
	}
	return ctr, net.JoinHostPort(host, port.Port())
}

// newValkey builds and starts a Valkey cache against addr.
func newValkey(t *testing.T, addr string) *cachevk.Service {
	t.Helper()
	cfg := cachevk.DefaultConfig()
	cfg.Cache.Addr = addr
	svc, err := cachevk.New(cachevk.WithConfig(cfg))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("valkey.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("valkey Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

// backends returns one started cache per backend the page covers. It needs
// Docker for the Valkey backend.
func backends(t *testing.T) map[string]gas.CacheProvider {
	t.Helper()
	_, addr := valkeyContainer(t)
	return map[string]gas.CacheProvider{
		"memory": newMemory(t, cachemem.DefaultConfig()),
		"valkey": newValkey(t, addr),
	}
}

// Docs: guides/caching.mdx#in-memory-is-per-process
// "Two instances behind a load balancer have two separate caches, so an
// invalidation on one is invisible to the other."
func TestMemoryInstancesAreSeparate(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	a := newMemory(t, cachemem.DefaultConfig())
	b := newMemory(t, cachemem.DefaultConfig())

	for _, c := range []*cachemem.Service{a, b} {
		if err := c.Set(ctx, "k", []byte("v"), 0); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if err := a.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err := b.Get(ctx, "k")
	if err != nil || string(got) != "v" {
		t.Fatalf("other instance Get = %q, %v; want %q, nil", got, err, "v")
	}
}

func TestBackends(t *testing.T) {
	t.Parallel()
	for name, c := range backends(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			// Docs: guides/caching.mdx#cache-aside
			// "A miss is `cache.ErrKeyNotFound`, a sentinel rather than a nil
			// value, so an empty result stays distinguishable from an absent one."
			t.Run("MissIsErrKeyNotFound", func(t *testing.T) {
				got, err := c.Get(ctx, "absent-"+name)
				if !errors.Is(err, cache.ErrKeyNotFound) {
					t.Fatalf("Get missing: err = %v, want cache.ErrKeyNotFound", err)
				}
				if got != nil {
					t.Fatalf("Get missing: value = %q, want nil", got)
				}
			})
			t.Run("EmptyValueIsNotAMiss", func(t *testing.T) {
				if err := c.Set(ctx, "empty", []byte{}, 0); err != nil {
					t.Fatalf("Set empty: %v", err)
				}
				got, err := c.Get(ctx, "empty")
				if err != nil {
					t.Fatalf("Get empty: err = %v, want nil", err)
				}
				if len(got) != 0 {
					t.Fatalf("Get empty: value = %q, want empty", got)
				}
			})

			// Docs: guides/caching.mdx#invalidating
			// "a delete just costs the next reader a miss."
			t.Run("DeleteThenMiss", func(t *testing.T) {
				if err := c.Set(ctx, "del", []byte("v"), 0); err != nil {
					t.Fatalf("Set: %v", err)
				}
				if err := c.Delete(ctx, "del"); err != nil {
					t.Fatalf("Delete: %v", err)
				}
				if _, err := c.Get(ctx, "del"); !errors.Is(err, cache.ErrKeyNotFound) {
					t.Fatalf("Get after Delete: err = %v, want cache.ErrKeyNotFound", err)
				}
			})

			// Docs: guides/caching.mdx#ttls
			// "`Set` takes a TTL per entry."
			t.Run("TTLPerEntry", func(t *testing.T) {
				if err := c.Set(ctx, "short", []byte("v"), 200*time.Millisecond); err != nil {
					t.Fatalf("Set short: %v", err)
				}
				if err := c.Set(ctx, "long", []byte("v"), time.Hour); err != nil {
					t.Fatalf("Set long: %v", err)
				}
				time.Sleep(600 * time.Millisecond)
				if _, err := c.Get(ctx, "short"); !errors.Is(err, cache.ErrKeyNotFound) {
					t.Fatalf("Get short after TTL: err = %v, want cache.ErrKeyNotFound", err)
				}
				if _, err := c.Get(ctx, "long"); err != nil {
					t.Fatalf("Get long: err = %v, want nil", err)
				}
			})
		})
	}
}

// Docs: guides/caching.mdx#ttls
// "Passing zero uses `Cache.DefaultTTL`"
// Assumption: DefaultTTL exists only on the memory backend's Settings, so the
// claim is tested there.
func TestMemoryZeroTTLUsesDefaultTTL(t *testing.T) {
	t.Parallel()
	cfg := cachemem.DefaultConfig()
	cfg.Cache.DefaultTTL = 200 * time.Millisecond
	c := newMemory(t, cfg)
	ctx := t.Context()

	if err := c.Set(ctx, "k", []byte("v"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := c.Get(ctx, "k"); err != nil {
		t.Fatalf("Get before DefaultTTL: %v", err)
	}
	time.Sleep(600 * time.Millisecond)
	if _, err := c.Get(ctx, "k"); !errors.Is(err, cache.ErrKeyNotFound) {
		t.Fatalf("Get after DefaultTTL: err = %v, want cache.ErrKeyNotFound", err)
	}
}

// Docs: guides/caching.mdx#ttls
// "which is itself zero by default, meaning entries never expire."
func TestMemoryDefaultTTLZeroNeverExpires(t *testing.T) {
	t.Parallel()
	cfg := cachemem.DefaultConfig()
	if cfg.Cache.DefaultTTL != 0 {
		t.Fatalf("DefaultConfig().Cache.DefaultTTL = %v, want 0", cfg.Cache.DefaultTTL)
	}
	cfg.Cache.CleanupInterval = 50 * time.Millisecond
	c := newMemory(t, cfg)
	ctx := t.Context()
	if err := c.Set(ctx, "k", []byte("v"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if _, err := c.Get(ctx, "k"); err != nil {
		t.Fatalf("Get after cleanup cycles: err = %v, want nil", err)
	}
}

// Docs: guides/caching.mdx#config
// Memory table defaults: "`Cache.MaxEntries` | `0`", "`Cache.CleanupInterval` |
// `1m`", "`Cache.DefaultTTL` | `0`".
func TestMemoryConfigDefaults(t *testing.T) {
	t.Parallel()
	got := cachemem.DefaultConfig().Cache
	if got.MaxEntries != 0 {
		t.Errorf("MaxEntries = %d, want 0", got.MaxEntries)
	}
	if got.CleanupInterval != time.Minute {
		t.Errorf("CleanupInterval = %v, want 1m", got.CleanupInterval)
	}
	if got.DefaultTTL != 0 {
		t.Errorf("DefaultTTL = %v, want 0", got.DefaultTTL)
	}
}

// Docs: guides/caching.mdx#config
// "`Cache.MaxEntries` | `0` | Maximum entries; 0 is unlimited"
func TestMemoryMaxEntries(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("Bounded", func(t *testing.T) {
		t.Parallel()
		cfg := cachemem.DefaultConfig()
		cfg.Cache.MaxEntries = 2
		c := newMemory(t, cfg)
		for _, k := range []string{"a", "b"} {
			if err := c.Set(ctx, k, []byte("v"), 0); err != nil {
				t.Fatalf("Set %s: %v", k, err)
			}
		}
		if err := c.Set(ctx, "c", []byte("v"), 0); err == nil {
			t.Fatal("Set of a third key with MaxEntries 2: err = nil, want an error")
		}
	})

	t.Run("ZeroUnlimited", func(t *testing.T) {
		t.Parallel()
		c := newMemory(t, cachemem.DefaultConfig())
		for i := range 5000 {
			if err := c.Set(ctx, fmt.Sprintf("k%d", i), []byte("v"), 0); err != nil {
				t.Fatalf("Set k%d with MaxEntries 0: %v", i, err)
			}
		}
	})
}

// Docs: guides/caching.mdx#config
// Valkey table defaults: Addr `localhost:6379`, Password empty, DB `0`,
// DialTimeout `5s`, WriteTimeout `3s`, ConnRetries `0`, ConnRetryInterval `2s`.
func TestValkeyConfigDefaults(t *testing.T) {
	t.Parallel()
	got := cachevk.DefaultConfig().Cache
	if got.Addr != "localhost:6379" {
		t.Errorf("Addr = %q, want localhost:6379", got.Addr)
	}
	if got.Password != "" {
		t.Errorf("Password = %q, want empty", got.Password)
	}
	if got.DB != 0 {
		t.Errorf("DB = %d, want 0", got.DB)
	}
	if got.DialTimeout != 5*time.Second {
		t.Errorf("DialTimeout = %v, want 5s", got.DialTimeout)
	}
	if got.WriteTimeout != 3*time.Second {
		t.Errorf("WriteTimeout = %v, want 3s", got.WriteTimeout)
	}
	if got.ConnRetries != 0 {
		t.Errorf("ConnRetries = %d, want 0", got.ConnRetries)
	}
	if got.ConnRetryInterval != 2*time.Second {
		t.Errorf("ConnRetryInterval = %v, want 2s", got.ConnRetryInterval)
	}
}

// closedAddr returns a loopback address with nothing listening on it.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// Docs: guides/caching.mdx#config
// "`Cache.ConnRetries` | `0` | Connection retry attempts" and
// "`Cache.ConnRetryInterval` | `2s` | Base retry interval, doubling each attempt"
func TestValkeyConnRetryBackoff(t *testing.T) {
	t.Parallel()

	start := func(t *testing.T, retries int, interval time.Duration) (time.Duration, error) {
		t.Helper()
		cfg := cachevk.DefaultConfig()
		cfg.Cache.Addr = closedAddr(t)
		cfg.Cache.DialTimeout = time.Second
		cfg.Cache.ConnRetries = retries
		cfg.Cache.ConnRetryInterval = interval
		svc, err := cachevk.New(cachevk.WithConfig(cfg))(nil, &gas.NopLogger{})
		if err != nil {
			t.Fatalf("valkey.New: %v", err)
		}
		t.Cleanup(func() { _ = svc.Close(context.Background()) })
		began := time.Now()
		err = svc.Init(t.Context())
		return time.Since(began), err
	}

	t.Run("NoRetries", func(t *testing.T) {
		t.Parallel()
		elapsed, err := start(t, 0, 2*time.Second)
		if err == nil {
			t.Fatal("Init against a closed port: err = nil, want an error")
		}
		if elapsed >= 2*time.Second {
			t.Fatalf("Init with ConnRetries 0 took %v, want under one retry interval (2s)", elapsed)
		}
	})

	t.Run("Doubling", func(t *testing.T) {
		t.Parallel()
		// Two retries at a 200ms base wait 200ms then 400ms when the interval
		// doubles; a constant interval would wait 400ms in total.
		elapsed, err := start(t, 2, 200*time.Millisecond)
		if err == nil {
			t.Fatal("Init against a closed port: err = nil, want an error")
		}
		if elapsed < 600*time.Millisecond {
			t.Fatalf("Init with 2 retries at 200ms took %v, want at least 600ms (200ms + 400ms)", elapsed)
		}
	})
}

// Docs: guides/caching.mdx#health-and-readiness
// "The Valkey backend implements both."
var (
	_ gas.HealthReporter = (*cachevk.Service)(nil)
	_ gas.ReadyReporter  = (*cachevk.Service)(nil)
)

// Docs: guides/caching.mdx#health-and-readiness
// "`CheckHealth` reports only whether the service is closed, because the
// client reconnects internally and a restart would not fix a transient network
// fault. `CheckReady` issues a `PING`, so traffic drains off an instance while
// the dependency is unreachable."
func TestValkeyHealthAndReadiness(t *testing.T) {
	t.Parallel()
	ctr, addr := valkeyContainer(t)
	svc := newValkey(t, addr)
	ctx := t.Context()

	if err := svc.CheckHealth(ctx); err != nil {
		t.Fatalf("CheckHealth while running: %v, want nil", err)
	}
	if err := svc.CheckReady(ctx); err != nil {
		t.Fatalf("CheckReady while reachable: %v, want nil", err)
	}

	stopTimeout := 10 * time.Second
	if err := ctr.Stop(context.Background(), &stopTimeout); err != nil {
		t.Fatalf("stop valkey container: %v", err)
	}

	if err := svc.CheckHealth(ctx); err != nil {
		t.Fatalf("CheckHealth with server unreachable: %v, want nil", err)
	}
	readyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := svc.CheckReady(readyCtx); err == nil {
		t.Fatal("CheckReady with server unreachable: err = nil, want an error")
	}

	if err := svc.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := svc.CheckHealth(ctx); err == nil {
		t.Fatal("CheckHealth after Close: err = nil, want an error")
	}
}

// Docs: guides/caching.mdx#health-and-readiness
// "The in-memory backend implements neither on purpose"
func TestMemoryImplementsNeitherReporter(t *testing.T) {
	t.Parallel()
	var svc any = newMemory(t, cachemem.DefaultConfig())
	if _, ok := svc.(gas.HealthReporter); ok {
		t.Error("memory.Service implements gas.HealthReporter, want it not to")
	}
	if _, ok := svc.(gas.ReadyReporter); ok {
		t.Error("memory.Service implements gas.ReadyReporter, want it not to")
	}
}

// Docs: guides/caching.mdx#testing
// "`cachetest.MockCache` records calls and takes `GetFn`, `SetFn`, `DeleteFn`,
// and `ExistsFn` overrides, so cache-aside logic can be tested for both the hit
// and the miss path without a server."
func TestMockCache(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	var setKey string
	var deleted, existsAsked bool
	m := &cachetest.MockCache{
		GetFn: func(_ context.Context, key string) ([]byte, error) {
			if key == "hit" {
				return []byte("v"), nil
			}
			return nil, cache.ErrKeyNotFound
		},
		SetFn: func(_ context.Context, key string, _ []byte, _ time.Duration) error {
			setKey = key
			return nil
		},
		DeleteFn: func(context.Context, string) error {
			deleted = true
			return nil
		},
		ExistsFn: func(context.Context, string) (bool, error) {
			existsAsked = true
			return true, nil
		},
	}
	var c gas.CacheProvider = m

	if got, err := c.Get(ctx, "hit"); err != nil || string(got) != "v" {
		t.Fatalf("Get hit = %q, %v; want %q, nil", got, err, "v")
	}
	if _, err := c.Get(ctx, "miss"); !errors.Is(err, cache.ErrKeyNotFound) {
		t.Fatalf("Get miss err = %v, want cache.ErrKeyNotFound", err)
	}
	if err := c.Set(ctx, "s", nil, time.Minute); err != nil || setKey != "s" {
		t.Fatalf("Set: err = %v, SetFn key = %q; want nil, %q", err, setKey, "s")
	}
	if err := c.Delete(ctx, "d"); err != nil || !deleted {
		t.Fatalf("Delete: err = %v, DeleteFn called = %v; want nil, true", err, deleted)
	}
	if ok, err := c.Exists(ctx, "e"); err != nil || !ok || !existsAsked {
		t.Fatalf("Exists = %v, %v, ExistsFn called = %v; want true, nil, true", ok, err, existsAsked)
	}

	for method, want := range map[string]int{"Get": 2, "Set": 1, "Delete": 1, "Exists": 1} {
		if got := m.CallCount(method); got != want {
			t.Errorf("CallCount(%q) = %d, want %d", method, got, want)
		}
	}
	if got := len(m.Calls); got != 5 {
		t.Errorf("len(Calls) = %d, want 5", got)
	}
}
