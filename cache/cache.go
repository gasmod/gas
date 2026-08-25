package cache

import "errors"

// Sentinel errors returned by CacheProvider implementations.
var (
	// ErrKeyNotFound is returned by Get when the requested key does not
	// exist or has expired.
	ErrKeyNotFound = errors.New("cache: key not found")

	// ErrClosed is returned when an operation is attempted on a closed
	// cache service.
	ErrClosed = errors.New("cache: service is closed")
)
