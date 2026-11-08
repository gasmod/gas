package svcutil

import "errors"

var (
	// ErrNilConfig indicates that a service's WithConfig option was given a
	// nil configuration.
	ErrNilConfig = errors.New("config is nil")
	// ErrNoConfigProvider indicates that a service has neither a
	// configuration given to WithConfig nor a ConfigProvider to bind one
	// from.
	ErrNoConfigProvider = errors.New("no config provider")
	// ErrInvalidConfig indicates that a service's configuration failed to
	// bind, failed its validate tags, or broke one of the service's rules.
	ErrInvalidConfig = errors.New("invalid config")
	// ErrNotInitialized indicates that a service method was called before
	// the service's Init succeeded. Services return it, wrapped or as
	// their own alias of it.
	ErrNotInitialized = errors.New("service not initialized")
	// ErrNilLogger indicates that a service's constructor was given a nil
	// logger. Services return it, wrapped or as their own alias of it.
	ErrNilLogger = errors.New("logger is nil")
	// ErrNegativeCleanupInterval indicates that a cleanup interval is
	// negative. CheckCleanup returns it.
	ErrNegativeCleanupInterval = errors.New("cleanup interval must not be negative")
	// ErrCleanupTimeout indicates that a cleanup is enabled without a
	// positive timeout. CheckCleanup returns it.
	ErrCleanupTimeout = errors.New("cleanup timeout must be positive when cleanup is enabled")
)
