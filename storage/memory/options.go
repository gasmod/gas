package memory

// Option configures a Provider. Options only record what they are given;
// the constructor and Init check it.
type Option func(*Provider)

// WithConfig makes Init use a copy of cfg instead of binding the
// configuration from the ConfigProvider. Init copies *cfg each time it
// runs and never changes it; the copy is checked against its validate
// tags as a bound configuration is. A nil cfg makes the constructor fail
// with storage.ErrNilConfig.
//
// Default: bind the `storage` section from the ConfigProvider over
// DefaultConfig.
func WithConfig(cfg *Config) Option {
	return func(p *Provider) { p.cfgSource.Set(cfg) }
}
