package apikey

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/apikeystore"
	"github.com/gasmod/gas/auth/internal/svcutil"
)

const (
	// ServiceName is the service's name, which prefixes its errors.
	ServiceName = "gas/auth/apikey"
	// PrincipalMetadataKeyScopes is the principal metadata key that holds
	// a key's scopes, as a []string. Key metadata may not use it.
	PrincipalMetadataKeyScopes = "scopes"
)

// cleanupTaskName names the expired-key cleanup in its log events.
const cleanupTaskName = "api key cleanup"

var (
	// ErrNilConfig indicates that WithConfig was given a nil
	// configuration. It is svcutil's sentinel, so either matches.
	ErrNilConfig = svcutil.ErrNilConfig
	// ErrNoConfigProvider indicates that the service has neither a
	// configuration given to WithConfig nor a ConfigProvider.
	ErrNoConfigProvider = svcutil.ErrNoConfigProvider
	// ErrInvalidConfig indicates that the configuration failed to bind,
	// failed its validate tags, or broke one of the service's rules.
	ErrInvalidConfig = svcutil.ErrInvalidConfig
	// ErrNotInitialized indicates that a method was called before Init
	// succeeded, after a failed Init, or after Close.
	ErrNotInitialized = svcutil.ErrNotInitialized
	// ErrNilLogger indicates that New's constructor was given a nil
	// logger. It is svcutil's sentinel, so either matches.
	ErrNilLogger = svcutil.ErrNilLogger
	// ErrEmptySubject indicates that Generate was given an empty subject.
	// It is auth.ErrEmptySubject, so either matches.
	ErrEmptySubject = auth.ErrEmptySubject
	// ErrValueTooLong indicates that Generate was given a subject or a
	// name longer than auth.MaxValueLength bytes. It is
	// auth.ErrValueTooLong, so either matches.
	ErrValueTooLong = auth.ErrValueTooLong
	// ErrNilPrincipal indicates that Revoke or Delete was given a nil
	// principal or a nil pointer. It is auth.ErrNilPrincipal, so either
	// matches.
	ErrNilPrincipal = auth.ErrNilPrincipal
	// ErrSchemeMismatch indicates that Revoke or Delete was given a
	// principal of another scheme than auth.SchemeAPIKey. It is
	// auth.ErrSchemeMismatch, so either matches.
	ErrSchemeMismatch = auth.ErrSchemeMismatch
	// ErrReservedMetadataKey indicates that Generate was given metadata
	// holding PrincipalMetadataKeyScopes.
	ErrReservedMetadataKey = errors.New("reserved metadata key")
	// ErrInvalidMetadata indicates that Generate was given metadata that
	// does not encode as JSON.
	ErrInvalidMetadata = errors.New("invalid metadata")
	// ErrInvalidScope indicates that Generate was given a scope that is
	// empty, holds a comma, or starts or ends with white space.
	ErrInvalidScope = errors.New("invalid scope")
	// ErrInvalidTTL indicates that Generate was given a TTL that is not
	// positive.
	ErrInvalidTTL = errors.New("invalid ttl")
	// ErrInvalidExpiry indicates that Generate was given an expiry time
	// that is not in the future.
	ErrInvalidExpiry = errors.New("invalid expiry")
)

// Service authenticates requests by API key and manages the keys, as
// described in the package documentation. It is safe for concurrent use.
type Service struct {
	// db is the database the store uses.
	db gas.DatabaseProvider
	// logger receives the service's log events.
	logger gas.Logger
	// migMgr receives the store's migrations in Init.
	migMgr gas.MigrationManager
	// cfgProvider binds the configuration when WithConfig is not used.
	cfgProvider gas.ConfigProvider
	// root is the Service whose state a Provider WithTx returned uses, or
	// nil for a Service New built.
	root *Service
	// tx is the transaction a Provider WithTx returned runs in, or nil.
	tx *sql.Tx
	// state is what the last successful Init set up, or nil before it,
	// after a failed Init, and after Close.
	state atomic.Pointer[state]
	// bound is the configuration the last successful Init resolved, or
	// nil before one. Close keeps it. Config returns a copy of it.
	bound atomic.Pointer[Config]
	// cfgSource records the configuration given to WithConfig.
	cfgSource svcutil.ConfigSource[Config]
	// cleanup runs the expired-key cleanup between Init and Close.
	cleanup svcutil.Loop
	// lifecycle makes Init and Close one at a time, so the state they
	// publish and the cleanup they start or stop always agree. The other
	// methods never take it.
	lifecycle sync.Mutex
}

// state is what one successful Init set up. It is never changed after it
// is published, so a request that loaded it keeps a consistent view.
type state struct {
	// cfg is the resolved configuration.
	cfg *Config
	// store reads and writes the keys.
	store *apikeystore.Store
}

var (
	_ gas.Service           = (*Service)(nil)
	_ auth.Authenticator    = (*Service)(nil)
	_ auth.PrincipalRevoker = (*Service)(nil)
	_ Provider              = (*Service)(nil)
)

// New returns a constructor for a Service, in the shape the dependency
// injection container calls. The constructor applies opts, and fails only
// for a nil logger, with ErrNilLogger; the configuration is resolved and
// checked in Init.
func New(
	opts ...Option,
) func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager, gas.ConfigProvider) (*Service, error) {
	return func(
		db gas.DatabaseProvider,
		logger gas.Logger,
		migMgr gas.MigrationManager,
		cfgProvider gas.ConfigProvider,
	) (*Service, error) {
		if logger == nil {
			return nil, fmt.Errorf("%s: %w", ServiceName, ErrNilLogger)
		}

		s := &Service{
			db:          db,
			logger:      logger,
			migMgr:      migMgr,
			cfgProvider: cfgProvider,
		}

		for _, opt := range opts {
			opt(s)
		}

		return s, nil
	}
}

// Name returns ServiceName.
func (s *Service) Name() string { return ServiceName }

// Init resolves and checks the configuration, sets up the store and
// registers its migrations, and starts the expired-key cleanup when it is
// enabled, as described in the package documentation. A failed Init
// stops the cleanup and leaves the service uninitialized. The context is
// unused.
func (s *Service) Init(context.Context) error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()

	st, err := s.newState()
	if err != nil {
		s.state.Store(nil)
		s.cleanup.Stop()

		return err
	}

	s.state.Store(st)
	s.bound.Store(st.cfg)

	//nolint:contextcheck // the cleanup outlives Init's context; Close stops it.
	s.cleanup.Start(svcutil.Task{
		Name:     cleanupTaskName,
		Interval: st.cfg.APIKey.CleanupInterval,
		Timeout:  st.cfg.APIKey.CleanupTimeout,
		Logger:   s.logger.With().Str("service", ServiceName).Logger(),
		Run:      s.deleteExpired(st.store),
	})

	s.logger.Info("service initialized").Str("service", ServiceName).Send()

	return nil
}

// Close stops the expired-key cleanup, waits for a run in progress to
// return, and uninitializes the service: its methods, and those of the
// Providers WithTx returned, fail with ErrNotInitialized until Init
// succeeds again. The context is unused.
func (s *Service) Close(context.Context) error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()

	s.state.Store(nil)
	s.cleanup.Stop()

	return nil
}

// Config returns a copy of the configuration the last successful Init
// resolved, defaults included, or the zero Config before one. A failed
// Init and Close leave it as it was. Config holds no slice, map or
// pointer, so the copy shares nothing with the service. On a Provider WithTx returned, it is the
// configuration of the service it came from.
func (s *Service) Config() Config {
	if s.root != nil {
		return s.root.Config()
	}

	cfg := s.bound.Load()
	if cfg == nil {
		return Config{}
	}

	return *cfg
}

// WithTx returns a Provider whose operations run in tx, as described in
// the package documentation. The caller owns tx; the Provider must not
// outlive it. The Provider uses what the service's last successful Init
// set up, at each call, so its methods fail with ErrNotInitialized
// whenever the service's do.
func (s *Service) WithTx(tx *sql.Tx) Provider {
	root := s
	if s.root != nil {
		root = s.root
	}

	return &Service{
		db:          s.db,
		logger:      s.logger,
		migMgr:      s.migMgr,
		cfgProvider: s.cfgProvider,
		root:        root,
		tx:          tx,
	}
}

// newState resolves the configuration and builds and initializes a store,
// for Init to publish.
func (s *Service) newState() (*state, error) {
	cfg, err := s.cfgSource.Resolve(s.cfgProvider, DefaultConfig, checkRules)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ServiceName, err)
	}

	store, err := apikeystore.New()(s.db, s.logger, s.migMgr)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ServiceName, err)
	}

	err = store.Init(s)
	if err != nil {
		//nolint:wrapcheck // Store.Init starts its errors with the service's name.
		return nil, err
	}

	return &state{cfg: cfg, store: store}, nil
}

// current returns the state the last successful Init set up, with the
// store bound to the transaction of a Provider WithTx returned, or an
// error wrapping ErrNotInitialized.
func (s *Service) current() (*state, error) {
	if s.root == nil {
		st := s.state.Load()
		if st == nil {
			return nil, fmt.Errorf("%s: %w", ServiceName, ErrNotInitialized)
		}

		return st, nil
	}

	st, err := s.root.current()
	if err != nil {
		return nil, err
	}

	return &state{cfg: st.cfg, store: st.store.WithTx(s.tx)}, nil
}

// deleteExpired returns the cleanup task's run: it removes the keys of
// store that have expired, and logs how many at debug level.
func (s *Service) deleteExpired(store *apikeystore.Store) func(context.Context) error {
	return func(ctx context.Context) error {
		n, err := store.DeleteExpiredKeys(ctx, time.Now())
		if err != nil {
			//nolint:wrapcheck // the store prefixes its errors; the Loop logs them with the task's name.
			return err
		}

		s.logger.Debug("expired api keys deleted").
			Str("service", ServiceName).
			Int64("count", n).
			Send()

		return nil
	}
}
