package memory

import "time"

// Option configures a Service. Options only record what they are given;
// Init checks it.
type Option func(*Service)

// Clock tells the time the store's messages are timed by: delays,
// visibility timeouts, the dedupe window and the timestamps Dequeue
// reports. It must be safe for concurrent use.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
}

// systemClock is the Clock of time.Now.
type systemClock struct{}

// Now returns time.Now().
func (systemClock) Now() time.Time { return time.Now() }

// WithConfig makes Init use a copy of cfg instead of binding the
// configuration from the ConfigProvider. Init copies *cfg each time it
// runs and never changes it; the copy is checked against its validate
// tags as a bound configuration is. A nil cfg makes Init fail with
// queue.ErrNilConfig.
//
// Default: bind the `queue` section from the ConfigProvider over
// DefaultConfig.
func WithConfig(cfg *Config) Option {
	return func(s *Service) { s.cfgSource.Set(cfg) }
}

// WithClock makes the store tell time by c, such as a clock a test moves
// by hand, as described in the package documentation. A nil c keeps the
// system clock.
//
// Default: the system clock, time.Now.
func WithClock(c Clock) Option {
	return func(s *Service) { s.clock = c }
}
