package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache"
	"github.com/gasmod/gas/cache/internal/configsrc"
)

// ServiceName is the name of the memory store, returned by Service.Name
// and prefixed to every error the store returns.
const ServiceName = "gas/cache/memory"

// The operations errors name.
const (
	opInit   = "init"
	opGet    = "get"
	opSet    = "set"
	opDelete = "delete"
	opExists = "exists"
)

// Service is a cache held in the process's memory, as described in the
// package documentation. It is safe for concurrent use.
type Service struct {
	// cfgProvider is the ConfigProvider Init binds from, unless a
	// configuration was given to WithConfig.
	cfgProvider gas.ConfigProvider
	// logger receives the store's events, with `service` set.
	logger gas.Logger
	// es holds what Init opened, or nil before Init succeeds and after
	// Close.
	es *entries
	// cfgSource records the configuration given to WithConfig.
	cfgSource configsrc.Source[Config]
	// closed reports whether Close ran after a successful Init, and Init
	// has not succeeded since.
	closed bool
	// mu guards es, closed and every entry. Each method holds it for its
	// whole call, which does no I/O; Close releases it before it waits
	// for the janitor, which takes it on each sweep.
	mu sync.Mutex
}

var (
	_ gas.Service       = (*Service)(nil)
	_ gas.CacheProvider = (*Service)(nil)
)

// New returns the constructor of a Service with opts applied, in the shape
// the gas container expects. The constructor returns an error wrapping
// cache.ErrNilLogger for a nil logger; a nil ConfigProvider is accepted,
// and Init then requires WithConfig.
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Service, error) {
	return func(cfgProvider gas.ConfigProvider, logger gas.Logger) (*Service, error) {
		if logger == nil {
			return nil, fmt.Errorf("%s: %w", ServiceName, cache.ErrNilLogger)
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

// Init resolves the configuration, opens an empty cache and starts the
// janitor, as described in the package documentation. On an initialized
// store it returns cache.ErrAlreadyInitialized and changes nothing.
func (s *Service) Init(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.es != nil {
		return opError(opInit, cache.ErrAlreadyInitialized)
	}

	err := ctx.Err()
	if err != nil {
		return opError(opInit, err)
	}

	cfg, err := s.cfgSource.Resolve(s.cfgProvider, DefaultConfig, noRules)
	if err != nil {
		return opError(opInit, err)
	}

	es := newEntries(cfg.Cache)

	s.es = es
	s.closed = false

	go s.janitor(es)

	s.logger.Info("service initialized").
		Int("max_entries", es.settings.MaxEntries).
		Duration("cleanup_interval", es.settings.CleanupInterval).
		Duration("default_ttl", es.settings.DefaultTTL).
		Send()

	return nil
}

// Close drops every entry, stops the janitor and waits for it to return.
// Every later call but Init returns an error wrapping cache.ErrClosed.
// Close on a store that is not initialized returns nil and logs nothing.
// It ignores its context.
func (s *Service) Close(context.Context) error {
	s.mu.Lock()

	es := s.es
	if es != nil {
		s.es = nil
		s.closed = true
	}

	s.mu.Unlock()

	if es == nil {
		return nil
	}

	close(es.stop)
	<-es.done

	s.logger.Info("service closed").Send()

	return nil
}

// Get returns a copy of the value of key, or an error wrapping
// cache.ErrKeyNotFound when the store holds no live value under it. A hit
// makes the entry the most recently used.
func (s *Service) Get(ctx context.Context, key string) ([]byte, error) {
	err := cache.CheckKey(key)
	if err != nil {
		return nil, opError(opGet, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	es, err := s.open(ctx, opGet)
	if err != nil {
		return nil, err
	}

	e, ok := es.get(key, time.Now(), true)
	if !ok {
		return nil, opError(opGet, cache.ErrKeyNotFound)
	}

	return append([]byte{}, e.value...), nil
}

// Set stores a copy of value under key, as the most recently used entry,
// expiring as ttl says, as described in the package documentation. A nil
// value is stored as empty. A new key on a full store evicts one entry
// first.
func (s *Service) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	err := cache.CheckKey(key)
	if err != nil {
		return opError(opSet, err)
	}

	err = cache.CheckTTL(ttl)
	if err != nil {
		return opError(opSet, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	es, err := s.open(ctx, opSet)
	if err != nil {
		return err
	}

	now := time.Now()

	var expires time.Time

	if d := cache.EffectiveTTL(ttl, es.settings.DefaultTTL); d > 0 {
		expires = now.Add(d)
	}

	es.set(key, append([]byte{}, value...), expires, now)

	return nil
}

// Delete removes the value of key. Deleting a key the store does not hold
// is not an error.
func (s *Service) Delete(ctx context.Context, key string) error {
	err := cache.CheckKey(key)
	if err != nil {
		return opError(opDelete, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	es, err := s.open(ctx, opDelete)
	if err != nil {
		return err
	}

	es.delete(key)

	return nil
}

// Exists reports whether the store holds a live value under key. It does
// not make the entry the most recently used.
func (s *Service) Exists(ctx context.Context, key string) (bool, error) {
	err := cache.CheckKey(key)
	if err != nil {
		return false, opError(opExists, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	es, err := s.open(ctx, opExists)
	if err != nil {
		return false, err
	}

	_, ok := es.get(key, time.Now(), false)

	return ok, nil
}

// Len returns the number of entries the store holds, counting those that
// have expired and that neither a call nor the janitor has removed yet.
// It is 0 when the store is not initialized.
func (s *Service) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.es == nil {
		return 0
	}

	return len(s.es.byKey)
}

// open returns the entries of an initialized store whose ctx has not
// ended, or the error of op: cache.ErrNotInitialized, cache.ErrClosed or
// the context's. The caller holds s.mu.
func (s *Service) open(ctx context.Context, op string) (*entries, error) {
	switch {
	case s.es == nil && s.closed:
		return nil, opError(op, cache.ErrClosed)
	case s.es == nil:
		return nil, opError(op, cache.ErrNotInitialized)
	}

	err := ctx.Err()
	if err != nil {
		return nil, opError(op, err)
	}

	return s.es, nil
}

// janitor removes the expired entries of es every CleanupInterval until
// Close stops it, and then closes es.done.
func (s *Service) janitor(es *entries) {
	defer close(es.done)

	ticker := time.NewTicker(es.settings.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-es.stop:
			return
		case now := <-ticker.C:
			s.mu.Lock()
			es.sweep(now)
			s.mu.Unlock()
		}
	}
}

// opError returns err as the failure of op, after ServiceName.
func opError(op string, err error) error {
	return fmt.Errorf("%s: %s: %w", ServiceName, op, err)
}
