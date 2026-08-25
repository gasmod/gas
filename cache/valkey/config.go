package valkey

import (
	"errors"
	"time"

	env "github.com/gasmod/gas/config/extensions/gasenv"
)

const (
	defaultAddr              = "localhost:6379"
	defaultDB                = 0
	defaultDialTimeout       = 5 * time.Second
	defaultWriteTimeout      = 3 * time.Second
	defaultConnRetries       = 0
	defaultConnRetryInterval = 2 * time.Second
)

// Config holds Valkey cache settings.
type Config struct {
	env.WithGasEnv

	Cache Settings
}

// Settings represents the configuration for the Valkey cache.
type Settings struct {
	// Addr is the Valkey server address in host:port format.
	Addr string

	// Password is the authentication password. Empty means no auth.
	//nolint:gosec // intentional
	Password string

	// DB is the database number to use (0-15).
	DB int

	// DialTimeout is the timeout for establishing new connections.
	DialTimeout time.Duration

	// WriteTimeout is applied to net.Conn.SetDeadline and controls
	// the maximum duration for write operations and periodic PINGs.
	WriteTimeout time.Duration

	// ConnRetries is the number of times to retry connecting on failure.
	// 0 means no retries (fail immediately).
	ConnRetries int

	// ConnRetryInterval is the base interval between connection retry
	// attempts. The interval doubles after each failed attempt.
	ConnRetryInterval time.Duration
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Cache: Settings{
			Addr:              defaultAddr,
			DB:                defaultDB,
			DialTimeout:       defaultDialTimeout,
			WriteTimeout:      defaultWriteTimeout,
			ConnRetries:       defaultConnRetries,
			ConnRetryInterval: defaultConnRetryInterval,
		},
	}
}

// Validate checks the Config for correctness.
func (c *Config) Validate() error {
	if c.Cache.Addr == "" {
		return errors.New("Cache.Addr must not be empty")
	}
	if c.Cache.DB < 0 {
		return errors.New("Cache.DB must be >= 0")
	}
	return nil
}
