package modulescache_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache"
	cachevk "github.com/gasmod/gas/cache/valkey"
)

// Docs: modules/cache.mdx#the-valkey-backend
// "`Init` creates the client and pings the server, bounded by
// `cache.dialtimeout`."
// Assumption: "bounded" means Init gives up within a small multiple of the
// dial timeout, both when the server never completes the connection and when
// it connects but never answers the PING.
func TestValkeyInitBoundedByDialTimeout(t *testing.T) {
	t.Parallel()
	silent, _ := blackhole(t)
	p := newProxy(t, valkeyServer(t))
	p.dropPing.Store(true)
	for name, addr := range map[string]string{"silent server": silent, "unanswered ping": p.addr()} {
		cfg := valkeyConfig(addr)
		cfg.Cache.DialTimeout = 300 * time.Millisecond
		cfg.Cache.WriteTimeout = 30 * time.Second
		s := buildValkey(t, cfg, nil)
		begin := time.Now()
		err := s.Init(context.Background())
		if err == nil {
			t.Errorf("%s: Init succeeded", name)
			continue
		}
		if took := time.Since(begin); took > 2*time.Second {
			t.Errorf("%s: Init took %v with a 300ms dial timeout (error %v)", name, took, err)
		}
	}
}

// Docs: modules/cache.mdx#the-valkey-backend
// "When that fails and `cache.connretries` is above zero, it
// waits `cache.connretryinterval`, retries, and doubles the wait after each
// failure, for at most `connretries` retries ... Each retry is logged at warn level. ... When every retry fails, `Init` returns
// `gas/cache/valkey: connect failed after <n> retries: <last error>`."
func TestValkeyRetries(t *testing.T) {
	t.Parallel()
	sink, logger := newLogger(t)
	cfg := valkeyConfig(closedAddr(t))
	cfg.Cache.ConnRetries = 3
	cfg.Cache.ConnRetryInterval = 100 * time.Millisecond
	s := buildValkey(t, cfg, logger)
	begin := time.Now()
	err := s.Init(context.Background())
	took := time.Since(begin)
	if err == nil || !strings.HasPrefix(err.Error(), "gas/cache/valkey: connect failed after 3 retries: ") {
		t.Fatalf("Init = %v, want gas/cache/valkey: connect failed after 3 retries: ...", err)
	}
	// 100ms + 200ms + 400ms of waiting; without doubling it would be 300ms.
	if took < 700*time.Millisecond || took > 3*time.Second {
		t.Errorf("Init took %v, want about 700ms of doubling waits", took)
	}
	if n := len(sink.find(t, "WARN", "")); n != 3 {
		t.Errorf("warn records = %d, want one per retry (3)", n)
	}
}

// Docs: modules/cache.mdx#configuration
// "| `cache.connretries` | int | `0` | Connection retries after the first failed attempt; 0 fails at once |"
func TestValkeyNoRetriesFailsAtOnce(t *testing.T) {
	t.Parallel()
	sink, logger := newLogger(t)
	s := buildValkey(t, valkeyConfig(closedAddr(t)), logger)
	begin := time.Now()
	if err := s.Init(context.Background()); err == nil {
		t.Fatal("Init against a closed port succeeded")
	}
	if took := time.Since(begin); took > time.Second {
		t.Errorf("Init with 0 retries took %v, want an immediate failure", took)
	}
	if n := len(sink.find(t, "WARN", "")); n != 0 {
		t.Errorf("warn records = %d, want 0", n)
	}
}

// Docs: modules/cache.mdx#the-valkey-backend
// "an interval of zero or less uses
// 2 seconds."
func TestValkeyNonPositiveIntervalIsTwoSeconds(t *testing.T) {
	t.Parallel()
	for _, interval := range []time.Duration{0, -time.Second} {
		t.Run(interval.String(), func(t *testing.T) {
			t.Parallel()
			cfg := valkeyConfig(closedAddr(t))
			cfg.Cache.ConnRetries = 1
			cfg.Cache.ConnRetryInterval = interval
			s := buildValkey(t, cfg, nil)
			begin := time.Now()
			_ = s.Init(context.Background())
			if took := time.Since(begin); took < 2*time.Second || took > 4*time.Second {
				t.Errorf("one retry with interval %v took %v, want about 2s", interval, took)
			}
		})
	}
}

// Docs: modules/cache.mdx#the-valkey-backend
// "The ping and the waits run
// under the context `Init` receives, so when that context ends during a wait,
// `Init` stops with an error wrapping both the context's error and the last
// connection error."
func TestValkeyInitContext(t *testing.T) {
	t.Parallel()
	t.Run("wait", func(t *testing.T) {
		t.Parallel()
		cfg := valkeyConfig(closedAddr(t))
		cfg.Cache.ConnRetries = 5
		cfg.Cache.ConnRetryInterval = 10 * time.Second
		s := buildValkey(t, cfg, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		begin := time.Now()
		err := s.Init(ctx)
		if took := time.Since(begin); took > 2*time.Second {
			t.Errorf("Init took %v after its context ended at 300ms", took)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Init = %v, want it to wrap context.DeadlineExceeded", err)
		}
		if err == nil || !strings.Contains(err.Error(), "connection refused") {
			t.Errorf("Init = %v, want it to carry the last connection error", err)
		}
	})
	t.Run("ping", func(t *testing.T) {
		t.Parallel()
		p := newProxy(t, valkeyServer(t))
		p.dropPing.Store(true)
		cfg := valkeyConfig(p.addr())
		cfg.Cache.DialTimeout = 30 * time.Second
		cfg.Cache.WriteTimeout = 30 * time.Second
		s := buildValkey(t, cfg, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		begin := time.Now()
		err := s.Init(ctx)
		if err == nil {
			t.Fatal("Init with an unanswered PING succeeded")
		}
		if took := time.Since(begin); took > 3*time.Second {
			t.Errorf("Init took %v; the ping should end with Init's 300ms context", took)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Init = %v, want the ping to fail with Init's context error", err)
		}
	})
}

// Docs: modules/cache.mdx#the-valkey-backend
// "The context does not cover what comes before the `PING`:
// valkey-go's `NewClient` takes no context, so the dial and handshake it runs
// ignore it. Against a server that accepts the connection and never answers,
// `Init` runs until `cache.dialtimeout` ends the attempt, however soon the
// context ends (KI-052"
func TestValkeyInitContextSkipsHandshake(t *testing.T) {
	t.Parallel()
	addr, _ := blackhole(t)
	cfg := valkeyConfig(addr)
	cfg.Cache.DialTimeout = 2 * time.Second
	cfg.Cache.WriteTimeout = 30 * time.Second
	s := buildValkey(t, cfg, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	begin := time.Now()
	if err := s.Init(ctx); err == nil {
		t.Fatal("Init against a silent server succeeded")
	}
	if took := time.Since(begin); took < 1800*time.Millisecond || took > 5*time.Second {
		t.Errorf("Init took %v with a 100ms context and a 2s dial timeout, want about 2s", took)
	}
}

// Docs: modules/cache.mdx#the-valkey-backend
// "For commands `gas.CacheProvider` does not cover, `Client()` on
// `*valkey.Service` returns the underlying `valkey.Client`. It returns nil
// before `Init` first connects, and the closed client after `Close`."
func TestValkeyClient(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s := buildValkey(t, valkeyConfig(valkeyServer(t)), nil)
	if s.Client() != nil {
		t.Fatal("Client() before Init is not nil")
	}
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	c := s.Client()
	n, err := c.Do(ctx, c.B().Incr().Key(key(t, "hits")).Build()).AsInt64()
	if err != nil || n != 1 {
		t.Fatalf("INCR through Client() = %d, %v; want 1", n, err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	after := s.Client()
	if after != c {
		t.Fatal("Client() after Close is not the client Init created")
	}
	if err := after.Do(ctx, after.B().Ping().Build()).Error(); !errors.Is(err, valkey.ErrClosing) {
		t.Errorf("PING on Client() after Close = %v, want valkey.ErrClosing", err)
	}
}

// Docs: modules/cache.mdx#errors
// "On Valkey also an operation that was running when the service closed, or when a restart replaced the client"
// Assumption: "a restart replaced the client" includes Init running again
// while the service is open, which is the only way to replace the client
// without closing first.
func TestValkeyInFlightClosed(t *testing.T) {
	t.Parallel()
	addr := valkeyServer(t)
	for name, interrupt := range map[string]func(*cachevk.Service) error{
		"close":   func(s *cachevk.Service) error { return s.Close(context.Background()) },
		"restart": func(s *cachevk.Service) error { return s.Init(context.Background()) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newProxy(t, addr)
			s := buildValkey(t, valkeyConfig(p.addr()), nil)
			if err := s.Init(t.Context()); err != nil {
				t.Fatal(err)
			}
			p.stall()
			done := make(chan error, 1)
			go func() { _, err := s.Get(context.Background(), "k"); done <- err }()
			time.Sleep(200 * time.Millisecond)
			select {
			case err := <-done:
				t.Fatalf("Get returned %v before the interruption; the proxy did not stall it", err)
			default:
			}
			p.isolate()
			if err := interrupt(s); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			select {
			case err := <-done:
				if err != cache.ErrClosed { //nolint:errorlint // unwrapped is the claim
					t.Errorf("in-flight Get after %s = %v, want cache.ErrClosed", name, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("in-flight Get did not return after %s", name)
			}
		})
	}
}

// Docs: modules/cache.mdx#errors
// "A Valkey command error also names the command and the
// key, as in `gas/cache/valkey: get "profile:42": <cause>`, and wraps the
// valkey-go error."
func TestValkeyCommandError(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s := newValkey(t, valkeyServer(t))
	c := s.Client()
	if err := c.Do(ctx, c.B().Lpush().Key("profile:42").Element("x").Build()).Error(); err != nil {
		t.Fatal(err)
	}
	_, err := s.Get(ctx, "profile:42")
	if err == nil || !strings.HasPrefix(err.Error(), `gas/cache/valkey: get "profile:42": `) {
		t.Fatalf("Get of a list = %v, want gas/cache/valkey: get \"profile:42\": ...", err)
	}
	var vkErr *valkey.ValkeyError
	if !errors.As(err, &vkErr) {
		t.Errorf("Get error %v does not wrap a *valkey.ValkeyError", err)
	}
}

// Docs: modules/cache.mdx#health-and-readiness
// "The Valkey backend implements `gas.HealthReporter` and `gas.ReadyReporter`.
// `CheckHealth` fails, with `cache.ErrClosed`, only before `Init` or after
// `Close`: the client reconnects on its own, so a network fault is not a reason
// to restart the process. `CheckReady` sends `PING` with the caller's context,
// so while the server is unreachable the instance reports not ready"
func TestValkeyHealthAndReady(t *testing.T) {
	t.Parallel()
	var _ gas.HealthReporter = (*cachevk.Service)(nil)
	var _ gas.ReadyReporter = (*cachevk.Service)(nil)
	ctx := t.Context()
	p := newProxy(t, valkeyServer(t))
	s := buildValkey(t, valkeyConfig(p.addr()), nil)
	if err := s.CheckHealth(ctx); err != cache.ErrClosed { //nolint:errorlint // the page names the error
		t.Errorf("CheckHealth before Init = %v, want cache.ErrClosed", err)
	}
	if err := s.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckHealth(ctx); err != nil {
		t.Errorf("CheckHealth while connected = %v", err)
	}
	if err := s.CheckReady(ctx); err != nil {
		t.Errorf("CheckReady while connected = %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.CheckReady(canceled); err == nil {
		t.Error("CheckReady with a canceled context = nil, want the PING to fail")
	}

	p.cut()
	readyCtx, cancelReady := context.WithTimeout(ctx, 2*time.Second)
	defer cancelReady()
	if err := s.CheckReady(readyCtx); err == nil {
		t.Error("CheckReady with the server unreachable = nil, want an error")
	}
	if err := s.CheckHealth(ctx); err != nil {
		t.Errorf("CheckHealth with the server unreachable = %v, want nil", err)
	}

	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckHealth(ctx); err != cache.ErrClosed { //nolint:errorlint // the page names the error
		t.Errorf("CheckHealth after Close = %v, want cache.ErrClosed", err)
	}
}
