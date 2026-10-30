package test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas"
	cache "github.com/gasmod/gas/cache"
	vk "github.com/gasmod/gas/cache/valkey"
	"github.com/gasmod/gas/config/configtest"
)

// TestInit_RetryStopsWhenContextEnds checks that the connection retry loop
// stops waiting when Init's context ends, instead of sleeping out its backoff,
// and returns an error wrapping the context's error. Nothing listens on the
// address, so every connection attempt fails.
func TestInit_RetryStopsWhenContextEnds(t *testing.T) {
	t.Parallel()

	for name, canceledCase := range map[string]bool{"deadline": false, "canceled": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := vk.DefaultConfig()
			cfg.Cache.Addr = "127.0.0.1:1"
			cfg.Cache.DialTimeout = 50 * time.Millisecond
			cfg.Cache.ConnRetries = 5
			cfg.Cache.ConnRetryInterval = time.Minute
			s, err := vk.New(vk.WithConfig(cfg))(nil, &gas.NopLogger{})
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			want := context.DeadlineExceeded
			if canceledCase {
				cancel()
				want = context.Canceled
			}

			start := time.Now()
			err = s.Init(ctx)
			if !errors.Is(err, want) {
				t.Fatalf("Init error = %v, want %v", err, want)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("Init took %v, want it to stop when the context ends", elapsed)
			}
		})
	}
}

func TestUnstartedServiceReturnsErrClosed(t *testing.T) {
	t.Parallel()

	s, err := vk.New(vk.WithConfig(vk.DefaultConfig()))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// call turns a panic into an error, so every operation is checked
	// even when one of them panics.
	call := func(op func() error) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		return op()
	}

	ops := map[string]func() error{
		"Get": func() error {
			_, err := s.Get(ctx, "k")
			return err
		},
		"Set":    func() error { return s.Set(ctx, "k", []byte("v"), 0) },
		"Delete": func() error { return s.Delete(ctx, "k") },
		"Exists": func() error {
			_, err := s.Exists(ctx, "k")
			return err
		},
		"CheckHealth": func() error { return s.CheckHealth(ctx) },
		"CheckReady":  func() error { return s.CheckReady(ctx) },
	}
	for name, op := range ops {
		if err := call(op); !errors.Is(err, cache.ErrClosed) {
			t.Errorf("%s() before Init = %v, want %v", name, err, cache.ErrClosed)
		}
	}
}

// A configuration that fails validation when bound is returned with the
// service name, so the error is identifiable without a log line.
func TestInit_InvalidConfigNamesService(t *testing.T) {
	t.Parallel()

	provider, err := configtest.NewMockConfigWithValues(map[string]any{
		"cache": map[string]any{"addr": ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := vk.New()(provider, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	err = svc.Init(t.Context())
	if err == nil || !strings.HasPrefix(err.Error(), "gas/cache/valkey: config binding: ") {
		t.Errorf("Init() error = %v, want it prefixed with the service name", err)
	}
}
