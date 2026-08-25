package token //nolint:revive // intentional: "token" is the natural name for this domain package

import (
	"errors"
	"time"

	env "github.com/gasmod/gas/config/extensions/gasenv"
)

const (
	defaultTTL             = 15 * time.Minute
	defaultTokenLength     = 32
	defaultCleanupInterval = 1 * time.Hour
	defaultCleanupTimeout  = 30 * time.Second
)

// Config holds token service settings.
type Config struct {
	env.WithGasEnv

	Token Settings
}

// Settings represents the token configuration values.
type Settings struct {
	// DefaultTTL is the default token lifetime when Issue is called with ttl == 0.
	DefaultTTL time.Duration
	// TokenLength is the number of random bytes used for token generation.
	TokenLength int
	// CleanupInterval is the interval for the expired token cleanup goroutine.
	CleanupInterval time.Duration
	// CleanupTimeout is the timeout for the expired token cleanup goroutine.
	CleanupTimeout time.Duration
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Token: Settings{
			DefaultTTL:      defaultTTL,
			TokenLength:     defaultTokenLength,
			CleanupInterval: defaultCleanupInterval,
			CleanupTimeout:  defaultCleanupTimeout,
		},
	}
}

// Validate checks the Config for correctness.
func (c *Config) Validate() error {
	if c.Token.DefaultTTL <= 0 {
		return errors.New("token: default TTL must be positive")
	}
	if c.Token.TokenLength < 16 {
		return errors.New("token: token length must be at least 16 bytes")
	}
	if c.Token.CleanupInterval > 0 && c.Token.CleanupTimeout <= 0 {
		return errors.New("token: cleanup timeout must be positive when cleanup is enabled")
	}
	return nil
}
