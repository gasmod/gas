package session_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/gasmod/gas/auth/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfig_Validates(t *testing.T) {
	t.Run("default config passes validation", func(t *testing.T) {
		cfg := session.DefaultConfig()

		err := cfg.Validate()

		require.NoError(t, err)
	})
}

func TestConfig_Validate_EmptyCookieName(t *testing.T) {
	t.Run("empty CookieName returns error", func(t *testing.T) {
		cfg := session.DefaultConfig()
		cfg.Session.CookieName = ""

		err := cfg.Validate()

		require.Error(t, err)
		assert.Equal(t, "session: cookie name is required", err.Error())
	})
}

func TestConfig_Validate_SessionTTL_Zero(t *testing.T) {
	t.Run("SessionTTL of 0 returns error", func(t *testing.T) {
		cfg := session.DefaultConfig()
		cfg.Session.SessionTTL = 0

		err := cfg.Validate()

		require.Error(t, err)
		assert.Equal(t, "session: session TTL must be positive", err.Error())
	})
}

func TestConfig_Validate_SessionTTL_Negative(t *testing.T) {
	t.Run("SessionTTL of -1 returns error", func(t *testing.T) {
		cfg := session.DefaultConfig()
		cfg.Session.SessionTTL = -1

		err := cfg.Validate()

		require.Error(t, err)
		assert.Equal(t, "session: session TTL must be positive", err.Error())
	})
}

func TestConfig_Validate_SessionTTL_Nanosecond(t *testing.T) {
	t.Run("SessionTTL of 1 nanosecond passes validation", func(t *testing.T) {
		cfg := session.DefaultConfig()
		cfg.Session.SessionTTL = 1 * time.Nanosecond

		err := cfg.Validate()

		require.NoError(t, err)
	})
}

func TestConfig_Validate_CleanupTimeout_Zero(t *testing.T) {
	t.Run("CleanupTimeout of 0 with cleanup enabled returns error", func(t *testing.T) {
		cfg := session.DefaultConfig()
		cfg.Session.CleanupTimeout = 0

		err := cfg.Validate()

		require.Error(t, err)
		assert.Equal(t, "session: cleanup timeout must be positive when cleanup is enabled", err.Error())
	})
}

func TestConfig_Validate_CleanupTimeout_ZeroWithCleanupDisabled(t *testing.T) {
	t.Run("CleanupTimeout of 0 with cleanup disabled passes validation", func(t *testing.T) {
		cfg := session.DefaultConfig()
		cfg.Session.CleanupInterval = 0
		cfg.Session.CleanupTimeout = 0

		err := cfg.Validate()

		require.NoError(t, err)
	})
}

func TestDefaultConfig_Values(t *testing.T) {
	t.Run("default values are correct", func(t *testing.T) {
		cfg := session.DefaultConfig()

		assert.Equal(t, "session_id", cfg.Session.CookieName)
		assert.Equal(t, "/", cfg.Session.CookiePath)
		assert.True(t, cfg.Session.CookieSecure)
		assert.True(t, cfg.Session.CookieHTTPOnly)
		assert.Equal(t, http.SameSiteLaxMode, cfg.Session.CookieSameSite)
		assert.Equal(t, 24*time.Hour, cfg.Session.SessionTTL)
		assert.True(t, cfg.Session.ExtendOnAccess)
		assert.Equal(t, 1*time.Hour, cfg.Session.CleanupInterval)
	})
}
