package memory

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gasmod/gas"
	cache "github.com/gasmod/gas/cache"
)

const serviceName = "gas/cache/memory"

type entry struct {
	expiresAt time.Time // zero value means no expiration
	value     []byte
}

func (e entry) isExpired(now time.Time) bool {
	return !e.expiresAt.IsZero() && now.After(e.expiresAt)
}

// Service is an in-memory cache implementing gas.Service and
// gas.CacheProvider. It uses a simple map with optional background
// cleanup of expired entries.
type Service struct {
	logger gas.Logger
	items  map[string]entry
	cfg    *Config

	cfgProvider gas.ConfigProvider
	cancel      context.CancelFunc

	mu     sync.RWMutex
	closed atomic.Bool

	customConfigProvided bool
}

var _ gas.Service = (*Service)(nil)
var _ gas.CacheProvider = (*Service)(nil)

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

// Init validates the configuration and starts the background cleanup goroutine.
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

	s.items = make(map[string]entry)
	s.closed.Store(false)

	if s.cfg.Cache.CleanupInterval > 0 {
		ctx, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		go s.cleanup(ctx)
	}

	s.logger.Info("in-memory cache initialized").Send()
	return nil
}

// Close stops the background cleanup goroutine and clears the cache.
func (s *Service) Close() error {
	s.closed.Store(true)

	if s.cancel != nil {
		s.cancel()
	}

	s.mu.Lock()
	s.items = nil
	s.mu.Unlock()

	s.logger.Info("in-memory cache closed").Send()
	return nil
}

// Get returns the value for the given key, or cache.ErrKeyNotFound if
// the key does not exist or has expired.
func (s *Service) Get(_ context.Context, key string) ([]byte, error) {
	if s.closed.Load() {
		return nil, cache.ErrClosed
	}

	s.mu.RLock()
	e, ok := s.items[key]
	s.mu.RUnlock()

	if !ok || e.isExpired(time.Now()) {
		if ok {
			// lazy delete expired entry
			s.mu.Lock()
			if e2, exists := s.items[key]; exists && e2.isExpired(time.Now()) {
				delete(s.items, key)
			}
			s.mu.Unlock()
		}
		return nil, cache.ErrKeyNotFound
	}

	return e.value, nil
}

// Set stores a value with the given TTL. If ttl is 0, the configured
// DefaultTTL is used. If DefaultTTL is also 0, the entry never expires.
// When MaxEntries is reached and a new key is inserted, the oldest
// expired entry is evicted first; if none are expired, the set is
// rejected with an error.
func (s *Service) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	if s.closed.Load() {
		return cache.ErrClosed
	}

	if ttl == 0 {
		ttl = s.cfg.Cache.DefaultTTL
	}

	e := entry{value: value}
	if ttl > 0 {
		e.expiresAt = time.Now().Add(ttl)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// enforce max entries for new keys
	if s.cfg.Cache.MaxEntries > 0 {
		if _, exists := s.items[key]; !exists && len(s.items) >= s.cfg.Cache.MaxEntries {
			// try to evict one expired entry
			if !s.evictOneExpired() {
				return fmt.Errorf("%s: max entries (%d) reached", s.Name(), s.cfg.Cache.MaxEntries)
			}
		}
	}

	s.items[key] = e
	return nil
}

// Delete removes a key from the cache.
func (s *Service) Delete(_ context.Context, key string) error {
	if s.closed.Load() {
		return cache.ErrClosed
	}

	s.mu.Lock()
	delete(s.items, key)
	s.mu.Unlock()

	return nil
}

// Exists checks whether a key exists and has not expired.
func (s *Service) Exists(_ context.Context, key string) (bool, error) {
	if s.closed.Load() {
		return false, cache.ErrClosed
	}

	s.mu.RLock()
	e, ok := s.items[key]
	s.mu.RUnlock()

	if !ok || e.isExpired(time.Now()) {
		return false, nil
	}
	return true, nil
}

// cleanup periodically removes expired entries.
func (s *Service) cleanup(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.Cache.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.removeExpired()
		}
	}
}

func (s *Service) removeExpired() {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	for k, e := range s.items {
		if e.isExpired(now) {
			delete(s.items, k)
		}
	}
}

// evictOneExpired removes a single expired entry. Must be called with
// s.mu held. Returns true if an entry was evicted.
func (s *Service) evictOneExpired() bool {
	now := time.Now()
	for k, e := range s.items {
		if e.isExpired(now) {
			delete(s.items, k)
			return true
		}
	}
	return false
}
