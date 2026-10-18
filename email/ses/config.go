package ses

import (
	"github.com/gasmod/gas"
)

// Config holds SES email settings.
type Config struct {
	gas.WithGasEnv

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
