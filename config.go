package gas

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
)

// The defaults DefaultConfig sets.
const (
	// DefaultHost is the address the server listens on: every interface.
	DefaultHost = "0.0.0.0"
	// DefaultPort is the port the server listens on.
	DefaultPort = 8080
	// DefaultReadTimeout bounds reading a whole request.
	DefaultReadTimeout = 5 * time.Second
	// DefaultReadHeaderTimeout bounds reading a request's headers.
	DefaultReadHeaderTimeout = 5 * time.Second
	// DefaultWriteTimeout bounds writing a response.
	DefaultWriteTimeout = 10 * time.Second
	// DefaultIdleTimeout bounds the wait for the next request on a
	// keep-alive connection.
	DefaultIdleTimeout = 2 * time.Minute
	// DefaultShutdownTimeout bounds a graceful shutdown.
	DefaultShutdownTimeout = 30 * time.Second
	// DefaultMaxJSONBodyBytes caps the request body Context.BindJSON
	// reads: 1 MiB.
	DefaultMaxJSONBodyBytes = 1 << 20
	// DefaultMaxFormBodyBytes caps the request body Context.BindForm
	// reads: 1 MiB.
	DefaultMaxFormBodyBytes = 1 << 20
)

// ErrInvalidConfig indicates that the App's Config, as bound, breaks one of
// the rules listed under Configuration in the package documentation.
var ErrInvalidConfig = errors.New("gas: invalid config")

// Config is the App's configuration, as described under Configuration in
// the package documentation. Start binds it from the ConfigProvider, when
// one is registered, and validates it.
type Config struct {
	// Env is the environment the App runs in. It binds from the key
	// `gasenv`, where the config module's EnvExtension stores `GAS_ENV`.
	Env Environment `json:"gasenv"`
	// Server holds the HTTP server's settings, bound from the key
	// `server`.
	Server ServerSettings
}

// ServerSettings holds the HTTP server's settings, as described under
// Configuration in the package documentation.
type ServerSettings struct {
	// Host is the address the server listens on: an IP address or an RFC
	// 1123 hostname, at most 253 bytes. It is checked for shape only, with
	// no DNS lookup.
	Host string `validate:"required,max=253,ip|hostname_rfc1123"`

	// Port is the port the server listens on, 0 to 65535. 0 picks a free
	// port.
	Port int

	// ReadTimeout bounds reading a whole request, body included. 0 is no
	// limit, as in net/http, so a server that streams request bodies can
	// zero it and keep ReadHeaderTimeout.
	ReadTimeout time.Duration

	// ReadHeaderTimeout bounds reading a request's headers. It must be
	// positive, so no configuration leaves a connection open to a client
	// that trickles its headers. net/http bounds a TLS handshake by the
	// smallest of ReadHeaderTimeout, ReadTimeout and WriteTimeout that is
	// set.
	ReadHeaderTimeout time.Duration

	// WriteTimeout bounds writing a response, from the end of the request
	// headers. 0 is no limit, as in net/http, for streaming responses.
	WriteTimeout time.Duration

	// IdleTimeout bounds the wait for the next request on a keep-alive
	// connection. 0 falls back to ReadTimeout, as in net/http, so it must
	// be positive when ReadTimeout is 0, or an idle connection would stay
	// open forever.
	IdleTimeout time.Duration

	// ShutdownTimeout bounds App.Stop, as described under Shutting down in the
	// package documentation: Stop returns within ShutdownTimeout plus up to
	// three times ShutdownGracePeriod. It must be positive.
	ShutdownTimeout time.Duration

	// MaxJSONBodyBytes caps how many bytes of the request body
	// Context.BindJSON reads; a larger body fails with a 413 *Error. It
	// must be positive. It does not apply to handlers that read the body
	// themselves; see BodyLimit for that.
	MaxJSONBodyBytes int64

	// MaxFormBodyBytes caps how many bytes of the request body
	// Context.BindForm reads, as MaxJSONBodyBytes does for BindJSON. It
	// must be positive.
	MaxFormBodyBytes int64
}

// DefaultConfig returns a Config holding the Default constants and
// DefaultEnvironment.
func DefaultConfig() *Config {
	return &Config{
		Env: DefaultEnvironment,
		Server: ServerSettings{
			Host:              DefaultHost,
			Port:              DefaultPort,
			ReadTimeout:       DefaultReadTimeout,
			ReadHeaderTimeout: DefaultReadHeaderTimeout,
			WriteTimeout:      DefaultWriteTimeout,
			IdleTimeout:       DefaultIdleTimeout,
			ShutdownTimeout:   DefaultShutdownTimeout,
			MaxJSONBodyBytes:  DefaultMaxJSONBodyBytes,
			MaxFormBodyBytes:  DefaultMaxFormBodyBytes,
		},
	}
}

// standardEnvironments are the environments an App accepts unless
// WithAllowedEnvironments sets others.
var standardEnvironments = []Environment{EnvDevelopment, EnvTesting, EnvStaging, EnvProduction}

// maxPort is the largest TCP port.
const maxPort = 65535

// hostRule is the validator rule Host must pass, the same as its tag.
const hostRule = "required,max=253,ip|hostname_rfc1123"

// validateEnv checks that env is one of allowed. The error names the
// allowed environments, not env.
func validateEnv(env Environment, allowed []Environment) error {
	if slices.Contains(allowed, env) {
		return nil
	}

	names := make([]string, len(allowed))
	for i, e := range allowed {
		names[i] = string(e)
	}

	return fmt.Errorf("%w: gasenv must be one of %s", ErrInvalidConfig, strings.Join(names, ", "))
}

// validate checks c against the rules under Configuration, in the order
// of the fields, and returns the first it breaks, wrapping
// ErrInvalidConfig. The errors name the key and the rule, never the
// value, which came from the configuration.
func (c *Config) validate(allowed []Environment) error {
	err := validateEnv(c.Env, allowed)
	if err != nil {
		return err
	}

	s := &c.Server

	// A Var error names the rule, and the value only through its Value
	// method, which is not part of the message.
	hostErr := validator.New().Var(s.Host, hostRule)
	if hostErr != nil {
		return fmt.Errorf(
			"%w: server.host must be an IP address or an RFC 1123 hostname",
			ErrInvalidConfig,
		)
	}

	if s.Port < 0 || s.Port > maxPort {
		return fmt.Errorf("%w: server.port must be 0 to %d", ErrInvalidConfig, maxPort)
	}

	err = validateTimeouts(s)
	if err != nil {
		return err
	}

	switch {
	case s.MaxJSONBodyBytes <= 0:
		return fmt.Errorf("%w: server.maxjsonbodybytes must be positive", ErrInvalidConfig)
	case s.MaxFormBodyBytes <= 0:
		return fmt.Errorf("%w: server.maxformbodybytes must be positive", ErrInvalidConfig)
	}

	return nil
}

// validateTimeouts checks the timeouts of s, in the order of the fields,
// and returns the first rule they break.
func validateTimeouts(s *ServerSettings) error {
	switch {
	case s.ReadTimeout < 0:
		return fmt.Errorf("%w: server.readtimeout must not be negative", ErrInvalidConfig)
	case s.ReadHeaderTimeout <= 0:
		return fmt.Errorf("%w: server.readheadertimeout must be positive", ErrInvalidConfig)
	case s.WriteTimeout < 0:
		return fmt.Errorf("%w: server.writetimeout must not be negative", ErrInvalidConfig)
	case s.IdleTimeout < 0:
		return fmt.Errorf("%w: server.idletimeout must not be negative", ErrInvalidConfig)
	case s.IdleTimeout == 0 && s.ReadTimeout == 0:
		return fmt.Errorf(
			"%w: server.idletimeout must be positive when server.readtimeout is 0",
			ErrInvalidConfig,
		)
	case s.ShutdownTimeout <= 0:
		return fmt.Errorf("%w: server.shutdowntimeout must be positive", ErrInvalidConfig)
	}

	return nil
}
