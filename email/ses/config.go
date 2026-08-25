package ses

import (
	"errors"

	env "github.com/gasmod/gas/config/extensions/gasenv"
)

// Config holds SES email settings.
type Config struct {
	env.WithGasEnv

	Email Settings
}

// Settings represents the configuration for the SES email service.
type Settings struct {
	// Region is the AWS region for the SES service.
	Region string

	// Endpoint is an optional custom endpoint URL (e.g. for LocalStack).
	// Empty means use the default AWS endpoint.
	Endpoint string

	// FromEmail is the default sender email address.
	FromEmail string

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

// Validate checks the Config for correctness.
func (c *Config) Validate() error {
	if c.Email.Region == "" {
		return errors.New("Email.Region must not be empty")
	}
	if c.Email.FromEmail == "" {
		return errors.New("Email.FromEmail must not be empty")
	}
	return nil
}
