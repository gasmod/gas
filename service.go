package gas

// Service is the core interface for lifecycle-managed services.
// Any type registered with the DI container that implements this interface
// will have Init() called after construction and Close() called at shutdown
// (singletons) or scope end (scoped). Transient services cannot implement
// this interface — registration will be rejected.
type Service interface {
	// Name returns the unique identifier for this service (e.g., "gas-auth").
	Name() string

	// Init initializes the service. Called automatically by the DI container
	// after construction. Services register routes, middleware, migrations,
	// and event subscriptions here.
	Init() error

	// Close gracefully shuts down the service. Called at App shutdown
	// (singletons) or when a Scope is closed (scoped services).
	Close() error
}
