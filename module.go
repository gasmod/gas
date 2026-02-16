package gas

// Module is the core interface every Gas module must implement.
type Module interface {
	// Name returns the unique identifier for this module (e.g., "gas-auth").
	Name() string

	// Init initializes the module. Called by the base server after all
	// modules are constructed. Modules register routes, middleware,
	// migrations, and event subscriptions here. They also validate
	// that required dependencies were provided.
	Init() error

	// Close gracefully shuts down the module. Can be called at runtime
	// without restarting the server. The module must clean up its own
	// internal resources (close connections, stop goroutines, etc.).
	// Infrastructure cleanup (routes, middleware) is handled by the
	// base server via smart infrastructure objects.
	Close() error
}
