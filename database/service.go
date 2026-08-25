package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/gasmod/gas"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	serviceName = "gas/database"
)

// Service manages a database connection and implements both gas.Service
// and gas.DatabaseProvider. In ModeSQL it wraps *sql.DB with any driver.
// In ModePgx it creates a native pgxpool.Pool and derives *sql.DB from
// it via the pgx stdlib adapter, so DB() always works regardless of mode.
type Service struct {
	db                   *sql.DB
	pool                 *pgxpool.Pool // non-nil only in ModePgx
	connector            driver.Connector
	cfg                  *Config
	cfgProvider          gas.ConfigProvider
	logger               gas.Logger
	customConfigProvided bool
	closed               atomic.Bool
}

var _ gas.Service = (*Service)(nil)
var _ gas.DatabaseProvider = (*Service)(nil)
var _ gas.HealthReporter = (*Service)(nil)
var _ gas.ReadyReporter = (*Service)(nil)

// Option configures a Service.
type Option func(*Service)

// WithConfig sets the database configuration.
func WithConfig(cfg *Config) Option {
	return func(s *Service) {
		s.cfg = cfg
		s.customConfigProvided = true
	}
}

// WithConnector sets a driver.Connector for ModeSQL. When provided,
// sql.OpenDB(connector) is used instead of sql.Open(driver, dsn), and
// Database.Driver / Database.DSN are not required.
func WithConnector(c driver.Connector) Option {
	return func(s *Service) {
		s.connector = c
	}
}

// New captures options and returns a DI-injectable constructor.
// The returned func receives gas.ConfigProvider and gas.Logger from the
// DI container.
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) *Service {
	return func(cfgProvider gas.ConfigProvider, logger gas.Logger) *Service {
		s := &Service{
			cfg:         DefaultConfig(),
			cfgProvider: cfgProvider,
			logger:      logger.With().Str("service", serviceName).Logger(),
		}
		for _, opt := range opts {
			opt(s)
		}
		return s
	}
}

// Name returns the service identifier.
func (s *Service) Name() string {
	return serviceName
}

// Init opens the database connection, configures the pool, and pings
// the database to verify connectivity.
func (s *Service) Init() error {
	if !s.customConfigProvided {
		// no custom config provided, try to bind from config service
		if s.cfgProvider != nil {
			if err := s.cfgProvider.Bind(s.cfg); err != nil {
				return fmt.Errorf("%s: config binding: %w", s.Name(), err)
			}
		}
	}

	s.cfg.hasConnector = s.connector != nil

	if err := s.cfg.Validate(); err != nil {
		s.logger.Error("invalid database configuration").Err("error", err).Send()
		return err
	}

	var initFn func() error
	switch s.cfg.Database.Mode {
	case ModePgx:
		initFn = s.initPgx
	case ModeSQL, "":
		initFn = s.initSQL
	default:
		s.logger.Error("unknown database mode").Str("mode", s.cfg.Database.Mode).Send()
		return fmt.Errorf("%s: unknown mode %q", s.Name(), s.cfg.Database.Mode)
	}

	if err := s.connectWithRetry(initFn); err != nil {
		return err
	}

	s.closed.Store(false)
	return nil
}

func (s *Service) connectWithRetry(initFn func() error) error {
	err := initFn()
	if err == nil {
		return nil
	}

	maxRetries := s.cfg.Database.ConnRetries
	if maxRetries <= 0 {
		return err
	}

	interval := s.cfg.Database.ConnRetryInterval
	if interval <= 0 {
		interval = defaultConnRetryInterval
	}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		s.logger.Warn("database connection failed, retrying").
			Err("error", err).
			Int("attempt", attempt).
			Int("max_retries", maxRetries).
			Str("next_retry_in", interval.String()).
			Send()

		time.Sleep(interval)
		interval *= 2

		err = initFn()
		if err == nil {
			return nil
		}
	}

	s.logger.Error("database connection failed after all retries").
		Err("error", err).
		Int("retries", maxRetries).
		Send()
	return fmt.Errorf("%s: connect failed after %d retries: %w", s.Name(), maxRetries, err)
}

func (s *Service) initSQL() error {
	var db *sql.DB
	if s.connector != nil {
		db = sql.OpenDB(s.connector)
	} else {
		var err error
		db, err = sql.Open(s.cfg.Database.Driver, s.cfg.Database.DSN)
		if err != nil {
			s.logger.Error("failed to open database connection").Err("error", err).Send()
			return fmt.Errorf("%s: open: %w", s.Name(), err)
		}
	}

	db.SetMaxOpenConns(int(s.cfg.Database.MaxOpenConns))
	db.SetMaxIdleConns(s.cfg.Database.MaxIdleConns)
	db.SetConnMaxLifetime(s.cfg.Database.ConnMaxLifetime)
	db.SetConnMaxIdleTime(s.cfg.Database.ConnMaxIdleTime)

	ctx, cancel := context.WithTimeout(context.Background(), defaultPingTimeout)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		if cErr := db.Close(); cErr != nil {
			s.logger.Error("failed to close database connection after failed ping").Err("error", cErr).Send()
		}
		s.logger.Error("failed to ping database connection").Err("error", err).Send()
		return fmt.Errorf("%s: ping: %w", s.Name(), err)
	}

	s.logger.Info("SQL database connection initialized").Send()

	s.db = db
	return nil
}

func (s *Service) initPgx() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultPingTimeout)
	defer cancel()

	poolCfg, err := pgxpool.ParseConfig(s.cfg.Database.DSN)
	if err != nil {
		s.logger.Error("failed to parse pgx config").Err("error", err).Send()
		return fmt.Errorf("%s: parse pgx config: %w", s.Name(), err)
	}

	if s.cfg.Database.MaxOpenConns > 0 {
		poolCfg.MaxConns = s.cfg.Database.MaxOpenConns
	}
	if s.cfg.Database.ConnMaxLifetime > 0 {
		poolCfg.MaxConnLifetime = s.cfg.Database.ConnMaxLifetime
	}
	if s.cfg.Database.ConnMaxIdleTime > 0 {
		poolCfg.MaxConnIdleTime = s.cfg.Database.ConnMaxIdleTime
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		s.logger.Error("failed to create pgx pool").Err("error", err).Send()
		return fmt.Errorf("%s: pgxpool: %w", s.Name(), err)
	}

	if pErr := pool.Ping(ctx); pErr != nil {
		pool.Close()
		s.logger.Error("failed to ping pgx pool").Err("error", pErr).Send()
		return fmt.Errorf("%s: ping: %w", s.Name(), pErr)
	}

	s.logger.Info("PGX database connection initialized").Send()

	s.pool = pool
	s.db = stdlib.OpenDBFromPool(pool)
	return nil
}

// Close closes the underlying database connections.
func (s *Service) Close() error {
	s.closed.Store(true)

	if s.db != nil {
		if err := s.db.Close(); err != nil {
			s.logger.Error("failed to close database connection").Err("error", err).Send()
			return fmt.Errorf("%s: close: %w", s.Name(), err)
		}

		s.logger.Info("database connection closed").Send()
	}

	if s.pool != nil {
		s.pool.Close()

		s.logger.Info("pgx pool closed").Send()
	}

	return nil
}

// DB returns the underlying *sql.DB. This satisfies gas.DatabaseProvider
// and works in both ModeSQL and ModePgx (via stdlib adapter).
func (s *Service) DB() *sql.DB {
	return s.db
}

// Pool returns the native pgxpool.Pool. Returns nil when running in
// ModeSQL. Consuming services holding only a gas.DatabaseProvider can
// reach the pool with PoolFrom rather than type-asserting themselves.
func (s *Service) Pool() *pgxpool.Pool {
	return s.pool
}

// CheckHealth reports liveness. It only fails for states a restart
// would resolve (uninitialized or closed). Transient connectivity
// issues are surfaced via CheckReady instead, since database/sql and
// pgxpool both auto-reconnect.
func (s *Service) CheckHealth(_ context.Context) error {
	if s.closed.Load() {
		return errors.New("database: closed")
	}
	if s.db == nil && s.pool == nil {
		return errors.New("database: not initialized")
	}
	return nil
}

// CheckReady reports readiness by pinging the database. A failure
// here means traffic should not be routed to this instance until the
// dependency is reachable again.
func (s *Service) CheckReady(ctx context.Context) error {
	if s.closed.Load() {
		return errors.New("database: closed")
	}
	return s.Ping(ctx)
}

// Ping verifies the database connection is still alive.
func (s *Service) Ping(ctx context.Context) error {
	if s.pool != nil {
		if err := s.pool.Ping(ctx); err != nil {
			return fmt.Errorf("%s: ping: %w", s.Name(), err)
		}
		return nil
	}
	if s.db == nil {
		return fmt.Errorf("%s: not initialized", s.Name())
	}
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("%s: ping: %w", s.Name(), err)
	}
	return nil
}

// Driver returns the database driver name based on the configured mode and settings.
func (s *Service) Driver() string {
	if s.cfg.Database.Mode == ModePgx {
		return DriverPgx
	}
	return s.cfg.Database.Driver
}
