package session

import (
	"errors"
	"net/http"
	"time"

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
	CookieName      string
	CookiePath      string
	CookieDomain    string
	CookieSameSite  http.SameSite
	SessionTTL      time.Duration
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

// Validate checks the Config for correctness.
func (c *Config) Validate() error {
	if c.Session.CookieName == "" {
		return errors.New("session: cookie name is required")
	}
	if c.Session.SessionTTL <= 0 {
		return errors.New("session: session TTL must be positive")
	}
	if c.Session.CleanupInterval > 0 && c.Session.CleanupTimeout <= 0 {
		return errors.New("session: cleanup timeout must be positive when cleanup is enabled")
	}
	return nil
}
