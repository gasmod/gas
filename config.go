package gas

import (
	"errors"
	"fmt"
	"net"
	"time"

	env "github.com/gasmod/gas-config/extensions/gas-env"
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
	Host string

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

// Validate checks that the Config fields are valid.
func (c *Config) Validate() error {
	if err := validateHost(c.Server.Host); err != nil {
		return fmt.Errorf("Server.Host: %w", err)
	}
	return nil
}

func validateHost(host string) error {
	if host == "" {
		return errors.New("must not be empty")
	}
	// Valid if it's a resolvable hostname, this also checks IPv4 and IPv6
	if _, err := net.LookupHost(host); err == nil {
		return nil
	}
	return fmt.Errorf("%q is not a valid IP or resolvable hostname", host)
}
