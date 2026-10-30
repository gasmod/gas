package apikey_test

import (
	"testing"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/apikey"
	"github.com/gasmod/gas/auth/internal/svcutil"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/config/configtest"
)

// DefaultConfig holds the documented defaults: the header
// DefaultHeaderName, no prefix, DefaultKeyLength bytes, the cleanup off,
// and DefaultCleanupTimeout.
func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	assert.Equal(t, &apikey.Config{APIKey: apikey.Settings{
		HeaderName:     "X-API-Key",
		KeyLength:      32,
		CleanupTimeout: 30 * time.Second,
	}}, apikey.DefaultConfig())

	changed := apikey.DefaultConfig()

	changed.APIKey.Prefix = "changed_"

	assert.Empty(t, apikey.DefaultConfig().APIKey.Prefix, "each call returns a new Config")
}

// The re-exported sentinels are svcutil's and the auth package's, so
// either matches.
func TestErrors_AreShared(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, apikey.ErrNilConfig, svcutil.ErrNilConfig)
	require.ErrorIs(t, apikey.ErrNoConfigProvider, svcutil.ErrNoConfigProvider)
	require.ErrorIs(t, apikey.ErrInvalidConfig, svcutil.ErrInvalidConfig)
	require.ErrorIs(t, apikey.ErrNotInitialized, svcutil.ErrNotInitialized)
	require.ErrorIs(t, apikey.ErrNilLogger, svcutil.ErrNilLogger)
	require.ErrorIs(t, apikey.ErrEmptySubject, auth.ErrEmptySubject)
	require.ErrorIs(t, apikey.ErrValueTooLong, auth.ErrValueTooLong)
	require.ErrorIs(t, apikey.ErrNilPrincipal, auth.ErrNilPrincipal)
	require.ErrorIs(t, apikey.ErrSchemeMismatch, auth.ErrSchemeMismatch)
}

// A configuration given to WithConfig is checked against the validate tags
// and the cleanup rules, and one that fails makes Init fail with
// ErrInvalidConfig, naming the field or rule and never the value.
func TestInit_WithConfigChecked(t *testing.T) {
	t.Parallel()

	tests := []struct {
		change func(*apikey.Settings)
		name   string
		want   string
	}{
		{
			name:   "key length below 16",
			change: func(s *apikey.Settings) { s.KeyLength = 15 },
			want: "gas/auth/apikey: invalid config: validate config: Key: 'Config.APIKey.KeyLength' " +
				"Error:Field validation for 'KeyLength' failed on the 'min' tag",
		},
		{
			name:   "empty header name",
			change: func(s *apikey.Settings) { s.HeaderName = "" },
			want: "gas/auth/apikey: invalid config: validate config: Key: 'Config.APIKey.HeaderName' " +
				"Error:Field validation for 'HeaderName' failed on the 'required' tag",
		},
		{
			name:   "negative cleanup interval",
			change: func(s *apikey.Settings) { s.CleanupInterval = -time.Second },
			want:   "gas/auth/apikey: invalid config: cleanup interval must not be negative",
		},
		{
			name: "cleanup without timeout",
			change: func(s *apikey.Settings) {
				s.CleanupInterval = time.Minute
				s.CleanupTimeout = 0
			},
			want: "gas/auth/apikey: invalid config: cleanup timeout must be positive when cleanup is enabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := apikey.DefaultConfig()
			tt.change(&cfg.APIKey)

			svc, err := apikey.New(apikey.WithConfig(cfg))(nil, testutil.NewNopLogger(), nil, nil)
			require.NoError(t, err)

			err = svc.Init(t.Context())
			require.ErrorIs(t, err, apikey.ErrInvalidConfig)
			require.EqualError(t, err, tt.want)
		})
	}
}

// A tag failure keeps the validator's error in the chain.
func TestInit_ValidationErrorsInChain(t *testing.T) {
	t.Parallel()

	cfg := apikey.DefaultConfig()

	cfg.APIKey.KeyLength = 1

	svc, err := apikey.New(apikey.WithConfig(cfg))(nil, testutil.NewNopLogger(), nil, nil)
	require.NoError(t, err)

	var verrs validator.ValidationErrors

	require.ErrorAs(t, svc.Init(t.Context()), &verrs)
}

// A cleanup with an interval and a timeout passes the rules, and a
// timeout without an interval is allowed.
func TestInit_CleanupRulesPass(t *testing.T) {
	t.Parallel()

	for _, s := range []apikey.Settings{
		{CleanupInterval: time.Hour, CleanupTimeout: time.Second},
		{CleanupInterval: 0, CleanupTimeout: 0},
	} {
		cfg := apikey.DefaultConfig()

		cfg.APIKey.CleanupInterval = s.CleanupInterval
		cfg.APIKey.CleanupTimeout = s.CleanupTimeout

		svc := newService(t, sqliteBackend(t), cfg, nil)
		require.NoError(t, svc.Close(t.Context()))
	}
}

// A nil Config given to WithConfig fails Init with ErrNilConfig, without a
// panic.
func TestInit_NilConfig(t *testing.T) {
	t.Parallel()

	svc, err := apikey.New(apikey.WithConfig(nil))(nil, testutil.NewNopLogger(), nil, nil)
	require.NoError(t, err)

	err = svc.Init(t.Context())
	require.ErrorIs(t, err, apikey.ErrNilConfig)
	require.EqualError(t, err, "gas/auth/apikey: config is nil")
}

// Without WithConfig and without a ConfigProvider, Init fails with
// ErrNoConfigProvider.
func TestInit_NoConfigProvider(t *testing.T) {
	t.Parallel()

	svc, err := apikey.New()(nil, testutil.NewNopLogger(), nil, nil)
	require.NoError(t, err)

	err = svc.Init(t.Context())
	require.ErrorIs(t, err, apikey.ErrNoConfigProvider)
	require.EqualError(t, err, "gas/auth/apikey: no config provider")
}

// WithConfig takes a copy in Init: a change to the caller's Config after
// Init does not reach the service, and the service never changes it.
func TestInit_WithConfigCopied(t *testing.T) {
	t.Parallel()

	b := sqliteBackend(t)
	cfg := apikey.DefaultConfig()

	cfg.APIKey.Prefix = "before_"

	svc := newService(t, b, cfg, nil)

	cfg.APIKey.Prefix = "after_"

	key, _, err := svc.Generate(t.Context(), "alice", "k", nil)
	require.NoError(t, err)
	assert.Contains(t, key, "before_")
	assert.Equal(t, "after_", cfg.APIKey.Prefix)
}

// Without WithConfig, Init binds DefaultConfig from the ConfigProvider,
// under the section `apikey`, so a setting the configuration does not set
// keeps its default, and the bound configuration is checked too.
func TestInit_BindsFromProvider(t *testing.T) {
	t.Parallel()

	provider, err := configtest.NewMockConfigWithValues(map[string]any{
		"apikey": map[string]any{"prefix": "bound_", "headername": "X-Key"},
	})
	require.NoError(t, err)

	b := sqliteBackend(t)
	svc, err := apikey.New()(b.DB, testutil.NewNopLogger(), b.Migrations, provider)
	require.NoError(t, err)
	require.NoError(t, svc.Init(t.Context()))
	require.NoError(t, b.Migrations.RunPending())

	key, _, err := svc.Generate(t.Context(), "alice", "k", nil)
	require.NoError(t, err)
	assert.Len(t, key, len("bound_")+43, "the default KeyLength of 32 bytes is kept")

	r := request("")
	r.Header.Set("X-Key", key)

	p, err := svc.Authenticate(t.Context(), r)
	require.NoError(t, err)
	assert.Equal(t, "alice", p.Subject())

	bad, err := configtest.NewMockConfigWithValues(map[string]any{
		"apikey": map[string]any{"keylength": 15},
	})
	require.NoError(t, err)

	svc, err = apikey.New()(b.DB, testutil.NewNopLogger(), b.Migrations, bad)
	require.NoError(t, err)
	require.ErrorIs(t, svc.Init(t.Context()), apikey.ErrInvalidConfig)
}
