package dir

import "os"

// SkipHandler receives an entry the provider passed over or left behind:
// path is the entry's path relative to the root, for an operator to
// inspect, and err says why. err wraps ErrCorrupt for an object that cannot
// be read and ErrUnnamedBucket for a bucket that cannot be named; anything
// else, such as a file DeleteBucket had to leave behind, is a plain error.
// Paths and the reasons in err never quote a key or a bucket name.
type SkipHandler func(path string, err error)

// Option configures a Provider in NewProvider.
type Option func(*Provider)

// WithRoot sets the root the provider stores into. It is required. The
// provider never closes root; the caller that opened it does, after the
// provider is no longer used. The last WithRoot given wins.
func WithRoot(root *os.Root) Option {
	return func(s *Provider) { s.root = root }
}

// WithSkipHandler sets the function the provider calls for each object
// ListObjects leaves out, each bucket ListBuckets leaves out, and each piece
// of debris DeleteBucket removes with a bucket or file it leaves behind.
// The listings also count what they leave out in Skipped, but only the
// handler is told what and where. It is called synchronously on the
// calling goroutine, once per entry per call, so a paged listing reports
// the same entry on every page. It may call the provider: DeleteBucket
// calls it only after giving up every lock it took, and the listings hold
// none while they call it. A nil handler, the default, reports nothing.
func WithSkipHandler(fn SkipHandler) Option {
	return func(s *Provider) { s.skip = fn }
}
