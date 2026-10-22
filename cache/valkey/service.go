package valkey

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/gasmod/gas"
	cache "github.com/gasmod/gas/cache"
)

const serviceName = "gas/cache/valkey"

// Service is a Valkey-backed cache implementing gas.Service and
// gas.CacheProvider.
type Service struct {
	cfg    *Config
	logger gas.Logger

	cfgProvider gas.ConfigProvider

	// testHookBeforeCall, when set by a test, runs in each cache
	// operation after its open check and before it uses the client, so a
	// test can close the service in that window.
	testHookBeforeCall func()

	// client holds the current Valkey client. It is nil until Init first
	// connects, and Close keeps it so Client still returns it. Init and
	// Close swap and read it while operations run, so it is atomic.
	client atomic.Pointer[clientRef]

	// closed is true from Close until the next successful Init. It
	// starts false, so an operation also checks that client is set.
	closed atomic.Bool

	customConfigProvided bool
}

// clientRef boxes a valkey.Client so it can be held in an
// atomic.Pointer. Comparing two *clientRef values tells whether Init
// replaced the client while an operation was using it.
type clientRef struct {
	c valkey.Client
}

var (
	_ gas.Service        = (*Service)(nil)
	_ gas.CacheProvider  = (*Service)(nil)
	_ gas.HealthReporter = (*Service)(nil)
	_ gas.ReadyReporter  = (*Service)(nil)
)

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
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Service, error) {
	return func(cfgProvider gas.ConfigProvider, logger gas.Logger) (*Service, error) {
		s := &Service{
			cfg:         DefaultConfig(),
			cfgProvider: cfgProvider,
			logger:      logger.With().Str("service", serviceName).Logger(),
		}
		for _, opt := range opts {
			opt(s)
		}
		return s, nil
	}
}

// Name returns the service identifier.
func (s *Service) Name() string { return serviceName }

// Init binds the configuration, creates the Valkey client, and verifies
// connectivity. A config bound from the ConfigProvider is validated by Bind
// against its validate tags; one passed with WithConfig is used as given. The
// ping and the retry backoff use ctx, so a canceled or expired ctx stops the
// retries.
func (s *Service) Init(ctx context.Context) error {
	if !s.customConfigProvided && s.cfgProvider != nil {
		if err := s.cfgProvider.Bind(s.cfg); err != nil {
			return fmt.Errorf("%s: config binding: %w", s.Name(), err)
		}
	}

	if err := s.connectWithRetry(ctx); err != nil {
		return err
	}

	s.closed.Store(false)
	s.logger.Info("valkey cache initialized").Str("addr", s.cfg.Cache.Addr).Send()
	return nil
}

func (s *Service) connectWithRetry(ctx context.Context) error {
	err := s.connect(ctx)
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

		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: connect canceled after %d attempts: %w",
				s.Name(), attempt, errors.Join(ctx.Err(), err))
		case <-time.After(interval):
		}
		interval *= 2

		err = s.connect(ctx)
		if err == nil {
			return nil
		}
	}

	return fmt.Errorf("%s: connect failed after %d retries: %w", s.Name(), maxRetries, err)
}

func (s *Service) connect(ctx context.Context) error {
	client, err := valkey.NewClient(valkey.ClientOption{
		InitAddress:      []string{s.cfg.Cache.Addr},
		Password:         s.cfg.Cache.Password,
		SelectDB:         s.cfg.Cache.DB,
		Dialer:           net.Dialer{Timeout: s.cfg.Cache.DialTimeout},
		ConnWriteTimeout: s.cfg.Cache.WriteTimeout,
	})
	if err != nil {
		return fmt.Errorf("%s: new client: %w", s.Name(), err)
	}

	// verify connectivity
	pingCtx, cancel := context.WithTimeout(ctx, s.cfg.Cache.DialTimeout)
	defer cancel()

	if pErr := client.Do(pingCtx, client.B().Ping().Build()).Error(); pErr != nil {
		client.Close()
		return fmt.Errorf("%s: ping: %w", s.Name(), pErr)
	}

	// close previous client if reconnecting
	if prev := s.client.Swap(&clientRef{c: client}); prev != nil {
		prev.c.Close()
	}
	return nil
}

// Close shuts down the Valkey client.
func (s *Service) Close(context.Context) error {
	s.closed.Store(true)

	if ref := s.client.Load(); ref != nil {
		ref.c.Close()
		s.logger.Info("valkey cache closed").Send()
	}

	return nil
}

// acquire returns the current client, or cache.ErrClosed if the service
// was never started or has been closed.
func (s *Service) acquire() (*clientRef, error) {
	if s.closed.Load() {
		return nil, cache.ErrClosed
	}
	ref := s.client.Load()
	if ref == nil {
		return nil, cache.ErrClosed
	}
	return ref, nil
}

// closedDuring reports whether the service was closed, or its client
// replaced, after acquire returned ref. A command that fails because of
// that reports cache.ErrClosed rather than the client's own error, which
// valkey-go also uses for connection failures.
func (s *Service) closedDuring(ref *clientRef) bool {
	return s.closed.Load() || s.client.Load() != ref
}

// Get returns the value for the given key, or cache.ErrKeyNotFound if
// the key does not exist. It returns cache.ErrClosed if the service was
// never started or is closed before the command completes.
func (s *Service) Get(ctx context.Context, key string) ([]byte, error) {
	ref, err := s.acquire()
	if err != nil {
		return nil, err
	}

	s.runTestHookBeforeCall()

	resp := ref.c.Do(ctx, ref.c.B().Get().Key(key).Build())
	if err = resp.Error(); err != nil {
		if valkey.IsValkeyNil(err) {
			return nil, cache.ErrKeyNotFound
		}
		if s.closedDuring(ref) {
			return nil, cache.ErrClosed
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
// without expiration. It returns cache.ErrClosed if the service was never
// started or is closed before the command completes.
func (s *Service) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	ref, err := s.acquire()
	if err != nil {
		return err
	}

	s.runTestHookBeforeCall()

	var cmd valkey.Completed
	if ttl > 0 {
		cmd = ref.c.B().Set().Key(key).Value(string(value)).Px(ttl).Build()
	} else {
		cmd = ref.c.B().Set().Key(key).Value(string(value)).Build()
	}

	if err = ref.c.Do(ctx, cmd).Error(); err != nil {
		if s.closedDuring(ref) {
			return cache.ErrClosed
		}
		return fmt.Errorf("%s: set %q: %w", s.Name(), key, err)
	}
	return nil
}

// Delete removes a key from the cache. It returns cache.ErrClosed if the
// service was never started or is closed before the command completes.
func (s *Service) Delete(ctx context.Context, key string) error {
	ref, err := s.acquire()
	if err != nil {
		return err
	}

	s.runTestHookBeforeCall()

	if err = ref.c.Do(ctx, ref.c.B().Del().Key(key).Build()).Error(); err != nil {
		if s.closedDuring(ref) {
			return cache.ErrClosed
		}
		return fmt.Errorf("%s: del %q: %w", s.Name(), key, err)
	}
	return nil
}

// Exists checks whether a key exists in the cache. It returns
// cache.ErrClosed if the service was never started or is closed before the
// command completes.
func (s *Service) Exists(ctx context.Context, key string) (bool, error) {
	ref, err := s.acquire()
	if err != nil {
		return false, err
	}

	s.runTestHookBeforeCall()

	n, err := ref.c.Do(ctx, ref.c.B().Exists().Key(key).Build()).AsInt64()
	if err != nil {
		if s.closedDuring(ref) {
			return false, cache.ErrClosed
		}
		return false, fmt.Errorf("%s: exists %q: %w", s.Name(), key, err)
	}
	return n > 0, nil
}

// CheckHealth reports liveness. The valkey-go client reconnects
// internally, so a transient network failure is not a liveness failure;
// only a service that was never started or has been closed is, and it
// reports cache.ErrClosed.
func (s *Service) CheckHealth(context.Context) error {
	_, err := s.acquire()
	return err
}

// CheckReady reports readiness by pinging the Valkey server with the
// caller's context. It returns cache.ErrClosed if the service was never
// started or is closed before the ping completes.
func (s *Service) CheckReady(ctx context.Context) error {
	ref, err := s.acquire()
	if err != nil {
		return err
	}

	s.runTestHookBeforeCall()

	if err = ref.c.Do(ctx, ref.c.B().Ping().Build()).Error(); err != nil {
		if s.closedDuring(ref) {
			return cache.ErrClosed
		}
		return fmt.Errorf("%s: ping: %w", s.Name(), err)
	}
	return nil
}

// runTestHookBeforeCall runs testHookBeforeCall if a test has set it.
func (s *Service) runTestHookBeforeCall() {
	if s.testHookBeforeCall != nil {
		s.testHookBeforeCall()
	}
}

// Client returns the underlying valkey.Client for advanced operations.
// It returns nil before Init first connects; after Close it returns the
// closed client.
func (s *Service) Client() valkey.Client {
	ref := s.client.Load()
	if ref == nil {
		return nil
	}
	return ref.c
}
