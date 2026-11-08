package jwt

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	gojwt "github.com/golang-jwt/jwt/v5"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/svcutil"
)

// ServiceName is the name of the JWT service, returned by Service.Name and
// prefixed to every error the service returns.
const ServiceName = "gas/auth/jwt"

var (
	// ErrNilConfig indicates that WithConfig was given a nil configuration.
	ErrNilConfig = svcutil.ErrNilConfig
	// ErrNoConfigProvider indicates that there is neither a configuration
	// given to WithConfig nor a ConfigProvider to bind one from.
	ErrNoConfigProvider = svcutil.ErrNoConfigProvider
	// ErrInvalidConfig indicates that the configuration failed to bind,
	// failed its validate tags, or broke one of the package's rules.
	ErrInvalidConfig = svcutil.ErrInvalidConfig
	// ErrNotInitialized indicates that a method was called before Init
	// succeeded, after a failed Init, or after Close.
	ErrNotInitialized = svcutil.ErrNotInitialized
	// ErrNilLogger indicates that the constructor was given a nil logger.
	ErrNilLogger = svcutil.ErrNilLogger
	// ErrKeyLoadFailed indicates that Init could not load an RSA key from
	// its file.
	ErrKeyLoadFailed = errors.New("failed to load key")
	// ErrKeyTooSmall indicates that an RSA key has fewer than
	// MinRSAKeyBits. It comes wrapped with ErrKeyLoadFailed.
	ErrKeyTooSmall = errors.New("key too small")
	// ErrSigningKeyNotConfigured indicates that Sign was called on a
	// service that only verifies: RS256 without a private key.
	ErrSigningKeyNotConfigured = errors.New("signing key not configured")
	// ErrEmptySubject indicates that Sign was given an empty subject.
	ErrEmptySubject = auth.ErrEmptySubject
	// ErrValueTooLong indicates that Sign was given a subject longer than
	// auth.MaxValueLength bytes.
	ErrValueTooLong = auth.ErrValueTooLong
	// ErrInvalidTTL indicates that SignWithExpiry was given an expiry that
	// is not positive.
	ErrInvalidTTL = errors.New("invalid ttl")
	// ErrReservedClaim indicates that Sign was given a custom claim named
	// after a registered claim.
	ErrReservedClaim = errors.New("reserved claim")
	// ErrInvalidClaim indicates that Sign was given a custom claim whose
	// value cannot be encoded as JSON.
	ErrInvalidClaim = errors.New("invalid claim")
	// ErrTokenInvalid indicates that a token is not valid: malformed,
	// signed with another key or method, or with claims that fail a
	// check. It wraps auth.ErrUnauthenticated.
	ErrTokenInvalid = fmt.Errorf("%w: invalid token", auth.ErrUnauthenticated)
	// ErrTokenExpired indicates that a token is valid but for its `exp`,
	// which has passed. It wraps auth.ErrCredentialsExpired, and so
	// auth.ErrUnauthenticated.
	ErrTokenExpired = fmt.Errorf("%w: token expired", auth.ErrCredentialsExpired)
)

// Service signs and verifies JWTs and authenticates requests that carry
// one, as described in the package documentation. It is safe for
// concurrent use.
type Service struct {
	// cfgProvider is the ConfigProvider Init binds from, unless a
	// configuration was given to WithConfig.
	cfgProvider gas.ConfigProvider
	// logger receives Init's info message.
	logger gas.Logger
	// state is what Init loaded, or nil before Init succeeds and after
	// Close. It is swapped whole, so a method sees one Init's state.
	state atomic.Pointer[state]
	// bound is the configuration the last successful Init resolved, or
	// nil before one. Close keeps it. Config returns a copy of it.
	bound atomic.Pointer[Config]
	// cfgSource records the configuration given to WithConfig.
	cfgSource svcutil.ConfigSource[Config]
}

var (
	_ gas.Service        = (*Service)(nil)
	_ auth.Authenticator = (*Service)(nil)
	_ Provider           = (*Service)(nil)
)

// state is the resolved configuration and the keys loaded from it.
type state struct {
	// signKey is the HMAC key ([]byte) or the RSA private key
	// (*rsa.PrivateKey), or nil when the service only verifies.
	signKey any
	// verifyKey is the HMAC key ([]byte) or the RSA public key
	// (*rsa.PublicKey).
	verifyKey any
	// method is the signing method of the configuration.
	method gojwt.SigningMethod
	// parser verifies tokens. A Parser is safe for concurrent use once
	// built.
	parser *gojwt.Parser
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

		s := &Service{cfgProvider: cfgProvider, logger: logger}
		for _, opt := range opts {
			opt(s)
		}

		return s, nil
	}
}

// Name returns ServiceName.
func (*Service) Name() string { return ServiceName }

// Init resolves and checks the configuration and loads the keys, as
// described in the package documentation. A failed Init leaves the
// service uninitialized, whatever an earlier Init loaded. It reads only
// key files, so the context is unused.
func (s *Service) Init(context.Context) error {
	st, err := s.load()
	if err != nil {
		s.state.Store(nil)

		return fmt.Errorf("%s: %w", ServiceName, err)
	}

	s.state.Store(st)
	s.bound.Store(&Config{JWT: st.settings})

	s.logger.Info("service initialized").
		Str("service", ServiceName).
		Str("signing_method", st.settings.SigningMethod).
		Bool("verify_only", st.signKey == nil).
		Send()

	return nil
}

// Close makes the service uninitialized, so its methods return
// ErrNotInitialized until Init succeeds again. It holds no resources, so
// it always returns nil and the context is unused.
func (s *Service) Close(context.Context) error {
	s.state.Store(nil)

	return nil
}

// Config returns a copy of the configuration the last successful Init
// resolved, defaults included, or the zero Config before one. A failed
// Init and Close leave it as it was. Config holds no slice, map or
// pointer, so the copy shares nothing with the service, and SigningKey
// stays a gas.Secret.
func (s *Service) Config() Config {
	cfg := s.bound.Load()
	if cfg == nil {
		return Config{}
	}

	return *cfg
}

// load resolves the configuration and builds the state from it.
func (s *Service) load() (*state, error) {
	cfg, err := s.cfgSource.Resolve(s.cfgProvider, DefaultConfig, checkRules)
	if err != nil {
		//nolint:wrapcheck // Init prefixes the service name.
		return nil, err
	}

	st := &state{settings: cfg.JWT}

	switch cfg.JWT.SigningMethod {
	case MethodRS256:
		keys, err := loadRSAKeys(&cfg.JWT)
		if err != nil {
			return nil, err
		}

		st.method = gojwt.SigningMethodRS256
		st.verifyKey = keys.public

		// Assigned only when present, so a verify-only service holds a
		// nil interface rather than a typed nil.
		if keys.private != nil {
			st.signKey = keys.private
		}

	default: // MethodHS256; the validate tag allows no other
		key := []byte(string(cfg.JWT.SigningKey))

		st.method = gojwt.SigningMethodHS256
		st.signKey = key
		st.verifyKey = key
	}

	st.parser = newParser(&cfg.JWT)

	return st, nil
}

// newParser returns a parser with the checks the package documentation
// lists for s.
func newParser(s *Settings) *gojwt.Parser {
	opts := []gojwt.ParserOption{
		gojwt.WithValidMethods([]string{s.SigningMethod}),
		gojwt.WithExpirationRequired(),
		gojwt.WithIssuedAt(),
		gojwt.WithLeeway(s.Leeway),
		gojwt.WithStrictDecoding(),
	}

	if s.Issuer != "" {
		opts = append(opts, gojwt.WithIssuer(s.Issuer))
	}

	if s.Audience != "" {
		opts = append(opts, gojwt.WithAudience(s.Audience))
	}

	return gojwt.NewParser(opts...)
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
