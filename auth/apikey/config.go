package apikey

import (
	"time"

	"github.com/gasmod/gas/auth/internal/svcutil"
)

const (
	// DefaultHeaderName is the default HTTP header the key is read from.
	DefaultHeaderName = "X-API-Key"
	// DefaultKeyLength is the default number of random bytes in a key.
	DefaultKeyLength = 32
	// DefaultCleanupTimeout is the default bound on one run of the
	// expired-key cleanup.
	DefaultCleanupTimeout = 30 * time.Second
)

// Config is the API key service's configuration, as described in the
// package documentation. It binds from the `apikey` section.
type Config struct {
	// APIKey holds the service's settings.
	APIKey Settings
}

// Settings are the API key service's settings, as described in the
// package documentation.
type Settings struct {
	// HeaderName is the HTTP header the key is read from. It is required.
	HeaderName string `validate:"required"`
	// Prefix is prepended to every generated key, such as `gas_`. It may
	// be empty.
	Prefix string
	// KeyLength is the number of random bytes in a generated key, at
	// least 16.
	KeyLength int `validate:"min=16"`
	// CleanupInterval is the time between runs of the expired-key
	// cleanup. Zero turns the cleanup off; a negative value is an error.
	CleanupInterval time.Duration
	// CleanupTimeout bounds one run of the cleanup. It must be positive
	// when CleanupInterval is.
	CleanupTimeout time.Duration
}

// DefaultConfig returns a new Config holding the defaults: the header
// DefaultHeaderName, no prefix, DefaultKeyLength random bytes, the
// cleanup off, and DefaultCleanupTimeout.
func DefaultConfig() *Config {
	return &Config{
		APIKey: Settings{
			HeaderName:     DefaultHeaderName,
			KeyLength:      DefaultKeyLength,
			CleanupTimeout: DefaultCleanupTimeout,
		},
	}
}

// checkRules checks what the validate tags cannot express: the cleanup
// settings, as svcutil.CheckCleanup does.
func checkRules(cfg *Config) error {
	//nolint:wrapcheck // svcutil.ConfigSource.Resolve wraps it with ErrInvalidConfig.
	return svcutil.CheckCleanup(cfg.APIKey.CleanupInterval, cfg.APIKey.CleanupTimeout)
}
