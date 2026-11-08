package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/storage"
	"github.com/gasmod/gas/storage/internal/configsrc"
	"github.com/gasmod/gas/storage/internal/lifecycle"
	"github.com/gasmod/gas/storage/internal/listing"
)

// ServiceName is the name of the memory store, returned by Provider.Name
// and prefixed to every error the store returns.
const ServiceName = "gas/storage/memory"

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
	opCopyObject   = "copy object"
)

// ErrStorageFull indicates that a write would take the store past
// MaxBytes or MaxObjects, or that CreateBucket found MaxBuckets buckets.
var ErrStorageFull = errors.New("storage full")

// Provider is an object store held in the process's memory, as described
// in the package documentation. It is safe for concurrent use.
type Provider struct {
	// cfgProvider is the ConfigProvider Init binds from, unless a
	// configuration was given to WithConfig.
	cfgProvider gas.ConfigProvider
	// logger receives the store's events, with `service` set.
	logger gas.Logger
	// buckets holds the buckets by name. They outlive Close.
	buckets map[string]*bucket
	// cfg is the configuration the last successful Init bound, or nil
	// before the first. It outlives Close.
	cfg atomic.Pointer[Config]
	// cfgSource records the configuration given to WithConfig.
	cfgSource configsrc.Source[Config]
	// lc is the lifecycle, whose sessions hold what one Init opened.
	lc lifecycle.Lifecycle[*session]
	// mu guards buckets, the objects they hold, bytes, reserved and
	// objects. No call holds it while it reads a body.
	mu sync.Mutex
	// bytes is the sum of the sizes of the objects held.
	bytes int64
	// reserved is the room the PutObjects in flight hold for the bytes of
	// their bodies read so far.
	reserved int64
	// objects counts the objects held.
	objects int
}

// session is what one Init opened.
type session struct {
	// codec seals and opens the continuation tokens of this Init.
	codec *listing.Codec
	// settings is the resolved configuration.
	settings Settings
}

var (
	_ storage.Provider  = (*Provider)(nil)
	_ gas.Service       = (*Provider)(nil)
	_ gas.ReadyReporter = (*Provider)(nil)
)

// New returns the constructor of a Provider with opts applied, in the
// shape the gas container expects. The constructor returns an error
// wrapping storage.ErrNilLogger for a nil logger, storage.ErrNilOption for
// a nil option, and storage.ErrNilConfig for WithConfig(nil); a nil
// ConfigProvider is accepted, and Init then requires WithConfig.
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Provider, error) {
	return func(cfgProvider gas.ConfigProvider, logger gas.Logger) (*Provider, error) {
		if logger == nil {
			return nil, fmt.Errorf("%s: %w", ServiceName, storage.ErrNilLogger)
		}

		p := &Provider{
			cfgProvider: cfgProvider,
			logger:      logger.With().Str("service", ServiceName).Logger(),
			buckets:     make(map[string]*bucket),
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

		if p.cfgSource.IsNil() {
			return nil, fmt.Errorf("%s: %w", ServiceName, storage.ErrNilConfig)
		}

		return p, nil
	}
}

// Name returns ServiceName.
func (*Provider) Name() string { return ServiceName }

// Init resolves the configuration, opens the store and creates the
// configured bucket, as described in the package documentation. On an
// initialized store it returns storage.ErrAlreadyInitialized and changes
// nothing.
func (p *Provider) Init(ctx context.Context) error {
	if p == nil {
		return opError(opInit, storage.ErrNotInitialized)
	}

	var set Settings

	err := p.lc.Init(ctx, func() (*session, error) {
		cfg, err := p.cfgSource.Resolve(p.cfgProvider, DefaultConfig, rules)
		if err != nil {
			return nil, err //nolint:wrapcheck // Init adds the name and the operation.
		}

		codec, err := listing.NewCodec()
		if err != nil {
			return nil, err //nolint:wrapcheck // Init adds the name and the operation.
		}

		if cfg.Storage.Bucket != "" {
			err = p.createBucket(cfg.Storage.Bucket)
			if err != nil {
				return nil, err
			}
		}

		set = cfg.Storage

		// Nothing fails once this function returns, so the Init succeeds.
		p.cfg.Store(new(*cfg))

		return &session{codec: codec, settings: set}, nil
	})
	if err != nil {
		return opError(opInit, err)
	}

	p.logger.Info("service initialized").
		Bool("bucket", set.Bucket != "").
		Int64("max_bytes", set.MaxBytes).
		Int("max_objects", set.MaxObjects).
		Send()

	return nil
}

// Config returns a copy of the configuration the last successful Init
// bound, defaults included, as described in the package documentation:
// the zero Config before the first successful Init, and on a nil
// Provider, and the last one bound after Close. Changing it changes
// nothing in the store. It is safe to call at any time, while Init or
// Close runs included.
func (p *Provider) Config() Config {
	if p == nil {
		return Config{}
	}

	cfg := p.cfg.Load()
	if cfg == nil {
		return Config{}
	}

	return *cfg
}

// Close makes every later call but Init return an error wrapping
// storage.ErrClosed, and waits for every call in flight to return, as
// described in the package documentation. When ctx ends first, it
// cancels the calls in flight, which return storage.ErrClosed, and
// returns an error wrapping ctx's error. The buckets and objects stay.
// Close on a store that is not initialized returns nil and logs nothing.
func (p *Provider) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}

	sess, err := p.lc.Close(ctx)
	if sess == nil {
		return nil
	}

	if err != nil {
		err = opError(opClose, err)
		p.logger.Warn("service closed, calls in flight canceled").Err("error", err).Send()

		return err
	}

	p.logger.Info("service closed").Send()

	return nil
}

// CheckReady reports readiness without I/O: an error wrapping
// storage.ErrNotInitialized or storage.ErrClosed when the store is not
// initialized, the context's error when ctx has ended, and else nil.
func (p *Provider) CheckReady(ctx context.Context) error {
	sess, err := p.begin(ctx)
	if err != nil {
		return opError(opReady, err)
	}

	sess.End()

	return nil
}

// begin starts a call, as lifecycle.Lifecycle.Begin does, on a Provider
// that may be nil.
func (p *Provider) begin(ctx context.Context) (*lifecycle.Session[*session], error) {
	if p == nil {
		return nil, storage.ErrNotInitialized
	}

	return p.lc.Begin(ctx) //nolint:wrapcheck // the method adds the name and the operation.
}

// opError returns err as the failure of op, after ServiceName.
func opError(op string, err error) error {
	return fmt.Errorf("%s: %s: %w", ServiceName, op, err)
}
