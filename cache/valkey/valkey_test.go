package valkey

import (
	"testing"
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
		name    string
		modify  func(*Config)
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
