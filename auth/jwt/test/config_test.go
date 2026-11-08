package jwt_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth/jwt"
	"github.com/gasmod/gas/config/configtest"
)

// bound returns a Service that binds its configuration from the `jwt`
// section values.
func bound(t *testing.T, values m) *jwt.Service {
	t.Helper()

	provider, err := configtest.NewMockConfigWithValues(m{"jwt": values})
	require.NoError(t, err)

	svc, err := jwt.New()(provider, &gas.NopLogger{})
	require.NoError(t, err)

	return svc
}

// short is an HS256 signing key that is too short.
const short = "short-secret-value"

// DefaultConfig holds HS256, a 15 minute expiry, no leeway and no key.
func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	assert.Equal(t, &jwt.Config{JWT: jwt.Settings{
		SigningMethod: jwt.DefaultSigningMethod,
		Expiry:        jwt.DefaultExpiry,
	}}, jwt.DefaultConfig())
	assert.Equal(t, "HS256", jwt.DefaultSigningMethod)
	assert.Equal(t, 15*time.Minute, jwt.DefaultExpiry)
}

// Without WithConfig, Init binds DefaultConfig from the ConfigProvider, so
// a key the configuration does not set keeps its default.
func TestInit_BindsFromProvider(t *testing.T) {
	t.Parallel()

	svc := bound(t, m{"signingkey": testKey, "issuer": "api"})
	require.NoError(t, svc.Init(t.Context()))

	token, err := svc.Sign("alice", nil)
	require.NoError(t, err)

	c := unverified(t, token)
	assert.Equal(t, "api", c["iss"])

	exp, err := c.GetExpirationTime()
	require.NoError(t, err)

	iat, err := c.GetIssuedAt()
	require.NoError(t, err)
	assert.Equal(t, jwt.DefaultExpiry, exp.Sub(iat.Time))
}

// Binding checks the validate tags: the signing method is HS256 or RS256,
// the expiry positive, the leeway not negative.
func TestInit_BindChecksTags(t *testing.T) {
	t.Parallel()

	for name, values := range map[string]m{
		"unsupported method": {"signingkey": testKey, "signingmethod": "ES256"},
		"zero expiry":        {"signingkey": testKey, "expiry": "0s"},
		"negative expiry":    {"signingkey": testKey, "expiry": "-1m"},
		"negative leeway":    {"signingkey": testKey, "leeway": "-1s"},
	} {
		err := bound(t, values).Init(t.Context())
		require.ErrorIs(t, err, jwt.ErrInvalidConfig, name)
		assert.NotContains(t, err.Error(), testKey, name)
	}
}

// A given configuration is checked against the same tags.
func TestWithConfig_ChecksTags(t *testing.T) {
	t.Parallel()

	err := initErr(t, hsConfig(func(s *jwt.Settings) { s.Expiry = 0 }))
	require.ErrorIs(t, err, jwt.ErrInvalidConfig)
	require.EqualError(t, err, "gas/auth/jwt: invalid config: validate config: "+
		"Key: 'Config.JWT.Expiry' Error:Field validation for 'Expiry' failed on the 'gt' tag")

	err = initErr(t, hsConfig(func(s *jwt.Settings) { s.Leeway = -time.Second }))
	require.ErrorIs(t, err, jwt.ErrInvalidConfig)

	err = initErr(t, hsConfig(func(s *jwt.Settings) { s.SigningMethod = "PS256" }))
	require.ErrorIs(t, err, jwt.ErrInvalidConfig)
}

// The rules a tag cannot express hold for a bound and a given
// configuration alike, and their errors name settings, never values.
func TestInit_Rules(t *testing.T) {
	t.Parallel()

	pub, priv := rsaFiles(t, rsaKey2048())

	tests := ruleCases(pub, priv)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := initErr(t, tt.cfg)
			require.ErrorIs(t, err, jwt.ErrInvalidConfig)
			require.EqualError(t, err, "gas/auth/jwt: invalid config: "+tt.want)
			assert.NotContains(t, err.Error(), short)
			assert.NotContains(t, err.Error(), testKey)
			assert.NotContains(t, err.Error(), pub)
			assert.NotContains(t, err.Error(), priv)

			s := tt.cfg.JWT

			values := m{
				"signingmethod":  s.SigningMethod,
				"signingkey":     s.SigningKey,
				"publickeypath":  s.PublicKeyPath,
				"privatekeypath": s.PrivateKeyPath,
			}

			err = bound(t, values).Init(t.Context())
			require.EqualError(t, err, "gas/auth/jwt: invalid config: "+tt.want)
		})
	}
}

// A minimal HS256 key, MinHMACKeyBytes long, is accepted.
func TestInit_MinimalHMACKey(t *testing.T) {
	t.Parallel()

	require.Len(t, testKey, jwt.MinHMACKeyBytes)
	require.NoError(t, initErr(t, hsConfig()))
}

// Init copies the given configuration and never changes it, so a change
// to the caller's struct after Init does not reach the service, and the
// next Init picks it up.
func TestWithConfig_Copies(t *testing.T) {
	t.Parallel()

	cfg := hsConfig()
	before := *cfg
	svc := newService(t, cfg)
	assert.Equal(t, before, *cfg)

	cfg.JWT.SigningKey = otherKey

	token, err := svc.Sign("alice", nil)
	require.NoError(t, err)

	_, err = newService(t, hsConfig()).Verify(token)
	require.NoError(t, err, "still signed with the key at Init")

	require.NoError(t, svc.Init(t.Context()))

	token, err = svc.Sign("alice", nil)
	require.NoError(t, err)

	_, err = newService(t, hsConfig()).Verify(token)
	require.ErrorIs(t, err, jwt.ErrTokenInvalid, "signed with the key of the second Init")
}

// WithConfig(nil) fails Init with ErrNilConfig rather than panicking.
func TestWithConfig_Nil(t *testing.T) {
	t.Parallel()

	err := initErr(t, nil)
	require.ErrorIs(t, err, jwt.ErrNilConfig)
	require.EqualError(t, err, "gas/auth/jwt: config is nil")
}

// Without WithConfig and without a ConfigProvider, Init fails with
// ErrNoConfigProvider.
func TestInit_NoConfigProvider(t *testing.T) {
	t.Parallel()

	svc, err := jwt.New()(nil, &gas.NopLogger{})
	require.NoError(t, err)

	err = svc.Init(t.Context())
	require.ErrorIs(t, err, jwt.ErrNoConfigProvider)
	require.EqualError(t, err, "gas/auth/jwt: no config provider")
}

// ruleCase is a configuration that breaks a rule, and the rule's message.
type ruleCase struct {
	cfg  *jwt.Config
	name string
	want string
}

// ruleCases returns the cases of TestInit_Rules, with pub and priv the
// paths of an RSA key pair.
func ruleCases(pub, priv string) []ruleCase {
	return []ruleCase{
		{
			name: "HS256 without a key",
			cfg:  hsConfig(func(s *jwt.Settings) { s.SigningKey = "" }),
			want: "HS256 requires JWT.SigningKey of at least 32 bytes",
		},
		{
			name: "HS256 with a short key",
			cfg:  hsConfig(func(s *jwt.Settings) { s.SigningKey = short }),
			want: "HS256 requires JWT.SigningKey of at least 32 bytes",
		},
		{
			name: "HS256 with a public key path",
			cfg:  hsConfig(func(s *jwt.Settings) { s.PublicKeyPath = pub }),
			want: "HS256 requires JWT.PublicKeyPath and JWT.PrivateKeyPath to be empty",
		},
		{
			name: "HS256 with a private key path",
			cfg:  hsConfig(func(s *jwt.Settings) { s.PrivateKeyPath = priv }),
			want: "HS256 requires JWT.PublicKeyPath and JWT.PrivateKeyPath to be empty",
		},
		{
			name: "RS256 without a public key path",
			cfg:  rsConfig("", priv),
			want: "RS256 requires JWT.PublicKeyPath",
		},
		{
			name: "RS256 with a signing key",
			cfg: func() *jwt.Config {
				cfg := rsConfig(pub, priv)

				cfg.JWT.SigningKey = testKey

				return cfg
			}(),
			want: "RS256 requires JWT.SigningKey to be empty",
		},
	}
}

// The signing key printed raw with fmt, encoding/json, a YAML encoder and
// log/slog. It is now a gas.Secret, which each writes as `[redacted]`,
// and binds from `jwt.signingkey` as a string: the service signs with it.
func TestConfig_SigningKeyRedacted(t *testing.T) {
	t.Parallel()

	const key = "zqxsecret-0123456789abcdef0123456789"

	cfg := jwt.DefaultConfig()

	cfg.JWT.SigningKey = key

	j, err := json.Marshal(cfg) //nolint:musttag // untagged: the field names an encoder writes.
	require.NoError(t, err)

	y, err := yaml.Marshal(cfg)
	require.NoError(t, err)

	var logs bytes.Buffer

	slog.New(slog.NewTextHandler(&logs, nil)).Info("cfg", "cfg", cfg, "key", cfg.JWT.SigningKey)
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("cfg", "cfg", cfg, "key", cfg.JWT.SigningKey)

	for _, text := range []string{
		fmt.Sprintf("%v %+v %#v %s %q %x", cfg, cfg, cfg, cfg.JWT.SigningKey, cfg.JWT, cfg.JWT),
		string(j),
		string(y),
		logs.String(),
	} {
		assert.NotContains(t, text, "zqxsecret")
		assert.Contains(t, text, "[redacted]")
	}

	svc := bound(t, m{"signingkey": key})
	require.NoError(t, svc.Init(t.Context()))

	_, err = svc.Sign("user-42", nil)
	require.NoError(t, err)
}

// Config returns the zero Config before Init, and after Init a copy of
// the configuration it bound, defaults included, with the signing key
// still redacted. Changing a result does not reach the service, and Close
// leaves it as it was.
func TestService_Config(t *testing.T) {
	t.Parallel()

	svc := bound(t, m{"signingkey": testKey, "issuer": "api"})
	assert.Equal(t, jwt.Config{}, svc.Config(), "before Init")

	require.NoError(t, svc.Init(t.Context()))

	want := *jwt.DefaultConfig()

	want.JWT.SigningKey = testKey
	want.JWT.Issuer = "api"

	got := svc.Config()
	assert.Equal(t, want, got)
	assert.Equal(t, "[redacted]", fmt.Sprint(got.JWT.SigningKey))
	assert.NotContains(t, fmt.Sprintf("%+v", got), testKey)

	got.JWT.Issuer = "changed"
	got.JWT.SigningKey = otherKey

	assert.Equal(t, want, svc.Config(), "a changed result does not reach the service")

	require.NoError(t, svc.Close(t.Context()))
	assert.Equal(t, want, svc.Config(), "after Close")
}

// A failed Init leaves Config on the configuration of the last Init that
// succeeded.
func TestService_ConfigAfterFailedInit(t *testing.T) {
	t.Parallel()

	cfg := hsConfig()
	svc := newService(t, cfg)

	cfg.JWT.SigningKey = short

	require.ErrorIs(t, svc.Init(t.Context()), jwt.ErrInvalidConfig)
	assert.Equal(t, *hsConfig(), svc.Config())
}

// Config may run while Init and Close do, and returns the configuration
// of one Init throughout. Run with -race.
func TestService_ConfigConcurrentWithRestart(t *testing.T) {
	t.Parallel()

	svc := newService(t, hsConfig())
	ctx := t.Context()

	var wg sync.WaitGroup

	wg.Go(func() {
		for range 50 {
			assert.NoError(t, svc.Init(ctx))
			assert.NoError(t, svc.Close(ctx))
		}
	})

	for range 4 {
		wg.Go(func() {
			for range 200 {
				assert.Equal(t, *hsConfig(), svc.Config())
			}
		})
	}

	wg.Wait()
}
