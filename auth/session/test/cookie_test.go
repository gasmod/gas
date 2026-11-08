package session_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth/session"
)

// responseCookie returns the one cookie a response set.
func responseCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()

	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)

	return cookies[0]
}

// SetCookie sends the session ID with the configured attributes and an
// Expires of the session's absolute expiry, its creation plus
// MaxLifetime.
func TestSetCookie_Defaults(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	sess := e.create(t, "alice", nil)
	rec := httptest.NewRecorder()

	require.NoError(t, e.svc.SetCookie(rec, sess))

	c := responseCookie(t, rec)
	assert.Equal(t, session.DefaultCookieName, c.Name)
	assert.Equal(t, sess.ID, c.Value)
	assert.Equal(t, session.DefaultCookiePath, c.Path)
	assert.Empty(t, c.Domain)
	assert.True(t, c.Secure)
	assert.True(t, c.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
	assert.Zero(t, c.MaxAge)
	assert.True(t, within(sess.CreatedAt.Add(session.DefaultMaxLifetime), c.Expires, time.Second),
		c.Expires)
}

// Every attribute follows the configuration.
func TestSetCookie_Configured(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, func(s *session.Settings) {
		s.CookieName = "__Host-sid"
		s.CookiePath = "/app"
		s.CookieDomain = "example.com"
		s.CookieSecure = false
		s.CookieHTTPOnly = false
		s.CookieSameSite = http.SameSiteStrictMode
		s.MaxLifetime = 48 * time.Hour
	})
	sess := e.create(t, "alice", nil)
	rec := httptest.NewRecorder()

	require.NoError(t, e.svc.SetCookie(rec, sess))

	c := responseCookie(t, rec)
	assert.Equal(t, "__Host-sid", c.Name)
	assert.Equal(t, "/app", c.Path)
	assert.Equal(t, "example.com", c.Domain)
	assert.False(t, c.Secure)
	assert.False(t, c.HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, c.SameSite)
	assert.True(t, within(sess.CreatedAt.Add(48*time.Hour), c.Expires, time.Second))

	p, err := e.svc.Authenticate(t.Context(), withCookie("__Host-sid", sess.ID))
	require.NoError(t, err)
	assert.Equal(t, "alice", p.Subject())
}

// A rotated session's cookie keeps the original absolute expiry.
func TestSetCookie_Rotated(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	old := e.create(t, "alice", nil)

	sess, err := e.svc.Rotate(t.Context(), withCookie("session_id", old.ID))
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	require.NoError(t, e.svc.SetCookie(rec, sess))

	c := responseCookie(t, rec)
	assert.Equal(t, sess.ID, c.Value)
	assert.True(t, within(old.CreatedAt.Add(session.DefaultMaxLifetime), c.Expires, time.Second))
}

// SetCookie rejects a nil session and one that Create or Rotate did not
// return, and writes nothing.
func TestSetCookie_Invalid(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	good := e.create(t, "alice", nil)

	noTime := *good

	noTime.CreatedAt = time.Time{}

	badID := *good

	badID.ID = "hunter2; Domain=evil.example"

	for _, sess := range []*session.Session{nil, &noTime, &badID, {}} {
		rec := httptest.NewRecorder()

		err := e.svc.SetCookie(rec, sess)
		require.ErrorIs(t, err, session.ErrInvalidSession)
		require.EqualError(t, err, "gas/auth/session: set cookie: invalid session")
		assert.Empty(t, rec.Header().Values("Set-Cookie"))
	}
}

// ClearCookie sends the same name and attributes with an empty value and
// `Max-Age=0`, and leaves the session in the database.
func TestClearCookie(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, func(s *session.Settings) {
		s.CookiePath = "/app"
		s.CookieDomain = "example.com"
	})
	sess := e.create(t, "alice", nil)
	rec := httptest.NewRecorder()

	require.NoError(t, e.svc.ClearCookie(rec))

	assert.Contains(t, rec.Header().Get("Set-Cookie"), "Max-Age=0")

	c := responseCookie(t, rec)
	assert.Equal(t, session.DefaultCookieName, c.Name)
	assert.Empty(t, c.Value)
	assert.Equal(t, -1, c.MaxAge)
	assert.Equal(t, "/app", c.Path)
	assert.Equal(t, "example.com", c.Domain)
	assert.True(t, c.Secure)
	assert.True(t, c.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, c.SameSite)

	_, err := e.authenticate(t.Context(), sess.ID)
	require.NoError(t, err)
}

// Init logs one warning per insecure cookie attribute, and none for the
// defaults.
func TestInit_WarnsInsecureCookie(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	assert.Empty(t, e.entries("warn", "session cookie is not Secure"))
	assert.Empty(t, e.entries("warn", "session cookie is not HttpOnly"))
	assert.Empty(t, e.entries("warn", "session cookie is not SameSite Lax or Strict"))

	e = sqliteEnv(t, func(s *session.Settings) {
		s.CookieSecure = false
		s.CookieHTTPOnly = false
		s.CookieSameSite = http.SameSiteNoneMode
	})

	var msgs []string

	for _, entry := range e.logger.Entries() {
		if entry.Level != "warn" {
			continue
		}

		assert.Equal(t, session.ServiceName, entry.Fields["service"])

		msgs = append(msgs, entry.Msg)
	}

	assert.Equal(t, []string{
		"session cookie is not Secure",
		"session cookie is not HttpOnly",
		"session cookie is not SameSite Lax or Strict",
	}, msgs)

	// SetCookie and ClearCookie do not warn again.
	n := len(e.logger.Entries())
	sess := e.create(t, "alice", nil)
	require.NoError(t, e.svc.SetCookie(httptest.NewRecorder(), sess))
	require.NoError(t, e.svc.ClearCookie(httptest.NewRecorder()))
	assert.Len(t, e.logger.Entries(), n)
}
