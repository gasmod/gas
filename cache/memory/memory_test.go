package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gasmod/gas"
	cache "github.com/gasmod/gas/cache"
)

func newTestService(t *testing.T, opts ...Option) *Service {
	t.Helper()

	cfg := DefaultConfig()
	cfg.Cache.CleanupInterval = 0 // disable background cleanup in tests

	svc := New(WithConfig(cfg))(nil, gas.NewNopLogger()())
	if err := svc.Init(); err != nil {
		t.Fatalf("Init() failed: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func TestSetAndGet(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Set(ctx, "key1", []byte("value1"), 5*time.Minute); err != nil {
		t.Fatalf("Set() error: %v", err)
	}

	got, err := svc.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if string(got) != "value1" {
		t.Errorf("Get() = %q, want %q", got, "value1")
	}
}

func TestGetKeyNotFound(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	_, err := svc.Get(ctx, "nonexistent")
	if !errors.Is(err, cache.ErrKeyNotFound) {
		t.Errorf("Get() error = %v, want %v", err, cache.ErrKeyNotFound)
	}
}

func TestGetExpired(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Set(ctx, "key1", []byte("value1"), 1*time.Millisecond); err != nil {
		t.Fatalf("Set() error: %v", err)
	}

	time.Sleep(5 * time.Millisecond)

	_, err := svc.Get(ctx, "key1")
	if !errors.Is(err, cache.ErrKeyNotFound) {
		t.Errorf("Get() error = %v, want %v", err, cache.ErrKeyNotFound)
	}
}

func TestSetNoExpiry(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Set(ctx, "key1", []byte("value1"), 0); err != nil {
		t.Fatalf("Set() error: %v", err)
	}

	got, err := svc.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if string(got) != "value1" {
		t.Errorf("Get() = %q, want %q", got, "value1")
	}
}

func TestDelete(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	_ = svc.Set(ctx, "key1", []byte("value1"), 5*time.Minute)

	if err := svc.Delete(ctx, "key1"); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}

	_, err := svc.Get(ctx, "key1")
	if !errors.Is(err, cache.ErrKeyNotFound) {
		t.Errorf("Get() after Delete() error = %v, want %v", err, cache.ErrKeyNotFound)
	}
}

func TestDeleteNonexistent(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Delete(ctx, "nonexistent"); err != nil {
		t.Errorf("Delete() error = %v, want nil", err)
	}
}

func TestExists(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	_ = svc.Set(ctx, "key1", []byte("value1"), 5*time.Minute)

	ok, err := svc.Exists(ctx, "key1")
	if err != nil {
		t.Fatalf("Exists() error: %v", err)
	}
	if !ok {
		t.Error("Exists() = false, want true")
	}

	ok, err = svc.Exists(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("Exists() error: %v", err)
	}
	if ok {
		t.Error("Exists() = true, want false")
	}
}

func TestExistsExpired(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	_ = svc.Set(ctx, "key1", []byte("value1"), 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	ok, err := svc.Exists(ctx, "key1")
	if err != nil {
		t.Fatalf("Exists() error: %v", err)
	}
	if ok {
		t.Error("Exists() = true for expired key, want false")
	}
}

func TestMaxEntries(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Cache.MaxEntries = 2
	cfg.Cache.CleanupInterval = 0

	svc := New(WithConfig(cfg))(nil, gas.NewNopLogger()())
	if err := svc.Init(); err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	ctx := context.Background()
	_ = svc.Set(ctx, "k1", []byte("v1"), 5*time.Minute)
	_ = svc.Set(ctx, "k2", []byte("v2"), 5*time.Minute)

	err := svc.Set(ctx, "k3", []byte("v3"), 5*time.Minute)
	if err == nil {
		t.Error("Set() should return error when max entries reached")
	}

	// overwriting existing key should succeed
	if err := svc.Set(ctx, "k1", []byte("v1-new"), 5*time.Minute); err != nil {
		t.Errorf("Set() existing key error: %v", err)
	}
}

func TestClosedServiceReturnsError(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	_ = svc.Close()

	if _, err := svc.Get(ctx, "k"); !errors.Is(err, cache.ErrClosed) {
		t.Errorf("Get() on closed = %v, want %v", err, cache.ErrClosed)
	}
	if err := svc.Set(ctx, "k", []byte("v"), 0); !errors.Is(err, cache.ErrClosed) {
		t.Errorf("Set() on closed = %v, want %v", err, cache.ErrClosed)
	}
	if err := svc.Delete(ctx, "k"); !errors.Is(err, cache.ErrClosed) {
		t.Errorf("Delete() on closed = %v, want %v", err, cache.ErrClosed)
	}
	if _, err := svc.Exists(ctx, "k"); !errors.Is(err, cache.ErrClosed) {
		t.Errorf("Exists() on closed = %v, want %v", err, cache.ErrClosed)
	}
}

func TestCleanup(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Cache.CleanupInterval = 10 * time.Millisecond

	svc := New(WithConfig(cfg))(nil, gas.NewNopLogger()())
	if err := svc.Init(); err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	ctx := context.Background()
	_ = svc.Set(ctx, "key1", []byte("v1"), 1*time.Millisecond)

	time.Sleep(50 * time.Millisecond)

	svc.mu.RLock()
	_, exists := svc.items["key1"]
	svc.mu.RUnlock()

	if exists {
		t.Error("expired entry should have been cleaned up")
	}
}

func TestDefaultTTL(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Cache.DefaultTTL = 1 * time.Millisecond
	cfg.Cache.CleanupInterval = 0

	svc := New(WithConfig(cfg))(nil, gas.NewNopLogger()())
	if err := svc.Init(); err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	ctx := context.Background()
	_ = svc.Set(ctx, "key1", []byte("v1"), 0) // uses DefaultTTL

	time.Sleep(5 * time.Millisecond)

	_, err := svc.Get(ctx, "key1")
	if !errors.Is(err, cache.ErrKeyNotFound) {
		t.Errorf("Get() error = %v, want %v (DefaultTTL should have expired)", err, cache.ErrKeyNotFound)
	}
}
