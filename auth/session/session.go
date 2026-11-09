package session

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/cryptoutil"
	"github.com/gasmod/gas/auth/internal/sessionstore"
	"github.com/gasmod/gas/auth/internal/svcutil"
)

// ServiceName is the session service's name, which prefixes its errors.
const ServiceName = "gas/auth/session"

const (
	// idBytes is how many random bytes a session ID has: 256 bits.
	idBytes = 32
	// maxUserAgentBytes bounds the stored User-Agent.
	maxUserAgentBytes = 512
	// maxIPAddressBytes bounds the stored remote address.
	maxIPAddressBytes = 255
)

// idLength is the length of a session ID: idBytes in unpadded URL-safe
// base64.
var idLength = base64.RawURLEncoding.EncodedLen(idBytes)

// cleanupTaskName names the cleanup in its log events.
const cleanupTaskName = "session cleanup"

var (
	// ErrNilConfig indicates that WithConfig was given nil. It is
	// svcutil.ErrNilConfig.
	ErrNilConfig = svcutil.ErrNilConfig
	// ErrNoConfigProvider indicates that there is neither a configuration
	// given to WithConfig nor a ConfigProvider to bind one from. It is
	// svcutil.ErrNoConfigProvider.
	ErrNoConfigProvider = svcutil.ErrNoConfigProvider
	// ErrInvalidConfig indicates that the configuration failed to bind,
	// failed its validate tags, or broke a rule. It is
	// svcutil.ErrInvalidConfig.
	ErrInvalidConfig = svcutil.ErrInvalidConfig
	// ErrNotInitialized indicates that a method was called before Init
	// succeeded, after a failed Init, or after Close. It is
	// svcutil.ErrNotInitialized.
	ErrNotInitialized = svcutil.ErrNotInitialized
	// ErrNilLogger indicates that the constructor was given a nil logger.
	// It is svcutil.ErrNilLogger.
	ErrNilLogger = svcutil.ErrNilLogger
	// ErrEmptySubject indicates that Create was given an empty subject. It
	// is auth.ErrEmptySubject.
	ErrEmptySubject = auth.ErrEmptySubject
	// ErrNilRequest indicates that Create or Rotate was given a nil
	// request.
	ErrNilRequest = errors.New("request is nil")
	// ErrInvalidSession indicates that SetCookie or RevokeSession was
	// given a nil session, or one that Create or Rotate did not issue: its
	// ID is not a session ID or, for SetCookie, it has no creation time.
	ErrInvalidSession = errors.New("invalid session")
)

// Service authenticates requests by a session cookie and manages the
// sessions, as described in the package documentation. It is safe for
// concurrent use, Init and Close included.
type Service struct {
	// db is the database each Init builds a store on.
	db gas.DatabaseProvider
	// logger receives the service's warnings and its cleanup's results.
	logger gas.Logger
	// migMgr receives the store's migrations in Init.
	migMgr gas.MigrationManager
	// cfgProvider binds the configuration when WithConfig was not used.
	cfgProvider gas.ConfigProvider
	// state is what the last successful Init set up, or nil before Init,
	// after a failed Init and after Close. It is atomic so that methods
	// may load it while Init or Close runs.
	state atomic.Pointer[state]
	// bound is the configuration the last successful Init resolved, or
	// nil before one. Close keeps it. Config returns a copy of it.
	bound atomic.Pointer[Config]
	// cfgSource records the configuration given to WithConfig.
	cfgSource svcutil.ConfigSource[Config]
	// cleanup deletes expired sessions in the background.
	cleanup svcutil.Loop
	// lifecycle makes Init and Close one at a time, so the state they
	// publish and the cleanup they start or stop always agree. The other
	// methods never take it.
	lifecycle sync.Mutex
}

// state is what one successful Init set up. It is never changed after it
// is published, so a method that loaded it keeps a consistent view.
type state struct {
	// cfg is the resolved configuration.
	cfg *Config
	// store keeps the sessions.
	store *sessionstore.Store
}

var (
	_ gas.Service           = (*Service)(nil)
	_ auth.Authenticator    = (*Service)(nil)
	_ auth.PrincipalRevoker = (*Service)(nil)
	_ Provider              = (*Service)(nil)
)

// New returns a constructor for a Service, in the shape the dependency
// injection container calls. The constructor fails only for a nil logger,
// with ErrNilLogger; Init checks the configuration.
func New(
	opts ...Option,
) func(gas.DatabaseProvider, gas.Logger, gas.MigrationManager, gas.ConfigProvider) (*Service, error) {
	return func(
		db gas.DatabaseProvider,
		logger gas.Logger,
		migMgr gas.MigrationManager,
		cfgProvider gas.ConfigProvider,
	) (*Service, error) {
		if logger == nil {
			return nil, fmt.Errorf("%s: %w", ServiceName, ErrNilLogger)
		}

		s := &Service{db: db, logger: logger, migMgr: migMgr, cfgProvider: cfgProvider}
		for _, opt := range opts {
			opt(s)
		}

		return s, nil
	}
}

// Name returns ServiceName.
func (s *Service) Name() string { return ServiceName }

// Init resolves and checks the configuration, builds a store and
// registers its migrations, and starts the cleanup, as described in the
// package documentation. A failed Init stops the cleanup and leaves the
// service uninitialized. The context is unused: the cleanup runs until
// Close.
func (s *Service) Init(context.Context) error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()

	st, err := s.newState()
	if err != nil {
		s.state.Store(nil)
		s.cleanup.Stop()

		return err
	}

	s.warnInsecureCookie(st.cfg)
	s.state.Store(st)
	s.bound.Store(st.cfg)

	store := st.store
	maxLifetime := st.cfg.Session.MaxLifetime

	//nolint:contextcheck // the cleanup runs until Close, not until Init's context ends.
	s.cleanup.Start(svcutil.Task{
		Name:     cleanupTaskName,
		Interval: st.cfg.Session.CleanupInterval,
		Timeout:  st.cfg.Session.CleanupTimeout,
		Logger:   s.logger.With().Str("service", ServiceName).Logger(),
		Run: func(ctx context.Context) error {
			return s.deleteExpired(ctx, store, maxLifetime)
		},
	})

	s.logger.Info("service initialized").Str("service", ServiceName).Send()

	return nil
}

// Close stops the cleanup, waiting for a run in progress, after which the
// service's methods return ErrNotInitialized until Init runs again. It
// never fails, and the context is unused.
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

// Authenticate returns the principal of the session the request's cookie
// names, as described in the package documentation.
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error) {
	st, err := s.current()
	if err != nil {
		return nil, err
	}

	if r == nil {
		return nil, deny(auth.ErrNoCredentials)
	}

	sess, now, err := st.lookup(ctx, r, "authenticate")
	if err != nil {
		return nil, err
	}

	s.extend(ctx, st, sess, now)

	return auth.NewPrincipal(sess.Subject, auth.SchemeSession, sess.IDHash, sess.Metadata), nil
}

// Create issues a new session for subject with meta, from the request r,
// and stores it, as described in the package documentation. The caller
// sends it with SetCookie. meta may be nil; the session holds a copy.
func (s *Service) Create(
	ctx context.Context,
	subject string,
	meta gas.BasePrincipalMetadata,
	r *http.Request,
) (*Session, error) {
	st, err := s.current()
	if err != nil {
		return nil, err
	}

	if r == nil {
		return nil, fmt.Errorf("%s: create: %w", ServiceName, ErrNilRequest)
	}

	if subject == "" {
		return nil, fmt.Errorf("%s: create: %w", ServiceName, ErrEmptySubject)
	}

	err = auth.CheckLength("subject", subject)
	if err != nil {
		return nil, fmt.Errorf("%s: create: %w", ServiceName, err)
	}

	meta = maps.Clone(meta)
	if meta == nil {
		meta = gas.BasePrincipalMetadata{}
	}

	now := time.Now()
	rec := &sessionstore.Session{
		Subject:    subject,
		Metadata:   meta,
		IPAddress:  cleanText(r.RemoteAddr, maxIPAddressBytes),
		UserAgent:  cleanText(r.UserAgent(), maxUserAgentBytes),
		CreatedAt:  now,
		ExpiresAt:  now.Add(st.cfg.Session.SessionTTL),
		LastActive: now,
	}

	id, err := newID(rec)
	if err != nil {
		return nil, fmt.Errorf("%s: create: %w", ServiceName, err)
	}

	err = st.store.InsertSession(ctx, rec)
	if err != nil {
		return nil, fmt.Errorf("%s: create: %w", ServiceName, err)
	}

	return toSession(id, rec), nil
}

// Rotate moves the session the request's cookie names to a new ID, as
// described in the package documentation, and returns it. The caller
// sends it with SetCookie.
func (s *Service) Rotate(ctx context.Context, r *http.Request) (*Session, error) {
	st, err := s.current()
	if err != nil {
		return nil, err
	}

	if r == nil {
		return nil, fmt.Errorf("%s: rotate: %w", ServiceName, ErrNilRequest)
	}

	old, now, err := st.lookup(ctx, r, "rotate")
	if err != nil {
		return nil, err
	}

	rec := &sessionstore.Session{
		Subject:   old.Subject,
		Metadata:  old.Metadata,
		IPAddress: cleanText(r.RemoteAddr, maxIPAddressBytes),
		UserAgent: cleanText(r.UserAgent(), maxUserAgentBytes),
		CreatedAt: old.CreatedAt,
		ExpiresAt: earlier(
			now.Add(st.cfg.Session.SessionTTL),
			old.CreatedAt.Add(st.cfg.Session.MaxLifetime),
		),
		LastActive: now,
	}

	id, err := newID(rec)
	if err != nil {
		return nil, fmt.Errorf("%s: rotate: %w", ServiceName, err)
	}

	err = st.store.RotateSession(ctx, old.IDHash, rec)
	if errors.Is(err, sessionstore.ErrNotFound) {
		// Another request rotated or revoked it since the lookup.
		return nil, deny(auth.ErrUnauthenticated)
	}

	if err != nil {
		return nil, fmt.Errorf("%s: rotate: %w", ServiceName, err)
	}

	return toSession(id, rec), nil
}

// Revoke deletes the session a principal from Authenticate stands for.
// A session that is already gone is not an error. A nil principal is
// auth.ErrNilPrincipal, and one of another scheme auth.ErrSchemeMismatch.
//
// The principal's credential ID is the hash of the session ID, so a
// principal built from Session.ID matches no session and deletes
// nothing; RevokeSession deletes a Session.
func (s *Service) Revoke(ctx context.Context, principal gas.Principal) error {
	st, err := s.current()
	if err != nil {
		return err
	}

	if isNil(principal) {
		return fmt.Errorf("%s: revoke: %w", ServiceName, auth.ErrNilPrincipal)
	}

	if principal.Scheme() != auth.SchemeSession {
		return fmt.Errorf("%s: revoke: %w", ServiceName, auth.ErrSchemeMismatch)
	}

	err = st.store.DeleteSession(ctx, principal.CredentialID())
	if err != nil {
		return fmt.Errorf("%s: revoke: %w", ServiceName, err)
	}

	return nil
}

// RevokeSession deletes the session sess, as returned by Create or
// Rotate. A session that is already gone is not an error. A nil session,
// or one whose ID is not a session ID, is ErrInvalidSession and deletes
// nothing.
func (s *Service) RevokeSession(ctx context.Context, sess *Session) error {
	st, err := s.current()
	if err != nil {
		return err
	}

	if sess == nil || !validID(sess.ID) {
		return fmt.Errorf("%s: revoke session: %w", ServiceName, ErrInvalidSession)
	}

	err = st.store.DeleteSession(ctx, cryptoutil.SHA256Hex(sess.ID))
	if err != nil {
		return fmt.Errorf("%s: revoke session: %w", ServiceName, err)
	}

	return nil
}

// RevokeAll deletes every session of subject. A subject with no sessions
// is not an error.
func (s *Service) RevokeAll(ctx context.Context, subject string) error {
	st, err := s.current()
	if err != nil {
		return err
	}

	return st.revokeAll(ctx, subject)
}

// RevokeAllByScheme is RevokeAll when scheme is auth.SchemeSession, and
// does nothing for any other scheme. Either way it first checks that the
// service is initialized.
func (s *Service) RevokeAllByScheme(ctx context.Context, subject, scheme string) error {
	st, err := s.current()
	if err != nil {
		return err
	}

	if scheme != auth.SchemeSession {
		return nil
	}

	return st.revokeAll(ctx, subject)
}

// newState resolves the configuration and builds and initializes a store,
// for Init to publish.
func (s *Service) newState() (*state, error) {
	cfg, err := s.cfgSource.Resolve(s.cfgProvider, DefaultConfig, checkRules)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ServiceName, err)
	}

	store, err := sessionstore.New()(s.db, s.logger, s.migMgr)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ServiceName, err)
	}

	err = store.Init(s)
	if err != nil {
		//nolint:wrapcheck // Store.Init prefixes the service's name.
		return nil, err
	}

	return &state{cfg: cfg, store: store}, nil
}

// current returns the state the last successful Init set up, or an error
// wrapping ErrNotInitialized.
func (s *Service) current() (*state, error) {
	st := s.state.Load()
	if st == nil {
		return nil, fmt.Errorf("%s: %w", ServiceName, ErrNotInitialized)
	}

	return st, nil
}

// revokeAll deletes every session of subject.
func (st *state) revokeAll(ctx context.Context, subject string) error {
	err := st.store.DeleteSessionsBySubject(ctx, subject)
	if err != nil {
		return fmt.Errorf("%s: revoke all: %w", ServiceName, err)
	}

	return nil
}

// lookup returns the live session the request's cookie names, and the
// time it was checked at. A denial is the auth sentinel prefixed with the
// service's name, and any other error is prefixed with the service's name
// and op.
func (st *state) lookup(
	ctx context.Context,
	r *http.Request,
	op string,
) (*sessionstore.Session, time.Time, error) {
	cookie, err := r.Cookie(st.cfg.Session.CookieName)
	if err != nil || cookie.Value == "" {
		return nil, time.Time{}, deny(auth.ErrNoCredentials)
	}

	if !validID(cookie.Value) {
		return nil, time.Time{}, deny(auth.ErrUnauthenticated)
	}

	sess, err := st.store.GetSession(ctx, cryptoutil.SHA256Hex(cookie.Value))
	if errors.Is(err, sessionstore.ErrNotFound) {
		return nil, time.Time{}, deny(auth.ErrUnauthenticated)
	}

	if err != nil {
		return nil, time.Time{}, fmt.Errorf("%s: %s: %w", ServiceName, op, err)
	}

	now := time.Now()

	if !now.Before(sess.ExpiresAt) || !now.Before(sess.CreatedAt.Add(st.cfg.Session.MaxLifetime)) {
		return nil, time.Time{}, deny(auth.ErrCredentialsExpired)
	}

	return sess, now, nil
}

// extend moves the session's expiry to SessionTTL from now, but not past
// its absolute expiry, when ExtendOnAccess is on and less than half of
// SessionTTL remains. A failed write is logged, not returned, unless the
// request's context ended.
func (s *Service) extend(
	ctx context.Context,
	st *state,
	sess *sessionstore.Session,
	now time.Time,
) {
	cfg := st.cfg
	if !cfg.Session.ExtendOnAccess || sess.ExpiresAt.Sub(now) >= cfg.Session.SessionTTL/2 {
		return
	}

	expiresAt := earlier(
		now.Add(cfg.Session.SessionTTL),
		sess.CreatedAt.Add(cfg.Session.MaxLifetime),
	)
	if !expiresAt.After(sess.ExpiresAt) {
		return
	}

	err := st.store.ExtendSession(ctx, sess.IDHash, expiresAt, now)
	if err != nil && ctx.Err() == nil {
		s.logger.Warn("session extend failed").Str("service", ServiceName).Err("error", err).Send()
	}
}

// deleteExpired deletes from store the sessions past their idle or
// absolute expiry, and logs how many at debug level.
func (s *Service) deleteExpired(
	ctx context.Context,
	store *sessionstore.Store,
	maxLifetime time.Duration,
) error {
	now := time.Now()

	n, err := store.DeleteExpiredSessions(ctx, now, now.Add(-maxLifetime))
	if err != nil {
		//nolint:wrapcheck // the store prefixes its errors, and svcutil.Loop logs them with the task's name.
		return err
	}

	s.logger.Debug("expired sessions deleted").
		Str("service", ServiceName).
		Int64("count", n).
		Send()

	return nil
}

// deny returns a denial: the auth sentinel err prefixed with the service's
// name, so that errors.Is matches it.
func deny(err error) error {
	return fmt.Errorf("%s: %w", ServiceName, err)
}

// newID makes a session ID and sets rec's IDHash to its hash.
func newID(rec *sessionstore.Session) (string, error) {
	id, err := cryptoutil.RandomString(idBytes)
	if err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}

	rec.IDHash = cryptoutil.SHA256Hex(id)

	return id, nil
}

// toSession returns the Session for a stored record and its ID.
func toSession(id string, rec *sessionstore.Session) *Session {
	return &Session{
		ID:         id,
		Subject:    rec.Subject,
		Metadata:   rec.Metadata,
		IPAddress:  rec.IPAddress,
		UserAgent:  rec.UserAgent,
		CreatedAt:  rec.CreatedAt,
		ExpiresAt:  rec.ExpiresAt,
		LastActive: rec.LastActive,
	}
}

// validID reports whether v has the form of a session ID: idLength
// characters of unpadded URL-safe base64. Anything else cannot be one, so
// it is denied without a database read.
func validID(v string) bool {
	if len(v) != idLength {
		return false
	}

	for i := range len(v) {
		if !isBase64URL(v[i]) {
			return false
		}
	}

	return true
}

// isBase64URL reports whether c is in the URL-safe base64 alphabet.
func isBase64URL(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	default:
		return c == '-' || c == '_'
	}
}

// cleanText makes text from a request safe to store in any of the
// dialects: invalid UTF-8 becomes U+FFFD, control characters are dropped,
// and the result is cut to at most limit bytes on a character boundary.
func cleanText(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}

		return r
	}, strings.ToValidUTF8(s, "�"))

	if len(s) <= limit {
		return s
	}

	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}

	return s[:cut]
}

// earlier returns the earlier of a and b.
func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}

	return b
}

// isNil reports whether p is nil or a nil pointer.
func isNil(p gas.Principal) bool {
	if p == nil {
		return true
	}

	v := reflect.ValueOf(p)

	return v.Kind() == reflect.Pointer && v.IsNil()
}
