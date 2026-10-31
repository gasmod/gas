package sqs

// Option configures a Service. Options only record what they are given;
// Init checks it.
type Option func(*Service)

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

// WithClient makes the store send its requests with client, such as an
// *sqs.Client built elsewhere or a fake, instead of building one, as
// described in the package documentation. The Region, Endpoint,
// credential and MaxRetries settings then have no effect. A nil client,
// or a nil *sqs.Client, makes Init fail with ErrNilClient.
//
// Default: Init builds an *sqs.Client from the settings.
func WithClient(client API) Option {
	return func(s *Service) {
		s.client = client
		s.clientSet = true
	}
}
