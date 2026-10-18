package session

import (
	"errors"
	"net/http"
	"time"

	"github.com/gasmod/gas/auth/internal/cfgvalidate"
	env "github.com/gasmod/gas/config/extensions/gasenv"
)

const (
	defaultCookieName      = "session_id"
	defaultCookiePath      = "/"
	defaultSessionTTL      = 24 * time.Hour
	defaultCleanupInterval = 1 * time.Hour
	defaultCleanupTimeout  = 30 * time.Second
)

// Config holds session service settings.
type Config struct {
	env.WithGasEnv

	Session Settings
}

// Settings represents the session configuration values.
type Settings struct {
	CookieName      string `validate:"required"`
	CookiePath      string
	CookieDomain    string
	CookieSameSite  http.SameSite
	SessionTTL      time.Duration `validate:"gt=0s"`
	CleanupInterval time.Duration
	CleanupTimeout  time.Duration
	CookieSecure    bool
	CookieHTTPOnly  bool
	ExtendOnAccess  bool
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Session: Settings{
			CookieName:      defaultCookieName,
			CookiePath:      defaultCookiePath,
			CookieSecure:    true,
			CookieHTTPOnly:  true,
			CookieSameSite:  http.SameSiteLaxMode,
			SessionTTL:      defaultSessionTTL,
			ExtendOnAccess:  true,
			CleanupInterval: defaultCleanupInterval,
			CleanupTimeout:  defaultCleanupTimeout,
		},
	}
}

// validateConfig checks cfg against its validate tags and the rule a tag
// cannot express. Init calls it for a config that did not come through
// ConfigProvider.Bind.
func validateConfig(cfg *Config) error {
	if err := cfgvalidate.Struct(cfg); err != nil {
		return err //nolint:wrapcheck // Init adds the context
	}
	return checkRules(cfg)
}

// checkRules checks that background cleanup, when enabled, has a positive
// timeout, which a validate tag cannot express.
func checkRules(cfg *Config) error {
	if cfg.Session.CleanupInterval > 0 && cfg.Session.CleanupTimeout <= 0 {
		return errors.New("session: cleanup timeout must be positive when cleanup is enabled")
	}
	return nil
}
