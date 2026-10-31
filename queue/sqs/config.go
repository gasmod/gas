package sqs

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/gasmod/gas"
)

const (
	// DefaultOpTimeout is the bound of each request of DefaultConfig, and
	// what a Dequeue may take past its wait.
	DefaultOpTimeout = 10 * time.Second
	// DefaultVisibilityTimeout is the visibility timeout of Dequeue in
	// DefaultConfig: 0, the queue's own.
	DefaultVisibilityTimeout time.Duration = 0
	// DefaultMaxRetries is how many times DefaultConfig retries a failed
	// request: 2, so 3 attempts, as the AWS SDK's standard retryer makes
	// by default.
	DefaultMaxRetries = 2
	// MaxRetries is the most retries the MaxRetries setting allows.
	MaxRetries = 10
)

// Config holds the SQS store's configuration, read from the `queue`
// section.
type Config struct {
	// Queue holds the settings.
	Queue Settings
}

// Settings are the SQS store's settings, as described in the package
// documentation.
type Settings struct {
	// Region is the AWS region; empty leaves it to the AWS SDK's default
	// chain.
	Region string
	// Endpoint is the URL requests go to, such as an emulator's; empty is
	// the region's SQS endpoint.
	Endpoint string
	// AccessKeyID is the access key ID of static credentials, used only
	// with SecretAccessKey. It is not a secret, but an identifier that
	// maps to the AWS account ID, so the store never logs it.
	AccessKeyID string
	// SecretAccessKey is the secret access key of static credentials,
	// used only with AccessKeyID. It prints, marshals and logs as
	// `[redacted]`.
	SecretAccessKey Secret
	// SessionToken is the session token of temporary static credentials.
	// It prints, marshals and logs as `[redacted]`.
	SessionToken Secret
	// OpTimeout bounds each request, and a Dequeue past its wait.
	OpTimeout time.Duration `validate:"gt=0"`
	// VisibilityTimeout is how long a message Dequeue returns stays
	// hidden: 0 is the queue's own, else 1s to 12h.
	VisibilityTimeout time.Duration `validate:"min=0,max=12h"`
	// MaxRetries is how many times a failed request is retried.
	MaxRetries int `validate:"min=0,max=10"`
}

// Secret is gas.Secret: a string that is never printed, marshaled or
// logged, such as Settings.SecretAccessKey. Configuration binds into it as
// into a string, and string(s) is the value.
type Secret = gas.Secret

// DefaultConfig returns the configuration a store starts from before
// binding: no region, no endpoint, no static credentials,
// DefaultOpTimeout, DefaultVisibilityTimeout and DefaultMaxRetries.
func DefaultConfig() *Config {
	return &Config{
		Queue: Settings{
			OpTimeout:         DefaultOpTimeout,
			VisibilityTimeout: DefaultVisibilityTimeout,
			MaxRetries:        DefaultMaxRetries,
		},
	}
}

// The rule errors name the settings, never their values.
var (
	// errSubSecond reports a visibility timeout between 0 and a second.
	errSubSecond = errors.New("Queue.VisibilityTimeout must be 0 or 1s to 12h")
	// errEndpoint reports an endpoint that is not an absolute http or
	// https URL with a host, and with no user information, query,
	// fragment, or path but `/`.
	errEndpoint = errors.New(
		"Queue.Endpoint must be an http or https URL with a host and nothing more but /",
	)
)

// rules checks what the validate tags cannot express, in the order the
// package documentation lists them.
func rules(cfg *Config) error {
	s := &cfg.Queue

	if s.VisibilityTimeout > 0 && s.VisibilityTimeout < time.Second {
		return errSubSecond
	}

	if s.Endpoint != "" && !validEndpoint(s.Endpoint) {
		return errEndpoint
	}

	return nil
}

// validEndpoint reports whether endpoint is an absolute http or https
// URL with a host, and with no user information, query, fragment, or
// path but `/`.
func validEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)

	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" &&
		u.User == nil && onlyHost(endpoint, u)
}

// onlyHost reports whether u, parsed from endpoint, has nothing after
// its host but `/`: no path, query or fragment, even an empty one.
func onlyHost(endpoint string, u *url.URL) bool {
	return u.Opaque == "" && (u.Path == "" || u.Path == "/") &&
		!strings.ContainsAny(endpoint, "?#")
}
