package gas

import (
	"errors"
	"fmt"
)

// Sentinel errors for dependency resolution. Errors from the container wrap
// one of these, so callers can classify a failure with errors.Is while the
// message still names the types involved.
var (
	// ErrNotRegistered reports that a type, or a dependency of one, has no
	// registration or instance in the container.
	ErrNotRegistered = errors.New("gas: service not registered")

	// ErrCircularDependency reports that the registered constructors depend
	// on each other in a cycle, so no build order exists. It is also returned
	// when a constructor or Init resolves the type being built through a
	// captured container or scope, when two goroutines' singleton builds
	// would wait on each other, and when constructors nest more than 256 deep.
	ErrCircularDependency = errors.New("gas: circular dependency")

	// ErrCaptiveDependency reports a singleton whose constructor depends on a
	// scoped or transient service, which the singleton would hold for the
	// life of the process.
	ErrCaptiveDependency = errors.New("gas: captive dependency")

	// ErrScopeRequired reports a scoped service resolved from the container
	// itself; it can only be resolved from a Scope.
	ErrScopeRequired = errors.New("gas: scoped service resolved outside a scope")

	// ErrInvalidLifetime reports a registration whose ServiceLifetime is not
	// Singleton, Scoped, or Transient. Registering a transient Service
	// panics with an error wrapping it, since a transient has no managed
	// lifecycle.
	ErrInvalidLifetime = errors.New("gas: invalid service lifetime")

	// ErrInvalidConstructor reports a constructor the container cannot
	// call. Registration panics with an error wrapping it.
	ErrInvalidConstructor = errors.New("gas: invalid constructor")

	// ErrNilInstance reports a nil value passed to RegisterServiceInstance
	// or WithServiceInstance. Registration panics with an error wrapping
	// it.
	ErrNilInstance = errors.New("gas: nil instance")

	// ErrInvalidAlias reports an alias RegisterServiceAlias cannot
	// register, or a service registered under a type that is an alias.
	// Registration panics with an error wrapping it.
	ErrInvalidAlias = errors.New("gas: invalid alias")

	// ErrScopeClosed reports a scoped service resolved from a Scope after
	// its Close.
	ErrScopeClosed = errors.New("gas: scope closed")

	// ErrInvalidServiceShape reports a registered type that declares Init or
	// Close but does not implement Service.
	ErrInvalidServiceShape = errors.New("gas: invalid service shape")
)

// diError is a dependency-resolution error whose message is written for
// the case at hand and which wraps one of the sentinels above, so the
// sentinel classifies the error without changing its text.
type diError struct {
	sentinel error
	msg      string
}

// newDIError formats a message like fmt.Errorf (without %w) and returns an
// error with that text that wraps sentinel.
func newDIError(sentinel error, format string, args ...any) error {
	return &diError{msg: fmt.Sprintf(format, args...), sentinel: sentinel}
}

// Error returns the message.
func (e *diError) Error() string { return e.msg }

// Unwrap returns the sentinel the error belongs to.
func (e *diError) Unwrap() error { return e.sentinel }
