package s3

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

// WithClient makes the store send its requests with client, such as an
// *s3.Client built elsewhere or a fake, instead of building one, as
// described in the package documentation. The Region, Endpoint,
// UsePathStyle, credential and MaxRetries settings then have no effect
// on the requests. A nil client, or a nil *s3.Client, makes Init fail
// with ErrNilClient.
//
// Default: Init builds an *s3.Client from the settings.
func WithClient(client API) Option {
	return func(p *Provider) {
		p.client = client
		p.clientSet = true
	}
}

// PresignerOption configures a Presigner. Options only record what they
// are given; the constructor and Init check it.
type PresignerOption func(*Presigner)

// WithPresignerConfig makes the Presigner's Init use a copy of cfg, as
// WithConfig does for a Provider. A nil cfg makes the constructor fail
// with storage.ErrNilConfig.
//
// Default: bind the `storage` section from the ConfigProvider over
// DefaultConfig.
func WithPresignerConfig(cfg *Config) PresignerOption {
	return func(p *Presigner) { p.cfgSource.Set(cfg) }
}

// WithPresignClient makes the Presigner sign with client, such as an
// *s3.PresignClient built elsewhere or a fake, instead of building one,
// as described in the package documentation. The Region, Endpoint,
// UsePathStyle and credential settings then have no effect on the URLs.
// A nil client, or a nil *s3.PresignClient, makes Init fail with
// ErrNilClient.
//
// Default: Init builds an *s3.PresignClient from the settings.
func WithPresignClient(client PresignAPI) PresignerOption {
	return func(p *Presigner) {
		p.client = client
		p.clientSet = true
	}
}
