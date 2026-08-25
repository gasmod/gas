package s3

import (
	"errors"

	env "github.com/gasmod/gas/config/extensions/gasenv"
)

// Config holds S3 storage settings.
type Config struct {
	env.WithGasEnv

	Storage Settings
}

// Settings represents the configuration for the S3 storage service.
type Settings struct {
	// Region is the AWS region for the S3 bucket.
	Region string

	// Bucket is the S3 bucket name.
	Bucket string

	// AccessKeyID is the AWS access key.
	AccessKeyID string

	// SecretAccessKey is the AWS secret access key.
	//nolint:gosec // intentional config field
	SecretAccessKey string

	// Endpoint is an optional custom S3 endpoint for S3-compatible
	// services (MinIO, LocalStack, DigitalOcean Spaces, etc.).
	// When set, path-style addressing is enabled automatically.
	Endpoint string
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{Storage: Settings{}}
}

// Validate checks the Config for correctness. Storage.Bucket is optional;
// when unset, callers must supply gas.InBucket() per operation.
func (c *Config) Validate() error {
	if c.Storage.Region == "" {
		return errors.New("Storage.Region must not be empty")
	}
	return nil
}
