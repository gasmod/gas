package cache

import "errors"

// The sentinel errors every store of the module shares, as described in
// the package documentation. A store's errors start with its own service
// name and the operation, and wrap one of these.
var (
	// ErrKeyNotFound indicates that a store holds no live value under the
	// key: it was never set, was deleted, or expired.
	ErrKeyNotFound = errors.New("key not found")
	// ErrInvalidKey indicates that a key is not one a store accepts: it
	// is empty.
	ErrInvalidKey = errors.New("invalid key")
	// ErrInvalidTTL indicates that a TTL is negative.
	ErrInvalidTTL = errors.New("invalid TTL")
	// ErrNotInitialized indicates that a method was called before the
	// store's first successful Init.
	ErrNotInitialized = errors.New("service not initialized")
	// ErrAlreadyInitialized indicates that Init was called on a store that
	// is initialized.
	ErrAlreadyInitialized = errors.New("service already initialized")
	// ErrClosed indicates that a method was called after Close, before
	// Init succeeded again, or that Close ran while the call was in
	// progress.
	ErrClosed = errors.New("service closed")
	// ErrNilLogger indicates that a store's constructor was given a nil
	// logger.
	ErrNilLogger = errors.New("logger is nil")
	// ErrNilConfig indicates that WithConfig was given a nil
	// configuration.
	ErrNilConfig = errors.New("config is nil")
	// ErrNoConfigProvider indicates that there is neither a configuration
	// given to WithConfig nor a ConfigProvider to bind one from.
	ErrNoConfigProvider = errors.New("no config provider")
	// ErrInvalidConfig indicates that the configuration failed to bind,
	// failed its validate tags, or broke one of the store's rules.
	ErrInvalidConfig = errors.New("invalid config")
)
