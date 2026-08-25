package valkey

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/gasmod/gas"
	cache "github.com/gasmod/gas/cache"

	"github.com/valkey-io/valkey-go"
)

const serviceName = "gas/cache/valkey"

// Service is a Valkey-backed cache implementing gas.Service and
// gas.CacheProvider.
type Service struct {
	client valkey.Client
	cfg    *Config
	logger gas.Logger

	cfgProvider          gas.ConfigProvider
	customConfigProvided bool
	closed               atomic.Bool
}

var _ gas.Service = (*Service)(nil)
var _ gas.CacheProvider = (*Service)(nil)
var _ gas.HealthReporter = (*Service)(nil)
var _ gas.ReadyReporter = (*Service)(nil)

// Option configures a Service.
type Option func(*Service)

// WithConfig sets a custom configuration.
func WithConfig(cfg *Config) Option {
	return func(s *Service) {
		s.cfg = cfg
		s.customConfigProvided = true
	}
}

// New captures options and returns a DI-injectable constructor.
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
func (s *Service) Name() string { return serviceName }

// Init validates the configuration, creates the Valkey client, and
// verifies connectivity.
func (s *Service) Init() error {
	if !s.customConfigProvided {
		if s.cfgProvider != nil {
			if err := s.cfgProvider.Bind(s.cfg); err != nil {
				return fmt.Errorf("%s: config binding: %w", s.Name(), err)
			}
		}
	}

	if err := s.cfg.Validate(); err != nil {
		s.logger.Error("invalid cache configuration").Err("error", err).Send()
		return err
	}

	if err := s.connectWithRetry(); err != nil {
		return err
	}

	s.closed.Store(false)
	s.logger.Info("valkey cache initialized").Str("addr", s.cfg.Cache.Addr).Send()
	return nil
}

func (s *Service) connectWithRetry() error {
	err := s.connect()
	if err == nil {
		return nil
	}

	maxRetries := s.cfg.Cache.ConnRetries
	if maxRetries <= 0 {
		return err
	}

	interval := s.cfg.Cache.ConnRetryInterval
	if interval <= 0 {
		interval = defaultConnRetryInterval
	}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		s.logger.Warn("valkey connection failed, retrying").
			Err("error", err).
			Int("attempt", attempt).
			Int("max_retries", maxRetries).
			Str("next_retry_in", interval.String()).
			Send()

		time.Sleep(interval)
		interval *= 2

		err = s.connect()
		if err == nil {
			return nil
		}
	}

	s.logger.Error("valkey connection failed after all retries").
		Err("error", err).
		Int("retries", maxRetries).
		Send()
	return fmt.Errorf("%s: connect failed after %d retries: %w", s.Name(), maxRetries, err)
}

func (s *Service) connect() error {
	client, err := valkey.NewClient(valkey.ClientOption{
		InitAddress:      []string{s.cfg.Cache.Addr},
		Password:         s.cfg.Cache.Password,
		SelectDB:         s.cfg.Cache.DB,
		Dialer:           net.Dialer{Timeout: s.cfg.Cache.DialTimeout},
		ConnWriteTimeout: s.cfg.Cache.WriteTimeout,
	})
	if err != nil {
		s.logger.Error("failed to create valkey client").Err("error", err).Send()
		return fmt.Errorf("%s: new client: %w", s.Name(), err)
	}

	// verify connectivity
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Cache.DialTimeout)
	defer cancel()

	if pErr := client.Do(ctx, client.B().Ping().Build()).Error(); pErr != nil {
		client.Close()
		s.logger.Error("failed to ping valkey").Err("error", pErr).Send()
		return fmt.Errorf("%s: ping: %w", s.Name(), pErr)
	}

	// close previous client if reconnecting
	if s.client != nil {
		s.client.Close()
	}

	s.client = client
	return nil
}

// Close shuts down the Valkey client.
func (s *Service) Close() error {
	s.closed.Store(true)

	if s.client != nil {
		s.client.Close()
		s.logger.Info("valkey cache closed").Send()
	}

	return nil
}

// Get returns the value for the given key, or cache.ErrKeyNotFound if
// the key does not exist.
func (s *Service) Get(ctx context.Context, key string) ([]byte, error) {
	if s.closed.Load() {
		return nil, cache.ErrClosed
	}

	resp := s.client.Do(ctx, s.client.B().Get().Key(key).Build())
	if err := resp.Error(); err != nil {
		if valkey.IsValkeyNil(err) {
			return nil, cache.ErrKeyNotFound
		}
		return nil, fmt.Errorf("%s: get %q: %w", s.Name(), key, err)
	}

	b, err := resp.AsBytes()
	if err != nil {
		return nil, fmt.Errorf("%s: get %q: %w", s.Name(), key, err)
	}
	return b, nil
}

// Set stores a value with the given TTL. If ttl is 0, the key is set
// without expiration.
func (s *Service) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if s.closed.Load() {
		return cache.ErrClosed
	}

	var cmd valkey.Completed
	if ttl > 0 {
		cmd = s.client.B().Set().Key(key).Value(string(value)).Px(ttl).Build()
	} else {
		cmd = s.client.B().Set().Key(key).Value(string(value)).Build()
	}

	if err := s.client.Do(ctx, cmd).Error(); err != nil {
		return fmt.Errorf("%s: set %q: %w", s.Name(), key, err)
	}
	return nil
}

// Delete removes a key from the cache.
func (s *Service) Delete(ctx context.Context, key string) error {
	if s.closed.Load() {
		return cache.ErrClosed
	}

	if err := s.client.Do(ctx, s.client.B().Del().Key(key).Build()).Error(); err != nil {
		return fmt.Errorf("%s: del %q: %w", s.Name(), key, err)
	}
	return nil
}

// Exists checks whether a key exists in the cache.
func (s *Service) Exists(ctx context.Context, key string) (bool, error) {
	if s.closed.Load() {
		return false, cache.ErrClosed
	}

	n, err := s.client.Do(ctx, s.client.B().Exists().Key(key).Build()).AsInt64()
	if err != nil {
		return false, fmt.Errorf("%s: exists %q: %w", s.Name(), key, err)
	}
	return n > 0, nil
}

// CheckHealth reports liveness. The valkey-go client reconnects
// internally, so a transient network failure is not a liveness failure;
// only a closed service is.
func (s *Service) CheckHealth(_ context.Context) error {
	if s.closed.Load() {
		return cache.ErrClosed
	}
	return nil
}

// CheckReady reports readiness by pinging the Valkey server with the
// caller's context.
func (s *Service) CheckReady(ctx context.Context) error {
	if s.closed.Load() {
		return cache.ErrClosed
	}
	if err := s.client.Do(ctx, s.client.B().Ping().Build()).Error(); err != nil {
		return fmt.Errorf("%s: ping: %w", s.Name(), err)
	}
	return nil
}

// Client returns the underlying valkey.Client for advanced operations.
func (s *Service) Client() valkey.Client {
	return s.client
}
