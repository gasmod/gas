package gas

import (
	"time"

	env "github.com/gasmod/gas-config/extensions/gas-env"
)

// Config holds server-level configuration passed from the host server
// to the App. Sensible defaults are applied via DefaultConfig().
type Config struct {
	// Addr is the address the HTTP server listens on (e.g., ":8080").
	Addr string

	// Embedded GasEnv
	env.WithGasEnv

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
		Addr:            ":8080",
		ReadTimeout:     5 * time.Second,
		WriteTimeout:    10 * time.Second,
		IdleTimeout:     120 * time.Second,
		ShutdownTimeout: 30 * time.Second,
	}
}
