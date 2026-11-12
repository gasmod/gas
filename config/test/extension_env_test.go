package config_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
)

// loadEnv loads a Config with the extension and a provider holding values,
// and returns it.
func loadEnv(t *testing.T, ext *config.EnvExtension, values map[string]any) *config.Config {
	t.Helper()

	c := config.New(config.WithProvider(newStatic("static", values)), config.WithExtension(ext))
	require.NoError(t, c.Load())

	return c
}

func TestEnvExtension_Defaults(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "GasEnv", config.ExtensionName)
	assert.Equal(t, "GasEnv", config.DefaultConfigKey)
	assert.Equal(t, "GAS_ENV", config.DefaultEnvVarName)
	assert.Equal(t, config.EnvDevelopment, config.DefaultEnvironment)
	assert.Equal(t, config.ExtensionName, config.NewEnvExtension().Name())
}

// config.Environment is gas.Environment, so values pass between the
// packages without a conversion.
func TestEnvironment_IsGasEnvironment(t *testing.T) {
	t.Parallel()

	// assert.Equal compares the types too.
	assert.Equal(t, gas.EnvProduction, config.EnvProduction)
	assert.Equal(t, gas.DefaultEnvironment, config.DefaultEnvironment)

	ext := config.NewEnvExtension(config.WithAllowedEnvs(gas.EnvStaging))
	loadEnv(t, ext, map[string]any{"gasenv": "staging"})
	assert.Equal(t, gas.EnvStaging, ext.Current())
	assert.True(t, ext.Current().IsProductionLike())

	// Compiles only while the types are one.
	_ = gas.WithAllowedEnvironments(config.EnvStaging, ext.Current())
}

// The environment comes from the variable unless it is unset or empty,
// else from the key, else from gas_env, the key a provider makes of the
// variable's name, else from the fallback.
//
// Not parallel: it sets GAS_ENV.
func TestEnvExtension_Resolution(t *testing.T) {
	for _, tc := range []struct {
		name     string
		variable string
		values   map[string]any
		want     config.Environment
	}{
		{name: "fallback", want: config.EnvDevelopment},
		{name: "variable", variable: "staging", want: config.EnvStaging},
		{name: "variable over key", variable: "staging", values: m{"gasenv": "production"}, want: config.EnvStaging},
		{name: "variable over gas_env", variable: "staging", values: m{"gas_env": "production"}, want: config.EnvStaging},
		{
			name:     "variable over both",
			variable: "testing",
			values:   m{"gasenv": "production", "gas_env": "staging"},
			want:     config.EnvTesting,
		},
		{name: "key without variable", values: m{"GasEnv": "testing"}, want: config.EnvTesting},
		{name: "gas_env without variable", values: m{"GAS_ENV": "production"}, want: config.EnvProduction},
		{name: "key over gas_env", values: m{"gasenv": "staging", "gas_env": "production"}, want: config.EnvStaging},
		{
			name:   "key is the fallback",
			values: m{"gasenv": "development", "gas_env": "production"},
			want:   config.EnvDevelopment,
		},
		{name: "empty gas_env is unset", values: m{"gas_env": ""}, want: config.EnvDevelopment},
	} {
		t.Setenv("GAS_ENV", tc.variable)

		ext := config.NewEnvExtension()
		c := loadEnv(t, ext, tc.values)

		assert.Equal(t, tc.want, ext.Current(), tc.name)
		assert.Equal(t, string(tc.want), c.Get("GasEnv"), tc.name)
	}
}

// A variable or key holding an environment that is not allowed fails the
// load, whatever else holds an allowed one: a typo never turns production
// into development. Values are compared with their case. The error names
// the variable or key and what is allowed, not the value, and the current
// environment stays as it was.
//
// Not parallel: it sets GAS_ENV.
func TestEnvExtension_NotAllowed(t *testing.T) {
	const (
		allowed  = "a value that is not one of development, testing, staging, production"
		inKey    = "key GasEnv holds " + allowed
		inVarKey = "key gas_env holds " + allowed
	)

	pre, post := config.ErrExtensionPreLoadHookFailed, config.ErrExtensionPostLoadHookFailed

	for _, tc := range []struct {
		name     string
		variable string
		values   map[string]any
		wantErr  error
		wantMsg  string
	}{
		{name: "variable", variable: "Production", wantErr: pre, wantMsg: "variable GAS_ENV holds " + allowed},
		{name: "key, allowed variable", variable: "production", values: m{"gasenv": "prod"}, wantErr: post, wantMsg: inKey},
		{name: "key", values: m{"gasenv": "prod"}, wantErr: post, wantMsg: inKey},
		{name: "key not a string", values: m{"gasenv": 1}, wantErr: post, wantMsg: inKey},
		{name: "key nil", values: m{"gasenv": nil}, wantErr: post, wantMsg: inKey},
		{name: "gas_env", values: m{"gas_env": "prod"}, wantErr: post, wantMsg: inVarKey},
		{name: "gas_env, allowed key", values: m{"gasenv": "staging", "gas_env": "prod"}, wantErr: post, wantMsg: inVarKey},
		{name: "gas_env not a string", values: m{"gas_env": 1}, wantErr: post, wantMsg: inVarKey},
	} {
		t.Setenv("GAS_ENV", tc.variable)

		ext := config.NewEnvExtension()
		c := config.New(
			config.WithProvider(newStatic("static", tc.values)),
			config.WithExtension(ext),
		)

		err := c.Load()
		require.ErrorIs(t, err, tc.wantErr, tc.name)
		require.ErrorIs(t, err, config.ErrEnvironmentNotAllowed, tc.name)
		assert.Contains(t, err.Error(), "environment not allowed: "+tc.wantMsg, tc.name)
		assert.NotContains(t, err.Error(), "prod\"", tc.name)
		assert.Equal(t, config.Environment(""), ext.Current(), tc.name)
	}
}

// The options change the variable, the allowed values, the fallback and
// the key. With another key, the environment is stored at GasEnv too.
//
// Not parallel: it sets environment variables.
func TestEnvExtension_Options(t *testing.T) {
	t.Setenv("GAS_ENV", "production")
	t.Setenv("APP_ENV", "staging")

	ext := config.NewEnvExtension(config.WithEnvVarName("APP_ENV"))
	loadEnv(t, ext, nil)
	assert.Equal(t, config.EnvStaging, ext.Current())

	t.Setenv("APP_ENV", "")

	ext = config.NewEnvExtension(
		config.WithEnvVarName("APP_ENV"),
		config.WithAllowedEnvs(config.EnvDevelopment, config.EnvProduction),
		config.WithDefault("local"),
	)
	loadEnv(t, ext, nil)
	assert.Equal(t, config.Environment("local"), ext.Current(), "the fallback need not be allowed")

	loadEnv(t, ext, m{"gasenv": "local"})
	assert.Equal(t, config.Environment("local"), ext.Current(), "a provider may set the fallback")

	t.Setenv("APP_ENV", "staging")

	err := config.New(config.WithExtension(ext)).Load()
	require.ErrorIs(t, err, config.ErrEnvironmentNotAllowed)
	assert.Contains(
		t,
		err.Error(),
		"variable APP_ENV holds a value that is not one of development, production, local",
	)

	t.Setenv("GAS_ENV", "")

	ext = config.NewEnvExtension(config.WithConfigKey("app.env"))

	c := loadEnv(t, ext, m{"app": m{"env": "testing"}})
	assert.Equal(t, config.EnvTesting, ext.Current())
	assert.Equal(t, "testing", c.Get("app.env"))
	assert.Equal(t, "testing", c.Get("GasEnv"))

	// Another key replaces gasenv, not gas_env.
	ext = config.NewEnvExtension(config.WithConfigKey("app.env"))
	c = loadEnv(t, ext, m{"app": m{"env": "testing"}, "gasenv": "production", "gas_env": "staging"})
	assert.Equal(t, config.EnvTesting, ext.Current(), "the key")
	assert.Equal(t, "testing", c.Get("GasEnv"))

	ext = config.NewEnvExtension(config.WithConfigKey("app.env"))
	loadEnv(t, ext, m{"gasenv": "production", "gas_env": "staging"})
	assert.Equal(t, config.EnvStaging, ext.Current(), "gas_env, as gasenv is not the key")

	// Another variable replaces gas_env with the key made of its name, levels
	// included.
	t.Setenv("APP__ENV", "")

	ext = config.NewEnvExtension(config.WithEnvVarName("APP__ENV"))
	loadEnv(t, ext, m{"app": m{"env": "production"}, "gas_env": "staging"})
	assert.Equal(t, config.EnvProduction, ext.Current())
}

// A .env file sets the environment as GAS_ENV or GASENV, and either loses
// to the variable.
//
// Not parallel: it sets GAS_ENV and changes the working directory.
func TestEnvExtension_DotEnv(t *testing.T) {
	dir := chdir(t)

	for _, tc := range []struct {
		name     string
		variable string
		file     string
		want     config.Environment
	}{
		{name: "GAS_ENV", file: "GAS_ENV=production\n", want: config.EnvProduction},
		{name: "GASENV", file: "GASENV=production\n", want: config.EnvProduction},
		{name: "GASENV over GAS_ENV", file: "GAS_ENV=production\nGASENV=staging\n", want: config.EnvStaging},
		{name: "variable over GAS_ENV", variable: "testing", file: "GAS_ENV=production\n", want: config.EnvTesting},
		{name: "variable over GASENV", variable: "testing", file: "GASENV=production\n", want: config.EnvTesting},
	} {
		t.Setenv("GAS_ENV", tc.variable)
		writeFile(t, filepath.Join(dir, ".env"), tc.file)

		ext := config.NewEnvExtension()
		c := config.New(config.WithProvider(config.NewDotEnvProvider()), config.WithExtension(ext))
		require.NoError(t, c.Load(), tc.name)

		assert.Equal(t, tc.want, ext.Current(), tc.name)
		assert.Equal(t, string(tc.want), c.Get("GasEnv"), tc.name)
	}

	t.Setenv("GAS_ENV", "")
	writeFile(t, filepath.Join(dir, ".env"), "GAS_ENV=prod\n")

	err := config.New(config.WithProvider(config.NewDotEnvProvider())).Load()
	require.ErrorIs(t, err, config.ErrEnvironmentNotAllowed)
	assert.Contains(t, err.Error(), "key gas_env holds")
}

// An unprefixed EnvProvider stores GAS_ENV at gas_env too, which agrees
// with the variable, and an empty GAS_ENV there counts as unset.
//
// Not parallel: it sets GAS_ENV.
func TestEnvExtension_UnprefixedEnvProvider(t *testing.T) {
	provider := config.WithProvider(config.NewEnvProvider(config.WithoutEnvPrefix()))

	t.Setenv("GAS_ENV", "staging")

	ext := config.NewEnvExtension()
	c := config.New(provider, config.WithExtension(ext))
	require.NoError(t, c.Load())
	assert.Equal(t, config.EnvStaging, ext.Current())
	assert.Equal(t, "staging", c.Get("gas_env"), "gas_env is left in place")

	t.Setenv("GAS_ENV", "")

	ext = config.NewEnvExtension()
	require.NoError(t, config.New(provider, config.WithExtension(ext)).Load())
	assert.Equal(t, config.EnvDevelopment, ext.Current())
}

// An Environment field binds the environment from GasEnv.
//
// Not parallel: it sets GAS_ENV.
func TestEnvExtension_Bind(t *testing.T) {
	t.Setenv("GAS_ENV", "production")

	c := loadEnv(t, config.NewEnvExtension(), nil)

	var app struct{ GasEnv config.Environment }

	require.NoError(t, c.Bind(&app))
	assert.Equal(t, config.EnvProduction, app.GasEnv)

	// The package documentation's example: a field named Env needs the tag.
	var tagged struct {
		Env config.Environment `json:"gasenv"`
	}

	require.NoError(t, c.Bind(&tagged))
	assert.Equal(t, config.EnvProduction, tagged.Env)

	var untagged struct{ Env config.Environment }

	require.NoError(t, c.Bind(&untagged))
	assert.Empty(t, untagged.Env, "without the tag, Env is the key env, which holds nothing")

	// With WithConfigKey set to env, it binds without one.
	c = loadEnv(t, config.NewEnvExtension(config.WithConfigKey("env")), nil)

	require.NoError(t, c.Bind(&untagged))
	assert.Equal(t, config.EnvProduction, untagged.Env)
}

// Current is empty before the first load, and the Is helpers follow it.
//
// Not parallel: it sets GAS_ENV.
func TestEnvExtension_Current(t *testing.T) {
	t.Setenv("GAS_ENV", "staging")

	ext := config.NewEnvExtension()
	assert.Equal(t, config.Environment(""), ext.Current())

	loadEnv(t, ext, nil)
	assert.True(t, ext.Is(config.EnvStaging))
	assert.True(t, ext.IsStaging())
	assert.True(t, ext.IsProductionLike())
	assert.False(t, ext.IsProduction())
	assert.False(t, ext.IsDevelopment())
	assert.False(t, ext.IsTesting())
	assert.False(t, ext.IsDevelopmentLike())
}

// The variable wins on every load. Once a load has stored the environment
// at the key, the key holds a value, so a later change to gas_env takes
// effect only through the variable or the key.
//
// Not parallel: it sets GAS_ENV.
func TestEnvExtension_Reload(t *testing.T) {
	t.Setenv("GAS_ENV", "staging")

	ext := config.NewEnvExtension()
	provider := newStatic("static", nil)
	c := config.New(config.WithProvider(provider), config.WithExtension(ext))
	require.NoError(t, c.Load())

	t.Setenv("GAS_ENV", "production")
	require.NoError(t, c.Load())
	assert.Equal(t, config.EnvProduction, ext.Current())

	t.Setenv("GAS_ENV", "")
	provider.set(m{"gas_env": "testing"}, nil)
	require.NoError(t, c.Load())
	assert.Equal(t, config.EnvProduction, ext.Current(), "the key keeps the stored value")

	provider.set(m{"gasenv": "testing"}, nil)
	require.NoError(t, c.Load())
	assert.Equal(t, config.EnvTesting, ext.Current())
}

func TestEnvironment_Methods(t *testing.T) {
	t.Parallel()

	for env, want := range map[config.Environment][6]bool{
		config.EnvDevelopment: {true, false, false, false, true, false},
		config.EnvTesting:     {false, true, false, false, true, false},
		config.EnvStaging:     {false, false, true, false, false, true},
		config.EnvProduction:  {false, false, false, true, false, true},
		"Production":          {},
	} {
		assert.Equal(t, want, [6]bool{
			env.IsDevelopment(),
			env.IsTesting(),
			env.IsStaging(),
			env.IsProduction(),
			env.IsDevelopmentLike(),
			env.IsProductionLike(),
		}, env)
		assert.Equal(t, string(env), env.String())
	}
}

// New adds an EnvExtension with the default options, before the
// extensions WithExtension adds, so a Config given none reads GAS_ENV and
// fails on a value that is not allowed.
//
// Not parallel: it sets GAS_ENV.
func TestNew_DefaultEnvExtension(t *testing.T) {
	t.Setenv("GAS_ENV", "staging")

	var log []string

	seen := map[string]any{}
	c := config.New(
		config.WithProvider(newStatic("static", nil)),
		config.WithExtension(&recorder{
			name: "ext",
			log:  &log,
			fn:   func(hook string, cfg *config.Config) { seen[hook] = cfg.Get("GasEnv") },
		}),
	)
	require.NoError(t, c.Load())

	assert.Equal(t, "staging", c.Get("GasEnv"))
	assert.Equal(t, map[string]any{"pre": "staging", "post": "staging"}, seen)
	assert.Equal(t, "config.Config{providers: 1, extensions: 2, keys: 1}", c.String())

	t.Setenv("GAS_ENV", "prod")

	err := config.New(config.WithProvider(newStatic("static", nil))).Load()
	require.ErrorIs(t, err, config.ErrExtensionPreLoadHookFailed)
	require.ErrorIs(t, err, config.ErrEnvironmentNotAllowed)
	assert.Contains(t, err.Error(), "failed to execute extension pre-load hook GasEnv")
}

// WithoutEnvExtension leaves the default EnvExtension out, so GAS_ENV is
// neither read nor checked. An EnvExtension given to WithExtension still
// runs.
//
// Not parallel: it sets environment variables.
func TestNew_WithoutEnvExtension(t *testing.T) {
	t.Setenv("GAS_ENV", "prod")

	c := config.New(
		config.WithoutEnvExtension(),
		config.WithProvider(newStatic("static", nil)),
	)
	require.NoError(t, c.Load())

	assert.Nil(t, c.Get("GasEnv"))
	assert.Equal(t, "config.Config{providers: 1, extensions: 0, keys: 0}", c.String())

	t.Setenv("APP_ENV", "testing")

	ext := config.NewEnvExtension(config.WithEnvVarName("APP_ENV"))

	c = config.New(
		config.WithoutEnvExtension(),
		config.WithProvider(newStatic("static", nil)),
		config.WithExtension(ext),
	)
	require.NoError(t, c.Load())
	assert.Equal(t, config.EnvTesting, ext.Current())
}

// An EnvExtension given to WithExtension takes the default's place, so
// only it runs: GAS_ENV holding a value that is not allowed does not fail
// a load whose extension reads another variable.
//
// Not parallel: it sets environment variables.
func TestNew_EnvExtensionReplacesDefault(t *testing.T) {
	t.Setenv("GAS_ENV", "prod")
	t.Setenv("APP_ENV", "staging")

	ext := config.NewEnvExtension(config.WithEnvVarName("APP_ENV"))
	c := config.New(
		config.WithProvider(newStatic("static", nil)),
		config.WithExtension(ext),
	)
	require.NoError(t, c.Load())

	assert.Equal(t, config.EnvStaging, ext.Current())
	assert.Equal(t, "staging", c.Get("GasEnv"))
	assert.Equal(t, "config.Config{providers: 1, extensions: 1, keys: 1}", c.String())
}
