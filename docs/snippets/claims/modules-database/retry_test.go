package modulesdatabase_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas/database"
)

// failingInit runs Init on a service whose connector always fails, with the
// given retry settings, and returns the connector, how long Init took, and
// Init's error.
func failingInit(t *testing.T, ctx context.Context, log *recLogger, retries int, interval time.Duration) (*fakeConnector, time.Duration, error) {
	t.Helper()
	conn := &fakeConnector{failConnect: true}
	cfg := connectorConfig()
	cfg.Database.ConnRetries = retries
	cfg.Database.ConnRetryInterval = interval
	if log == nil {
		log = &recLogger{}
	}
	svc := build(t, nil, log, database.WithConfig(cfg), database.WithConnector(conn))
	began := time.Now()
	err := svc.Init(ctx)
	return conn, time.Since(began), err
}

// Docs: modules/database.mdx#connection-retries
// "`Init` opens the connection and pings it, with a 5-second timeout per
// attempt."
func TestPingTimeoutPerAttempt(t *testing.T) {
	t.Parallel()
	conn, _, _ := failingInit(t, t.Context(), nil, 2, 50*time.Millisecond)
	_, remaining, _ := conn.record()
	if len(remaining) != 3 {
		t.Fatalf("connection attempts = %d, want 3", len(remaining))
	}
	for i, left := range remaining {
		if left <= 4*time.Second || left > 5*time.Second {
			t.Errorf("attempt %d: time left on the context deadline = %v, want just under 5s", i+1, left)
		}
	}
}

// Docs: modules/database.mdx#connection-retries
// "When the attempt fails and `connretries` is above zero, it waits
// `connretryinterval`, retries, and doubles the wait after each failure, for at
// most `connretries` retries"
// and "Each retry is logged at warn level. `Init` blocks while it waits."
// and "When every attempt fails, `Init` returns
// `gas/database: connect failed after <n> retries: <last error>`."
// and
// Docs: modules/database.mdx#configuration
// "| `database.connretries` | int | `0` | Connection retries after the first failed attempt;"
// and "| `database.connretryinterval` | duration | `2s` | Wait before the first retry, doubled after each |"
func TestRetryBackoff(t *testing.T) {
	t.Parallel()
	const interval = 100 * time.Millisecond
	log := &recLogger{}
	conn, elapsed, err := failingInit(t, t.Context(), log, 3, interval)

	at, _, _ := conn.record()
	if len(at) != 4 {
		t.Fatalf("connection attempts with connretries 3 = %d, want 4 (1 + 3 retries)", len(at))
	}
	wait := interval
	for i := 1; i < len(at); i++ {
		if gap := at[i].Sub(at[i-1]); gap < wait {
			t.Errorf("wait before retry %d = %v, want at least %v", i, gap, wait)
		}
		wait *= 2
	}
	// 100ms + 200ms + 400ms of waiting, all inside Init.
	if elapsed < 700*time.Millisecond {
		t.Errorf("Init returned after %v, want it to block for at least 700ms", elapsed)
	}
	if warns := log.at("WARN"); len(warns) != 3 {
		t.Errorf("warn-level entries = %d, want 3, one per retry; logged %q", len(warns), log.lines())
	}
	const prefix = "gas/database: connect failed after 3 retries: "
	if err == nil || !strings.HasPrefix(err.Error(), prefix) || !errors.Is(err, errConnect) {
		t.Fatalf("Init err = %v, want prefix %q wrapping the last connection error", err, prefix)
	}
}

// Docs: modules/database.mdx#connection-retries
// "an interval of zero or less uses 2 seconds."
func TestRetryIntervalFallback(t *testing.T) {
	t.Parallel()
	for name, interval := range map[string]time.Duration{"Zero": 0, "Negative": -time.Second} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			conn, _, _ := failingInit(t, t.Context(), nil, 1, interval)
			at, _, _ := conn.record()
			if len(at) != 2 {
				t.Fatalf("connection attempts = %d, want 2", len(at))
			}
			if gap := at[1].Sub(at[0]); gap < 2*time.Second || gap > 3*time.Second {
				t.Fatalf("wait before the retry with interval %v = %v, want about 2s", interval, gap)
			}
		})
	}
}

// Docs: modules/database.mdx#connection-retries
// "The ping and the waits run under the context `Init` receives, so when that
// context ends during a wait, `Init` stops and returns an error wrapping both
// the context's error and the last connection error."
func TestRetryStopsWhenContextEnds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancel)
	conn, elapsed, err := failingInit(t, ctx, nil, 5, 10*time.Second)
	if elapsed > 5*time.Second {
		t.Errorf("Init returned after %v, want it to stop when the context ends", elapsed)
	}
	if n := conn.connects.Load(); n != 1 {
		t.Errorf("connection attempts = %d, want 1", n)
	}
	if !errors.Is(err, context.Canceled) || !errors.Is(err, errConnect) {
		t.Fatalf("Init err = %v, want it to wrap context.Canceled and the connection error", err)
	}
}

// Docs: modules/database.mdx#connection-retries
// "The ping and the waits run under the context `Init` receives"
func TestPingUsesInitContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	conn := &fakeConnector{}
	err := build(t, nil, nil, database.WithConfig(connectorConfig()), database.WithConnector(conn)).Init(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Init with a canceled context = %v, want context.Canceled from the ping", err)
	}
}

// Docs: modules/database.mdx#connection-retries
// "With the default of zero retries, the first failure is returned as is."
// and
// Docs: modules/database.mdx#configuration
// "0 fails at once"
func TestNoRetriesByDefault(t *testing.T) {
	t.Parallel()
	conn := &fakeConnector{failConnect: true}
	cfg := connectorConfig()
	if cfg.Database.ConnRetries != 0 {
		t.Fatalf("precondition: default ConnRetries = %d, want 0", cfg.Database.ConnRetries)
	}
	err := build(t, nil, nil, database.WithConfig(cfg), database.WithConnector(conn)).Init(t.Context())
	if n := conn.connects.Load(); n != 1 {
		t.Errorf("connection attempts = %d, want 1", n)
	}
	if err == nil || !strings.HasPrefix(err.Error(), "gas/database: ping: ") || !errors.Is(err, errConnect) {
		t.Fatalf("Init err = %v, want the first attempt's gas/database: ping: error unchanged", err)
	}
}
