package gas

import (
	"sync"
	"time"

	"github.com/go-playground/validator/v10"

	env "github.com/gasmod/gas/config/extensions/gasenv"
)

const (
	defaultHost            = "0.0.0.0"
	defaultPort            = 8080
	defaultReadTimeout     = 5 * time.Second
	defaultWriteTimeout    = 10 * time.Second
	defaultIdleTimeout     = 2 * time.Minute
	defaultShutdownTimeout = 30 * time.Second
)

// Config holds server-level configuration passed from the host server
// to the App. Sensible defaults are applied via DefaultConfig().
type Config struct {
	// Embedded GasEnv
	env.WithGasEnv

	Server ServerSettings
}

// ServerSettings defines the configuration for a server, including host, port, timeouts, and graceful shutdown settings.
type ServerSettings struct {
	// Host specifies the hostname or IP address where the server will be hosted.
	Host string `validate:"required,max=253,ip|hostname_rfc1123"`

	// Port is the port number on which the server listens for incoming requests.
	Port int

	// ReadTimeout is the maximum duration for reading the entire request.
	ReadTimeout time.Duration

	// WriteTimeout is the maximum duration before timing out writes of the response.
	WriteTimeout time.Duration

	// IdleTimeout is the maximum time to wait for the next request when keep-alives are enabled.
	IdleTimeout time.Duration

	// ShutdownTimeout is how long to wait for in-flight requests to complete
	// during graceful shutdown.
	ShutdownTimeout time.Duration
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Server: ServerSettings{
			Host:            defaultHost,
			Port:            defaultPort,
			ReadTimeout:     defaultReadTimeout,
			WriteTimeout:    defaultWriteTimeout,
			IdleTimeout:     defaultIdleTimeout,
			ShutdownTimeout: defaultShutdownTimeout,
		},
	}
}

// Validate checks the Config fields against their validate tags. Server.Host
// must be an IP address or an RFC 1123 hostname of at most 253 characters;
// it is checked for shape only, with no DNS lookup, so a host that does not
// resolve fails when the server listens.
func (c *Config) Validate() error {
	// Returned as is: callers such as App.Start add their own context, and
	// errors.As still reaches validator.ValidationErrors.
	return configValidator().Struct(c) //nolint:wrapcheck // see above
}

// configValidator validates Config. It is built on first use and shared,
// since a *validator.Validate caches each struct's parsed tags.
var configValidator = sync.OnceValue(func() *validator.Validate { return validator.New() })
