package gas

import (
	"context"
	"time"
)

// Service is the core interface for lifecycle-managed services.
// Any type registered with the DI container that implements this interface
// will have Init(ctx) called after construction and Close(ctx) called at
// shutdown (singletons) or scope end (scoped). This holds however the value was
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
	// Name returns the unique identifier for this service (e.g., "gas/auth").
	Name() string

	// Init initializes the service. Called automatically by the DI container
	// after construction. Services register routes, middleware, migrations,
	// and event subscriptions here.
	//
	// ctx carries the service's init deadline. For a singleton it is added to
	// the caller's startup context (or RestartService context); for a scoped
	// service, to the scope's context. Init must honor ctx: when the deadline
	// passes the container cancels ctx and waits for Init to return, so an
	// Init that ignores it holds up startup or the resolution. ctx is
	// canceled as soon as Init returns, so work that outlives Init, such as a
	// background goroutine, must not derive from it.
	Init(ctx context.Context) error

	// Close gracefully shuts down the service. Called at App shutdown
	// (singletons) or when a Scope is closed (scoped services). ctx is the
	// caller's shutdown context and bounds the shutdown work.
	Close(ctx context.Context) error
}

// ServiceInitTimeout is implemented by a Service that needs a different Init
// deadline than the container default (see WithServiceInitTimeout). It
// applies to a singleton at startup and on RestartService, and to a scoped
// service on its first resolution in each scope. When the deadline passes,
// Init's context is canceled and, once Init returns, the call fails with an
// error that wraps context.DeadlineExceeded. If Init ignored the
// cancellation and returned nil, the service is closed again before the
// error is returned. 0 means no deadline; a negative value fails Init
// without running it.
type ServiceInitTimeout interface {
	// InitTimeout returns how long the container waits for Init.
	InitTimeout() time.Duration
}
