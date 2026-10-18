package jwt

import (
	"errors"
	"time"

	env "github.com/gasmod/gas/config/extensions/gasenv"
)

const (
	defaultSigningMethod = "HS256"
	defaultExpiry        = 15 * time.Minute
)

// Config holds JWT service settings.
type Config struct {
	env.WithGasEnv

	JWT Settings
}

// Settings represents the JWT configuration values.
type Settings struct {
	// SigningKey is the HMAC key used for HS256 signing and verification.
	SigningKey string
	// SigningMethod is the JWT signing algorithm. Supported: "HS256", "RS256".
	SigningMethod string `validate:"oneof=HS256 RS256"`
	// PublicKeyPath is the path to the RSA public key file for RS256 verification.
	PublicKeyPath string
	// PrivateKeyPath is the path to the RSA private key file for RS256 signing.
	PrivateKeyPath string
	// Issuer is the expected "iss" claim value.
	Issuer string
	// Audience is the expected "aud" claim value.
	Audience string
	// Expiry is the default token lifetime.
	Expiry time.Duration `validate:"gt=0s"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		JWT: Settings{
			SigningMethod: defaultSigningMethod,
			Expiry:        defaultExpiry,
		},
	}
}

// checkRules checks the key material the signing method needs, which a
// validate tag cannot express: HS256 needs a signing key of at least 32
// bytes, RS256 a public key path.
func checkRules(cfg *Config) error {
	switch cfg.JWT.SigningMethod {
	case "HS256":
		if cfg.JWT.SigningKey == "" {
			return errors.New("jwt: signing key is required for HS256")
		}
		if len(cfg.JWT.SigningKey) < 32 {
			return errors.New("jwt: HS256 signing key must be at least 32 bytes")
		}
	case "RS256":
		if cfg.JWT.PublicKeyPath == "" {
			return errors.New("jwt: public key path is required for RS256")
		}
	}
	return nil
}
