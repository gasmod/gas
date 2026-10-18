package token //nolint:revive // intentional: "token" is the natural name for this domain package

import (
	"errors"
	"time"

	"github.com/gasmod/gas"
)

const (
	defaultTTL             = 15 * time.Minute
	defaultTokenLength     = 32
	defaultCleanupInterval = 1 * time.Hour
	defaultCleanupTimeout  = 30 * time.Second
)

// Config holds token service settings.
type Config struct {
	gas.WithGasEnv

	Token Settings
}

// Settings represents the token configuration values.
type Settings struct {
	// DefaultTTL is the default token lifetime when Issue is called with ttl == 0.
	DefaultTTL time.Duration `validate:"gt=0s"`
	// TokenLength is the number of random bytes used for token generation.
	TokenLength int `validate:"min=16"`
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

// checkRules checks that background cleanup, when enabled, has a positive
// timeout, which a validate tag cannot express.
func checkRules(cfg *Config) error {
	if cfg.Token.CleanupInterval > 0 && cfg.Token.CleanupTimeout <= 0 {
		return errors.New("token: cleanup timeout must be positive when cleanup is enabled")
	}
	return nil
}
