package gas

// Service is the core interface for lifecycle-managed services.
// Any type registered with the DI container that implements this interface
// will have Init() called after construction and Close() called at shutdown
// (singletons) or scope end (scoped). This holds however the value was
// registered: a pre-built instance passed to WithServiceInstance is
// initialized too. Transient services cannot implement this interface —
// registration will be rejected.
//
// Implementing it partially is rejected too. Init and Close are the managed
// lifecycle, so a type declaring either one must implement all three. A type
// that declares one and stops is not a Service and would be silently inert:
// Init never runs, Close never runs, and the kill switch cannot see it.
// BuildAll returns an error naming the missing or mis-typed methods instead.
//
// Name is not a trigger. Declaring only Name leaves a type an ordinary
// dependency, and the same registration options carry those — loggers, config
// providers, per-request values — which must keep working.
//
// A type from another package that declares Close (an io.Closer such as
// *sql.DB) cannot be given the remaining methods; wrap it in a service of your
// own rather than registering it directly.
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
