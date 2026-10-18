package valkey

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas"
	cache "github.com/gasmod/gas/cache"
)

func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()

	if cfg.Cache.Addr != defaultAddr {
		t.Errorf("Addr = %q, want %q", cfg.Cache.Addr, defaultAddr)
	}
	if cfg.Cache.DB != defaultDB {
		t.Errorf("DB = %d, want %d", cfg.Cache.DB, defaultDB)
	}
	if cfg.Cache.DialTimeout != defaultDialTimeout {
		t.Errorf("DialTimeout = %v, want %v", cfg.Cache.DialTimeout, defaultDialTimeout)
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		modify  func(*Config)
		name    string
		wantErr bool
	}{
		{
			name:    "valid defaults",
			modify:  func(_ *Config) {},
			wantErr: false,
		},
		{
			name:    "empty addr",
			modify:  func(c *Config) { c.Cache.Addr = "" },
			wantErr: true,
		},
		{
			name:    "DB negative",
			modify:  func(c *Config) { c.Cache.DB = -1 },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := DefaultConfig()
			tt.modify(cfg)

			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

// TestInit_RetryStopsWhenContextEnds checks that the connection retry loop
// stops waiting when Init's context ends, instead of sleeping out its backoff,
// and returns an error wrapping the context's error. Nothing listens on the
// address, so every connection attempt fails.
func TestInit_RetryStopsWhenContextEnds(t *testing.T) {
	t.Parallel()

	for name, canceledCase := range map[string]bool{"deadline": false, "canceled": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := DefaultConfig()
			cfg.Cache.Addr = "127.0.0.1:1"
			cfg.Cache.DialTimeout = 50 * time.Millisecond
			cfg.Cache.ConnRetries = 5
			cfg.Cache.ConnRetryInterval = time.Minute
			s, err := New(WithConfig(cfg))(nil, &gas.NopLogger{})
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

	s, err := New(WithConfig(DefaultConfig()))(nil, &gas.NopLogger{})
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

// A configuration that fails validation is returned with the service name,
// so the error is identifiable without a log line.
func TestInit_InvalidConfigNamesService(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Cache.Addr = ""
	svc, err := New(WithConfig(cfg))(nil, &gas.NopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	err = svc.Init(t.Context())
	if err == nil || !strings.HasPrefix(err.Error(), "gas/cache/valkey: invalid configuration: ") {
		t.Errorf("Init() error = %v, want it prefixed with the service name", err)
	}
}
