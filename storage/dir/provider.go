package dir

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/dir/internal/vfs"
	"github.com/gasmod/gas/storage/internal/configsrc"
	"github.com/gasmod/gas/storage/internal/lifecycle"
	"github.com/gasmod/gas/storage/internal/listing"
)

// ServiceName is the name of the dir store, returned by Provider.Name and
// prefixed to every error the store returns.
const ServiceName = "gas/storage/dir"

// The operations errors name.
const (
	opInit         = "init"
	opReady        = "ready"
	opClose        = "close"
	opCreateBucket = "create bucket"
	opHeadBucket   = "head bucket"
	opDeleteBucket = "delete bucket"
	opListBuckets  = "list buckets"
	opHeadObject   = "head object"
	opPutObject    = "put object"
	opGetObject    = "get object"
	opDeleteObject = "delete object"
	opListObjects  = "list objects"
)

// Provider is an object store in a directory on a local disk, as
// described in the package documentation. It is safe for concurrent use.
type Provider struct {
	// cfgProvider is the ConfigProvider Init binds from, unless a
	// configuration was given to WithConfig.
	cfgProvider gas.ConfigProvider
	// logger receives the store's events, with `service` set.
	logger gas.Logger
	// root is the directory WithRoot gave, which the caller owns, or nil.
	root *os.Root
	// skip receives the entries the store passes over, or is nil.
	skip SkipHandler
	// cfgSource records the configuration given to WithConfig.
	cfgSource configsrc.Source[Config]
	// lc is the lifecycle, whose sessions hold what one Init opened.
	lc lifecycle.Lifecycle[*session]
	// rootSet reports whether WithRoot was given, so that WithRoot(nil)
	// is told apart from no WithRoot.
	rootSet bool
}

// session is what one Init opened.
type session struct {
	// vfs stores the buckets and objects.
	vfs *vfs.VFS
	// root is the directory, which Close closes when owned is set.
	root *os.Root
	// codec seals and opens the continuation tokens of this Init.
	codec *listing.Codec
	// settings is the resolved configuration.
	settings Settings
	// owned reports whether Init opened root, so that Close closes it.
	owned bool
}

var (
	_ storage.Provider  = (*Provider)(nil)
	_ gas.Service       = (*Provider)(nil)
	_ gas.ReadyReporter = (*Provider)(nil)
)

// New returns the constructor of a Provider with opts applied, in the
// shape the gas container expects. The constructor opens nothing; Init
// does. It returns an error wrapping storage.ErrNilLogger for a nil
// logger, storage.ErrNilOption for a nil option, storage.ErrNilConfig for
// WithConfig(nil), and ErrNilRoot for WithRoot(nil); a nil ConfigProvider
// is accepted, and Init then requires WithConfig.
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Provider, error) {
	return func(cfgProvider gas.ConfigProvider, logger gas.Logger) (*Provider, error) {
		if logger == nil {
			return nil, fmt.Errorf("%s: %w", ServiceName, storage.ErrNilLogger)
		}

		p := &Provider{
			cfgProvider: cfgProvider,
			logger:      logger.With().Str("service", ServiceName).Logger(),
		}

		for i, opt := range opts {
			if opt == nil {
				return nil, fmt.Errorf(
					"%s: %w: at position %d",
					ServiceName,
					storage.ErrNilOption,
					i,
				)
			}

			opt(p)
		}

		switch {
		case p.cfgSource.IsNil():
			return nil, fmt.Errorf("%s: %w", ServiceName, storage.ErrNilConfig)
		case p.rootSet && p.root == nil:
			return nil, fmt.Errorf("%s: %w", ServiceName, ErrNilRoot)
		}

		return p, nil
	}
}

// Name returns ServiceName.
func (*Provider) Name() string { return ServiceName }

// Init resolves the configuration, opens the directory, removes the
// leftovers of crashed writes and creates the configured bucket, as
// described in the package documentation. On an initialized store it
// returns storage.ErrAlreadyInitialized and changes nothing.
func (p *Provider) Init(ctx context.Context) error {
	if p == nil {
		return opError(opInit, storage.ErrNotInitialized)
	}

	var (
		set   Settings
		swept vfs.SweepResult
	)

	err := p.lc.Init(ctx, func() (*session, error) {
		sess, res, err := p.open(ctx)
		if err != nil {
			return nil, err
		}

		set, swept = sess.settings, res

		return sess, nil
	})
	if err != nil {
		return opError(opInit, err)
	}

	if swept.Removed > 0 {
		p.logger.Info("leftovers of crashed writes removed").Int("removed", swept.Removed).Send()
	}

	if swept.Reindexed > 0 {
		p.logger.Info("objects with no index entry re-indexed").
			Int("reindexed", swept.Reindexed).
			Send()
	}

	if swept.Limited {
		p.logger.Warn("leftover sweep stopped at its limit").Int("limit", set.SweepLimit).Send()
	}

	p.logger.Info("service initialized").
		Bool("bucket", set.Bucket != "").
		Bool("own_root", p.root == nil).
		Send()

	return nil
}

// Close makes every later call but Init return an error wrapping
// storage.ErrClosed, waits for every call in flight to return, and closes
// the directory Init opened, as described in the package documentation.
// When ctx ends first, it cancels the calls in flight, which return
// storage.ErrClosed, and returns an error wrapping ctx's error. Close on
// a store that is not initialized returns nil and logs nothing.
func (p *Provider) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}

	sess, err := p.lc.Close(ctx)
	if sess == nil {
		return nil
	}

	if sess.Value.owned {
		cErr := sess.Value.root.Close()
		if cErr != nil {
			err = errors.Join(
				err,
				fmt.Errorf("%w: close dir: %w", storage.ErrBackend, pathFree(cErr)),
			)
		}
	}

	if err != nil {
		err = opError(opClose, err)
		p.logger.Warn("service closed, calls in flight canceled").Err("error", err).Send()

		return err
	}

	p.logger.Info("service closed").Send()

	return nil
}

// CheckReady reports readiness: an error wrapping
// storage.ErrNotInitialized or storage.ErrClosed when the store is not
// initialized, the context's error when ctx has ended, one wrapping
// storage.ErrBackend when the directory no longer holds the store's
// layout marker, such as a directory that was removed, and else nil.
func (p *Provider) CheckReady(ctx context.Context) error {
	sess, err := p.begin(ctx)
	if err != nil {
		return opError(opReady, err)
	}
	defer sess.End()

	err = sess.Value.vfs.Check()
	if err != nil {
		return opError(opReady, fmt.Errorf("%w: %w", storage.ErrBackend, err))
	}

	return nil
}

// open does the work of Init: it resolves the configuration, opens the
// directory and the store in it, sweeps it, and creates the configured
// bucket. A failure closes the directory it opened.
func (p *Provider) open(ctx context.Context) (*session, vfs.SweepResult, error) {
	cfg, err := p.cfgSource.Resolve(p.cfgProvider, DefaultConfig, rules(p.root != nil))
	if err != nil {
		return nil, vfs.SweepResult{}, err //nolint:wrapcheck // Init adds the name and the operation.
	}

	sess := &session{root: p.root, settings: cfg.Storage}

	if sess.root == nil {
		sess.root, err = os.OpenRoot(cfg.Storage.Dir)
		if err != nil {
			return nil, vfs.SweepResult{}, fmt.Errorf(
				"%w: open dir: %w",
				storage.ErrBackend,
				pathFree(err),
			)
		}

		sess.owned = true
	}

	res, err := p.prepare(ctx, sess)
	if err != nil && sess.owned {
		err = errors.Join(err, sess.root.Close())
	}

	if err != nil {
		return nil, vfs.SweepResult{}, err
	}

	return sess, res, nil
}

// prepare opens the store of sess in its directory, sweeps it, creates the
// configured bucket and draws the codec.
func (p *Provider) prepare(ctx context.Context, sess *session) (vfs.SweepResult, error) {
	var err error

	sess.vfs, err = vfs.Open(sess.root)
	if err != nil {
		return vfs.SweepResult{}, fmt.Errorf("%w: %w", storage.ErrBackend, err)
	}

	res, err := sess.vfs.Sweep(ctx, sess.settings.SweepLimit, p.report)
	if err == nil && sess.settings.Bucket != "" {
		err = sess.vfs.CreateBucket(ctx, sess.settings.Bucket)
	}

	switch {
	case ctx.Err() != nil && err != nil:
		return vfs.SweepResult{}, ctx.Err() //nolint:wrapcheck // Init adds the name and the operation.
	case err != nil:
		return vfs.SweepResult{}, classify(err, !isRead)
	}

	sess.codec, err = listing.NewCodec()
	if err != nil {
		return vfs.SweepResult{}, err //nolint:wrapcheck // Init adds the name and the operation.
	}

	return res, nil
}

// begin starts a call, as lifecycle.Lifecycle.Begin does, on a Provider
// that may be nil.
func (p *Provider) begin(ctx context.Context) (*lifecycle.Session[*session], error) {
	if p == nil {
		return nil, storage.ErrNotInitialized
	}

	return p.lc.Begin(ctx) //nolint:wrapcheck // the method adds the name and the operation.
}

// report passes s to the skip handler, if there is one.
func (p *Provider) report(s vfs.Skipped) {
	if p.skip != nil {
		p.skip(s.Path, s.Err)
	}
}

// pathFree returns err, an error of an os call, without the path it
// quotes.
func pathFree(err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err
	}

	return err
}

// callContext returns a context that ends when ctx does or when a Close
// cancels sess, and the function that frees it.
func callContext(
	ctx context.Context,
	sess *lifecycle.Session[*session],
) (context.Context, func()) {
	cctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(sess.Context(), cancel)

	return cctx, func() {
		stop()
		cancel()
	}
}

// withBucket opens the bucket name of sess, runs fn on it and closes it.
// A failure of fn for want of a file, because DeleteBucket removed the
// bucket meanwhile, even if a bucket of the same name was created since,
// is an error wrapping vfs.ErrBucketNotFound.
func withBucket(sess *session, name string, fn func(*vfs.Bucket) error) error {
	bk, err := sess.vfs.OpenBucket(name)
	if err != nil {
		return err //nolint:wrapcheck // the method classifies it.
	}

	err = fn(bk)
	if err != nil && vfs.IsMissing(err) && sess.vfs.Removed(bk) {
		err = vfs.ErrBucketNotFound
	}

	cErr := bk.Close()
	if cErr != nil && err == nil {
		return cErr //nolint:wrapcheck // the method classifies it.
	}

	if cErr != nil {
		return &vfs.JoinedError{Errs: []error{err, cErr}}
	}

	return err
}
