// Package session provides server-side session authentication for the Gas
// ecosystem. It implements gas.Authenticator, gas.PrincipalRevoker, and
// gas.Service.
package session

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gasmod/gas"
	auth "github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/cryptoutil"
	"github.com/gasmod/gas/auth/session/db"
)

const serviceName = "gas/auth/session"

// Service is a session authenticator implementing gas.Authenticator,
// gas.PrincipalRevoker, and gas.Service.
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
var _ gas.Authenticator = (*Service)(nil)
var _ gas.PrincipalRevoker = (*Service)(nil)
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

	s.verifySecureCookieOptions()

	if err := s.store.Init(s.Name()); err != nil {
		return fmt.Errorf("%s: store init: %w", s.Name(), err)
	}

	if s.cfg.Session.CleanupInterval > 0 {
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

// Authenticate reads a session ID from the configured cookie, looks it up
// in the database, validates expiry, and optionally extends the TTL.
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error) {
	cookie, cErr := r.Cookie(s.cfg.Session.CookieName)
	if cErr != nil {
		return nil, auth.ErrUnauthenticated
	}

	sessionID := cookie.Value
	if sessionID == "" {
		return nil, auth.ErrUnauthenticated
	}

	sess, sesErr := s.store.GetSession(ctx, sessionID)
	if sesErr != nil {
		return nil, fmt.Errorf("%s: authenticate: %w", s.Name(), sesErr)
	}

	now := time.Now()
	if now.After(sess.ExpiresAt) {
		return nil, auth.ErrCredentialsExpired
	}

	if s.cfg.Session.ExtendOnAccess {
		newExpiry := now.Add(s.cfg.Session.SessionTTL)
		if err := s.store.ExtendSession(ctx, sessionID, newExpiry, now); err != nil {
			return nil, fmt.Errorf("%s: extend session: %w", s.Name(), err)
		}
	}

	return auth.NewPrincipal(sess.Subject, auth.SchemeSession, sess.ID, sess.Metadata), nil
}

// Revoke deletes a session by the principal's credential ID.
func (s *Service) Revoke(ctx context.Context, principal gas.Principal) error {
	sessionID := principal.CredentialID()
	if err := s.store.DeleteSession(ctx, sessionID); err != nil {
		return fmt.Errorf("%s: revoke: %w", s.Name(), err)
	}
	return nil
}

// RevokeAll deletes all sessions for the given subject.
func (s *Service) RevokeAll(ctx context.Context, subject string) error {
	if err := s.store.DeleteSessionsBySubject(ctx, subject); err != nil {
		return fmt.Errorf("%s: revoke all: %w", s.Name(), err)
	}
	return nil
}

// RevokeAllByScheme delegates to RevokeAll if the scheme is "session",
// otherwise it is a no-op.
func (s *Service) RevokeAllByScheme(ctx context.Context, subject, scheme string) error {
	if scheme != auth.SchemeSession {
		return nil
	}
	return s.RevokeAll(ctx, subject)
}

// Create generates a cryptographically random session ID, stores the session
// in the database, and returns it. The caller is responsible for calling
// SetCookie afterward. The *http.Request is used to capture IP and user agent.
func (s *Service) Create(ctx context.Context, subject string, meta gas.BasePrincipalMetadata, r *http.Request) (*Session, error) {
	id, err := cryptoutil.RandomString(32)
	if err != nil {
		return nil, fmt.Errorf("%s: generate session id: %w", s.Name(), err)
	}

	if meta == nil {
		meta = gas.BasePrincipalMetadata{}
	}

	now := time.Now()
	sess := &db.Session{
		ID:         id,
		Subject:    subject,
		Metadata:   meta,
		IPAddress:  r.RemoteAddr,
		UserAgent:  r.UserAgent(),
		CreatedAt:  now,
		ExpiresAt:  now.Add(s.cfg.Session.SessionTTL),
		LastActive: now,
	}

	if insErr := s.store.InsertSession(ctx, sess); insErr != nil {
		return nil, fmt.Errorf("%s: create session: %w", s.Name(), insErr)
	}

	return &Session{
		CreatedAt:  sess.CreatedAt,
		ExpiresAt:  sess.ExpiresAt,
		LastActive: sess.LastActive,
		Metadata:   sess.Metadata,
		ID:         sess.ID,
		Subject:    sess.Subject,
		IPAddress:  sess.IPAddress,
		UserAgent:  sess.UserAgent,
	}, nil
}

// SetCookie writes the session cookie to the response.
func (s *Service) SetCookie(w http.ResponseWriter, session *Session) {
	s.verifySecureCookieOptions()

	//nolint:gosec // Secure, HttpOnly, and SameSite are provided by config
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.Session.CookieName,
		Value:    session.ID,
		Path:     s.cfg.Session.CookiePath,
		Domain:   s.cfg.Session.CookieDomain,
		Expires:  session.ExpiresAt,
		Secure:   s.cfg.Session.CookieSecure,
		HttpOnly: s.cfg.Session.CookieHTTPOnly,
		SameSite: s.cfg.Session.CookieSameSite,
	})
}

// ClearCookie writes an expired cookie to the response, effectively removing it.
func (s *Service) ClearCookie(w http.ResponseWriter) {
	s.verifySecureCookieOptions()

	//nolint:gosec // Secure, HttpOnly, and SameSite are provided by config
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.Session.CookieName,
		Value:    "",
		Path:     s.cfg.Session.CookiePath,
		Domain:   s.cfg.Session.CookieDomain,
		MaxAge:   -1,
		Secure:   s.cfg.Session.CookieSecure,
		HttpOnly: s.cfg.Session.CookieHTTPOnly,
		SameSite: s.cfg.Session.CookieSameSite,
	})
}

func (s *Service) cleanupLoop() {
	ticker := time.NewTicker(s.cfg.Session.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), s.cfg.Session.CleanupTimeout)
			n, err := s.store.DeleteExpiredSessions(ctx, time.Now())
			if err != nil {
				cancel()
				s.logger.Error("session cleanup failed").Err("error", err).Send()
				continue
			}
			if n > 0 {
				s.logger.Info("expired sessions cleaned up").Int64("count", n).Send()
			}
			cancel()
		}
	}
}

func (s *Service) verifySecureCookieOptions() {
	if !s.cfg.Session.CookieSecure {
		s.logger.Warn("cookie missing or has insecure 'Secure' attribute").Send()
	}
	if !s.cfg.Session.CookieHTTPOnly {
		s.logger.Warn("cookie missing or has insecure 'HttpOnly' attribute").Send()
	}
	if s.cfg.Session.CookieSameSite != http.SameSiteStrictMode && s.cfg.Session.CookieSameSite != http.SameSiteLaxMode {
		s.logger.Warn("cookie missing or has insecure 'SameSite' attribute").Send()
	}
}
