package token_test

import (
	"errors"
	"testing"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/auth/internal/svcutil"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/token"
	"github.com/gasmod/gas/config/configtest"
)

// DefaultConfig holds the documented defaults, as a new value each time.
func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := token.DefaultConfig()
	assert.Equal(t, 15*time.Minute, cfg.Token.DefaultTTL)
	assert.Equal(t, 32, cfg.Token.TokenLength)
	assert.Equal(t, time.Hour, cfg.Token.CleanupInterval)
	assert.Equal(t, 30*time.Second, cfg.Token.CleanupTimeout)
	assert.Equal(t, token.DefaultTTL, cfg.Token.DefaultTTL)
	assert.Equal(t, token.DefaultTokenLength, cfg.Token.TokenLength)
	assert.Equal(t, token.DefaultCleanupInterval, cfg.Token.CleanupInterval)
	assert.Equal(t, token.DefaultCleanupTimeout, cfg.Token.CleanupTimeout)
	assert.NotSame(t, cfg, token.DefaultConfig())
}

// initWith builds a service on SQLite with opts and the config provider,
// and returns what its Init returns, after running the migrations when
// Init succeeds.
func initWith(
	t *testing.T,
	provider *configtest.MockConfig,
	opts ...token.Option,
) (*token.Service, error) {
	t.Helper()

	b := sqliteBackend(t)

	var (
		svc *token.Service
		err error
	)

	if provider == nil {
		svc, err = token.New(opts...)(b.DB, testutil.NewNopLogger(), b.Migrations, nil)
	} else {
		svc, err = token.New(opts...)(b.DB, testutil.NewNopLogger(), b.Migrations, provider)
	}

	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, svc.Close(t.Context())) })

	err = svc.Init(t.Context())
	if err == nil {
		require.NoError(t, b.Migrations.RunPending())
	}

	return svc, err
}

// configCheckCase is a configuration that breaks a rule, as a change to
// the defaults and as values of the `token` section, with the error Init
// gives for it.
type configCheckCase struct {
	// apply breaks the rule in a Config given to WithConfig.
	apply func(*token.Settings)
	// values break the rule in the bound section.
	values map[string]any
	// name names the subtest.
	name string
	// want is Init's error for the given Config.
	want string
	// tag reports whether a validate tag, not a rule, catches it.
	tag bool
}

// configCheckCases returns the configurations TestInit_ConfigChecks
// tries.
func configCheckCases() []configCheckCase {
	return []configCheckCase{
		{
			name:   "zero ttl",
			apply:  func(s *token.Settings) { s.DefaultTTL = 0 },
			values: map[string]any{"defaultttl": "0s"},
			want: "gas/auth/token: invalid config: validate config: Key: 'Config.Token.DefaultTTL' " +
				"Error:Field validation for 'DefaultTTL' failed on the 'gt' tag",
			tag: true,
		},
		{
			name:   "negative ttl",
			apply:  func(s *token.Settings) { s.DefaultTTL = -time.Second },
			values: map[string]any{"defaultttl": "-1s"},
			want: "gas/auth/token: invalid config: validate config: Key: 'Config.Token.DefaultTTL' " +
				"Error:Field validation for 'DefaultTTL' failed on the 'gt' tag",
			tag: true,
		},
		{
			name:   "short token",
			apply:  func(s *token.Settings) { s.TokenLength = 15 },
			values: map[string]any{"tokenlength": 15},
			want: "gas/auth/token: invalid config: validate config: Key: 'Config.Token.TokenLength' " +
				"Error:Field validation for 'TokenLength' failed on the 'min' tag",
			tag: true,
		},
		{
			name:   "negative cleanup interval",
			apply:  func(s *token.Settings) { s.CleanupInterval = -time.Second },
			values: map[string]any{"cleanupinterval": "-1s"},
			want:   "gas/auth/token: invalid config: cleanup interval must not be negative",
		},
		{
			name:   "cleanup without timeout",
			apply:  func(s *token.Settings) { s.CleanupTimeout = 0 },
			values: map[string]any{"cleanuptimeout": "0s"},
			want:   "gas/auth/token: invalid config: cleanup timeout must be positive when cleanup is enabled",
		},
	}
}

// A configuration given to WithConfig and one bound from the
// ConfigProvider are checked the same way: the validate tags, then the
// cleanup rules. Init fails with ErrInvalidConfig and the service stays
// uninitialized.
func TestInit_ConfigChecks(t *testing.T) {
	t.Parallel()

	for _, tt := range configCheckCases() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := token.DefaultConfig()
			tt.apply(&cfg.Token)

			svc, err := initWith(t, nil, token.WithConfig(cfg))
			require.ErrorIs(t, err, token.ErrInvalidConfig)
			require.EqualError(t, err, tt.want)

			var verrs validator.ValidationErrors

			assert.Equal(t, tt.tag, errors.As(err, &verrs))

			_, err = svc.Issue(t.Context(), "s", "p", 0)
			require.ErrorIs(t, err, token.ErrNotInitialized)

			provider, err := configtest.NewMockConfigWithValues(map[string]any{"token": tt.values})
			require.NoError(t, err)

			_, err = initWith(t, provider)
			require.ErrorIs(t, err, token.ErrInvalidConfig)

			if !tt.tag {
				require.EqualError(t, err, tt.want)
			}
		})
	}
}

// The edges of the rules are accepted: a 16-byte token, and a cleanup
// that is off with no timeout, from WithConfig and from the provider.
func TestInit_ConfigEdges(t *testing.T) {
	t.Parallel()

	cfg := token.DefaultConfig()

	cfg.Token.TokenLength = 16
	cfg.Token.CleanupInterval = 0
	cfg.Token.CleanupTimeout = 0

	svc, err := initWith(t, nil, token.WithConfig(cfg))
	require.NoError(t, err)

	raw, err := svc.Issue(t.Context(), "s", "p", 0)
	require.NoError(t, err)
	assert.Len(t, raw, 22)

	provider, err := configtest.NewMockConfigWithValues(map[string]any{"token": map[string]any{
		"tokenlength":     16,
		"cleanupinterval": "0s",
		"cleanuptimeout":  "0s",
	}})
	require.NoError(t, err)

	_, err = initWith(t, provider)
	require.NoError(t, err)
}

// Without WithConfig the `token` section is bound over the defaults, so a
// key it does not set keeps its default.
func TestInit_BindsSection(t *testing.T) {
	t.Parallel()

	provider, err := configtest.NewMockConfigWithValues(map[string]any{"token": map[string]any{
		"tokenlength":     48,
		"cleanupinterval": "0s",
	}})
	require.NoError(t, err)

	svc, err := initWith(t, provider)
	require.NoError(t, err)

	raw, err := svc.Issue(t.Context(), "s", "p", 0)
	require.NoError(t, err)
	assert.Len(t, raw, 64)

	subject, err := svc.Verify(t.Context(), raw, "p")
	require.NoError(t, err)
	assert.Equal(t, "s", subject)
}

// WithConfig(nil) fails Init with ErrNilConfig, and no WithConfig and no
// ConfigProvider with ErrNoConfigProvider; neither panics. They are the
// shared service sentinels.
func TestInit_NoConfig(t *testing.T) {
	t.Parallel()

	_, err := initWith(t, nil, token.WithConfig(nil))
	require.ErrorIs(t, err, token.ErrNilConfig)
	require.ErrorIs(t, err, svcutil.ErrNilConfig)
	require.EqualError(t, err, "gas/auth/token: config is nil")

	_, err = initWith(t, nil)
	require.ErrorIs(t, err, token.ErrNoConfigProvider)
	require.ErrorIs(t, err, svcutil.ErrNoConfigProvider)
	require.EqualError(t, err, "gas/auth/token: no config provider")

	require.ErrorIs(t, token.ErrInvalidConfig, svcutil.ErrInvalidConfig)
	require.ErrorIs(t, token.ErrNotInitialized, svcutil.ErrNotInitialized)
}

// Init uses a copy of the configuration given to WithConfig: changing the
// caller's struct afterwards has no effect until Init runs again, which
// picks the change up.
func TestInit_CopiesConfig(t *testing.T) {
	t.Parallel()

	cfg := testConfig()

	svc, err := initWith(t, nil, token.WithConfig(cfg))
	require.NoError(t, err)

	cfg.Token.TokenLength = 16

	raw, err := svc.Issue(t.Context(), "s", "p", 0)
	require.NoError(t, err)
	assert.Len(t, raw, 43)

	require.NoError(t, svc.Close(t.Context()))
	require.NoError(t, svc.Init(t.Context()))

	raw, err = svc.Issue(t.Context(), "s", "p", 0)
	require.NoError(t, err)
	assert.Len(t, raw, 22)
}
