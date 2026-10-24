package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gasmod/gas/cache"
	"github.com/gasmod/gas/cache/cachetest"
	cachemem "github.com/gasmod/gas/cache/memory"
	gaslog "github.com/gasmod/gas/log"
)

// #region mock
func TestProfileMissPopulatesCache(t *testing.T) {
	mock := &cachetest.MockCache{
		// Without GetFn, Get returns nil, nil: a hit with an empty value.
		GetFn: func(context.Context, string) ([]byte, error) {
			return nil, cache.ErrKeyNotFound
		},
	}
	logger, err := gaslog.NewSlogLogger()()
	if err != nil {
		t.Fatal(err)
	}

	p, err := NewProfiles(mock, logger).Profile(t.Context(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "42" {
		t.Errorf("ID = %q, want 42", p.ID)
	}
	if got := mock.CallCount("Set"); got != 1 {
		t.Fatalf("Set called %d times, want 1", got)
	}
	if ttl := mock.Calls[1].Args[2]; ttl != 5*time.Minute {
		t.Errorf("Set TTL = %v, want 5m", ttl)
	}
}

// #endregion mock

func TestProfileCacheErrorSurfaces(t *testing.T) {
	broken := errors.New("connection refused")
	mock := &cachetest.MockCache{
		GetFn: func(context.Context, string) ([]byte, error) { return nil, broken },
	}
	logger, err := gaslog.NewSlogLogger()()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := NewProfiles(mock, logger).Profile(t.Context(), "42"); !errors.Is(err, broken) {
		t.Fatalf("Profile error = %v, want the cache error", err)
	}
}

func TestMemoryBackend(t *testing.T) {
	ctx := t.Context()
	logger, err := gaslog.NewSlogLogger()()
	if err != nil {
		t.Fatal(err)
	}
	cfg := cachemem.DefaultConfig()
	cfg.Cache.MaxEntries = 1
	cfg.Cache.DefaultTTL = 20 * time.Millisecond
	svc, err := cachemem.New(cachemem.WithConfig(cfg))(nil, logger)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Get(ctx, "a"); !errors.Is(err, cache.ErrClosed) {
		t.Fatalf("Get before Init = %v, want ErrClosed", err)
	}
	if err := svc.Init(ctx); err != nil {
		t.Fatal(err)
	}

	// A TTL of 0 uses DefaultTTL.
	if err := svc.Set(ctx, "a", []byte("1"), 0); err != nil {
		t.Fatal(err)
	}
	// MaxEntries reached and nothing has expired yet: a new key is rejected.
	if err := svc.Set(ctx, "b", []byte("2"), 0); err == nil {
		t.Fatal("Set over MaxEntries succeeded")
	}
	time.Sleep(40 * time.Millisecond)
	if _, err := svc.Get(ctx, "a"); !errors.Is(err, cache.ErrKeyNotFound) {
		t.Fatalf("Get after DefaultTTL = %v, want ErrKeyNotFound", err)
	}
	// "a" was deleted by that Get, so "b" fits.
	if err := svc.Set(ctx, "b", []byte("2"), -1); err != nil {
		t.Fatal(err)
	}

	if err := svc.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, "b"); !errors.Is(err, cache.ErrClosed) {
		t.Fatalf("Delete after Close = %v, want ErrClosed", err)
	}
}
