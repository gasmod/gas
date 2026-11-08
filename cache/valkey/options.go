package valkey

import "crypto/tls"

// Option configures a Service. Options only record what they are given;
// Init checks it.
type Option func(*Service)

// WithConfig makes Init use a copy of cfg instead of binding the
// configuration from the ConfigProvider. Init copies *cfg each time it
// runs and never changes it; the copy is checked against its validate
// tags as a bound configuration is. A nil cfg makes Init fail with
// cache.ErrNilConfig.
//
// Default: bind the `cache` section from the ConfigProvider over
// DefaultConfig.
func WithConfig(cfg *Config) Option {
	return func(s *Service) { s.cfgSource.Set(cfg) }
}

// WithTLSConfig makes the store connect with TLS configured by cfg, for
// what the TLS settings cannot express, such as a private certificate
// authority or a client certificate. It turns TLS on and replaces the
// TLS and TLSServerName settings, which are then ignored. Each Init
// uses cfg.Clone(), made when Init runs; the store never changes cfg.
// The clone is shallow: it shares RootCAs, Certificates, the callbacks
// and the other pointers with cfg, so do not change cfg, or what it
// points to, once it is passed. A nil cfg leaves the TLS settings in
// charge.
//
// Default: TLS as the TLS and TLSServerName settings say.
func WithTLSConfig(cfg *tls.Config) Option {
	return func(s *Service) { s.tlsConfig = cfg }
}
