package migrate

import "time"

// DefaultLockTimeout is how long a run waits for the migration lock when
// WithLockTimeout is not given.
const DefaultLockTimeout = 10 * time.Minute

// Option configures a Service. Options only record what they are given;
// Init checks it.
type Option func(*Service)

// WithLockTimeout sets how long Init, RunPending, Down and ClearDirty wait
// for the migration lock before they fail with ErrLockTimeout, as
// described in the package documentation. It must be positive; Init
// fails with ErrInvalidLockTimeout otherwise.
//
// Default: DefaultLockTimeout.
func WithLockTimeout(d time.Duration) Option {
	return func(s *Service) { s.lockTimeout = d }
}
