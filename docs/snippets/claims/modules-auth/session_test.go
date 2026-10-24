package modulesauth_test

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/session"
)

// Docs: modules/auth.mdx#sessions
// "`Create(ctx, subject, meta, r)` stores a new session with a random ID (32
// random bytes, URL-safe base64), the metadata, the client's address
// (`r.RemoteAddr`) and user agent, and an expiry `session.sessionttl` from now,
// and returns it as a `*session.Session`. Write the cookie with
// `SetCookie(w, sess)`; `ClearCookie(w)` writes an expired one."
func TestSessionCreateAndCookies(t *testing.T) {
	t.Parallel()
	db := sqliteDB(t)
	cfg := session.DefaultConfig()
	cfg.Session.SessionTTL = 3 * time.Hour
	svc := mustSession(t, db, cfg, nil)
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = "203.0.113.9:4321"
	r.Header.Set("User-Agent", "claims-agent/1")
	before := time.Now()
	s, err := svc.Create(t.Context(), "u1", gas.BasePrincipalMetadata{"plan": "pro"}, r)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(s.ID)
	if err != nil || len(raw) != 32 {
		t.Errorf("session ID %q decodes to %d bytes (%v), want 32 bytes of URL-safe base64", s.ID, len(raw), err)
	}
	if s.Subject != "u1" || s.IPAddress != "203.0.113.9:4321" || s.UserAgent != "claims-agent/1" || s.Metadata["plan"] != "pro" {
		t.Errorf("session = %+v", s)
	}
	if d := s.ExpiresAt.Sub(before); d < 3*time.Hour || d > 3*time.Hour+time.Minute {
		t.Errorf("expiry %v after Create, want sessionttl 3h", d)
	}
	var ip, ua string
	if err := db.DB().QueryRowContext(t.Context(), "SELECT ip_address, user_agent FROM __gas_auth_sessions WHERE id = ?", s.ID).Scan(&ip, &ua); err != nil || ip != s.IPAddress || ua != s.UserAgent {
		t.Errorf("stored address, agent = %q, %q, %v", ip, ua, err)
	}

	rec := httptest.NewRecorder()
	svc.SetCookie(rec, s)
	c := rec.Result().Cookies()[0]
	if c.Name != "session_id" || c.Value != s.ID || !c.Expires.Equal(s.ExpiresAt.Truncate(time.Second)) && c.Expires.Unix() != s.ExpiresAt.Unix() {
		t.Errorf("SetCookie wrote %+v", c)
	}
	rec = httptest.NewRecorder()
	svc.ClearCookie(rec)
	c = rec.Result().Cookies()[0]
	if c.Name != "session_id" || c.Value != "" || c.MaxAge >= 0 {
		t.Errorf("ClearCookie wrote %+v, want an expired cookie", c)
	}
}

// Docs: modules/auth.mdx#sessions
// "`Authenticate` reads the session ID from the cookie. A missing cookie or an
// unknown ID fails with `auth.ErrUnauthenticated`, an expired session with
// `auth.ErrCredentialsExpired`."
func TestSessionAuthenticate(t *testing.T) {
	t.Parallel()
	db := sqliteDB(t)
	cfg := session.DefaultConfig()
	cfg.Session.CookieName = "sid"
	cfg.Session.ExtendOnAccess = false
	svc := mustSession(t, db, cfg, nil)
	r, s := sessionRequest(t, svc, "u1", nil)
	if p, err := svc.Authenticate(t.Context(), r); err != nil || p.Subject() != "u1" {
		t.Fatalf("Authenticate = %v, %v", p, err)
	}
	if r.Header.Get("Cookie") != "sid="+s.ID {
		t.Errorf("cookie header = %q, want the sid cookie", r.Header.Get("Cookie"))
	}
	unknown := httptest.NewRequest(http.MethodGet, "/", nil)
	unknown.AddCookie(&http.Cookie{Name: "sid", Value: "nope"})
	wrongName := httptest.NewRequest(http.MethodGet, "/", nil)
	wrongName.AddCookie(&http.Cookie{Name: "session_id", Value: s.ID})
	for name, req := range map[string]*http.Request{
		"missing":    httptest.NewRequest(http.MethodGet, "/", nil),
		"unknown":    unknown,
		"other name": wrongName,
	} {
		if _, err := svc.Authenticate(t.Context(), req); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Errorf("%s cookie: %v, want ErrUnauthenticated", name, err)
		}
	}

	short := session.DefaultConfig()
	short.Session.SessionTTL = 50 * time.Millisecond
	short.Session.CleanupInterval = 0
	ss := mustSession(t, db, short, nil)
	er, _ := sessionRequest(t, ss, "u2", nil)
	time.Sleep(100 * time.Millisecond)
	if _, err := ss.Authenticate(t.Context(), er); !errors.Is(err, auth.ErrCredentialsExpired) {
		t.Errorf("expired session: %v, want ErrCredentialsExpired", err)
	}
}

// Docs: modules/auth.mdx#sessions
// "With `session.extendonaccess` on, the default, every authenticated request
// moves the session's expiry to `sessionttl` from now in the database. The
// cookie's own `Expires` is not rewritten; call `SetCookie` again to extend it
// in the browser."
func TestSessionExtendOnAccess(t *testing.T) {
	t.Parallel()
	// A 3s lifetime, an access at 1.8s, and a check at 3.6s: past the
	// original expiry, inside the extended one.
	if !session.DefaultConfig().Session.ExtendOnAccess {
		t.Error("extendonaccess is off by default")
	}
	for _, extend := range []bool{true, false} {
		db := sqliteDB(t)
		cfg := session.DefaultConfig()
		cfg.Session.SessionTTL = 3 * time.Second
		cfg.Session.CleanupInterval = 0
		cfg.Session.ExtendOnAccess = extend
		svc := mustSession(t, db, cfg, nil)
		r, _ := sessionRequest(t, svc, "u1", nil)
		time.Sleep(1800 * time.Millisecond)
		rec, _, reached := serve(auth.Middleware(svc), r)
		if !reached {
			t.Fatalf("extend=%v: authenticated request failed: %d", extend, rec.Code)
		}
		if rec.Header().Get("Set-Cookie") != "" {
			t.Errorf("extend=%v: authenticated request wrote a cookie: %q", extend, rec.Header().Get("Set-Cookie"))
		}
		time.Sleep(1800 * time.Millisecond)
		_, err := svc.Authenticate(t.Context(), r)
		if extend && err != nil {
			t.Errorf("extend on: session past its original expiry failed: %v, want it extended", err)
		}
		if !extend && !errors.Is(err, auth.ErrCredentialsExpired) {
			t.Errorf("extend off: session past its expiry = %v, want ErrCredentialsExpired", err)
		}
	}
}

// Docs: modules/auth.mdx#sessions
// "`Revoke(ctx, p)` | The session the principal came from" and "`RevokeAll` |
// Every session of a subject" and "`RevokeAllByScheme` | Every session of a
// subject when the scheme is `session`; a no-op otherwise"
func TestSessionRevoke(t *testing.T) {
	t.Parallel()
	db := sqliteDB(t)
	svc := mustSession(t, db, nil, nil)
	ctx := t.Context()
	r1, _ := sessionRequest(t, svc, "u1", nil)
	r2, _ := sessionRequest(t, svc, "u1", nil)
	r3, _ := sessionRequest(t, svc, "u2", nil)
	p1, _ := svc.Authenticate(ctx, r1)
	if err := svc.Revoke(ctx, p1); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := svc.Authenticate(ctx, r1); err == nil {
		t.Error("revoked session still authenticates")
	}
	if _, err := svc.Authenticate(ctx, r2); err != nil {
		t.Errorf("other session of the subject revoked: %v", err)
	}
	if err := svc.RevokeAllByScheme(ctx, "u1", auth.SchemeAPIKey); err != nil {
		t.Fatalf("RevokeAllByScheme(apikey): %v", err)
	}
	if _, err := svc.Authenticate(ctx, r2); err != nil {
		t.Errorf("RevokeAllByScheme with another scheme revoked a session: %v", err)
	}
	if err := svc.RevokeAllByScheme(ctx, "u1", auth.SchemeSession); err != nil {
		t.Fatalf("RevokeAllByScheme(session): %v", err)
	}
	if _, err := svc.Authenticate(ctx, r2); err == nil {
		t.Error("RevokeAllByScheme(session) left a session")
	}
	r4, _ := sessionRequest(t, svc, "u1", nil)
	if err := svc.RevokeAll(ctx, "u1"); err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}
	if _, err := svc.Authenticate(ctx, r4); err == nil {
		t.Error("RevokeAll left a session")
	}
	if _, err := svc.Authenticate(ctx, r3); err != nil {
		t.Errorf("RevokeAll revoked another subject's session: %v", err)
	}
	if n := rowCount(t, db.DB(), "__gas_auth_sessions"); n != 1 {
		t.Errorf("rows left = %d, want 1 (deleted, not marked)", n)
	}
}

// expiredRows inserts an already expired session through a short-lived
// service and returns the row count afterwards.
func addExpiredSession(t *testing.T, svc *session.Service) {
	t.Helper()
	if _, err := svc.Create(t.Context(), "old", nil, httptest.NewRequest(http.MethodGet, "/", nil)); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

// Docs: modules/auth.mdx#sessions
// "A background goroutine deletes expired sessions every
// `session.cleanupinterval`, bounding each run by `session.cleanuptimeout`;
// `Close` stops it. An interval of zero or less disables it."
func TestSessionCleanup(t *testing.T) {
	t.Parallel()
	cfgWith := func(interval time.Duration) *session.Config {
		cfg := session.DefaultConfig()
		cfg.Session.SessionTTL = time.Millisecond
		cfg.Session.CleanupInterval = interval
		return cfg
	}
	db := sqliteDB(t)
	svc := mustSession(t, db, cfgWith(50*time.Millisecond), nil)
	addExpiredSession(t, svc)
	if !eventually(func() bool { return rowCount(t, db.DB(), "__gas_auth_sessions") == 0 }) {
		t.Error("expired session not cleaned up")
	}
	if err := svc.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	addExpiredSession(t, svc)
	time.Sleep(300 * time.Millisecond)
	if n := rowCount(t, db.DB(), "__gas_auth_sessions"); n != 1 {
		t.Errorf("rows after Close = %d, want 1: cleanup still running", n)
	}

	for _, interval := range []time.Duration{0, -time.Second} {
		db := sqliteDB(t)
		svc := mustSession(t, db, cfgWith(interval), nil)
		addExpiredSession(t, svc)
		time.Sleep(300 * time.Millisecond)
		if n := rowCount(t, db.DB(), "__gas_auth_sessions"); n != 1 {
			t.Errorf("interval %v: rows = %d, want 1 (cleanup disabled)", interval, n)
		}
	}
}

// Docs: modules/auth.mdx#sessions
// "The service logs a warning at `Init` and on every `SetCookie` and
// `ClearCookie` when the cookie is not `Secure`, not `HttpOnly`, or has a
// `SameSite` other than `Lax` or `Strict`. It does not refuse to run."
func TestSessionCookieWarnings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*session.Settings)
		warn bool
	}{
		{"defaults", func(*session.Settings) {}, false},
		{"strict", func(s *session.Settings) { s.CookieSameSite = http.SameSiteStrictMode }, false},
		{"not secure", func(s *session.Settings) { s.CookieSecure = false }, true},
		{"not httponly", func(s *session.Settings) { s.CookieHTTPOnly = false }, true},
		{"samesite none", func(s *session.Settings) { s.CookieSameSite = http.SameSiteNoneMode }, true},
		{"samesite default", func(s *session.Settings) { s.CookieSameSite = http.SameSiteDefaultMode }, true},
	} {
		log := &recLogger{}
		cfg := session.DefaultConfig()
		tc.edit(&cfg.Session)
		svc := mustSession(t, sqliteDB(t), cfg, log)
		atInit := log.warns()
		s, err := svc.Create(t.Context(), "u", nil, httptest.NewRequest(http.MethodGet, "/", nil))
		if err != nil {
			t.Fatalf("%s: Create: %v", tc.name, err)
		}
		svc.SetCookie(httptest.NewRecorder(), s)
		afterSet := log.warns()
		svc.ClearCookie(httptest.NewRecorder())
		afterClear := log.warns()
		if tc.warn {
			if atInit == 0 || afterSet <= atInit || afterClear <= afterSet {
				t.Errorf("%s: warnings at Init, SetCookie, ClearCookie = %d, %d, %d; want one more at each", tc.name, atInit, afterSet, afterClear)
			}
		} else if afterClear != 0 {
			t.Errorf("%s: %d warnings, want none", tc.name, afterClear)
		}
	}
}

// Docs: modules/auth.mdx#configuration
// "| `session.cookiename` | string | `session_id` | Cookie that carries the session ID. Required. |"
// and "| `session.cookiepath` | string | `/` | Cookie path. |" and
// "| `session.cookiedomain` | string | empty | Cookie domain. |" and
// "| `session.cookiesecure` | bool | `true` | `Secure` attribute. |" and
// "| `session.cookiehttponly` | bool | `true` | `HttpOnly` attribute. |" and
// "| `session.cookiesamesite` | int (`http.SameSite`) | `2` (Lax) | `1` default, `2` Lax, `3` Strict, `4` None. |"
// and "| `session.sessionttl` | duration | `24h` | Session lifetime. Must be greater than zero. |"
// and "| `session.extendonaccess` | bool | `true` |" and
// "| `session.cleanupinterval` | duration | `1h` |" and
// "| `session.cleanuptimeout` | duration | `30s` | Bound on each cleanup run. Must be positive while cleanup is enabled. |"
func TestSessionConfigTable(t *testing.T) {
	t.Parallel()
	d := session.DefaultConfig().Session
	if d.CookieName != "session_id" || d.CookiePath != "/" || d.CookieDomain != "" || !d.CookieSecure || !d.CookieHTTPOnly ||
		d.CookieSameSite != 2 || d.SessionTTL != 24*time.Hour || !d.ExtendOnAccess || d.CleanupInterval != time.Hour || d.CleanupTimeout != 30*time.Second {
		t.Errorf("defaults = %+v", d)
	}
	if http.SameSiteDefaultMode != 1 || http.SameSiteLaxMode != 2 || http.SameSiteStrictMode != 3 || http.SameSiteNoneMode != 4 {
		t.Error("http.SameSite values differ from the table")
	}

	db := sqliteDB(t)
	svc, err := startStore(t, "session", db, boundConfig(t, map[string]any{"session": map[string]any{
		"cookiename": "bound_sid", "cookiepath": "/app", "cookiedomain": "example.com",
		"cookiesecure": false, "cookiehttponly": false, "cookiesamesite": 3, "sessionttl": "2h",
	}}), nil, nil)
	if err != nil {
		t.Fatalf("bound Init: %v", err)
	}
	s := svc.(*session.Service)
	sess, _ := s.Create(t.Context(), "u", nil, httptest.NewRequest(http.MethodGet, "/", nil))
	rec := httptest.NewRecorder()
	s.SetCookie(rec, sess)
	c := rec.Result().Cookies()[0]
	if c.Name != "bound_sid" || c.Path != "/app" || c.Domain != "example.com" || c.Secure || c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("bound cookie = %+v", c)
	}
	if d := time.Until(sess.ExpiresAt); d < 119*time.Minute || d > 2*time.Hour {
		t.Errorf("bound sessionttl gave expiry in %v, want 2h", d)
	}

	for name, doc := range map[string]map[string]any{
		"empty cookiename": {"cookiename": ""},
		"zero sessionttl":  {"sessionttl": "0s"},
	} {
		if _, err := startStore(t, "session", sqliteDB(t), boundConfig(t, map[string]any{"session": doc}), nil, nil); err == nil {
			t.Errorf("bound %s: Init succeeded, want a validation error", name)
		}
	}
	// Given with WithConfig, the same values skip the tags.
	given := session.DefaultConfig()
	given.Session.SessionTTL = 0
	given.Session.CookieName = ""
	if _, err := startStore(t, "session", sqliteDB(t), nil, nil, session.WithConfig(given)); err != nil {
		t.Errorf("WithConfig with zero sessionttl and empty cookiename: %v, want tags skipped", err)
	}
}

// Docs: modules/auth.mdx#configuration
// "The HS256 key, RS256 public key path and cleanup-timeout rules are checked
// for every config, including one given with `WithConfig`."
func TestSessionCleanupTimeoutRule(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		interval, timeout time.Duration
		ok                bool
	}{
		{time.Hour, 0, false},
		{time.Hour, -time.Second, false},
		{0, 0, true},
		{time.Hour, time.Second, true},
	} {
		cfg := session.DefaultConfig()
		cfg.Session.CleanupInterval, cfg.Session.CleanupTimeout = tc.interval, tc.timeout
		_, err := startStore(t, "session", sqliteDB(t), nil, nil, session.WithConfig(cfg))
		if (err == nil) != tc.ok {
			t.Errorf("WithConfig interval %v timeout %v: Init = %v, want ok %v", tc.interval, tc.timeout, err, tc.ok)
		}
		if err != nil && !strings.HasPrefix(err.Error(), "gas/auth/session: ") {
			t.Errorf("rule error %q does not start with gas/auth/session", err)
		}
		bound, bErr := startStore(t, "session", sqliteDB(t), boundConfig(t, map[string]any{"session": map[string]any{
			"cleanupinterval": tc.interval.String(), "cleanuptimeout": tc.timeout.String(),
		}}), nil, nil)
		_ = bound
		if (bErr == nil) != tc.ok {
			t.Errorf("bound interval %v timeout %v: Init = %v, want ok %v", tc.interval, tc.timeout, bErr, tc.ok)
		}
	}
}
