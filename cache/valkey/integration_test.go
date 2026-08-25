package valkey_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gasmod/gas"
	cache "github.com/gasmod/gas/cache"
	vk "github.com/gasmod/gas/cache/valkey"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// newTestService spins up a Valkey container and returns an initialised
// *vk.Service. Container and service are cleaned up via t.Cleanup.
func newTestService(t *testing.T) *vk.Service {
	t.Helper()

	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "valkey/valkey:8",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForLog("Ready to accept connections").WithStartupTimeout(30 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start valkey container: %v", err)
	}
	t.Cleanup(func() {
		if termErr := container.Terminate(ctx); termErr != nil {
			t.Logf("terminate container: %v", termErr)
		}
	})

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("get container host: %v", err)
	}
	port, err := container.MappedPort(ctx, "6379")
	if err != nil {
		t.Fatalf("get mapped port: %v", err)
	}

	cfg := vk.DefaultConfig()
	cfg.Cache.Addr = fmt.Sprintf("%s:%s", host, port.Port())

	svc := vk.New(vk.WithConfig(cfg))(nil, &gas.NopLogger{})
	if err := svc.Init(); err != nil {
		t.Fatalf("init service: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := svc.Close(); closeErr != nil {
			t.Logf("close service: %v", closeErr)
		}
	})

	return svc
}

// ---------------------------------------------------------------------------
// Basic operations
// ---------------------------------------------------------------------------

func TestIntegration_SetAndGet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	key := "test-key"
	value := []byte("hello valkey")

	if err := svc.Set(ctx, key, value, 10*time.Second); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := svc.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(value) {
		t.Errorf("Get = %q, want %q", got, value)
	}
}

func TestIntegration_GetNotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.Get(ctx, "nonexistent")
	if !errors.Is(err, cache.ErrKeyNotFound) {
		t.Errorf("Get(nonexistent) error = %v, want %v", err, cache.ErrKeyNotFound)
	}
}

func TestIntegration_Delete(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	key := "delete-me"
	if err := svc.Set(ctx, key, []byte("value"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := svc.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := svc.Get(ctx, key)
	if !errors.Is(err, cache.ErrKeyNotFound) {
		t.Errorf("Get after Delete error = %v, want %v", err, cache.ErrKeyNotFound)
	}
}

func TestIntegration_Exists(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	key := "exists-key"

	exists, err := svc.Exists(ctx, key)
	if err != nil {
		t.Fatalf("Exists (before set): %v", err)
	}
	if exists {
		t.Error("Exists = true before Set, want false")
	}

	if err := svc.Set(ctx, key, []byte("val"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}

	exists, err = svc.Exists(ctx, key)
	if err != nil {
		t.Fatalf("Exists (after set): %v", err)
	}
	if !exists {
		t.Error("Exists = false after Set, want true")
	}
}

func TestIntegration_TTLExpiry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	key := "ttl-key"
	if err := svc.Set(ctx, key, []byte("expires"), 500*time.Millisecond); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if _, err := svc.Get(ctx, key); err != nil {
		t.Fatalf("Get immediately after Set: %v", err)
	}

	time.Sleep(700 * time.Millisecond)

	_, err := svc.Get(ctx, key)
	if !errors.Is(err, cache.ErrKeyNotFound) {
		t.Errorf("Get after TTL expiry error = %v, want %v", err, cache.ErrKeyNotFound)
	}
}

func TestIntegration_SetWithoutTTL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	key := "no-ttl"
	value := []byte("persistent")

	if err := svc.Set(ctx, key, value, 0); err != nil {
		t.Fatalf("Set(ttl=0): %v", err)
	}

	got, err := svc.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(value) {
		t.Errorf("Get = %q, want %q", got, value)
	}
}

func TestIntegration_ClosedService(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := svc.Get(ctx, "any"); !errors.Is(err, cache.ErrClosed) {
		t.Errorf("Get after Close error = %v, want %v", err, cache.ErrClosed)
	}
	if err := svc.Set(ctx, "any", []byte("v"), 0); !errors.Is(err, cache.ErrClosed) {
		t.Errorf("Set after Close error = %v, want %v", err, cache.ErrClosed)
	}
	if err := svc.Delete(ctx, "any"); !errors.Is(err, cache.ErrClosed) {
		t.Errorf("Delete after Close error = %v, want %v", err, cache.ErrClosed)
	}
	if _, err := svc.Exists(ctx, "any"); !errors.Is(err, cache.ErrClosed) {
		t.Errorf("Exists after Close error = %v, want %v", err, cache.ErrClosed)
	}
}

// ---------------------------------------------------------------------------
// Adversarial: binary and weird data
// ---------------------------------------------------------------------------

// Test that null bytes survive the round-trip. The service casts value to
// string(value) internally — this can silently truncate if something treats
// the string as C-style.
func TestIntegration_BinaryValueWithNullBytes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	value := []byte{0x00, 0x01, 0x00, 0xFF, 0x00, 0xFE}

	if err := svc.Set(ctx, "binary-null", value, 0); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := svc.Get(ctx, "binary-null")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, value) {
		t.Errorf("binary round-trip failed:\n  got  %x\n  want %x", got, value)
	}
}

// Empty value — edge case that some serialisation layers mishandle as nil/missing.
func TestIntegration_EmptyValue(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Set(ctx, "empty-val", []byte{}, 0); err != nil {
		t.Fatalf("Set empty: %v", err)
	}

	got, err := svc.Get(ctx, "empty-val")
	if err != nil {
		t.Fatalf("Get empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Get = %x (len %d), want empty", got, len(got))
	}
}

// Empty key — Redis/Valkey allows it, but does the service?
func TestIntegration_EmptyKey(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Set(ctx, "", []byte("val"), 0); err != nil {
		t.Fatalf("Set(empty key): %v", err)
	}

	got, err := svc.Get(ctx, "")
	if err != nil {
		t.Fatalf("Get(empty key): %v", err)
	}
	if string(got) != "val" {
		t.Errorf("Get(empty key) = %q, want %q", got, "val")
	}
}

// Keys containing spaces, newlines, tabs, and Redis protocol-sensitive chars.
func TestIntegration_KeysWithSpecialChars(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	keys := []string{
		"key with spaces",
		"key\twith\ttabs",
		"key\nwith\nnewlines",
		"key\r\nwith\r\ncrlf",
		"key:with:colons",
		"key{with}braces",
		"key*with*stars",
		"key?with?questions",
		"key[with]brackets",
		"日本語キー",
		"مفتاح",
		"emoji-🔑-key",
		strings.Repeat("x", 512), // long key
	}

	for i, key := range keys {
		val := []byte(fmt.Sprintf("value-%d", i))
		if err := svc.Set(ctx, key, val, 0); err != nil {
			t.Errorf("Set(%q): %v", key, err)
			continue
		}
		got, err := svc.Get(ctx, key)
		if err != nil {
			t.Errorf("Get(%q): %v", key, err)
			continue
		}
		if !bytes.Equal(got, val) {
			t.Errorf("Get(%q) = %q, want %q", key, got, val)
		}
	}
}

// A large value (1 MB) to stress the path that converts []byte → string → wire.
func TestIntegration_LargeValue(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	size := 1 << 20 // 1 MB
	value := bytes.Repeat([]byte("A"), size)

	if err := svc.Set(ctx, "large", value, 0); err != nil {
		t.Fatalf("Set 1MB value: %v", err)
	}

	got, err := svc.Get(ctx, "large")
	if err != nil {
		t.Fatalf("Get 1MB value: %v", err)
	}
	if len(got) != size {
		t.Fatalf("Get len = %d, want %d", len(got), size)
	}
	if !bytes.Equal(got, value) {
		t.Error("1MB round-trip data mismatch")
	}
}

// ---------------------------------------------------------------------------
// Adversarial: overwrite / mutation semantics
// ---------------------------------------------------------------------------

// Set the same key twice — the second write must win.
func TestIntegration_OverwriteKey(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	key := "overwrite"
	if err := svc.Set(ctx, key, []byte("first"), 0); err != nil {
		t.Fatalf("Set first: %v", err)
	}
	if err := svc.Set(ctx, key, []byte("second"), 0); err != nil {
		t.Fatalf("Set second: %v", err)
	}

	got, err := svc.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "second" {
		t.Errorf("Get = %q, want %q", got, "second")
	}
}

// Overwrite a key that had a TTL with a new value that has no TTL.
// The TTL must be cleared — the key should persist.
func TestIntegration_OverwriteClearsTTL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	key := "ttl-then-persist"
	if err := svc.Set(ctx, key, []byte("temp"), 500*time.Millisecond); err != nil {
		t.Fatalf("Set with TTL: %v", err)
	}

	// Overwrite without TTL.
	if err := svc.Set(ctx, key, []byte("permanent"), 0); err != nil {
		t.Fatalf("Set without TTL: %v", err)
	}

	time.Sleep(700 * time.Millisecond)

	got, err := svc.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after TTL window: %v (key should still exist)", err)
	}
	if string(got) != "permanent" {
		t.Errorf("Get = %q, want %q", got, "permanent")
	}
}

// Delete a key that doesn't exist — should not error.
func TestIntegration_DeleteNonexistent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Delete(ctx, "never-existed"); err != nil {
		t.Errorf("Delete(nonexistent) returned error: %v", err)
	}
}

// Delete then re-set — the key must come back.
func TestIntegration_DeleteThenReSet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	key := "phoenix"
	if err := svc.Set(ctx, key, []byte("v1"), 0); err != nil {
		t.Fatalf("Set v1: %v", err)
	}
	if err := svc.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Set(ctx, key, []byte("v2"), 0); err != nil {
		t.Fatalf("Set v2: %v", err)
	}

	got, err := svc.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "v2" {
		t.Errorf("Get = %q, want %q", got, "v2")
	}
}

// ---------------------------------------------------------------------------
// Adversarial: negative TTL
// ---------------------------------------------------------------------------

// A negative TTL is not documented. If passed to Px(), Valkey will reject it.
// The service should either error or treat it sanely.
func TestIntegration_NegativeTTL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	err := svc.Set(ctx, "neg-ttl", []byte("val"), -1*time.Second)
	// Negative TTL goes through the ttl <= 0 path (no PX), so it should
	// behave like ttl=0 (persist forever) or return an error.
	// Either outcome is acceptable — a silent data loss is not.
	if err != nil {
		return // erroring is fine
	}

	// If it didn't error, the key must be retrievable.
	got, getErr := svc.Get(ctx, "neg-ttl")
	if getErr != nil {
		t.Fatalf("Set accepted negative TTL without error but Get failed: %v", getErr)
	}
	if string(got) != "val" {
		t.Errorf("Get = %q, want %q", got, "val")
	}
}

// ---------------------------------------------------------------------------
// Adversarial: context cancellation
// ---------------------------------------------------------------------------

// Cancelled context must not hang — it should return promptly with an error.
func TestIntegration_CancelledContext(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// All operations should fail with a context error, not hang.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = svc.Get(ctx, "k")
		_ = svc.Set(ctx, "k", []byte("v"), 0)
		_ = svc.Delete(ctx, "k")
		_, _ = svc.Exists(ctx, "k")
	}()

	select {
	case <-done:
		// ok
	case <-time.After(5 * time.Second):
		t.Fatal("operations with cancelled context hung for >5s")
	}
}

// Very short deadline — should fail, not succeed with stale data.
func TestIntegration_ContextDeadlineExceeded(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	// Pre-populate a key.
	if err := svc.Set(ctx, "deadline-key", []byte("val"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Now try with an already-expired context.
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
	defer cancel()

	_, err := svc.Get(expired, "deadline-key")
	if err == nil {
		t.Error("Get with expired context should have failed")
	}
}

// ---------------------------------------------------------------------------
// Adversarial: concurrency / race conditions
// ---------------------------------------------------------------------------

// Hammer the same key from many goroutines. Run with -race.
func TestIntegration_ConcurrentSetGet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	const goroutines = 50
	const iterations = 100
	key := "race-key"

	var wg sync.WaitGroup
	errs := make(chan error, goroutines*iterations*2)

	for g := range goroutines {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range iterations {
				val := []byte(fmt.Sprintf("g%d-i%d", id, i))
				if err := svc.Set(ctx, key, val, 0); err != nil {
					errs <- fmt.Errorf("Set g%d i%d: %w", id, i, err)
					return
				}
				if _, err := svc.Get(ctx, key); err != nil {
					errs <- fmt.Errorf("Get g%d i%d: %w", id, i, err)
					return
				}
			}
		}(g)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}

// Many distinct keys in parallel — stress connection pooling.
func TestIntegration_ConcurrentDistinctKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	const n = 500
	var wg sync.WaitGroup

	errs := make(chan error, n)

	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := fmt.Sprintf("dist-%d", idx)
			val := []byte(fmt.Sprintf("val-%d", idx))

			if err := svc.Set(ctx, key, val, 10*time.Second); err != nil {
				errs <- fmt.Errorf("Set %s: %w", key, err)
				return
			}
			got, err := svc.Get(ctx, key)
			if err != nil {
				errs <- fmt.Errorf("Get %s: %w", key, err)
				return
			}
			if !bytes.Equal(got, val) {
				errs <- fmt.Errorf("Get %s = %q, want %q", key, got, val)
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}

// Concurrent close while operations are in flight.
// This must not panic — the service should return ErrClosed or a
// connection error, but never crash.
func TestIntegration_ConcurrentCloseWhileOperating(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	// Seed a key so Get has something to find (sometimes).
	_ = svc.Set(ctx, "concurrent-close", []byte("seed"), 0)

	var wg sync.WaitGroup

	// Spawn workers that keep hitting the service.
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				_, _ = svc.Get(ctx, "concurrent-close")
				_ = svc.Set(ctx, "concurrent-close", []byte("v"), time.Second)
				_, _ = svc.Exists(ctx, "concurrent-close")
				_ = svc.Delete(ctx, "concurrent-close")
			}
		}()
	}

	// Close while workers are running.
	time.Sleep(5 * time.Millisecond)
	_ = svc.Close()

	// Must not hang or panic.
	wg.Wait()
}

// Double-close must not panic.
func TestIntegration_DoubleClose(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)

	if err := svc.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	// Second close — must not panic.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("second Close panicked: %v", r)
			}
		}()
		if err := svc.Close(); err != nil {
			t.Logf("second Close returned error (acceptable): %v", err)
		}
	}()
}

// ---------------------------------------------------------------------------
// Adversarial: rapid TTL churn
// ---------------------------------------------------------------------------

// Set the same key repeatedly with very short TTLs and immediately read.
// Tests for races between expiry and read.
func TestIntegration_RapidTTLChurn(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	key := "churn"
	for i := range 200 {
		val := []byte(fmt.Sprintf("v%d", i))
		if err := svc.Set(ctx, key, val, time.Millisecond); err != nil {
			t.Fatalf("Set i=%d: %v", i, err)
		}
		// Get may succeed or return ErrKeyNotFound — both are valid.
		// Any other error is a bug.
		_, err := svc.Get(ctx, key)
		if err != nil && !errors.Is(err, cache.ErrKeyNotFound) {
			t.Fatalf("Get i=%d unexpected error: %v", i, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Adversarial: Exists consistency
// ---------------------------------------------------------------------------

// Exists must agree with Get — if Exists says true, Get must not return
// ErrKeyNotFound (within the same logical instant, no TTL involved).
func TestIntegration_ExistsConsistentWithGet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	key := "consistency"
	val := []byte("data")

	if err := svc.Set(ctx, key, val, 0); err != nil {
		t.Fatalf("Set: %v", err)
	}

	exists, err := svc.Exists(ctx, key)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("Exists = false, but key was just set")
	}

	got, err := svc.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after Exists=true: %v", err)
	}
	if !bytes.Equal(got, val) {
		t.Errorf("Get = %q, want %q", got, val)
	}

	// Now delete and verify both agree.
	if err := svc.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	exists, err = svc.Exists(ctx, key)
	if err != nil {
		t.Fatalf("Exists after delete: %v", err)
	}
	if exists {
		t.Error("Exists = true after Delete")
	}

	_, err = svc.Get(ctx, key)
	if !errors.Is(err, cache.ErrKeyNotFound) {
		t.Errorf("Get after delete error = %v, want %v", err, cache.ErrKeyNotFound)
	}
}

// ---------------------------------------------------------------------------
// Adversarial: Init edge cases
// ---------------------------------------------------------------------------

// Init with unreachable address must fail, not hang forever.
func TestIntegration_InitBadAddress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	cfg := vk.DefaultConfig()
	cfg.Cache.Addr = "127.0.0.1:1" // port 1 — almost certainly not running valkey
	cfg.Cache.DialTimeout = 1 * time.Second

	svc := vk.New(vk.WithConfig(cfg))(nil, &gas.NopLogger{})

	done := make(chan error, 1)
	go func() {
		done <- svc.Init()
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Init with bad address should have failed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Init with bad address hung for >10s")
	}
}

// ---------------------------------------------------------------------------
// Health and readiness
// ---------------------------------------------------------------------------

func TestIntegration_CheckHealth(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.CheckHealth(ctx); err != nil {
		t.Fatalf("CheckHealth on live service: %v", err)
	}
}

func TestIntegration_CheckHealthClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := svc.CheckHealth(context.Background())
	if !errors.Is(err, cache.ErrClosed) {
		t.Errorf("CheckHealth after Close = %v, want ErrClosed", err)
	}
}

func TestIntegration_CheckReady(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.CheckReady(ctx); err != nil {
		t.Fatalf("CheckReady on live service: %v", err)
	}
}

func TestIntegration_CheckReadyClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := svc.CheckReady(context.Background())
	if !errors.Is(err, cache.ErrClosed) {
		t.Errorf("CheckReady after Close = %v, want ErrClosed", err)
	}
}

func TestIntegration_CheckReadyCanceledCtx(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	svc := newTestService(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := svc.CheckReady(ctx); err == nil {
		t.Error("CheckReady with canceled context should return an error")
	}
}
