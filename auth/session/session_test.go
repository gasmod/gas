package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
)

func TestService_Close_idempotent(t *testing.T) {
	svc := &Service{done: make(chan struct{})}

	assert.NoError(t, svc.Close(context.Background()), "first Close should succeed")
	assert.NotPanics(t, func() {
		_ = svc.Close(context.Background())
	}, "second Close must not panic")
}

// TestService_CookieAttributes checks that SetCookie and ClearCookie copy
// every cookie attribute from the config, for the secure defaults and for a
// config that overrides each one.
func TestService_CookieAttributes(t *testing.T) {
	custom := DefaultConfig()
	custom.Session.CookieName = "sid"
	custom.Session.CookiePath = "/app"
	custom.Session.CookieDomain = "example.com"
	custom.Session.CookieSecure = false
	custom.Session.CookieHTTPOnly = false
	custom.Session.CookieSameSite = http.SameSiteStrictMode

	tests := []struct {
		cfg  *Config
		name string
	}{
		{name: "defaults", cfg: DefaultConfig()},
		{name: "overrides", cfg: custom},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &Service{cfg: tt.cfg, logger: &gas.NopLogger{}}
			want := tt.cfg.Session

			assertAttrs := func(t *testing.T, c *http.Cookie) {
				t.Helper()
				assert.Equal(t, want.CookieName, c.Name)
				assert.Equal(t, want.CookiePath, c.Path)
				assert.Equal(t, want.CookieDomain, c.Domain)
				assert.Equal(t, want.CookieSecure, c.Secure)
				assert.Equal(t, want.CookieHTTPOnly, c.HttpOnly)
				assert.Equal(t, want.CookieSameSite, c.SameSite)
			}

			t.Run("SetCookie", func(t *testing.T) {
				sess := &Session{ID: "sess-123", ExpiresAt: time.Now().Add(time.Hour)}
				w := httptest.NewRecorder()
				svc.SetCookie(w, sess)

				cookies := w.Result().Cookies()
				require.Len(t, cookies, 1)
				c := cookies[0]
				assertAttrs(t, c)
				assert.Equal(t, sess.ID, c.Value)
				assert.WithinDuration(t, sess.ExpiresAt, c.Expires, time.Second)
			})

			t.Run("ClearCookie", func(t *testing.T) {
				w := httptest.NewRecorder()
				svc.ClearCookie(w)

				cookies := w.Result().Cookies()
				require.Len(t, cookies, 1)
				c := cookies[0]
				assertAttrs(t, c)
				assert.Empty(t, c.Value)
				assert.Negative(t, c.MaxAge)
			})
		})
	}
}
