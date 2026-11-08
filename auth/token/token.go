package token

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/cryptoutil"
	"github.com/gasmod/gas/auth/internal/svcutil"
	"github.com/gasmod/gas/auth/internal/tokenstore"
)

// ServiceName is the name of the service, which prefixes its errors.
const ServiceName = "gas/auth/token"

// cleanupTaskName names the expired-token cleanup in its log events.
const cleanupTaskName = "token cleanup"

var (
	// ErrTokenInvalid indicates that a token does not exist, was consumed
	// or revoked already, or was issued for another purpose. It wraps
	// auth.ErrUnauthenticated.
	ErrTokenInvalid = fmt.Errorf("%w: invalid token", auth.ErrUnauthenticated)
	// ErrTokenExpired indicates that a token was issued for the purpose
	// asked for and has expired. It wraps auth.ErrCredentialsExpired, and
	// so auth.ErrUnauthenticated.
	ErrTokenExpired = fmt.Errorf("%w: token expired", auth.ErrCredentialsExpired)
	// ErrInvalidTTL indicates that Issue was given a negative ttl.
	ErrInvalidTTL = errors.New("invalid ttl")

	// ErrNotInitialized indicates that a method was called before Init
	// succeeded, after an Init that failed, or after Close.
	ErrNotInitialized = svcutil.ErrNotInitialized
	// ErrNilLogger indicates that the constructor was given a nil logger.
	ErrNilLogger = svcutil.ErrNilLogger
	// ErrNilConfig indicates that WithConfig was given nil.
	ErrNilConfig = svcutil.ErrNilConfig
	// ErrNoConfigProvider indicates that the service has neither a
	// configuration from WithConfig nor a ConfigProvider to bind one from.
	ErrNoConfigProvider = svcutil.ErrNoConfigProvider
	// ErrInvalidConfig indicates that the configuration failed to bind,
	// failed its validate tags, or broke a rule.
	ErrInvalidConfig = svcutil.ErrInvalidConfig
)

// Service issues and verifies single-use tokens, as described in the
// package documentation. It is safe for concurrent use.
type Service struct {
	// db is the database each Init builds a store on.
	db gas.DatabaseProvider
	// logger receives the service's log events.
	logger gas.Logger
	// migMgr receives the store's migrations in Init.
	migMgr gas.MigrationManager
	// cfgProvider is what Init binds the configuration from when
	// WithConfig was not used.
	cfgProvider gas.ConfigProvider
	// state is what the last successful Init set up, or nil before Init
	// succeeds, after an Init that failed, and after Close. The methods
	// load it once per call.
	state atomic.Pointer[state]
	// bound is the configuration the last successful Init resolved, or
	// nil before one. Close keeps it. Config returns a copy of it.
	bound atomic.Pointer[Config]
	// cfgSource records the configuration given to WithConfig.
	cfgSource svcutil.ConfigSource[Config]
	// cleanup deletes expired tokens in the background between Init and
	// Close.
	cleanup svcutil.Loop
	// lifecycle makes Init and Close one at a time, so the state they
	// publish and the cleanup they start or stop always agree. The other
	// methods never take it.
	lifecycle sync.Mutex
}

// state is what one successful Init set up. It is never changed after it
// is published, so a call that loaded it keeps a consistent view.
type state struct {
	// cfg is the resolved configuration.
	cfg *Config
	// store keeps the tokens.
	store *tokenstore.Store
}

var (
	_ gas.Service = (*Service)(nil)
	_ Provider    = (*Service)(nil)
)

// New returns a constructor for a Service, in the shape the dependency
// injection container calls. The constructor applies opts and fails only
// for a nil logger, with an error wrapping ErrNilLogger; the
// configuration is resolved by Init.
func New(
	opts ...Option,
) func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager, gas.ConfigProvider) (*Service, error) {
	return func(
		dbProv gas.DatabaseProvider,
		logger gas.Logger,
		migMgr gas.MigrationManager,
		cfgProvider gas.ConfigProvider,
	) (*Service, error) {
		if logger == nil {
			return nil, fmt.Errorf("%s: %w", ServiceName, ErrNilLogger)
		}

		s := &Service{
			db:          dbProv,
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

// Init resolves and checks the configuration, builds a new store and
// registers its migrations, publishes them, and restarts the background
// cleanup on the new store. Calls running at the same time use the
// earlier Init's store until the new one is published. An Init that fails
// leaves the service uninitialized, with its cleanup stopped. The context
// is unused: the cleanup runs until Close, not until the context ends.
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
		Run:      s.deleteExpired(st.store),
		Logger:   s.logger.With().Str("service", ServiceName).Logger(),
		Name:     cleanupTaskName,
		Interval: st.cfg.Token.CleanupInterval,
		Timeout:  st.cfg.Token.CleanupTimeout,
	})

	s.logger.Info("service initialized").Str("service", ServiceName).Send()

	return nil
}

// Close stops the background cleanup and waits for a deletion in progress
// to end. The methods then return ErrNotInitialized until Init runs
// again. Close never fails, and does nothing to a service that is not
// initialized.
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
// pointer, so the copy shares nothing with the service.
func (s *Service) Config() Config {
	cfg := s.bound.Load()
	if cfg == nil {
		return Config{}
	}

	return *cfg
}

// Issue stores a new token for subject and purpose that expires ttl from
// now, and returns it. A ttl of 0 uses Settings.DefaultTTL; a negative ttl
// is an error wrapping ErrInvalidTTL. An empty subject is an error
// wrapping auth.ErrEmptySubject, and a subject or purpose longer than
// auth.MaxValueLength bytes one wrapping auth.ErrValueTooLong. Only the
// token's hash is stored, and nothing is stored on error.
func (s *Service) Issue(
	ctx context.Context,
	subject, purpose string,
	ttl time.Duration,
) (string, error) {
	st, err := s.current()
	if err != nil {
		return "", err
	}

	err = checkIssue(subject, purpose, ttl)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ServiceName, err)
	}

	if ttl == 0 {
		ttl = st.cfg.Token.DefaultTTL
	}

	rawToken, err := cryptoutil.RandomString(st.cfg.Token.TokenLength)
	if err != nil {
		return "", fmt.Errorf("%s: generate token: %w", ServiceName, err)
	}

	now := time.Now()

	err = st.store.InsertToken(
		ctx,
		uuid.NewString(),
		subject,
		cryptoutil.SHA256Hex(rawToken),
		purpose,
		now,
		now.Add(ttl),
	)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ServiceName, err)
	}

	return rawToken, nil
}

// checkIssue checks Issue's arguments, in the order the package
// documentation gives.
func checkIssue(subject, purpose string, ttl time.Duration) error {
	if ttl < 0 {
		return fmt.Errorf("%w: must not be negative", ErrInvalidTTL)
	}

	if subject == "" {
		return auth.ErrEmptySubject
	}

	err := auth.CheckLength("subject", subject)
	if err != nil {
		//nolint:wrapcheck // Issue wraps it with the service's name.
		return err
	}

	//nolint:wrapcheck // Issue wraps it with the service's name.
	return auth.CheckLength("purpose", purpose)
}

// Verify consumes the token and returns its subject when it was issued
// for purpose and has not expired. The token is deleted whatever the
// outcome, in one atomic step, so it verifies at most once. It returns an
// error wrapping ErrTokenInvalid for a token that does not exist, was
// consumed already, or was issued for another purpose, and one wrapping
// ErrTokenExpired for an expired one.
func (s *Service) Verify(ctx context.Context, rawToken, purpose string) (string, error) {
	st, err := s.current()
	if err != nil {
		return "", err
	}

	rec, err := st.store.ConsumeTokenByHash(ctx, cryptoutil.SHA256Hex(rawToken))
	if errors.Is(err, tokenstore.ErrNotFound) {
		return "", fmt.Errorf("%s: %w", ServiceName, ErrTokenInvalid)
	}

	if err != nil {
		return "", fmt.Errorf("%s: %w", ServiceName, err)
	}

	// The purpose is checked first, so a token presented for the wrong
	// flow says nothing about its expiry.
	if rec.Purpose != purpose {
		return "", fmt.Errorf("%s: %w", ServiceName, ErrTokenInvalid)
	}

	if !time.Now().Before(rec.ExpiresAt) {
		return "", fmt.Errorf("%s: %w", ServiceName, ErrTokenExpired)
	}

	return rec.Subject, nil
}

// Revoke deletes the token, whatever its purpose and expiry. A token that
// is not stored, including one consumed or revoked already, is not an
// error, so Revoke may be called again.
func (s *Service) Revoke(ctx context.Context, rawToken string) error {
	st, err := s.current()
	if err != nil {
		return err
	}

	_, err = st.store.DeleteTokenByHash(ctx, cryptoutil.SHA256Hex(rawToken))
	if err != nil {
		return fmt.Errorf("%s: %w", ServiceName, err)
	}

	return nil
}

// RevokeAllByPurpose deletes every token of subject issued for purpose,
// expired ones included. Deleting none is not an error.
func (s *Service) RevokeAllByPurpose(ctx context.Context, subject, purpose string) error {
	st, err := s.current()
	if err != nil {
		return err
	}

	err = st.store.DeleteTokensBySubjectPurpose(ctx, subject, purpose)
	if err != nil {
		return fmt.Errorf("%s: %w", ServiceName, err)
	}

	return nil
}

// newState resolves the configuration and builds and initializes a store,
// for Init to publish.
func (s *Service) newState() (*state, error) {
	cfg, err := s.cfgSource.Resolve(s.cfgProvider, DefaultConfig, checkRules)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ServiceName, err)
	}

	store, err := tokenstore.New()(s.db, s.logger, s.migMgr)
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

// current returns the state the last successful Init published, or an
// error wrapping ErrNotInitialized.
func (s *Service) current() (*state, error) {
	st := s.state.Load()
	if st == nil {
		return nil, fmt.Errorf("%s: %w", ServiceName, ErrNotInitialized)
	}

	return st, nil
}

// deleteExpired returns one run of the background cleanup on store: it
// deletes the tokens that have expired, and logs how many at debug level.
// The cleanup loop logs its error.
func (s *Service) deleteExpired(store *tokenstore.Store) func(context.Context) error {
	return func(ctx context.Context) error {
		n, err := store.DeleteExpiredTokens(ctx, time.Now())
		if err != nil {
			//nolint:wrapcheck // the store's errors name it and the operation; the loop logs them with the task's name.
			return err
		}

		s.logger.Debug("expired tokens deleted").
			Str("service", ServiceName).
			Int64("count", n).
			Send()

		return nil
	}
}
