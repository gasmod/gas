package memory

import (
	"time"

	env "github.com/gasmod/gas/config/extensions/gasenv"
)

const (
	defaultCleanupInterval = 1 * time.Minute
	defaultDefaultTTL      = 0 // no expiry
	defaultMaxEntries      = 0 // unlimited
)

// Config holds in-memory cache settings.
type Config struct {
	env.WithGasEnv

	Cache Settings
}

// Settings represents the configuration for the in-memory cache.
type Settings struct {
	// MaxEntries is the maximum number of entries in the cache.
	// 0 means unlimited.
	MaxEntries int

	// CleanupInterval is how often expired entries are evicted.
	// 0 disables background cleanup.
	CleanupInterval time.Duration

	// DefaultTTL is the default time-to-live for entries when Set is
	// called with ttl == 0. A value of 0 means entries never expire.
	DefaultTTL time.Duration
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Cache: Settings{
			MaxEntries:      defaultMaxEntries,
			CleanupInterval: defaultCleanupInterval,
			DefaultTTL:      defaultDefaultTTL,
		},
	}
}

// Validate checks the Config for correctness.
func (c *Config) Validate() error {
	return nil
}
