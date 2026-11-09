package config_test

import (
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
	assert.Equal(t, config.ExtensionName, config.NewExtension().Name())
}

// config.Environment is gas.Environment, so values pass between the
// packages without a conversion.
func TestEnvironment_IsGasEnvironment(t *testing.T) {
	t.Parallel()

	// assert.Equal compares the types too.
	assert.Equal(t, gas.EnvProduction, config.EnvProduction)
	assert.Equal(t, gas.DefaultEnvironment, config.DefaultEnvironment)

	ext := config.NewExtension(config.WithAllowedEnvs(gas.EnvStaging))
	loadEnv(t, ext, map[string]any{"gasenv": "staging"})
	assert.Equal(t, gas.EnvStaging, ext.Current())
	assert.True(t, ext.Current().IsProductionLike())

	// Compiles only while the types are one.
	_ = gas.WithAllowedEnvironments(config.EnvStaging, ext.Current())
}

// The environment comes from the key when a provider sets it, else from
// the variable, else, when the variable is unset or empty, from the
// fallback.
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
		{name: "key over variable", variable: "staging", values: m{"gasenv": "production"}, want: config.EnvProduction},
		{name: "key without variable", values: m{"GasEnv": "testing"}, want: config.EnvTesting},
		{name: "key is the fallback", variable: "staging", values: m{"gasenv": "development"}, want: config.EnvDevelopment},
	} {
		t.Setenv("GAS_ENV", tc.variable)

		ext := config.NewExtension()
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
	const allowed = "a value that is not one of development, testing, staging, production"

	for _, tc := range []struct {
		name     string
		variable string
		values   map[string]any
		wantErr  error
		wantMsg  string
	}{
		{
			name:     "variable",
			variable: "Production",
			wantErr:  config.ErrExtensionPreLoadHookFailed,
			wantMsg:  "variable GAS_ENV holds " + allowed,
		},
		{
			name:     "key over an allowed variable",
			variable: "production",
			values:   m{"gasenv": "prod"},
			wantErr:  config.ErrExtensionPostLoadHookFailed,
			wantMsg:  "key GasEnv holds " + allowed,
		},
		{
			name:    "key not a string",
			values:  m{"gasenv": 1},
			wantErr: config.ErrExtensionPostLoadHookFailed,
			wantMsg: "key GasEnv holds " + allowed,
		},
		{
			name:    "key nil",
			values:  m{"gasenv": nil},
			wantErr: config.ErrExtensionPostLoadHookFailed,
			wantMsg: "key GasEnv holds " + allowed,
		},
	} {
		t.Setenv("GAS_ENV", tc.variable)

		ext := config.NewExtension()
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

	ext := config.NewExtension(config.WithEnvVarName("APP_ENV"))
	loadEnv(t, ext, nil)
	assert.Equal(t, config.EnvStaging, ext.Current())

	t.Setenv("APP_ENV", "")

	ext = config.NewExtension(
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

	ext = config.NewExtension(config.WithConfigKey("app.env"))

	c := loadEnv(t, ext, m{"app": m{"env": "testing"}})
	assert.Equal(t, config.EnvTesting, ext.Current())
	assert.Equal(t, "testing", c.Get("app.env"))
	assert.Equal(t, "testing", c.Get("GasEnv"))
}

// An Environment field binds the environment from GasEnv.
//
// Not parallel: it sets GAS_ENV.
func TestEnvExtension_Bind(t *testing.T) {
	t.Setenv("GAS_ENV", "production")

	c := loadEnv(t, config.NewExtension(), nil)

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
	c = loadEnv(t, config.NewExtension(config.WithConfigKey("env")), nil)

	require.NoError(t, c.Bind(&untagged))
	assert.Equal(t, config.EnvProduction, untagged.Env)
}

// Current is empty before the first load, and the Is helpers follow it.
//
// Not parallel: it sets GAS_ENV.
func TestEnvExtension_Current(t *testing.T) {
	t.Setenv("GAS_ENV", "staging")

	ext := config.NewExtension()
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

// Once a load has stored the environment at the key, later loads keep it
// unless a provider sets the key: the variable only fills a key that holds
// nothing.
//
// Not parallel: it sets GAS_ENV.
func TestEnvExtension_Reload(t *testing.T) {
	t.Setenv("GAS_ENV", "staging")

	ext := config.NewExtension()
	c := loadEnv(t, ext, nil)

	t.Setenv("GAS_ENV", "production")
	require.NoError(t, c.Load())
	assert.Equal(t, config.EnvStaging, ext.Current())
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
