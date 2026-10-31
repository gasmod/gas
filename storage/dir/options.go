package dir

import "os"

// SkipHandler receives an entry the store passed over, removed or left
// behind, as described in the package documentation: path is the entry's
// path relative to the directory, for an operator to inspect, and err
// says why. Neither quotes a key or a bucket name.
type SkipHandler func(path string, err error)

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

// WithRoot makes the store keep its buckets in root, which the caller
// opened and closes, after the store is closed; the store never closes
// it. The `storage.dir` setting must then be empty. A nil root makes the
// constructor fail with ErrNilRoot.
//
// Default: Init opens the directory of the `storage.dir` setting, and
// Close closes it.
func WithRoot(root *os.Root) Option {
	return func(p *Provider) { p.root, p.rootSet = root, true }
}

// WithSkipHandler sets the function the store calls for each entry it
// passes over, removes or leaves behind, as described in the package
// documentation. A nil fn reports nothing, as without the option.
//
// Default: report nothing.
func WithSkipHandler(fn SkipHandler) Option {
	return func(p *Provider) { p.skip = fn }
}
