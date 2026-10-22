// Package gas is the core of the Gas framework: the DI container, the Worker
// and App lifecycles, the Router with service ownership, the EventBus,
// request scopes, the error model, the runtime kill switch, the provider
// interfaces (Logger, CacheProvider, ...) that other gasmod modules
// implement, and the Principal that authentication middleware stores on the
// request context.
//
// # Wiring
//
// NewWorker registers the *EventBus and the Worker itself, as HealthProvider
// and ReadyProvider, in its container; NewApp also registers its *Router. A
// type that implements Service has Init(ctx) called after construction and
// Close(ctx) at shutdown (singletons) or scope end (scoped); a type that
// declares Init or Close with any other signature is rejected at startup.
//
// Errors from dependency resolution wrap one of the sentinels
// ErrNotRegistered, ErrCircularDependency, ErrCaptiveDependency,
// ErrScopeRequired, ErrNoConstructor, ErrInvalidLifetime, and
// ErrInvalidServiceShape, so callers classify them with errors.Is.
//
// The container is safe for concurrent resolution. A singleton is built by
// one goroutine at a time: other goroutines asking for it while it is built
// wait and get the same instance, so its constructor and Init run once.
// Besides the cycles BuildAll finds in constructor parameters, resolution
// catches a constructor or Init that resolves the type being built through a
// captured container or scope, and two builds on different goroutines that
// would wait on each other; both return ErrCircularDependency naming the
// types. To tell a re-entrant resolution from a concurrent one, the container
// reads the calling goroutine's ID from runtime.Stack, once per constructor
// call and once per wait.
//
// # Lifecycle
//
// Every lifecycle method takes the caller's context: Worker.Start,
// InitServices, Shutdown, CloseService and RestartService, App.Start and
// App.Stop, ServiceContainer.BuildAll and NewScope, and Scope.Close. Run
// uses context.Background().
//
// Each singleton's Init gets the caller's context plus an init deadline, 15
// seconds by default (see WithServiceInitTimeout,
// WithServiceContainerServiceTimeout, and ServiceInitTimeout; 0 disables
// it). When the deadline passes, the container cancels Init's context and
// waits for Init to return, then fails with an error wrapping
// context.DeadlineExceeded, so no Init is left running behind a failed
// start. An Init that ignores the cancellation and returns nil late is
// closed again. When an init deadline applies, Init's context is canceled
// when Init returns, so background work a service starts in Init must not
// derive from it. A scoped service's Init gets the scope's context with the
// same init deadline; in an App that is the request context, and Close gets
// it without its cancellation, so scoped services clean up even after the
// client has gone.
//
// A Start that fails closes, in reverse order, every service it had
// initialized, with its context stripped of cancellation, and returns the
// startup error joined with any Close errors. It leaves nothing running, and
// the Worker cannot be started again: later calls to Start, and to
// InitServices after a failed InitServices, return the same error.
//
// Worker.Shutdown emits SystemShuttingDown and closes every service still
// active, in reverse initialization order, passing its context to Close.
// Services already closed by CloseService are skipped. A Close error does
// not stop the rest; all of them are returned, joined. Shutdown runs once,
// and later calls return nil. Once it has begun, CloseService and
// RestartService return an error. App.Stop first emits
// SystemServerShuttingDown and shuts the http.Server down within
// ServerSettings.ShutdownTimeout, or sooner if its context ends.
//
// Guides with usage examples are at https://gasmod.github.io/gas.
//
// SPDX-License-Identifier: MIT
package gas
