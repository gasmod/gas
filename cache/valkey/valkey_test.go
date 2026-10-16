package valkey

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gasmod/gas"
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
			s := New(WithConfig(cfg))(nil, gas.NewNopLogger()())

			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			want := context.DeadlineExceeded
			if canceledCase {
				cancel()
				want = context.Canceled
			}

			start := time.Now()
			err := s.Init(ctx)
			if !errors.Is(err, want) {
				t.Fatalf("Init error = %v, want %v", err, want)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("Init took %v, want it to stop when the context ends", elapsed)
			}
		})
	}
}
