package token_test

import (
	"testing"
	"time"

	"github.com/gasmod/gas/auth/token"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig_Validates(t *testing.T) {
	t.Run("default config passes validation", func(t *testing.T) {
		cfg := token.DefaultConfig()

		err := cfg.Validate()

		require.NoError(t, err)
	})
}

func TestConfig_Validate_DefaultTTL_Zero(t *testing.T) {
	t.Run("DefaultTTL of 0 returns error", func(t *testing.T) {
		cfg := token.DefaultConfig()
		cfg.Token.DefaultTTL = 0

		err := cfg.Validate()

		require.Error(t, err)
		assert.Equal(t, "token: default TTL must be positive", err.Error())
	})
}

func TestConfig_Validate_DefaultTTL_Negative(t *testing.T) {
	t.Run("DefaultTTL of -1 returns error", func(t *testing.T) {
		cfg := token.DefaultConfig()
		cfg.Token.DefaultTTL = -1

		err := cfg.Validate()

		require.Error(t, err)
		assert.Equal(t, "token: default TTL must be positive", err.Error())
	})
}

func TestConfig_Validate_TokenLength_TooShort(t *testing.T) {
	t.Run("TokenLength of 15 returns error", func(t *testing.T) {
		cfg := token.DefaultConfig()
		cfg.Token.TokenLength = 15

		err := cfg.Validate()

		require.Error(t, err)
		assert.Equal(t, "token: token length must be at least 16 bytes", err.Error())
	})
}

func TestConfig_Validate_TokenLength_Minimum(t *testing.T) {
	t.Run("TokenLength of 16 passes validation", func(t *testing.T) {
		cfg := token.DefaultConfig()
		cfg.Token.TokenLength = 16

		err := cfg.Validate()

		require.NoError(t, err)
	})
}

func TestConfig_Validate_CleanupTimeout_Zero(t *testing.T) {
	t.Run("CleanupTimeout of 0 with cleanup enabled returns error", func(t *testing.T) {
		cfg := token.DefaultConfig()
		cfg.Token.CleanupTimeout = 0

		err := cfg.Validate()

		require.Error(t, err)
		assert.Equal(t, "token: cleanup timeout must be positive when cleanup is enabled", err.Error())
	})
}

func TestConfig_Validate_CleanupTimeout_ZeroWithCleanupDisabled(t *testing.T) {
	t.Run("CleanupTimeout of 0 with cleanup disabled passes validation", func(t *testing.T) {
		cfg := token.DefaultConfig()
		cfg.Token.CleanupInterval = 0
		cfg.Token.CleanupTimeout = 0

		err := cfg.Validate()

		require.NoError(t, err)
	})
}

func TestDefaultConfig_Values(t *testing.T) {
	t.Run("default values are correct", func(t *testing.T) {
		cfg := token.DefaultConfig()

		assert.Equal(t, 15*time.Minute, cfg.Token.DefaultTTL)
		assert.Equal(t, 32, cfg.Token.TokenLength)
		assert.Equal(t, 1*time.Hour, cfg.Token.CleanupInterval)
	})
}
