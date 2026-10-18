package apikey

import (
	"github.com/gasmod/gas/auth/internal/cfgvalidate"
	env "github.com/gasmod/gas/config/extensions/gasenv"
)

const (
	defaultHeaderName = "X-API-Key"
	defaultKeyLength  = 32
)

// Config holds API key service settings.
type Config struct {
	env.WithGasEnv

	APIKey Settings
}

// Settings represents the API key configuration values.
type Settings struct {
	// HeaderName is the HTTP header used to pass the API key.
	HeaderName string
	// Prefix is prepended to generated keys for identification.
	Prefix string
	// KeyLength is the number of random bytes used for key generation.
	KeyLength int `validate:"min=16"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		APIKey: Settings{
			HeaderName: defaultHeaderName,
			KeyLength:  defaultKeyLength,
		},
	}
}

// validateConfig checks cfg against its validate tags. Init calls it for a
// config that did not come through ConfigProvider.Bind, which already runs
// the same check.
func validateConfig(cfg *Config) error {
	return cfgvalidate.Struct(cfg) //nolint:wrapcheck // Init adds the context
}
