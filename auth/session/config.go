package session

import (
	"errors"
	"net/http"
	"time"

	"github.com/gasmod/gas/auth/internal/svcutil"
)

const (
	// DefaultCookieName is the default name of the session cookie.
	DefaultCookieName = "session_id"
	// DefaultCookiePath is the default path of the session cookie.
	DefaultCookiePath = "/"
	// DefaultSessionTTL is the default idle lifetime of a session.
	DefaultSessionTTL = 24 * time.Hour
	// DefaultMaxLifetime is the default absolute lifetime of a session,
	// counted from its creation: 30 days.
	DefaultMaxLifetime = 30 * 24 * time.Hour
	// DefaultCleanupInterval is the default time between two deletions of
	// expired sessions.
	DefaultCleanupInterval = time.Hour
	// DefaultCleanupTimeout is the default bound on one deletion of expired
	// sessions.
	DefaultCleanupTimeout = 30 * time.Second
)

var (
	// errTTLOverMax is the rule that the idle lifetime fits in the absolute
	// one.
	errTTLOverMax = errors.New("session TTL must not exceed the max lifetime")
	// errCookieName is the rule that the cookie name is a valid one.
	errCookieName = errors.New("cookie name is not a valid cookie name")
	// errCookiePath is the rule that the cookie path is a valid one.
	errCookiePath = errors.New("cookie path is not a valid cookie path")
	// errCookieDomain is the rule that the cookie domain is a valid one.
	errCookieDomain = errors.New("cookie domain is not a valid cookie domain")
	// errCookieSameSite is the rule that SameSite is one of http.SameSite's
	// constants.
	errCookieSameSite = errors.New("cookie SameSite is not one of the http.SameSite values")
	// errActivityInterval is the rule that the activity interval is not
	// negative.
	errActivityInterval = errors.New("activity interval must not be negative")
)

// Config is the session service's configuration, in the `session`
// section.
type Config struct {
	// Session holds the settings.
	Session Settings
}

// Settings are the session service's settings, as described in the
// package documentation.
type Settings struct {
	// CookieName is the name of the session cookie. It must be a valid
	// cookie name.
	//
	// Default: DefaultCookieName.
	CookieName string `validate:"required"`
	// CookiePath is the cookie's Path attribute. Empty sends none.
	//
	// Default: DefaultCookiePath.
	CookiePath string
	// CookieDomain is the cookie's Domain attribute. Empty sends none, so
	// the cookie goes only to the host that set it.
	//
	// Default: empty.
	CookieDomain string
	// CookieSameSite is the cookie's SameSite attribute, one of the
	// http.SameSite constants or 0 for none.
	//
	// Default: http.SameSiteLaxMode.
	CookieSameSite http.SameSite
	// SessionTTL is how long a session lasts without being used: its idle
	// lifetime. It must be positive and at most MaxLifetime.
	//
	// Default: DefaultSessionTTL.
	SessionTTL time.Duration `validate:"gt=0s"`
	// MaxLifetime is how long a session lasts at most from its creation,
	// however much it is used. It must be positive.
	//
	// Default: DefaultMaxLifetime.
	MaxLifetime time.Duration `validate:"gt=0s"`
	// CleanupInterval is the time between two deletions of expired
	// sessions. Zero turns the cleanup off; a negative interval is a
	// configuration error.
	//
	// Default: DefaultCleanupInterval.
	CleanupInterval time.Duration
	// CleanupTimeout bounds one deletion of expired sessions. It must be
	// positive when the cleanup is on.
	//
	// Default: DefaultCleanupTimeout.
	CleanupTimeout time.Duration
	// ActivityInterval makes authentication record a session's last active
	// time once at least this long has passed since it was last recorded,
	// as described in the package documentation, so that LastActive is
	// accurate to within it. Zero records it only when the expiry is set;
	// a negative interval is a configuration error.
	//
	// Default: 0.
	ActivityInterval time.Duration
	// CookieSecure sets the cookie's Secure attribute.
	//
	// Default: true.
	CookieSecure bool
	// CookieHTTPOnly sets the cookie's HttpOnly attribute.
	//
	// Default: true.
	CookieHTTPOnly bool
	// ExtendOnAccess makes authentication move a session's idle expiry
	// forward, as described in the package documentation.
	//
	// Default: true.
	ExtendOnAccess bool
	// AcceptBearer makes Authenticate and Rotate also read the session ID
	// from an `Authorization: Bearer` header, for clients that are not
	// browsers, as described in the package documentation. The cookie
	// takes precedence when a request carries both.
	//
	// Default: false.
	AcceptBearer bool
}

// DefaultConfig returns a new Config holding the defaults.
func DefaultConfig() *Config {
	return &Config{
		Session: Settings{
			CookieName:      DefaultCookieName,
			CookiePath:      DefaultCookiePath,
			CookieSecure:    true,
			CookieHTTPOnly:  true,
			CookieSameSite:  http.SameSiteLaxMode,
			SessionTTL:      DefaultSessionTTL,
			MaxLifetime:     DefaultMaxLifetime,
			ExtendOnAccess:  true,
			CleanupInterval: DefaultCleanupInterval,
			CleanupTimeout:  DefaultCleanupTimeout,
		},
	}
}

// checkRules checks what a validate tag cannot express. Its errors name
// the setting, never its value.
func checkRules(cfg *Config) error {
	s := cfg.Session

	err := svcutil.CheckCleanup(s.CleanupInterval, s.CleanupTimeout)
	if err != nil {
		//nolint:wrapcheck // Resolve wraps the rules' errors with ErrInvalidConfig.
		return err
	}

	if s.SessionTTL > s.MaxLifetime {
		return errTTLOverMax
	}

	if s.ActivityInterval < 0 {
		return errActivityInterval
	}

	// net/http drops a cookie with an invalid name, and the invalid bytes
	// of a path, with nothing but a log line, so they are caught here.
	//nolint:gosec // G124: only checks the name; the cookie is never sent.
	if (&http.Cookie{Name: s.CookieName}).Valid() != nil {
		return errCookieName
	}

	//nolint:gosec // G124: only checks the path; the cookie is never sent.
	if (&http.Cookie{Name: "n", Path: s.CookiePath}).Valid() != nil {
		return errCookiePath
	}

	//nolint:gosec // G124: only checks the domain; the cookie is never sent.
	if (&http.Cookie{Name: "n", Domain: s.CookieDomain}).Valid() != nil {
		return errCookieDomain
	}

	if s.CookieSameSite < 0 || s.CookieSameSite > http.SameSiteNoneMode {
		return errCookieSameSite
	}

	return nil
}
