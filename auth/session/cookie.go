package session

import (
	"fmt"
	"net/http"
)

// SetCookie writes the session cookie for sess, as described in the
// package documentation: its value is sess.ID and it expires at
// sess.CreatedAt plus MaxLifetime. sess must come from Create or Rotate;
// a nil session, or one without a valid ID or creation time, is
// ErrInvalidSession and writes nothing.
func (s *Service) SetCookie(w http.ResponseWriter, sess *Session) error {
	st, err := s.current()
	if err != nil {
		return err
	}

	cfg := st.cfg

	if sess == nil || !validID(sess.ID) || sess.CreatedAt.IsZero() {
		return fmt.Errorf("%s: set cookie: %w", ServiceName, ErrInvalidSession)
	}

	//nolint:gosec // G124: the attributes come from the configuration, which Init warns about.
	c := newCookie(cfg, sess.ID)

	c.Expires = sess.CreatedAt.Add(cfg.Session.MaxLifetime)

	http.SetCookie(w, c)

	return nil
}

// ClearCookie writes a cookie that makes the browser delete the session
// cookie: the same name and attributes, an empty value and `Max-Age=0`.
// It does not delete the session; Revoke does.
func (s *Service) ClearCookie(w http.ResponseWriter) error {
	st, err := s.current()
	if err != nil {
		return err
	}

	cfg := st.cfg

	//nolint:gosec // G124: the attributes come from the configuration, which Init warns about.
	c := newCookie(cfg, "")

	c.MaxAge = -1

	http.SetCookie(w, c)

	return nil
}

// newCookie returns the session cookie with value and the configured
// attributes.
func newCookie(cfg *Config, value string) *http.Cookie {
	//nolint:gosec // G124: Secure, HttpOnly and SameSite come from the configuration, which Init warns about.
	return &http.Cookie{
		Name:     cfg.Session.CookieName,
		Value:    value,
		Path:     cfg.Session.CookiePath,
		Domain:   cfg.Session.CookieDomain,
		Secure:   cfg.Session.CookieSecure,
		HttpOnly: cfg.Session.CookieHTTPOnly,
		SameSite: cfg.Session.CookieSameSite,
	}
}

// warnInsecureCookie logs a warning for each cookie attribute the
// configuration leaves insecure.
func (s *Service) warnInsecureCookie(cfg *Config) {
	if !cfg.Session.CookieSecure {
		s.logger.Warn("session cookie is not Secure").Str("service", ServiceName).Send()
	}

	if !cfg.Session.CookieHTTPOnly {
		s.logger.Warn("session cookie is not HttpOnly").Str("service", ServiceName).Send()
	}

	if cfg.Session.CookieSameSite != http.SameSiteLaxMode &&
		cfg.Session.CookieSameSite != http.SameSiteStrictMode {
		s.logger.Warn("session cookie is not SameSite Lax or Strict").
			Str("service", ServiceName).
			Send()
	}
}
