package gas

import (
	"errors"
	"fmt"
	"net"
	"time"

	env "github.com/gasmod/gas-config/extensions/gas-env"
)

const (
	minPort = 1
	maxPort = 65535

	minReadTimeout  = 1 * time.Second
	maxReadTimeout  = 5 * time.Minute
	minWriteTimeout = 1 * time.Second
	maxWriteTimeout = 10 * time.Minute

	minIdleTimeout = 1 * time.Second
	maxIdleTimeout = 10 * time.Minute

	minShutdownTimeout = 1 * time.Second
	maxShutdownTimeout = 2 * time.Minute

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

	// ServerHost specifies the hostname or IP address where the server will be hosted.
	ServerHost string

	// ServerPort is the port number on which the server listens for incoming requests.
	ServerPort int

	// ServerReadTimeout is the maximum duration for reading the entire request.
	ServerReadTimeout time.Duration

	// ServerWriteTimeout is the maximum duration before timing out writes of the response.
	ServerWriteTimeout time.Duration

	// ServerIdleTimeout is the maximum time to wait for the next request when keep-alives are enabled.
	ServerIdleTimeout time.Duration

	// ServerShutdownTimeout is how long to wait for in-flight requests to complete
	// during graceful shutdown.
	ServerShutdownTimeout time.Duration
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		ServerHost:            defaultHost,
		ServerPort:            defaultPort,
		ServerReadTimeout:     defaultReadTimeout,
		ServerWriteTimeout:    defaultWriteTimeout,
		ServerIdleTimeout:     defaultIdleTimeout,
		ServerShutdownTimeout: defaultShutdownTimeout,
	}
}

// Validate checks that the Config fields are valid.
func (c *Config) Validate() error {
	if err := validateHost(c.ServerHost); err != nil {
		return fmt.Errorf("ServerHost: %w", err)
	}
	if c.ServerPort < minPort || c.ServerPort > maxPort {
		return fmt.Errorf("ServerPort must be between %d and %d, got %d", minPort, maxPort, c.ServerPort)
	}
	if c.ServerReadTimeout < minReadTimeout || c.ServerReadTimeout > maxReadTimeout {
		return fmt.Errorf("ServerReadTimeout must be between %s and %s, got %s", minReadTimeout, maxReadTimeout, c.ServerReadTimeout)
	}
	if c.ServerWriteTimeout < minWriteTimeout || c.ServerWriteTimeout > maxWriteTimeout {
		return fmt.Errorf("ServerWriteTimeout must be between %s and %s, got %s", minWriteTimeout, maxWriteTimeout, c.ServerWriteTimeout)
	}
	if c.ServerIdleTimeout < minIdleTimeout || c.ServerIdleTimeout > maxIdleTimeout {
		return fmt.Errorf("ServerIdleTimeout must be between %s and %s, got %s", minIdleTimeout, maxIdleTimeout, c.ServerIdleTimeout)
	}
	if c.ServerShutdownTimeout < minShutdownTimeout || c.ServerShutdownTimeout > maxShutdownTimeout {
		return fmt.Errorf("ServerShutdownTimeout must be between %s and %s, got %s", minShutdownTimeout, maxShutdownTimeout, c.ServerShutdownTimeout)
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
