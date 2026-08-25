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
	SigningMethod string
	// PublicKeyPath is the path to the RSA public key file for RS256 verification.
	PublicKeyPath string
	// PrivateKeyPath is the path to the RSA private key file for RS256 signing.
	PrivateKeyPath string
	// Issuer is the expected "iss" claim value.
	Issuer string
	// Audience is the expected "aud" claim value.
	Audience string
	// Expiry is the default token lifetime.
	Expiry time.Duration
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

// Validate checks the Config for correctness.
func (c *Config) Validate() error {
	switch c.JWT.SigningMethod {
	case "HS256":
		if c.JWT.SigningKey == "" {
			return errors.New("jwt: signing key is required for HS256")
		}
		if len(c.JWT.SigningKey) < 32 {
			return errors.New("jwt: HS256 signing key must be at least 32 bytes")
		}
	case "RS256":
		if c.JWT.PublicKeyPath == "" {
			return errors.New("jwt: public key path is required for RS256")
		}
	default:
		return errors.New("jwt: unsupported signing method: " + c.JWT.SigningMethod)
	}

	if c.JWT.Expiry <= 0 {
		return errors.New("jwt: expiry must be positive")
	}

	return nil
}
