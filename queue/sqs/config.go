package sqs

import (
	"time"

	"github.com/gasmod/gas"
)

const (
	defaultRegion            = "us-east-1"
	defaultVisibilityTimeout = 30 * time.Second
	defaultWaitTimeSeconds   = 20
)

// Config holds SQS queue settings.
type Config struct {
	gas.WithGasEnv

	Queue Settings
}

// Settings represents the configuration for the SQS queue service.
type Settings struct {
	// Region is the AWS region for the SQS service. Required.
	Region string `validate:"required"`

	// Endpoint is an optional custom endpoint URL (e.g. for LocalStack).
	// Empty means use the default AWS endpoint.
	Endpoint string

	// AccessKeyID is the AWS access key ID for static credentials.
	// If empty, the default AWS credential chain is used.
	AccessKeyID string

	// SecretAccessKey is the AWS secret access key for static credentials.
	// If empty, the default AWS credential chain is used.
	SecretAccessKey string

	// VisibilityTimeout is how long a dequeued message stays invisible
	// to other consumers. SQS max is 12 hours. Must not be negative.
	VisibilityTimeout time.Duration `validate:"gte=0s"`

	// WaitTimeSeconds is the long-poll duration for ReceiveMessage calls.
	// Must be 0-20 (SQS hard limit).
	WaitTimeSeconds int `validate:"min=0,max=20"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Queue: Settings{
			Region:            defaultRegion,
			VisibilityTimeout: defaultVisibilityTimeout,
			WaitTimeSeconds:   defaultWaitTimeSeconds,
		},
	}
}
