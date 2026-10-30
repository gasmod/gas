package jwt

import (
	"errors"
	"time"
)

const (
	// MethodHS256 is the signing method HMAC with SHA-256.
	MethodHS256 = "HS256"
	// MethodRS256 is the signing method RSASSA-PKCS1-v1_5 with SHA-256.
	MethodRS256 = "RS256"

	// DefaultSigningMethod is the signing method of DefaultConfig.
	DefaultSigningMethod = MethodHS256
	// DefaultExpiry is the token lifetime of DefaultConfig.
	DefaultExpiry = 15 * time.Minute

	// MinHMACKeyBytes is the shortest HS256 signing key Init accepts.
	MinHMACKeyBytes = 32
	// MinRSAKeyBits is the smallest RSA modulus Init accepts.
	MinRSAKeyBits = 2048
)

// Config holds the JWT service's configuration, read from the `jwt`
// section.
type Config struct {
	// JWT holds the settings.
	JWT Settings
}

// Settings are the JWT service's settings, as described in the package
// documentation.
type Settings struct {
	// SigningKey is the HMAC key for HS256, at least MinHMACKeyBytes long.
	// It must be empty for RS256.
	SigningKey string
	// SigningMethod is MethodHS256 or MethodRS256.
	SigningMethod string `validate:"oneof=HS256 RS256"`
	// PublicKeyPath is the PEM file of the RSA public key that verifies
	// RS256 tokens. RS256 requires it; HS256 requires it empty.
	PublicKeyPath string
	// PrivateKeyPath is the PEM file of the RSA private key that signs
	// RS256 tokens. Without it the service only verifies. HS256 requires
	// it empty.
	PrivateKeyPath string
	// Issuer, when not empty, is set as the `iss` claim of signed tokens
	// and required of verified ones.
	Issuer string
	// Audience, when not empty, is set as the `aud` claim of signed tokens
	// and required of verified ones.
	Audience string
	// Expiry is the lifetime of the tokens Sign makes.
	Expiry time.Duration `validate:"gt=0s"`
	// Leeway is the clock skew allowed when checking `exp`, `nbf` and
	// `iat`.
	Leeway time.Duration `validate:"gte=0s"`
}

// DefaultConfig returns the configuration a service starts from before
// binding: DefaultSigningMethod, DefaultExpiry, no leeway, and no key.
func DefaultConfig() *Config {
	return &Config{
		JWT: Settings{
			SigningMethod: DefaultSigningMethod,
			Expiry:        DefaultExpiry,
		},
	}
}

// The rule errors name the settings, never their values.
var (
	// errHMACKeyTooShort reports an HS256 signing key shorter than
	// MinHMACKeyBytes, or none.
	errHMACKeyTooShort = errors.New("HS256 requires JWT.SigningKey of at least 32 bytes")
	// errKeyPathsWithHMAC reports RSA key paths set for HS256.
	errKeyPathsWithHMAC = errors.New(
		"HS256 requires JWT.PublicKeyPath and JWT.PrivateKeyPath to be empty",
	)
	// errNoPublicKey reports RS256 without a public key path.
	errNoPublicKey = errors.New("RS256 requires JWT.PublicKeyPath")
	// errSigningKeyWithRSA reports an HMAC key set for RS256.
	errSigningKeyWithRSA = errors.New("RS256 requires JWT.SigningKey to be empty")
)

// checkRules checks what the validate tags cannot express: the key
// settings the signing method needs, and that the other method's are
// empty, so a setting is never silently ignored.
func checkRules(cfg *Config) error {
	s := &cfg.JWT

	switch s.SigningMethod {
	case MethodHS256:
		if len(s.SigningKey) < MinHMACKeyBytes {
			return errHMACKeyTooShort
		}

		if s.PublicKeyPath != "" || s.PrivateKeyPath != "" {
			return errKeyPathsWithHMAC
		}
	case MethodRS256:
		if s.PublicKeyPath == "" {
			return errNoPublicKey
		}

		if s.SigningKey != "" {
			return errSigningKeyWithRSA
		}
	}

	return nil
}
