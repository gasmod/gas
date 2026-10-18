package apikey_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth/apikey"
)

func TestDefaultConfig_Validates(t *testing.T) {
	t.Run("default config passes validation", func(t *testing.T) {
		cfg := apikey.DefaultConfig()

		err := apikey.ValidateConfig(cfg)

		require.NoError(t, err)
	})
}

func TestConfig_Validate_KeyLength_TooShort(t *testing.T) {
	t.Run("KeyLength of 15 returns error", func(t *testing.T) {
		cfg := apikey.DefaultConfig()
		cfg.APIKey.KeyLength = 15

		err := apikey.ValidateConfig(cfg)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "Field validation for 'KeyLength' failed on the 'min' tag")
	})
}

func TestConfig_Validate_KeyLength_Minimum(t *testing.T) {
	t.Run("KeyLength of 16 passes validation", func(t *testing.T) {
		cfg := apikey.DefaultConfig()
		cfg.APIKey.KeyLength = 16

		err := apikey.ValidateConfig(cfg)

		require.NoError(t, err)
	})
}

func TestDefaultConfig_Values(t *testing.T) {
	t.Run("default values are correct", func(t *testing.T) {
		cfg := apikey.DefaultConfig()

		assert.Equal(t, "X-API-Key", cfg.APIKey.HeaderName)
		assert.Equal(t, 32, cfg.APIKey.KeyLength)
	})
}
