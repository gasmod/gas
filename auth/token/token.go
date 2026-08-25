// Package token provides single-use, time-limited tokens for the Gas
// ecosystem. Tokens are used for magic links, email verification, password
// reset, and invite links. They implement gas.Service but NOT
// gas.Authenticator — tokens are verified once and consumed.
package token //nolint:revive // intentional: "token" is the natural name for this domain package

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/internal/cryptoutil"
	"github.com/gasmod/gas/auth/token/db"

	"github.com/google/uuid"
)

const serviceName = "gas/auth/token"

// Sentinel errors for token operations.
var (
	// ErrTokenInvalid indicates that the token does not exist or has already
	// been consumed.
	ErrTokenInvalid = errors.New("invalid token")
	// ErrTokenExpired indicates that the token has expired.
	ErrTokenExpired = errors.New("token expired")
)

// Service manages single-use tokens. It implements gas.Service.
type Service struct {
	store  *db.Store
	logger gas.Logger
	cfg    *Config

	cfgProvider gas.ConfigProvider
	done        chan struct{}
	doneOnce    sync.Once

	customConfigProvided bool
}

var _ gas.Service = (*Service)(nil)
var _ Provider = (*Service)(nil)

// Option configures a Service.
type Option func(*Service)

// WithConfig sets a custom configuration, skipping auto-bind from ConfigProvider.
func WithConfig(cfg *Config) Option {
	return func(s *Service) {
		s.cfg = cfg
		s.customConfigProvided = true
	}
}

// New captures options and returns a DI-injectable constructor.
func New(opts ...Option) func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager, gas.ConfigProvider) *Service {
	return func(dbProv gas.DatabaseProvider, logger gas.Logger, migMgr gas.MigrationManager, cfgProvider gas.ConfigProvider) *Service {
		s := &Service{
			cfg:         DefaultConfig(),
			store:       db.New()(dbProv, logger, migMgr),
			logger:      logger.With().Str("service", serviceName).Logger(),
			done:        make(chan struct{}),
			cfgProvider: cfgProvider,
		}
		for _, opt := range opts {
			opt(s)
		}
		return s
	}
}

// Name returns the service identifier.
func (s *Service) Name() string { return serviceName }

// Init validates configuration, initializes the store, and starts the
// background cleanup goroutine.
func (s *Service) Init() error {
	if !s.customConfigProvided {
		if s.cfgProvider != nil {
			if err := s.cfgProvider.Bind(s.cfg); err != nil {
				return fmt.Errorf("%s: config binding: %w", s.Name(), err)
			}
		}
	}

	if err := s.cfg.Validate(); err != nil {
		return fmt.Errorf("%s: %w", s.Name(), err)
	}

	if err := s.store.Init(s.Name()); err != nil {
		return fmt.Errorf("%s: store init: %w", s.Name(), err)
	}

	if s.cfg.Token.CleanupInterval > 0 {
		go s.cleanupLoop()
	}

	s.logger.Info("service initialized").Send()
	return nil
}

// Close stops the background cleanup goroutine.
func (s *Service) Close() error {
	s.doneOnce.Do(func() { close(s.done) })
	return nil
}

// Issue generates a random token, stores its hash with purpose, subject, and
// expiry, and returns the raw token. If ttl is 0, DefaultTTL is used.
func (s *Service) Issue(ctx context.Context, subject, purpose string, ttl time.Duration) (string, error) {
	if ttl == 0 {
		ttl = s.cfg.Token.DefaultTTL
	}

	rawToken, err := cryptoutil.RandomString(s.cfg.Token.TokenLength)
	if err != nil {
		return "", fmt.Errorf("%s: generate token: %w", s.Name(), err)
	}

	id := uuid.New().String()

	tokenHash := cryptoutil.SHA256Hex(rawToken)
	now := time.Now()
	expiresAt := now.Add(ttl)

	if insErr := s.store.InsertToken(ctx, id, subject, tokenHash, purpose, now, expiresAt); insErr != nil {
		return "", fmt.Errorf("%s: issue token: %w", s.Name(), insErr)
	}

	return rawToken, nil
}

// Verify hashes the raw token, atomically fetches-and-deletes it, then checks
// purpose match and expiry. Returns the subject on success.
//
// The fetch-and-delete happens in a single atomic store operation so that
// concurrent Verify calls for the same raw token cannot both succeed: only
// one caller observes the record, the other sees ErrTokenInvalid.
//
// Deletion is unconditional on validation outcome: once the raw secret has
// been presented, it is burned regardless of whether purpose matches or the
// token has expired. This keeps a simple invariant — once the raw secret has
// been presented, it cannot be reused — and prevents an attacker who has the
// hash but guesses the wrong purpose from retrying indefinitely.
//
// Trade-off: a bug that sends the wrong purpose string will silently consume
// a valid token. This is the preferable failure mode — it surfaces the bug
// immediately rather than masking cross-flow confusion.
func (s *Service) Verify(ctx context.Context, rawToken, purpose string) (subject string, err error) {
	tokenHash := cryptoutil.SHA256Hex(rawToken)

	record, tkErr := s.store.ConsumeTokenByHash(ctx, tokenHash)
	if tkErr != nil {
		return "", fmt.Errorf("%s: verify token: %w", s.Name(), tkErr)
	}
	if record == nil {
		return "", ErrTokenInvalid
	}

	if record.Purpose != purpose {
		return "", ErrTokenInvalid
	}

	if time.Now().After(record.ExpiresAt) {
		return "", ErrTokenExpired
	}

	return record.Subject, nil
}

// Revoke deletes a specific token by its raw value.
func (s *Service) Revoke(ctx context.Context, rawToken string) error {
	tokenHash := cryptoutil.SHA256Hex(rawToken)
	n, err := s.store.DeleteTokenByHash(ctx, tokenHash)
	if err != nil {
		return fmt.Errorf("%s: revoke token: %w", s.Name(), err)
	}
	if n == 0 {
		return ErrTokenInvalid
	}
	return nil
}

// RevokeAllByPurpose deletes all tokens for a subject with the given purpose.
func (s *Service) RevokeAllByPurpose(ctx context.Context, subject, purpose string) error {
	if err := s.store.DeleteTokensBySubjectPurpose(ctx, subject, purpose); err != nil {
		return fmt.Errorf("%s: revoke all by purpose: %w", s.Name(), err)
	}
	return nil
}

func (s *Service) cleanupLoop() {
	ticker := time.NewTicker(s.cfg.Token.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Token.CleanupTimeout)
			n, err := s.store.DeleteExpiredTokens(ctx, time.Now())
			if err != nil {
				cancel()
				s.logger.Error("token cleanup failed").Err("error", err).Send()
				continue
			}
			if n > 0 {
				s.logger.Info("expired tokens cleaned up").Int64("count", n).Send()
			}
			cancel()
		}
	}
}
