package memory

// Option configures a Service. Options only record what they are given;
// the constructor and Init check it.
type Option func(*Service)

// WithConfig makes Init use a copy of cfg instead of binding the
// configuration from the ConfigProvider. Init copies *cfg each time it
// runs and never changes it; the copy is checked against its validate
// tags as a bound configuration is. A nil cfg makes the constructor fail
// with email.ErrNilConfig.
//
// Default: bind the `email` section from the ConfigProvider over
// DefaultConfig.
func WithConfig(cfg *Config) Option {
	return func(s *Service) { s.cfgSource.Set(cfg) }
}
