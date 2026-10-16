package dir

import "os"

// Option configures a Provider in NewProvider.
type Option func(*Provider)

// WithRoot sets the root the provider stores into. It is required. The
// provider never closes root; the caller that opened it does, after the
// provider is no longer used. The last WithRoot given wins.
func WithRoot(root *os.Root) Option {
	return func(s *Provider) { s.root = root }
}
