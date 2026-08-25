// Package jwt provides a stateless JWT authenticator for the Gas ecosystem.
// It implements gas.Authenticator and gas.Service.
package jwt

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gasmod/gas"
	auth "github.com/gasmod/gas/auth"
	"github.com/golang-jwt/jwt/v5"
)

const serviceName = "gas/auth/jwt"

// TokenClaims holds parsed JWT data.
type TokenClaims struct {
	ExpiresAt    time.Time
	IssuedAt     time.Time
	CustomClaims map[string]any
	Subject      string
	TokenID      string
	Issuer       string
	Audience     []string
}

// Service is a JWT authenticator implementing gas.Authenticator and gas.Service.
type Service struct {
	logger gas.Logger
	cfg    *Config

	cfgProvider gas.ConfigProvider

	signingKey      any // []byte for HMAC, *rsa.PrivateKey for RSA
	verificationKey any // []byte for HMAC, *rsa.PublicKey for RSA
	signingMethod   jwt.SigningMethod

	customConfigProvided bool
}

var _ gas.Service = (*Service)(nil)
var _ gas.Authenticator = (*Service)(nil)
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

// WithSigningMethod overrides the default signing method.
func WithSigningMethod(method string) Option {
	return func(s *Service) {
		s.cfg.JWT.SigningMethod = method
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

// Init validates configuration and prepares signing keys.
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

	switch s.cfg.JWT.SigningMethod {
	case "HS256":
		s.signingMethod = jwt.SigningMethodHS256
		key := []byte(s.cfg.JWT.SigningKey)
		s.signingKey = key
		s.verificationKey = key
	case "RS256":
		s.signingMethod = jwt.SigningMethodRS256
		pubKey, err := loadRSAPublicKey(s.cfg.JWT.PublicKeyPath)
		if err != nil {
			return fmt.Errorf("%s: load public key: %w", s.Name(), err)
		}
		s.verificationKey = pubKey

		if s.cfg.JWT.PrivateKeyPath != "" {
			privKey, err := loadRSAPrivateKey(s.cfg.JWT.PrivateKeyPath)
			if err != nil {
				return fmt.Errorf("%s: load private key: %w", s.Name(), err)
			}
			s.signingKey = privKey
		}
	}

	if s.signingKey == nil {
		return errors.New("jwt: signing key not configured")
	}

	s.logger.Info("service initialized").Send()
	return nil
}

// Close performs cleanup. JWT is stateless so this is a no-op.
func (s *Service) Close() error { return nil }

// Authenticate reads a Bearer token from the Authorization header, verifies
// it, and returns a principal with scheme "jwt".
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (gas.Principal, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return nil, auth.ErrUnauthenticated
	}

	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return nil, auth.ErrUnauthenticated
	}

	schema, token := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])

	if !strings.EqualFold(schema, "Bearer") {
		return nil, auth.ErrUnauthenticated
	}

	claims, err := s.Verify(token)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, auth.ErrCredentialsExpired
		}
		return nil, auth.ErrUnauthenticated
	}

	meta := gas.BasePrincipalMetadata{}
	for k, v := range claims.CustomClaims {
		meta[k] = v
	}

	return auth.NewPrincipal(claims.Subject, auth.SchemeJWT, claims.TokenID, meta), nil
}

// Sign creates a signed JWT with the given subject and custom claims using
// the default expiry.
func (s *Service) Sign(subject string, claims map[string]any) (string, error) {
	return s.SignWithExpiry(subject, claims, s.cfg.JWT.Expiry)
}

// SignWithExpiry creates a signed JWT with the given subject, custom claims,
// and explicit expiry duration.
func (s *Service) SignWithExpiry(subject string, claims map[string]any, expiry time.Duration) (string, error) {
	mapClaims := jwt.MapClaims{}

	// set custom claims first so they don't override standard claims
	for k, v := range claims {
		mapClaims[k] = v
	}

	now := time.Now()
	mapClaims["sub"] = subject
	mapClaims["iat"] = jwt.NewNumericDate(now)
	mapClaims["exp"] = jwt.NewNumericDate(now.Add(expiry))

	if s.cfg.JWT.Issuer != "" {
		mapClaims["iss"] = s.cfg.JWT.Issuer
	}
	if s.cfg.JWT.Audience != "" {
		mapClaims["aud"] = s.cfg.JWT.Audience
	}

	token := jwt.NewWithClaims(s.signingMethod, mapClaims)
	signed, err := token.SignedString(s.signingKey)
	if err != nil {
		return "", fmt.Errorf("jwt: sign: %w", err)
	}
	return signed, nil
}

// Verify parses and validates a JWT string, returning the extracted claims.
func (s *Service) Verify(tokenString string) (*TokenClaims, error) {
	parserOpts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{s.cfg.JWT.SigningMethod}),
	}
	if s.cfg.JWT.Issuer != "" {
		parserOpts = append(parserOpts, jwt.WithIssuer(s.cfg.JWT.Issuer))
	}
	if s.cfg.JWT.Audience != "" {
		parserOpts = append(parserOpts, jwt.WithAudience(s.cfg.JWT.Audience))
	}

	token, err := jwt.Parse(tokenString, func(_ *jwt.Token) (any, error) {
		return s.verificationKey, nil
	}, parserOpts...)
	if err != nil {
		return nil, fmt.Errorf("jwt: %w", err)
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, errors.New("jwt: invalid token claims")
	}

	return extractClaims(mapClaims), nil
}

// extractClaims converts jwt.MapClaims into a structured TokenClaims value.
func extractClaims(mapClaims jwt.MapClaims) *TokenClaims {
	tc := &TokenClaims{
		CustomClaims: make(map[string]any),
	}

	// Extract standard claims.
	standardKeys := map[string]bool{
		"sub": true, "jti": true, "iss": true, "aud": true,
		"exp": true, "iat": true, "nbf": true,
	}

	if sub, _ := mapClaims["sub"].(string); sub != "" {
		tc.Subject = sub
	}
	if jti, _ := mapClaims["jti"].(string); jti != "" {
		tc.TokenID = jti
	}
	if iss, _ := mapClaims["iss"].(string); iss != "" {
		tc.Issuer = iss
	}

	if exp, err := mapClaims.GetExpirationTime(); err == nil && exp != nil {
		tc.ExpiresAt = exp.Time
	}
	if iat, err := mapClaims.GetIssuedAt(); err == nil && iat != nil {
		tc.IssuedAt = iat.Time
	}
	if aud, err := mapClaims.GetAudience(); err == nil {
		tc.Audience = aud
	}

	// Remaining claims go into CustomClaims.
	for k, v := range mapClaims {
		if !standardKeys[k] {
			tc.CustomClaims[k] = v
		}
	}

	return tc
}

func loadRSAPublicKey(path string) (*rsa.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("jwt: read public key: %w", err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("jwt: failed to decode PEM block for public key")
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("jwt: parse public key: %w", err)
	}

	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("jwt: not an RSA public key")
	}

	return rsaPub, nil
}

func loadRSAPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("jwt: read private key: %w", err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("jwt: failed to decode PEM block for private key")
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// Fall back to PKCS1.
		privKey, pkcs1Err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if pkcs1Err != nil {
			return nil, fmt.Errorf("jwt: parse private key: %w", pkcs1Err)
		}
		return privKey, nil
	}

	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("jwt: not an RSA private key")
	}

	return rsaKey, nil
}
