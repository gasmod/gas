package valkey

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache"
	"github.com/gasmod/gas/cache/internal/configsrc"
)

// ServiceName is the name of the Valkey store, returned by Service.Name
// and prefixed to every error the store returns.
const ServiceName = "gas/cache/valkey"

// The operations errors name.
const (
	opInit   = "init"
	opGet    = "get"
	opSet    = "set"
	opDelete = "delete"
	opExists = "exists"
	opHealth = "health"
	opPing   = "ping"
)

var (
	// ErrConnect indicates that Init could not connect to the server, as
	// described in the package documentation.
	ErrConnect = errors.New("connect failed")
	// ErrCommand indicates that a command failed, at the connection or
	// with a server reply, as described in the package documentation.
	ErrCommand = errors.New("command failed")
)

// Service is a cache on a Valkey server, as described in the package
// documentation. It is safe for concurrent use.
type Service struct {
	// cfgProvider is the ConfigProvider Init binds from, unless a
	// configuration was given to WithConfig.
	cfgProvider gas.ConfigProvider
	// logger receives the store's events, with `service` set.
	logger gas.Logger
	// tlsConfig is the TLS configuration given to WithTLSConfig, or nil.
	tlsConfig *tls.Config
	// initCancel cancels the context of the Init in progress, or is nil.
	initCancel context.CancelCauseFunc
	// st is what Init opened, or nil before Init succeeds and after
	// Close. It is swapped whole, so a call sees one Init's state.
	st atomic.Pointer[state]
	// cfgSource records the configuration given to WithConfig.
	cfgSource configsrc.Source[Config]
	// closed reports whether Close ran after a successful Init, and Init
	// has not succeeded since. Close sets it before it clears st, so a
	// call that finds st nil after a Close finds closed set.
	closed atomic.Bool
	// mu makes Init and Close run one at a time. Close holds it while it
	// closes the client too, so a concurrent Close waits for that. Calls
	// do not take it.
	mu sync.Mutex
	// cancelMu guards initCancel, which Close calls before it takes mu.
	cancelMu sync.Mutex
}

// errCloseCalled is the cause Close cancels an Init in progress with.
var errCloseCalled = errors.New("close called")

var (
	_ gas.Service        = (*Service)(nil)
	_ gas.CacheProvider  = (*Service)(nil)
	_ gas.HealthReporter = (*Service)(nil)
	_ gas.ReadyReporter  = (*Service)(nil)
)

// state is what one Init opened, with the settings calls use. It never
// changes once published.
type state struct {
	// client is the connected client.
	client valkey.Client
	// prefix is Settings.KeyPrefix.
	prefix string
	// defaultTTL is Settings.DefaultTTL.
	defaultTTL time.Duration
	// opTimeout is Settings.OpTimeout.
	opTimeout time.Duration
}

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

// Init resolves and checks the configuration, connects, and publishes the
// client, as described in the package documentation. On an initialized
// store it returns cache.ErrAlreadyInitialized and changes nothing. A
// failed Init closes what it opened and leaves the store as it was.
func (s *Service) Init(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.st.Load() != nil {
		return opError(opInit, cache.ErrAlreadyInitialized)
	}

	err := ctx.Err()
	if err != nil {
		return opError(opInit, err)
	}

	cfg, err := s.cfgSource.Resolve(s.cfgProvider, DefaultConfig, rules(s.tlsConfig != nil))
	if err != nil {
		return opError(opInit, err)
	}

	set := &cfg.Cache

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	s.setInitCancel(cancel)
	defer s.setInitCancel(nil)

	client, err := s.connect(ctx, set)
	if errors.Is(context.Cause(ctx), errCloseCalled) {
		if client != nil {
			client.Close()
		}

		return opError(opInit, cache.ErrClosed)
	}

	if err != nil {
		return err
	}

	s.st.Store(&state{
		client:     client,
		prefix:     set.KeyPrefix,
		defaultTTL: set.DefaultTTL,
		opTimeout:  set.OpTimeout,
	})
	s.closed.Store(false)

	s.logger.Info("service initialized").
		Int("db", set.DB).
		Bool("tls", s.tlsConfig != nil || set.TLS).
		Send()

	return nil
}

// Close cancels an Init in progress and waits for it to return, makes
// every later call but Init return an error wrapping cache.ErrClosed, then
// closes the client, which waits for the commands in progress to finish;
// each is bounded by OpTimeout. A Close that runs at the same time waits
// for it too. Close on a store that is not initialized returns nil and
// logs nothing. It ignores its context.
func (s *Service) Close(context.Context) error {
	s.cancelMu.Lock()

	if s.initCancel != nil {
		s.initCancel(errCloseCalled)
	}

	s.cancelMu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.st.Load()
	if st == nil {
		return nil
	}

	s.closed.Store(true)
	s.st.Store(nil)

	st.client.Close()

	s.logger.Info("service closed").Send()

	return nil
}

// CheckHealth reports liveness without I/O: an error wrapping
// cache.ErrNotInitialized or cache.ErrClosed when the store is not
// initialized, the context's error when ctx has ended, and else nil.
func (s *Service) CheckHealth(ctx context.Context) error {
	_, err := s.loaded(ctx, opHealth)

	return err
}

// CheckReady reports readiness by sending PING, within ctx and
// OpTimeout. Its errors are as a command's, after `ping: `.
func (s *Service) CheckReady(ctx context.Context) error {
	st, err := s.loaded(ctx, opPing)
	if err != nil {
		return err
	}

	_, err = s.do(ctx, st, opPing, st.client.B().Ping().Build())

	return err
}

// Client returns the connected valkey.Client, or nil when the store is
// not initialized, as described in the package documentation. Commands
// sent on it are not prefixed with KeyPrefix and not bounded by
// OpTimeout, and their errors are valkey-go's own.
func (s *Service) Client() valkey.Client {
	st := s.st.Load()
	if st == nil {
		return nil
	}

	return st.client
}

// setInitCancel records cancel as the cancel of the Init in progress, or
// nil when none is.
func (s *Service) setInitCancel(cancel context.CancelCauseFunc) {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()

	s.initCancel = cancel
}

// loaded returns the state of an initialized store whose ctx has not
// ended, or the error of op: cache.ErrNotInitialized, cache.ErrClosed or
// the context's.
func (s *Service) loaded(ctx context.Context, op string) (*state, error) {
	st := s.st.Load()

	switch {
	case st == nil && s.closed.Load():
		return nil, opError(op, cache.ErrClosed)
	case st == nil:
		return nil, opError(op, cache.ErrNotInitialized)
	}

	err := ctx.Err()
	if err != nil {
		return nil, opError(op, err)
	}

	return st, nil
}

// do sends cmd on st's client within ctx and st.opTimeout, and returns
// the result, or the error of op: cache.ErrClosed when Close ran during
// the call, the context's error when ctx ended, context.DeadlineExceeded
// for a timeout while ctx's deadline was the one in force, and else the
// command's error, as commandError makes it. A nil reply is not an error.
func (s *Service) do(
	ctx context.Context,
	st *state,
	op string,
	cmd valkey.Completed,
) (valkey.ValkeyResult, error) {
	opDeadline := time.Now().Add(st.opTimeout)

	opCtx, cancel := context.WithDeadline(ctx, opDeadline)
	defer cancel()

	resp := st.client.Do(opCtx, cmd)

	err := resp.Error()
	if err == nil || valkey.IsValkeyNil(err) {
		return resp, nil
	}

	if s.st.Load() != st {
		return resp, opError(op, cache.ErrClosed)
	}

	ctxErr := ctx.Err()
	if ctxErr != nil {
		return resp, opError(op, ctxErr)
	}

	// The socket's deadline can pass a moment before ctx's timer fires,
	// so which deadline was in force decides, not ctx.Err.
	callerDeadline, ok := ctx.Deadline()
	if ok && !callerDeadline.After(opDeadline) && reason(err) == reasonTimedOut {
		return resp, opError(op, context.DeadlineExceeded)
	}

	return resp, commandError(ctx, op, err)
}

// opError returns err as the failure of op, after ServiceName.
func opError(op string, err error) error {
	return fmt.Errorf("%s: %s: %w", ServiceName, op, err)
}
