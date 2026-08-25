package database_test

import (
	"testing"
	"time"

	database "github.com/gasmod/gas/database"
)

// validConfig returns a Config that passes Validate, as a base for mutation.
func validConfig() *database.Config {
	cfg := database.DefaultConfig()
	cfg.Database.DSN = "postgres://user:pass@localhost:5432/db"
	return cfg
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*database.Config)
		wantErr bool
	}{
		{
			name:   "defaults with DSN",
			mutate: func(*database.Config) {},
		},
		{
			name:   "pgx mode",
			mutate: func(c *database.Config) { c.Database.Mode = database.ModePgx },
		},
		{
			name:    "unknown mode",
			mutate:  func(c *database.Config) { c.Database.Mode = "mongo" },
			wantErr: true,
		},
		{
			name:    "empty mode",
			mutate:  func(c *database.Config) { c.Database.Mode = "" },
			wantErr: true,
		},
		{
			name:    "unknown driver in sql mode",
			mutate:  func(c *database.Config) { c.Database.Driver = "mysql" },
			wantErr: true,
		},
		{
			// The driver is a database/sql concept, so it is not validated
			// in pgx mode.
			name: "unknown driver ignored in pgx mode",
			mutate: func(c *database.Config) {
				c.Database.Mode = database.ModePgx
				c.Database.Driver = "mysql"
			},
		},
		{
			name:    "empty DSN",
			mutate:  func(c *database.Config) { c.Database.DSN = "" },
			wantErr: true,
		},
		{
			name:    "MaxOpenConns below minimum",
			mutate:  func(c *database.Config) { c.Database.MaxOpenConns = 0 },
			wantErr: true,
		},
		{
			name:    "MaxOpenConns above maximum",
			mutate:  func(c *database.Config) { c.Database.MaxOpenConns = 1001 },
			wantErr: true,
		},
		{
			name:    "MaxIdleConns below minimum",
			mutate:  func(c *database.Config) { c.Database.MaxIdleConns = 0 },
			wantErr: true,
		},
		{
			name: "MaxIdleConns above maximum",
			mutate: func(c *database.Config) {
				c.Database.MaxOpenConns = 1000
				c.Database.MaxIdleConns = 1001
			},
			wantErr: true,
		},
		{
			name: "MaxIdleConns exceeds MaxOpenConns",
			mutate: func(c *database.Config) {
				c.Database.MaxOpenConns = 5
				c.Database.MaxIdleConns = 10
			},
			wantErr: true,
		},
		{
			// pgx manages idle connections itself, so the idle bounds are
			// not enforced in pgx mode.
			name: "MaxIdleConns unchecked in pgx mode",
			mutate: func(c *database.Config) {
				c.Database.Mode = database.ModePgx
				c.Database.MaxIdleConns = 0
			},
		},
		{
			name:    "ConnMaxLifetime below minimum",
			mutate:  func(c *database.Config) { c.Database.ConnMaxLifetime = time.Millisecond },
			wantErr: true,
		},
		{
			name:    "ConnMaxLifetime above maximum",
			mutate:  func(c *database.Config) { c.Database.ConnMaxLifetime = 3 * time.Hour },
			wantErr: true,
		},
		{
			name: "ConnMaxIdleTime below minimum",
			mutate: func(c *database.Config) {
				c.Database.ConnMaxIdleTime = time.Millisecond
			},
			wantErr: true,
		},
		{
			name: "ConnMaxIdleTime above maximum",
			mutate: func(c *database.Config) {
				c.Database.ConnMaxLifetime = 2 * time.Hour
				c.Database.ConnMaxIdleTime = 90 * time.Minute
			},
			wantErr: true,
		},
		{
			name: "ConnMaxIdleTime exceeds ConnMaxLifetime",
			mutate: func(c *database.Config) {
				c.Database.ConnMaxLifetime = 1 * time.Minute
				c.Database.ConnMaxIdleTime = 5 * time.Minute
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(cfg)

			err := cfg.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected config to validate, got: %v", err)
			}
		})
	}
}

func TestDefaultConfig_Values(t *testing.T) {
	cfg := database.DefaultConfig()

	if cfg.Database.Mode != database.ModeSQL {
		t.Errorf("Mode = %q, want %q", cfg.Database.Mode, database.ModeSQL)
	}
	if cfg.Database.Driver != database.DriverPostgres {
		t.Errorf("Driver = %q, want %q", cfg.Database.Driver, database.DriverPostgres)
	}
	if cfg.Database.MaxOpenConns != 25 {
		t.Errorf("MaxOpenConns = %d, want 25", cfg.Database.MaxOpenConns)
	}
	if cfg.Database.MaxIdleConns != 5 {
		t.Errorf("MaxIdleConns = %d, want 5", cfg.Database.MaxIdleConns)
	}
	if cfg.Database.ConnMaxLifetime != 30*time.Minute {
		t.Errorf("ConnMaxLifetime = %s, want 30m", cfg.Database.ConnMaxLifetime)
	}
	if cfg.Database.ConnMaxIdleTime != 5*time.Minute {
		t.Errorf("ConnMaxIdleTime = %s, want 5m", cfg.Database.ConnMaxIdleTime)
	}
	if cfg.Database.ConnRetries != 0 {
		t.Errorf("ConnRetries = %d, want 0", cfg.Database.ConnRetries)
	}
	if cfg.Database.ConnRetryInterval != 2*time.Second {
		t.Errorf("ConnRetryInterval = %s, want 2s", cfg.Database.ConnRetryInterval)
	}

	// DefaultConfig has no DSN, so it does not validate on its own.
	if err := cfg.Validate(); err == nil {
		t.Error("expected DefaultConfig to fail validation without a DSN")
	}
}
