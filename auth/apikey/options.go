package apikey

// Option configures a Service. Options only record what they are given;
// Init checks it.
type Option func(*Service)

// WithConfig makes Init use a copy of cfg instead of binding the
// configuration from the ConfigProvider. The copy is checked like a bound
// one, and later changes to cfg do not reach the service. A nil cfg makes
// Init fail with ErrNilConfig.
//
// Default: bind the `apikey` section from the ConfigProvider over
// DefaultConfig.
func WithConfig(cfg *Config) Option {
	return func(s *Service) { s.cfgSource.Set(cfg) }
}
