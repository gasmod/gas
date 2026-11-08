package session

// Option configures a Service. Options only record what they are given;
// Init checks it.
type Option func(*Service)

// WithConfig makes Init use a copy of cfg instead of binding the
// configuration from the ConfigProvider. It only records cfg: Init copies
// *cfg each time it runs, never changes it, and checks the copy as it
// checks a bound configuration. A nil cfg makes Init fail with
// ErrNilConfig.
//
// Default: bind the `session` section from the ConfigProvider over
// DefaultConfig.
func WithConfig(cfg *Config) Option {
	return func(s *Service) { s.cfgSource.Set(cfg) }
}
