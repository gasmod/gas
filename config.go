package gas

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

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

// validateHost accepts an IP address or a syntactically valid hostname. It
// checks shape only and does no DNS lookup, so validation is fast, works
// offline, and does not depend on the resolver at startup; a host that does
// not resolve fails when the server listens.
func validateHost(host string) error {
	if host == "" {
		return errors.New("must not be empty")
	}
	if net.ParseIP(host) != nil || isHostname(host) {
		return nil
	}
	return fmt.Errorf("%q is not a valid IP or hostname", host)
}

// isHostname reports whether host is a hostname in the RFC 1123 sense: at
// most 253 characters (one more for a trailing dot), made of dot-separated
// labels of 1 to 63 letters, digits, and hyphens that neither start nor end
// with a hyphen.
func isHostname(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if !isHostnameLabel(label) {
			return false
		}
	}
	return true
}

func isHostnameLabel(label string) bool {
	if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	return !strings.ContainsFunc(label, func(r rune) bool { return !isHostnameRune(r) })
}

func isHostnameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-'
}
