package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gasmod/gas"
)

// ServiceName is the name of the database service, returned by
// Service.Name and prefixed to every error the service returns.
const ServiceName = "gas/database"

var (
	// ErrNilLogger indicates that the constructor was given a nil logger.
	ErrNilLogger = errors.New("logger is nil")
	// ErrNilConfig indicates that WithConfig was given a nil
	// configuration.
	ErrNilConfig = errors.New("config is nil")
	// ErrNoConfigProvider indicates that there is neither a configuration
	// given to WithConfig nor a ConfigProvider to bind one from.
	ErrNoConfigProvider = errors.New("no config provider")
	// ErrInvalidConfig indicates that the configuration failed to bind,
	// failed its validate tags, or broke one of the package's rules.
	ErrInvalidConfig = errors.New("invalid config")
	// ErrNotInitialized indicates that a method was called before Init
	// succeeded, after a failed Init, or after Close.
	ErrNotInitialized = errors.New("service not initialized")
	// ErrAlreadyInitialized indicates that Init was called on a service
	// that is initialized.
	ErrAlreadyInitialized = errors.New("service already initialized")
	// ErrDriverNotRegistered indicates that the database/sql driver the
	// dialect needs is not registered.
	ErrDriverNotRegistered = errors.New("driver not registered")
	// ErrInvalidDSN indicates that the dialect's driver could not parse
	// Settings.DSN.
	ErrInvalidDSN = errors.New("invalid DSN")
	// ErrConnect indicates that connecting to the database, or pinging
	// it, failed.
	ErrConnect = errors.New("connect failed")
	// ErrModeNotPgx indicates that a pgx transaction was asked of a
	// service that is not in ModePgx.
	ErrModeNotPgx = errors.New("mode is not pgx")
	// ErrNilFunc indicates that WithTx or WithPgxTx was given a nil
	// function.
	ErrNilFunc = errors.New("transaction function is nil")
	// ErrCloseTimeout indicates that the context of Close ended before
	// closing finished. The service is uninitialized, and closing goes on.
	ErrCloseTimeout = errors.New("close timed out")
)

// Service connects to a database and provides it to the app, as described
// in the package documentation. It is safe for concurrent use.
type Service struct {
	// cfgProvider is the ConfigProvider Init binds from, unless a
	// configuration was given to WithConfig.
	cfgProvider gas.ConfigProvider
	// logger receives the service's events, with `service` set.
	logger gas.Logger
	// connector is the connector given to WithConnector, or nil.
	connector driver.Connector
	// state is what Init opened, or nil before Init succeeds, after a
	// failed Init and after Close. It is swapped whole, so a method sees
	// one Init's state.
	state atomic.Pointer[state]
	// bound is the configuration the last successful Init resolved, or
	// nil before one. Close keeps it. Config returns a copy of it.
	bound atomic.Pointer[Config]
	// cfgSource records the configuration given to WithConfig.
	cfgSource configSource
	// connectorSpec records what WithConnector was given.
	connectorSpec connectorSpec
	// mu makes Init and the start of Close run one at a time, so that a
	// Close never misses what a running Init opens. Close holds it only
	// to take the state, not while closing. Readers do not take it.
	mu sync.Mutex
}

var (
	_ gas.Service          = (*Service)(nil)
	_ gas.DatabaseProvider = (*Service)(nil)
	_ gas.HealthReporter   = (*Service)(nil)
	_ gas.ReadyReporter    = (*Service)(nil)
)

// state is what one Init opened, with the configuration it opened it
// with. It never changes once published.
type state struct {
	// db is the database/sql handle. In ModePgx it is derived from pool.
	db *sql.DB
	// pool is the native pool in ModePgx, or nil in ModeSQL.
	pool *pgxpool.Pool
	// dialect is the dialect Driver returns.
	dialect Dialect
	// settings is the resolved configuration.
	settings Settings
}

// New returns the constructor of a Service with opts applied, in the shape
// the gas container expects. The constructor returns an error wrapping
// ErrNilLogger for a nil logger; a nil ConfigProvider is accepted, and
// Init then requires WithConfig.
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Service, error) {
	return func(cfgProvider gas.ConfigProvider, logger gas.Logger) (*Service, error) {
		if logger == nil {
			return nil, fmt.Errorf("%s: %w", ServiceName, ErrNilLogger)
		}

		s := &Service{
			cfgProvider: cfgProvider,
			logger:      logger.With().Str("service", ServiceName).Logger(),
		}
		for _, opt := range opts {
			opt(s)
		}

		return s, nil
	}
}

// Name returns ServiceName.
func (*Service) Name() string { return ServiceName }

// Init resolves and checks the configuration, connects, and publishes the
// connection, as described in the package documentation. On an
// initialized service it returns ErrAlreadyInitialized and changes
// nothing. A failed Init closes what it opened and leaves the service
// uninitialized.
func (s *Service) Init(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state.Load() != nil {
		return fmt.Errorf("%s: %w", ServiceName, ErrAlreadyInitialized)
	}

	cfg, err := s.cfgSource.resolve(s.cfgProvider, rules(s.connectorSpec))
	if err != nil {
		return fmt.Errorf("%s: %w", ServiceName, err)
	}

	st, err := s.connect(ctx, &cfg.Database)
	if err != nil {
		return err
	}

	s.state.Store(st)
	s.bound.Store(&Config{Database: st.settings})

	s.logger.Info("service initialized").
		Str("mode", string(st.settings.Mode)).
		Str("dialect", string(st.dialect)).
		Send()

	return nil
}

// Close makes the service uninitialized and closes the database handle
// and, in ModePgx, the pool, as described in the package documentation.
// The pool is closed even when closing the handle fails. Close waits for
// the closing until ctx ends, and then returns an error wrapping
// ErrCloseTimeout and ctx's error while the closing goes on. Close on a
// service that is not initialized returns nil.
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()

	st := s.state.Swap(nil)

	s.mu.Unlock()

	if st == nil {
		return nil
	}

	done := make(chan error, 1)

	go func() { done <- st.close() }()

	select {
	case err := <-done:
		return s.closed(ctx, err)
	case <-ctx.Done():
		go s.closedLate(done)

		return fmt.Errorf("%s: %w: %w", ServiceName, ErrCloseTimeout, ctx.Err())
	}
}

// close closes the handle and the pool, and returns the error of closing
// the handle, as the driver returned it.
func (st *state) close() error {
	err := st.db.Close()

	if st.pool != nil {
		st.pool.Close()
	}

	return err //nolint:wrapcheck // Close and closedLate classify it.
}

// Config returns a copy of the configuration the last successful Init
// resolved, defaults included, or the zero Config before one. A failed
// Init and Close leave it as it was. Config holds no slice, map or
// pointer, so the copy shares nothing with the service, and DSN stays a
// gas.Secret.
func (s *Service) Config() Config {
	cfg := s.bound.Load()
	if cfg == nil {
		return Config{}
	}

	return *cfg
}

// DB returns the database/sql handle, or nil when the service is not
// initialized. In ModePgx it is derived from the pool.
func (s *Service) DB() *sql.DB {
	st := s.state.Load()
	if st == nil {
		return nil
	}

	return st.db
}

// Pool returns the native pool in ModePgx, or nil in ModeSQL and when the
// service is not initialized. PoolFrom reaches it through a
// gas.DatabaseProvider.
func (s *Service) Pool() *pgxpool.Pool {
	st := s.state.Load()
	if st == nil {
		return nil
	}

	return st.pool
}

// Driver returns the dialect, `postgres`, `mysql` or `sqlite`, as a
// string, or an empty string when the service is not initialized. With a
// connector it is the dialect given to WithConnector.
func (s *Service) Driver() string {
	st := s.state.Load()
	if st == nil {
		return ""
	}

	return string(st.dialect)
}

// CheckHealth reports liveness without I/O: an error wrapping
// ErrNotInitialized when the service is not initialized, else nil.
func (s *Service) CheckHealth(context.Context) error {
	_, err := s.loaded()

	return err
}

// CheckReady reports readiness by pinging the database, as Ping does.
func (s *Service) CheckReady(ctx context.Context) error {
	return s.Ping(ctx)
}

// Ping checks that the database answers, within ctx and
// Settings.ConnectTimeout. A failure wraps ErrConnect and names a fixed
// reason, as described in the package documentation.
func (s *Service) Ping(ctx context.Context) error {
	st, err := s.loaded()
	if err != nil {
		return err
	}

	pingCtx, cancel := context.WithTimeout(ctx, st.settings.ConnectTimeout)
	defer cancel()

	if st.pool != nil {
		err = st.pool.Ping(pingCtx)
	} else {
		err = st.db.PingContext(pingCtx)
	}

	if err != nil {
		return connectError(ctx, "ping", err)
	}

	return nil
}

// loaded returns the state of the last successful Init, or an error
// wrapping ErrNotInitialized.
func (s *Service) loaded() (*state, error) {
	st := s.state.Load()
	if st == nil {
		return nil, fmt.Errorf("%s: %w", ServiceName, ErrNotInitialized)
	}

	return st, nil
}

// closed returns the result of a Close with ctx whose closing finished
// in time with err, logging `service closed` when err is nil.
func (s *Service) closed(ctx context.Context, err error) error {
	if err != nil {
		return runtimeError(ctx, "close", err)
	}

	s.logger.Info("service closed").Send()

	return nil
}

// closedLate waits for a closing that Close stopped waiting for, and logs
// how it ended: `service closed` at info level, or `service close failed`
// at warn level with the reason.
func (s *Service) closedLate(done <-chan error) {
	err := <-done
	if err != nil {
		s.logger.Warn("service close failed").Str("reason", reason(err)).Send()

		return
	}

	s.logger.Info("service closed").Send()
}
