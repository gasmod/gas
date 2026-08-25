package apikey

import (
	"encoding/base64"
	"errors"

	env "github.com/gasmod/gas/config/extensions/gasenv"
)

const (
	defaultHeaderName = "X-API-Key"
	defaultKeyLength  = 32
)

// Config holds API key service settings.
type Config struct {
	env.WithGasEnv

	APIKey Settings
}

// Settings represents the API key configuration values.
type Settings struct {
	// HeaderName is the HTTP header used to pass the API key.
	HeaderName string
	// Prefix is prepended to generated keys for identification.
	Prefix string
	// KeyLength is the number of random bytes used for key generation.
	KeyLength int
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		APIKey: Settings{
			HeaderName: defaultHeaderName,
			KeyLength:  defaultKeyLength,
		},
	}
}

// Validate checks the Config for correctness.
func (c *Config) Validate() error {
	if c.APIKey.KeyLength < 16 {
		return errors.New("apikey: key length must be at least 16 bytes")
	}
	// The generated key is base64-encoded (no padding), so its character
	// length is ceil(KeyLength * 4/3). The key prefix excerpt takes 8
	// characters from the random portion. Ensure the random part is long
	// enough so that fullKey[:len(Prefix)+8] never panics.
	randomLen := base64.URLEncoding.WithPadding(base64.NoPadding).EncodedLen(c.APIKey.KeyLength)
	if randomLen < 8 {
		return errors.New("apikey: key length too short to produce an 8-character prefix excerpt")
	}
	return nil
}
