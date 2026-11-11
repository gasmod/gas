package session_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/session"
	"github.com/gasmod/gas/config/configtest"
)

// errBind stands for a ConfigProvider that cannot bind.
var errBind = errors.New("bind failed")

// DefaultConfig holds the documented defaults.
func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	assert.Equal(t, &session.Config{Session: session.Settings{
		CookieName:      "session_id",
		CookiePath:      "/",
		CookieSameSite:  http.SameSiteLaxMode,
		SessionTTL:      24 * time.Hour,
		MaxLifetime:     30 * 24 * time.Hour,
		CleanupInterval: time.Hour,
		CleanupTimeout:  30 * time.Second,
		CookieSecure:    true,
		CookieHTTPOnly:  true,
		ExtendOnAccess:  true,
	}}, session.DefaultConfig())
}

// initWith builds a service with cfg on SQLite and runs Init.
func initWith(t *testing.T, cfg *session.Config) error {
	t.Helper()

	_, db, migrations := testutil.SetupSQLite(t)

	svc, err := session.New(session.WithConfig(cfg))(db, testutil.NewNopLogger(), migrations, nil)
	require.NoError(t, err)

	return svc.Init(t.Context())
}

// configRuleCases are configurations that fail a validate tag or a rule,
// with the error Init returns after the service's name.
var configRuleCases = []struct {
	mutate func(*session.Settings)
	name   string
	want   string
}{
	{
		name:   "cleanup timeout",
		mutate: func(s *session.Settings) { s.CleanupTimeout = 0 },
		want:   "invalid config: cleanup timeout must be positive when cleanup is enabled",
	},
	{
		name:   "negative cleanup interval",
		mutate: func(s *session.Settings) { s.CleanupInterval = -time.Second },
		want:   "invalid config: cleanup interval must not be negative",
	},
	{
		name:   "negative activity interval",
		mutate: func(s *session.Settings) { s.ActivityInterval = -time.Second },
		want:   "invalid config: activity interval must not be negative",
	},
	{
		name:   "ttl over max",
		mutate: func(s *session.Settings) { s.SessionTTL = s.MaxLifetime + time.Second },
		want:   "invalid config: session TTL must not exceed the max lifetime",
	},
	{
		name:   "cookie name",
		mutate: func(s *session.Settings) { s.CookieName = "hunter2;x" },
		want:   "invalid config: cookie name is not a valid cookie name",
	},
	{
		name:   "cookie path",
		mutate: func(s *session.Settings) { s.CookiePath = "/hunter2;x" },
		want:   "invalid config: cookie path is not a valid cookie path",
	},
	{
		name:   "cookie domain",
		mutate: func(s *session.Settings) { s.CookieDomain = "hunter2..example" },
		want:   "invalid config: cookie domain is not a valid cookie domain",
	},
	{
		name:   "same site high",
		mutate: func(s *session.Settings) { s.CookieSameSite = 5 },
		want:   "invalid config: cookie SameSite is not one of the http.SameSite values",
	},
	{
		name:   "same site negative",
		mutate: func(s *session.Settings) { s.CookieSameSite = -1 },
		want:   "invalid config: cookie SameSite is not one of the http.SameSite values",
	},
	{
		name:   "empty cookie name",
		mutate: func(s *session.Settings) { s.CookieName = "" },
		want: "invalid config: validate config: Key: 'Config.Session.CookieName' " +
			"Error:Field validation for 'CookieName' failed on the 'required' tag",
	},
	{
		name:   "zero ttl",
		mutate: func(s *session.Settings) { s.SessionTTL = 0 },
		want: "invalid config: validate config: Key: 'Config.Session.SessionTTL' " +
			"Error:Field validation for 'SessionTTL' failed on the 'gt' tag",
	},
	{
		name:   "zero max lifetime",
		mutate: func(s *session.Settings) { s.MaxLifetime = 0 },
		want: "invalid config: validate config: Key: 'Config.Session.MaxLifetime' " +
			"Error:Field validation for 'MaxLifetime' failed on the 'gt' tag",
	},
}

// Init rejects a configuration that fails a validate tag or a rule, with
// an error that wraps ErrInvalidConfig and names the setting, never its
// value.
func TestInit_ConfigRules(t *testing.T) {
	t.Parallel()

	for _, tt := range configRuleCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := session.DefaultConfig()
			tt.mutate(&cfg.Session)

			err := initWith(t, cfg)
			require.ErrorIs(t, err, session.ErrInvalidConfig)
			require.EqualError(t, err, "gas/auth/session: "+tt.want)
			assert.NotContains(t, err.Error(), "hunter2")
		})
	}
}

// The edges of the rules are allowed: cleanup off with no timeout, a TTL
// equal to the max lifetime, no path, domain or SameSite.
func TestInit_ConfigEdges(t *testing.T) {
	t.Parallel()

	cfg := session.DefaultConfig()

	cfg.Session.CleanupInterval = 0
	cfg.Session.CleanupTimeout = 0
	cfg.Session.SessionTTL = cfg.Session.MaxLifetime
	cfg.Session.CookiePath = ""
	cfg.Session.CookieDomain = ""
	cfg.Session.CookieSameSite = 0

	require.NoError(t, initWith(t, cfg))
}

// WithConfig(nil) fails Init with ErrNilConfig rather than panicking.
func TestWithConfig_Nil(t *testing.T) {
	t.Parallel()

	err := initWith(t, nil)
	require.ErrorIs(t, err, session.ErrNilConfig)
	require.EqualError(t, err, "gas/auth/session: config is nil")
}

// Init uses a copy of the WithConfig configuration: later changes to the
// caller's struct do not reach the service.
func TestWithConfig_Copied(t *testing.T) {
	t.Parallel()

	cfg := session.DefaultConfig()

	cfg.Session.CleanupInterval = 0

	_, db, migrations := testutil.SetupSQLite(t)

	svc, err := session.New(session.WithConfig(cfg))(db, testutil.NewNopLogger(), migrations, nil)
	require.NoError(t, err)
	require.NoError(t, svc.Init(t.Context()))
	require.NoError(t, migrations.RunPending())

	cfg.Session.CookieName = "changed"

	sess, err := svc.Create(t.Context(), "alice", nil, newRequest(http.MethodPost))
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	require.NoError(t, svc.SetCookie(rec, sess))
	assert.Equal(t, session.DefaultCookieName, responseCookie(t, rec).Name)
	assert.Equal(t, "changed", cfg.Session.CookieName)
}

// Without WithConfig, Init binds the settings from the ConfigProvider
// over the defaults, and runs the rules on the result.
func TestInit_BindsFromConfigProvider(t *testing.T) {
	t.Parallel()

	provider, err := configtest.NewMockConfigWithValues(map[string]any{
		"Session": map[string]any{
			"CookieName":      "sid",
			"MaxLifetime":     "48h",
			"CleanupInterval": "0s",
		},
	})
	require.NoError(t, err)

	_, db, migrations := testutil.SetupSQLite(t)

	svc, err := session.New()(db, testutil.NewNopLogger(), migrations, provider)
	require.NoError(t, err)
	require.NoError(t, svc.Init(t.Context()))
	require.NoError(t, migrations.RunPending())

	sess, err := svc.Create(t.Context(), "alice", nil, newRequest(http.MethodPost))
	require.NoError(t, err)
	assert.Equal(t, sess.CreatedAt.Add(session.DefaultSessionTTL), sess.ExpiresAt)

	rec := httptest.NewRecorder()
	require.NoError(t, svc.SetCookie(rec, sess))

	c := responseCookie(t, rec)
	assert.Equal(t, "sid", c.Name)
	assert.True(t, within(sess.CreatedAt.Add(48*time.Hour), c.Expires, time.Second))

	bad, err := configtest.NewMockConfigWithValues(map[string]any{
		"Session": map[string]any{"SessionTTL": "72h", "MaxLifetime": "48h"},
	})
	require.NoError(t, err)

	svc, err = session.New()(db, testutil.NewNopLogger(), migrations, bad)
	require.NoError(t, err)

	err = svc.Init(t.Context())
	require.ErrorIs(t, err, session.ErrInvalidConfig)
	require.EqualError(t, err,
		"gas/auth/session: invalid config: session TTL must not exceed the max lifetime")
}

// A bind failure is ErrInvalidConfig wrapping the provider's error, and
// no ConfigProvider at all is ErrNoConfigProvider.
func TestInit_BindErrors(t *testing.T) {
	t.Parallel()

	_, db, migrations := testutil.SetupSQLite(t)

	provider := &configtest.MockConfig{BindFn: func(any) error { return errBind }}

	svc, err := session.New()(db, testutil.NewNopLogger(), migrations, provider)
	require.NoError(t, err)

	err = svc.Init(t.Context())
	require.ErrorIs(t, err, session.ErrInvalidConfig)
	require.ErrorIs(t, err, errBind)
	require.EqualError(t, err, "gas/auth/session: invalid config: bind failed")

	svc, err = session.New()(db, testutil.NewNopLogger(), migrations, nil)
	require.NoError(t, err)

	err = svc.Init(t.Context())
	require.ErrorIs(t, err, session.ErrNoConfigProvider)
	require.EqualError(t, err, "gas/auth/session: no config provider")
}

// testConfig returns the configuration sqliteEnv gives the service when
// mutate is nil: the defaults with the cleanup off.
func testConfig() session.Config {
	cfg := *session.DefaultConfig()

	cfg.Session.CleanupInterval = 0

	return cfg
}

// Config returns the zero Config before Init, and after Init a copy of
// the configuration it bound, defaults included. Changing a result does
// not reach the service, and Close leaves it as it was.
func TestService_Config(t *testing.T) {
	t.Parallel()

	provider, err := configtest.NewMockConfigWithValues(map[string]any{
		"Session": map[string]any{"CookieName": "sid", "CleanupInterval": "0s"},
	})
	require.NoError(t, err)

	_, db, migrations := testutil.SetupSQLite(t)

	svc, err := session.New()(db, testutil.NewNopLogger(), migrations, provider)
	require.NoError(t, err)
	assert.Equal(t, session.Config{}, svc.Config(), "before Init")

	require.NoError(t, svc.Init(t.Context()))

	want := testConfig()

	want.Session.CookieName = "sid"

	got := svc.Config()
	assert.Equal(t, want, got)

	got.Session.CookieName = "changed"
	got.Session.CookieSecure = false

	assert.Equal(t, want, svc.Config(), "a changed result does not reach the service")

	require.NoError(t, svc.Close(t.Context()))
	assert.Equal(t, want, svc.Config(), "after Close")
}

// A failed Init leaves Config on the configuration of the last Init that
// succeeded.
func TestService_ConfigAfterFailedInit(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)

	e.cfg.Session.SessionTTL = 0

	require.ErrorIs(t, e.svc.Init(t.Context()), session.ErrInvalidConfig)
	assert.Equal(t, testConfig(), e.svc.Config())
}

// Config may run while Init and Close do, and returns the configuration
// of one Init throughout. Run with -race.
func TestService_ConfigConcurrentWithRestart(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, nil)
	ctx := t.Context()

	var wg sync.WaitGroup

	wg.Go(func() {
		for range 50 {
			assert.NoError(t, e.svc.Init(ctx))
			assert.NoError(t, e.svc.Close(ctx))
		}
	})

	for range 4 {
		wg.Go(func() {
			for range 200 {
				assert.Equal(t, testConfig(), e.svc.Config())
			}
		})
	}

	wg.Wait()
}
