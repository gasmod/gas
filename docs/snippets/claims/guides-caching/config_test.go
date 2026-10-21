package guidescaching_test

import (
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache"
	cachemem "github.com/gasmod/gas/cache/memory"
	cachevk "github.com/gasmod/gas/cache/valkey"
)

// memoryGoroutines counts the live goroutines whose stack runs code in the
// gas/cache/memory package.
func memoryGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "github.com/gasmod/gas/cache/memory.") {
			count++
		}
	}
	return count
}

// waitMemoryGoroutines polls until memoryGoroutines returns want or the
// deadline passes, and returns the last count.
func waitMemoryGoroutines(want int, d time.Duration) int {
	deadline := time.Now().Add(d)
	for {
		got := memoryGoroutines()
		if got == want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Docs: guides/caching.mdx#config
// "| `Cache.CleanupInterval` | `1m` | How often expired entries are evicted; 0 disables |"
//
// Expired entries are already invisible to Get and Exists, so eviction itself
// cannot be observed through the exported API. The test observes the
// background sweep instead: a goroutine running gas/cache/memory code exists
// after Init with a positive interval, until Close, and never with 0.
// Assumption: "0 disables" means no background goroutine is started.
//
// Not parallel: it counts goroutines process-wide, and parallel tests only
// start after the sequential ones finish, so no other memory cache is live.
func TestMemoryCleanupIntervalSweep(t *testing.T) {
	if got := memoryGoroutines(); got != 0 {
		t.Fatalf("precondition: %d gas/cache/memory goroutines before the test, want 0", got)
	}

	t.Run("ZeroStartsNoSweep", func(t *testing.T) {
		cfg := cachemem.DefaultConfig()
		cfg.Cache.CleanupInterval = 0
		svc, err := cachemem.New(cachemem.WithConfig(cfg))(nil, &gas.NopLogger{})
		if err != nil {
			t.Fatalf("memory.New: %v", err)
		}
		if err := svc.Init(t.Context()); err != nil {
			t.Fatalf("Init: %v", err)
		}
		defer func() { _ = svc.Close(context.Background()) }()
		time.Sleep(100 * time.Millisecond)
		if got := memoryGoroutines(); got != 0 {
			t.Fatalf("CleanupInterval 0: %d gas/cache/memory goroutines after Init, want 0", got)
		}
	})

	t.Run("PositiveRunsUntilClose", func(t *testing.T) {
		cfg := cachemem.DefaultConfig()
		cfg.Cache.CleanupInterval = 20 * time.Millisecond
		svc, err := cachemem.New(cachemem.WithConfig(cfg))(nil, &gas.NopLogger{})
		if err != nil {
			t.Fatalf("memory.New: %v", err)
		}
		if err := svc.Init(t.Context()); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if got := waitMemoryGoroutines(1, 2*time.Second); got != 1 {
			_ = svc.Close(context.Background())
			t.Fatalf("CleanupInterval 20ms: %d gas/cache/memory goroutines after Init, want 1", got)
		}
		if err := svc.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if got := waitMemoryGoroutines(0, 2*time.Second); got != 0 {
			t.Fatalf("after Close: %d gas/cache/memory goroutines, want 0", got)
		}
	})
}

// newValkeyDB builds and starts a Valkey cache against addr on database db.
func newValkeyDB(t *testing.T, addr string, db int) *cachevk.Service {
	t.Helper()
	cfg := cachevk.DefaultConfig()
	cfg.Cache.Addr = addr
	cfg.Cache.DB = db
	svc, err := cachevk.New(cachevk.WithConfig(cfg))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatalf("valkey.New: %v", err)
	}
	if err := svc.Init(t.Context()); err != nil {
		t.Fatalf("valkey Init on DB %d: %v", db, err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc
}

// Docs: guides/caching.mdx#config
// "| `Cache.DB` | `0` | Database number |"
func TestValkeyDBNumber(t *testing.T) {
	t.Parallel()
	_, addr := valkeyContainer(t)
	ctx := t.Context()

	writer := newValkeyDB(t, addr, 3)
	if err := writer.Set(ctx, "db-claim", []byte("in-3"), 0); err != nil {
		t.Fatalf("Set on DB 3: %v", err)
	}

	same := newValkeyDB(t, addr, 3)
	got, err := same.Get(ctx, "db-claim")
	if err != nil || string(got) != "in-3" {
		t.Fatalf("second client on DB 3: Get = %q, %v; want %q, nil", got, err, "in-3")
	}

	other := newValkeyDB(t, addr, 0)
	if _, err := other.Get(ctx, "db-claim"); !errors.Is(err, cache.ErrKeyNotFound) {
		t.Fatalf("client on DB 0: Get err = %v, want cache.ErrKeyNotFound (key lives in DB 3)", err)
	}
}

// silentServer accepts TCP connections and reads from them without ever
// answering, so a client connects but its handshake and ping never complete.
func silentServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var conns sync.WaitGroup
	t.Cleanup(func() {
		_ = l.Close()
		conns.Wait()
	})
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			go func() {
				defer conns.Done()
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(io.Discard, c)
			}()
		}
	}()
	return l.Addr().String()
}

// Docs: guides/caching.mdx#config
// "| `Cache.DialTimeout` | `5s` | Timeout for new connections, and the bound on the startup ping |"
//
// WriteTimeout is set to 30s in both cases so that only DialTimeout can end
// Init early.
func TestValkeyDialTimeout(t *testing.T) {
	t.Parallel()
	const dial = 500 * time.Millisecond
	const bound = 3 * time.Second

	start := func(t *testing.T, addr string) (time.Duration, error) {
		t.Helper()
		cfg := cachevk.DefaultConfig()
		cfg.Cache.Addr = addr
		cfg.Cache.DialTimeout = dial
		cfg.Cache.WriteTimeout = 30 * time.Second
		cfg.Cache.ConnRetries = 0
		svc, err := cachevk.New(cachevk.WithConfig(cfg))(nil, &gas.NopLogger{})
		if err != nil {
			t.Fatalf("valkey.New: %v", err)
		}
		t.Cleanup(func() { _ = svc.Close(context.Background()) })
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		began := time.Now()
		err = svc.Init(ctx)
		return time.Since(began), err
	}

	// An unroutable address: the TCP connect itself never completes.
	t.Run("NewConnection", func(t *testing.T) {
		t.Parallel()
		elapsed, err := start(t, "10.255.255.1:6379")
		if err == nil {
			t.Fatal("Init against an unroutable address: err = nil, want an error")
		}
		if elapsed > bound {
			t.Fatalf("Init with DialTimeout %v took %v, want at most %v", dial, elapsed, bound)
		}
	})

	// A server that accepts the connection but never answers: the connect
	// succeeds and the startup ping hangs.
	t.Run("StartupPing", func(t *testing.T) {
		t.Parallel()
		elapsed, err := start(t, silentServer(t))
		if err == nil {
			t.Fatal("Init against a server that never answers: err = nil, want an error")
		}
		if elapsed > bound {
			t.Fatalf("Init with DialTimeout %v took %v, want at most %v", dial, elapsed, bound)
		}
	})
}

// Proxy modes for freezeProxy.
const (
	forwarding int32 = iota
	// silent keeps reading from both sides and drops everything, so writes
	// succeed but no reply ever arrives.
	silent
	// stalled stops reading from both sides, so the peer's socket buffers
	// fill and its writes block.
	stalled
)

// freezeProxy forwards TCP traffic to target until its mode changes, and
// keeps every connection open in every mode.
type freezeProxy struct {
	addr string
	mode atomic.Int32
}

func newFreezeProxy(t *testing.T, target string) *freezeProxy {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	p := &freezeProxy{addr: l.Addr().String()}
	stop := make(chan struct{})
	var open sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		close(stop)
		_ = l.Close()
		open.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		open.Unlock()
	})
	pipe := func(dst, src net.Conn) {
		buf := make([]byte, 32<<10)
		for {
			for p.mode.Load() == stalled {
				select {
				case <-stop:
					return
				case <-time.After(10 * time.Millisecond):
				}
			}
			n, err := src.Read(buf)
			if n > 0 && p.mode.Load() == forwarding {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", target)
			if err != nil {
				_ = c.Close()
				continue
			}
			open.Lock()
			conns = append(conns, c, up)
			open.Unlock()
			go pipe(up, c)
			go pipe(c, up)
		}
	}()
	return p
}

// Docs: guides/caching.mdx#config
// "| `Cache.WriteTimeout` | `3s` | Passed to the valkey-go client as `ConnWriteTimeout`, which valkey-go uses as its write deadline |"
//
// That the value reaches valkey-go as ConnWriteTimeout is not observable
// through the exported API, so the test observes its effect. Assumption: with
// the server stalled mid-connection, a write with no deadline of its own (a
// 64 MiB value against a peer that stops reading) fails within three times
// WriteTimeout plus a second.
func TestValkeyWriteTimeout(t *testing.T) {
	t.Parallel()
	const write = time.Second
	// Kept under the default DialTimeout (5s) so the bound cannot be met by
	// some other 5s limit. The slack over WriteTimeout absorbs scheduling
	// under -race with the whole suite running in parallel.
	const bound = 3*write + time.Second

	for _, tc := range []struct {
		name string
		mode int32
		op   func(svc *cachevk.Service) error
	}{
		{"Write", stalled, func(svc *cachevk.Service) error {
			return svc.Set(context.Background(), "big", make([]byte, 64<<20), 0)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, addr := valkeyContainer(t)
			proxy := newFreezeProxy(t, addr)

			cfg := cachevk.DefaultConfig()
			cfg.Cache.Addr = proxy.addr
			cfg.Cache.WriteTimeout = write
			svc, err := cachevk.New(cachevk.WithConfig(cfg))(nil, &gas.NopLogger{})
			if err != nil {
				t.Fatalf("valkey.New: %v", err)
			}
			if err := svc.Init(t.Context()); err != nil {
				t.Fatalf("Init through the proxy: %v", err)
			}
			t.Cleanup(func() { _ = svc.Close(context.Background()) })
			if err := svc.Set(t.Context(), "wt", []byte("v"), 0); err != nil {
				t.Fatalf("Set before stalling: %v", err)
			}

			proxy.mode.Store(tc.mode)
			// The operation carries no deadline of its own; the test gives up
			// waiting after 30s so a hang fails instead of stalling the run.
			done := make(chan error, 1)
			began := time.Now()
			go func() { done <- tc.op(svc) }()
			select {
			case err = <-done:
			case <-time.After(30 * time.Second):
				t.Fatalf("operation against the stalled server still blocked after 30s, want an error within %v with WriteTimeout %v", bound, write)
			}
			elapsed := time.Since(began)
			if err == nil {
				t.Fatal("operation against the stalled server: err = nil, want an error")
			}
			if elapsed > bound {
				t.Fatalf("operation against the stalled server took %v (err %v), want at most %v with WriteTimeout %v", elapsed, err, bound, write)
			}
		})
	}
}
