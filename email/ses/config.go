package ses

import (
	"sync"

	"github.com/go-playground/validator/v10"

	env "github.com/gasmod/gas/config/extensions/gasenv"
)

// Config holds SES email settings.
type Config struct {
	env.WithGasEnv

	Email Settings
}

// Settings represents the configuration for the SES email service.
type Settings struct {
	// Region is the AWS region for the SES service. Required.
	Region string `validate:"required"`

	// Endpoint is an optional custom endpoint URL (e.g. for LocalStack).
	// Empty means use the default AWS endpoint.
	Endpoint string

	// FromEmail is the default sender email address. Required.
	FromEmail string `validate:"required"`

	// AccessKeyID is the AWS access key ID for static credentials.
	// If empty, the default AWS credential chain is used.
	AccessKeyID string

	// SecretAccessKey is the AWS secret access key for static credentials.
	// If empty, the default AWS credential chain is used.
	SecretAccessKey string
}

// DefaultConfig returns a Config with zero-value defaults.
func DefaultConfig() *Config {
	return &Config{}
}

// validateConfig checks cfg against its validate tags. Init calls it for a
// config that did not come through ConfigProvider.Bind, which already runs
// the same check.
func validateConfig(cfg *Config) error {
	return configValidator().Struct(cfg) //nolint:wrapcheck // Init adds the context
}

// configValidator is built on first use and shared, since a
// *validator.Validate caches each struct's parsed tags.
var configValidator = sync.OnceValue(func() *validator.Validate { return validator.New() })
