package valkey

import (
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	// DefaultDB is the database number of DefaultConfig.
	DefaultDB = 0
	// DefaultTTL is the TTL of DefaultConfig for a Set with a zero TTL:
	// no expiry.
	DefaultTTL time.Duration = 0
	// DefaultDialTimeout is the bound of each connect attempt of
	// DefaultConfig.
	DefaultDialTimeout = 5 * time.Second
	// DefaultOpTimeout is the bound of each command of DefaultConfig.
	DefaultOpTimeout = 3 * time.Second
	// DefaultConnRetries is the number of connect retries of
	// DefaultConfig.
	DefaultConnRetries = 0
	// DefaultConnRetryInterval is the wait before the first connect retry
	// of DefaultConfig.
	DefaultConnRetryInterval = 2 * time.Second
	// MaxConnRetryInterval is the longest ConnRetryInterval, and caps the
	// doubling of the wait between connect retries.
	MaxConnRetryInterval = 30 * time.Second
)

// minConnRetryInterval is the shortest ConnRetryInterval, checked only
// when ConnRetries is positive. The longest is MaxConnRetryInterval.
const minConnRetryInterval = 100 * time.Millisecond

// Config holds the Valkey store's configuration, read from the `cache`
// section.
type Config struct {
	// Cache holds the settings.
	Cache Settings
}

// Settings are the Valkey store's settings, as described in the package
// documentation.
type Settings struct {
	// Addr is the server's address, as host:port. It is required.
	Addr string `validate:"required"`
	// Username is the ACL user to authenticate as; empty is the default
	// user.
	Username string
	// Password is the password to authenticate with; empty sends none.
	// fmt prints it as `[redacted]`.
	Password Secret
	// KeyPrefix is prepended to every key the store's methods send.
	KeyPrefix string
	// TLSServerName is the name the server's certificate is checked
	// against when TLS is on; empty is the host of Addr.
	TLSServerName string
	// DefaultTTL is the TTL of a Set with a zero TTL; 0 is no expiry.
	DefaultTTL time.Duration `validate:"min=0"`
	// DialTimeout bounds each connect attempt: dial, handshake and ping.
	// OpTimeout bounds the handshake and the ping too.
	DialTimeout time.Duration `validate:"gt=0"`
	// OpTimeout bounds each command.
	OpTimeout time.Duration `validate:"gt=0"`
	// ConnRetryInterval is the wait before the first connect retry. It is
	// checked only when ConnRetries is positive.
	ConnRetryInterval time.Duration
	// DB is the database number to select.
	DB int `validate:"min=0"`
	// ConnRetries is how many times a failed connect is retried.
	ConnRetries int `validate:"min=0,max=10"`
	// TLS turns on TLS with the system's root certificates.
	TLS bool
}

// redacted is what fmt prints for a Secret.
const redacted = "[redacted]"

// Secret is a string that fmt never prints, such as Settings.Password:
// String returns `[redacted]`, and Format writes it for every verb, so
// `%v`, `%+v` and `%#v` of a Settings or a Config hide it. Configuration
// binds into it as into a string, and string(s) is the value.
type Secret string

// String returns `[redacted]`, never the value.
func (Secret) String() string { return redacted }

// Format writes `[redacted]`, never the value, whatever the verb.
func (Secret) Format(f fmt.State, _ rune) {
	// fmt's State writes to fmt's own buffer, which does not fail.
	_, _ = io.WriteString(f, redacted)
}

// DefaultConfig returns the configuration a store starts from before
// binding: no address, DefaultDB, DefaultTTL, DefaultDialTimeout,
// DefaultOpTimeout, DefaultConnRetries, DefaultConnRetryInterval, no
// credentials, no key prefix and no TLS.
func DefaultConfig() *Config {
	return &Config{
		Cache: Settings{
			DB:                DefaultDB,
			DefaultTTL:        DefaultTTL,
			DialTimeout:       DefaultDialTimeout,
			OpTimeout:         DefaultOpTimeout,
			ConnRetries:       DefaultConnRetries,
			ConnRetryInterval: DefaultConnRetryInterval,
		},
	}
}

// The rule errors name the settings, never their values.
var (
	// errRetryInterval reports a ConnRetryInterval out of bounds while
	// retries are on.
	errRetryInterval = errors.New(
		"Cache.ConnRetryInterval must be 100ms to 30s if Cache.ConnRetries > 0",
	)
	// errServerNameWithoutTLS reports a TLSServerName set while TLS is
	// off.
	errServerNameWithoutTLS = errors.New("Cache.TLSServerName requires Cache.TLS")
)

// rules returns the rules function of a store, given whether it has a
// TLS configuration from WithTLSConfig: the checks the validate tags
// cannot express, in the order the package documentation lists them.
func rules(tlsOverride bool) func(*Config) error {
	return func(cfg *Config) error {
		s := &cfg.Cache

		if s.ConnRetries > 0 &&
			(s.ConnRetryInterval < minConnRetryInterval || s.ConnRetryInterval > MaxConnRetryInterval) {
			return errRetryInterval
		}

		if !tlsOverride && !s.TLS && s.TLSServerName != "" {
			return errServerNameWithoutTLS
		}

		return nil
	}
}
