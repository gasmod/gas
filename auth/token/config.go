package token

import (
	"time"

	"github.com/gasmod/gas/auth/internal/svcutil"
)

const (
	// DefaultTTL is the default of Settings.DefaultTTL.
	DefaultTTL = 15 * time.Minute
	// DefaultTokenLength is the default of Settings.TokenLength.
	DefaultTokenLength = 32
	// DefaultCleanupInterval is the default of Settings.CleanupInterval.
	DefaultCleanupInterval = time.Hour
	// DefaultCleanupTimeout is the default of Settings.CleanupTimeout.
	DefaultCleanupTimeout = 30 * time.Second
)

// Config is the token service's configuration, bound from the `token`
// section, as described in the package documentation.
type Config struct {
	// Token holds the settings.
	Token Settings
}

// Settings are the token service's settings.
type Settings struct {
	// DefaultTTL is how long a token lives when Issue is given a ttl of 0.
	// It must be positive.
	//
	// Default: DefaultTTL.
	DefaultTTL time.Duration `validate:"gt=0s"`
	// TokenLength is the number of random bytes in a token, at least 16.
	//
	// Default: DefaultTokenLength.
	TokenLength int `validate:"min=16"`
	// CleanupInterval is the time between deletions of expired tokens. An
	// interval of 0 turns the cleanup off; a negative one is an error.
	//
	// Default: DefaultCleanupInterval.
	CleanupInterval time.Duration
	// CleanupTimeout bounds each deletion of expired tokens. It must be
	// positive when CleanupInterval is.
	//
	// Default: DefaultCleanupTimeout.
	CleanupTimeout time.Duration
}

// DefaultConfig returns a new Config holding the defaults.
func DefaultConfig() *Config {
	return &Config{
		Token: Settings{
			DefaultTTL:      DefaultTTL,
			TokenLength:     DefaultTokenLength,
			CleanupInterval: DefaultCleanupInterval,
			CleanupTimeout:  DefaultCleanupTimeout,
		},
	}
}

// checkRules checks what a validate tag cannot express: the cleanup
// interval is not negative, and the cleanup, when it is on, has a
// positive timeout.
func checkRules(cfg *Config) error {
	//nolint:wrapcheck // svcutil.ConfigSource.Resolve wraps it with ErrInvalidConfig.
	return svcutil.CheckCleanup(cfg.Token.CleanupInterval, cfg.Token.CleanupTimeout)
}
