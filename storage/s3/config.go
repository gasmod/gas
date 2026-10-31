package s3

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
)

const (
	// DefaultOpTimeout is the bound of each request of DefaultConfig, its
	// retries included: of each part of a PutObject, and of a GetObject
	// until its response headers arrive.
	DefaultOpTimeout = 10 * time.Second
	// DefaultMaxRetries is how many times DefaultConfig retries a failed
	// request: 2, so 3 attempts, as the AWS SDK's standard retryer makes by
	// default.
	DefaultMaxRetries = 2
	// MaxRetries is the most retries the MaxRetries setting allows.
	MaxRetries = 10
)

// Config holds the S3 store's configuration, read from the `storage`
// section.
type Config struct {
	// Storage holds the settings.
	Storage Settings
}

// Settings are the S3 store's settings, as described in the package
// documentation.
type Settings struct {
	// UsePathStyle sends the bucket in the URL's path rather than in its
	// host. Nil is true when Endpoint is set, and false otherwise.
	UsePathStyle *bool
	// Region is the AWS region; empty leaves it to the AWS SDK's default
	// chain.
	Region string
	// Endpoint is the URL requests go to, such as an emulator's; empty is
	// the region's S3 endpoint.
	Endpoint string
	// Bucket is the configured bucket, which an input with a nil Bucket
	// names; empty is none.
	Bucket string
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
	// OpTimeout bounds each request, its retries included.
	OpTimeout time.Duration `validate:"gt=0"`
	// MaxRetries is how many times a failed request is retried.
	MaxRetries int `validate:"min=0,max=10"`
}

// Secret is gas.Secret: a string that is never printed, marshaled or
// logged, such as Settings.SecretAccessKey. Configuration binds into it as
// into a string, and string(s) is the value.
type Secret = gas.Secret

// DefaultConfig returns the configuration a store starts from before
// binding: no region, no endpoint, path-style addressing as the endpoint
// decides, no configured bucket, no static credentials, DefaultOpTimeout
// and DefaultMaxRetries.
func DefaultConfig() *Config {
	return &Config{
		Storage: Settings{
			OpTimeout:  DefaultOpTimeout,
			MaxRetries: DefaultMaxRetries,
		},
	}
}

// The rule errors name the settings, never their values.
var (
	// errEndpoint reports an endpoint that is not an absolute http or
	// https URL with a host, and with no user information, query,
	// fragment, or path but `/`.
	errEndpoint = errors.New(
		"Storage.Endpoint must be an http or https URL with a host and nothing more but /",
	)
	// errBucket reports a configured bucket that breaks the bucket rules.
	errBucket = errors.New("Storage.Bucket must be a valid bucket name")
)

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

// rules checks what the validate tags cannot express, in the order the
// package documentation lists them.
func rules(cfg *Config) error {
	s := &cfg.Storage

	switch {
	case s.Endpoint != "" && !validEndpoint(s.Endpoint):
		return errEndpoint
	case s.Bucket != "" && storage.CheckBucket(s.Bucket) != nil:
		return errBucket
	}

	return nil
}

// pathStyle returns whether requests send the bucket in the URL's path:
// UsePathStyle when it is set, else whether an endpoint is.
func (s *Settings) pathStyle() bool {
	if s.UsePathStyle != nil {
		return *s.UsePathStyle
	}

	return s.Endpoint != ""
}
