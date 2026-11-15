package config_test

import (
	"sync"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas/config"
)

// KI-047: a JSON file holding `null` decoded to a nil map with no error,
// so it loaded as no values. It now fails like any other value that is not
// an object.
func TestJSONProvider_Null(t *testing.T) {
	t.Parallel()

	got, err := config.NewJSONProvider(
		config.WithJSONFilePath("app.json"),
		config.WithJSONFileFS(fstest.MapFS{"app.json": {Data: []byte(" null\n")}}),
	).Load()
	assert.Nil(t, got)
	require.ErrorIs(t, err, config.ErrJSONDecodeFailed)
	require.EqualError(
		t,
		err,
		"failed to decode JSON from app.json: value is a JSON null, not an object",
	)
}

// KI-055: a separator with uppercase letters never matched, as names were
// lowercased and the separator was not. Both are now lowercased, so it
// matches without regard to case, as the prefix does.
//
// Not parallel: it sets an environment variable.
func TestProviders_UppercaseSeparator(t *testing.T) {
	t.Setenv("CFGTEST_A_X_B", "1")

	got, err := config.NewEnvProvider(
		config.WithEnvPrefix("CFGTEST_"),
		config.WithEnvSeparator("_X_"),
	).Load()
	require.NoError(t, err)
	assert.Equal(t, m{"a": m{"b": "1"}}, got)

	got, err = config.NewDotEnvProvider(
		config.WithDotEnvFileFS(dotenvFS("A_X_B=1\n")),
		config.WithDotEnvSeparator("_X_"),
	).Load()
	require.NoError(t, err)
	assert.Equal(t, m{"a": m{"b": "1"}}, got)
}

// KI-075: SetDefaults with a nil *map[string]any dereferenced it and
// panicked. It is now ErrNilValues, as nil is.
func TestConfig_SetDefaultsNilMapPointer(t *testing.T) {
	t.Parallel()

	c := newConfig(t, m{"a": 1})

	require.ErrorIs(t, c.SetDefaults((*map[string]any)(nil)), config.ErrNilValues)
	assert.Equal(t, m{"a": 1}, c.Values())
}

// KI-076: two loads at the same time, or Current during a load, raced on
// the extension's current environment, which the race detector reports
// when this test runs under -race. PreLoad also set it before the
// providers loaded, so Current could return an environment that PostLoad
// then replaced. It is now set once per load, by PostLoad, under a lock.
//
// Not parallel: it sets GAS_ENV.
func TestEnvExtension_ConcurrentLoads(t *testing.T) {
	// The variable would win over the key, so it is left empty.
	t.Setenv("GAS_ENV", "")

	ext := config.NewEnvExtension()
	c := config.New(
		config.WithProvider(newStatic("static", m{"gasenv": "production"})),
		config.WithExtension(ext),
	)

	var wg sync.WaitGroup

	for range 4 {
		wg.Go(func() { assert.NoError(t, c.Load()) })
		wg.Go(func() {
			got := ext.Current()
			assert.Contains(t, []config.Environment{"", config.EnvProduction}, got)
		})
	}

	wg.Wait()

	assert.Equal(t, config.EnvProduction, ext.Current())
}

// KI-077: with a custom key, the extension stored the environment at
// GasEnv as an Environment rather than a string, so Get("GasEnv") held a
// string with the default key and an Environment with any other. Both are
// strings now.
//
// Not parallel: it sets GAS_ENV.
func TestEnvExtension_CustomKeyStoresString(t *testing.T) {
	t.Setenv("GAS_ENV", "staging")

	c := loadEnv(t, config.NewEnvExtension(config.WithConfigKey("app.env")), nil)

	assert.IsType(t, "", c.Get("GasEnv"))
	assert.Equal(t, "staging", c.Get("GasEnv"))
	assert.Equal(t, "staging", c.Get("app.env"))
}

// A key that only case folding, and not lowercasing, makes GasEnv, such
// as one with a long s, is another key, so the environment is copied to
// GasEnv too. The copy was skipped when the keys were compared with
// strings.EqualFold.
//
// Not parallel: it sets GAS_ENV.
func TestEnvExtension_FoldedKeyStillCopied(t *testing.T) {
	t.Setenv("GAS_ENV", "staging")

	c := loadEnv(t, config.NewEnvExtension(config.WithConfigKey("Ga\u017fEnv")), nil)

	assert.Equal(t, "staging", c.Get("GasEnv"))
	assert.Equal(t, "staging", c.Get("ga\u017fenv"))
}

// KI-078: Set and SetDefault stored the value they were given, so a map
// the caller changed afterwards changed the configuration, outside the
// lock that guards it. They now store a copy, as Get returns one.
func TestConfig_SetStoresCopy(t *testing.T) {
	t.Parallel()

	c := newConfig(t, m{})
	set := m{"hosts": []any{"a"}}
	def := m{"port": 1}

	require.NoError(t, c.Set("db", set))
	require.NoError(t, c.SetDefault("cache", def))

	set["hosts"].([]any)[0] = "x"
	set["new"] = true
	def["port"] = 2

	assert.Equal(t, m{"db": m{"hosts": []any{"a"}}, "cache": m{"port": 1}}, c.Values())
}

// KI-079: a JSON file that was not valid JSON failed with the error of
// encoding/json, which quotes the character it stopped at, so the error
// held part of the file, which may hold a password. The error now gives
// only the offset.
func TestJSONProvider_SyntaxErrorQuotesNothing(t *testing.T) {
	t.Parallel()

	_, err := config.NewJSONProvider(
		config.WithJSONFilePath("app.json"),
		config.WithJSONFileFS(fstest.MapFS{
			"app.json": {Data: []byte(`{"password": hunter2}`)},
		}),
	).Load()
	require.ErrorIs(t, err, config.ErrJSONDecodeFailed)
	require.EqualError(
		t,
		err,
		"failed to decode JSON from app.json: value is not valid JSON: syntax error after byte 14",
	)
}
