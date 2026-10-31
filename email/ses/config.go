package ses

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/gasmod/gas/email"
)

const (
	// DefaultOpTimeout is the bound of each send of DefaultConfig, its
	// retries included.
	DefaultOpTimeout = 10 * time.Second
	// DefaultMaxRetries is how many times DefaultConfig retries a failed
	// send: 2, so 3 attempts, as the AWS SDK's standard retryer makes by
	// default.
	DefaultMaxRetries = 2
	// MaxRetries is the most retries the MaxRetries setting allows.
	MaxRetries = 10
)

// Config holds the SES sender's configuration, read from the `email`
// section.
type Config struct {
	// Email holds the settings.
	Email Settings
}

// Settings are the SES sender's settings, as described in the package
// documentation.
type Settings struct {
	// Region is the AWS region; empty leaves it to the AWS SDK's default
	// chain.
	Region string
	// Endpoint is the URL requests go to, such as an emulator's; empty is
	// the region's SES endpoint.
	Endpoint string
	// FromEmail is the sender of a message whose From is empty; empty is
	// none.
	FromEmail string
	// AccessKeyID is the access key ID of static credentials, used only
	// with SecretAccessKey. It is not a secret, but an identifier that
	// maps to the AWS account ID, so the sender never logs it.
	AccessKeyID string
	// SecretAccessKey is the secret access key of static credentials,
	// used only with AccessKeyID. It prints, marshals and logs as
	// `[redacted]`.
	SecretAccessKey Secret
	// SessionToken is the session token of temporary static credentials.
	// It prints, marshals and logs as `[redacted]`.
	SessionToken Secret
	// OpTimeout bounds each send, its retries included.
	OpTimeout time.Duration `validate:"gt=0"`
	// MaxRetries is how many times a failed send is retried.
	MaxRetries int `validate:"min=0,max=10"`
}

// redacted is what a Secret prints, marshals and logs as.
const redacted = "[redacted]"

// Secret is a string that is never printed, marshaled or logged, such as
// Settings.SecretAccessKey: String returns `[redacted]`, and Format writes
// it for every verb, so `%v`, `%+v` and `%#v` of a Settings or a Config
// hide it; MarshalText returns it, so encoding/json and YAML encoders
// that use encoding.TextMarshaler write it; and LogValue returns it, so
// log/slog logs it. Configuration binds into it as into a string, and
// string(s) is the value. encoding/gob ignores MarshalText and encodes
// the value, so a Settings or a Config must never be gob-encoded.
type Secret string

var (
	_ fmt.Formatter  = Secret("")
	_ slog.LogValuer = Secret("")
)

// String returns `[redacted]`, never the value.
func (Secret) String() string { return redacted }

// Format writes `[redacted]`, never the value, whatever the verb.
func (Secret) Format(f fmt.State, _ rune) {
	// fmt's State writes to fmt's own buffer, which does not fail.
	_, _ = io.WriteString(f, redacted)
}

// MarshalText returns `[redacted]`, never the value.
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// LogValue returns `[redacted]`, never the value.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// DefaultConfig returns the configuration a sender starts from before
// binding: no region, no endpoint, no default sender, no static
// credentials, DefaultOpTimeout and DefaultMaxRetries.
func DefaultConfig() *Config {
	return &Config{
		Email: Settings{
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
		"Email.Endpoint must be an http or https URL with a host and nothing more but /",
	)
	// errFromEmail reports a FromEmail that is not one email address.
	errFromEmail = errors.New("Email.FromEmail must be one email address")
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
// package documentation lists them, and sets FromEmail to the normalized
// address.
func rules(cfg *Config) error {
	s := &cfg.Email

	if s.Endpoint != "" && !validEndpoint(s.Endpoint) {
		return errEndpoint
	}

	if s.FromEmail != "" {
		from, err := email.NormalizeAddress(s.FromEmail)
		if err != nil {
			return errFromEmail
		}

		s.FromEmail = from
	}

	return nil
}
