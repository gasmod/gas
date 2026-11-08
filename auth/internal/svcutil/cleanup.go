package svcutil

import "time"

// CheckCleanup checks a service's cleanup settings, as described in the
// package documentation: a zero interval turns the cleanup off, a
// negative one is ErrNegativeCleanupInterval, and a positive one needs a
// positive timeout, or it is ErrCleanupTimeout. A service calls it from
// the rules it passes to Resolve, which wraps the error with
// ErrInvalidConfig.
func CheckCleanup(interval, timeout time.Duration) error {
	if interval < 0 {
		return ErrNegativeCleanupInterval
	}

	if interval > 0 && timeout <= 0 {
		return ErrCleanupTimeout
	}

	return nil
}
