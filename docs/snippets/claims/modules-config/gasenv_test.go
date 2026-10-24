package modulesconfig_test

import (
	"os"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	gasenv "github.com/gasmod/gas/config/extensions/gasenv"
)

type envConfig struct {
	gas.WithGasEnv
}

// unsetGasEnv clears the variables that feed the extension for this test.
func unsetGasEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"GAS_ENV", "GASENV", "DOCSCLAIM_ENV"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
}

// resolve loads a config with the gasenv extension and the given provider
// values, and returns the stored key and the bound GasEnv.
func resolve(t *testing.T, provider map[string]any, opts ...gasenv.EnvOption) (*config.Config, *gasenv.Extension, gas.Environment) {
	t.Helper()
	ext := gasenv.NewExtension(opts...)
	cfg := load(t, values("m", provider), config.WithExtension(ext))
	var c envConfig
	if err := cfg.Bind(&c); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	return cfg, ext, c.GasEnv
}

// Docs: modules/config.mdx#environments
// "resolves which environment the app runs in and stores it at the config key `gasenv`. The allowed values are `development`, `testing`, `staging` and `production`, compared case-sensitively."
func TestGasEnvAllowedValues(t *testing.T) {
	unsetGasEnv(t)
	for _, env := range []string{"development", "testing", "staging", "production"} {
		cfg, _, got := resolve(t, map[string]any{"gasenv": env})
		if string(got) != env || cfg.Get("gasenv") != env {
			t.Errorf("%s: GasEnv=%q key=%v, want it kept", env, got, cfg.Get("gasenv"))
		}
	}
	if _, _, got := resolve(t, map[string]any{"gasenv": "Production"}); got != gas.EnvDevelopment {
		t.Errorf("Production: GasEnv = %q, want the development fallback (case-sensitive)", got)
	}
}

// Docs: modules/config.mdx#environment-resolution
// "1. Before the providers load, the extension reads the OS variable `GAS_ENV`. If it is unset or not an allowed value, it takes the fallback, `development`. The result is set as a default for the key. 2. After the providers load, an allowed value that a provider set at the key wins. Any other value there, including one a provider set that is not allowed, is replaced with the fallback."
func TestGasEnvResolutionSteps(t *testing.T) {
	unsetGasEnv(t)
	if _, _, got := resolve(t, map[string]any{}); got != gas.EnvDevelopment {
		t.Errorf("GAS_ENV unset: %q, want development", got)
	}
	t.Setenv("GAS_ENV", "bogus")
	if _, _, got := resolve(t, map[string]any{}); got != gas.EnvDevelopment {
		t.Errorf("GAS_ENV=bogus: %q, want development", got)
	}
	t.Setenv("GAS_ENV", "staging")
	if _, _, got := resolve(t, map[string]any{}); got != gas.EnvStaging {
		t.Errorf("GAS_ENV=staging: %q, want staging", got)
	}
	if _, _, got := resolve(t, map[string]any{"gasenv": "production"}); got != gas.EnvProduction {
		t.Errorf("provider production over GAS_ENV=staging: %q, want production", got)
	}
	if _, _, got := resolve(t, map[string]any{"gasenv": "nope"}); got != gas.EnvDevelopment {
		t.Errorf("provider nope with GAS_ENV=staging: %q, want the fallback development", got)
	}
	if _, _, got := resolve(t, map[string]any{"gasenv": 42}); got != gas.EnvDevelopment {
		t.Errorf("provider non-string with GAS_ENV=staging: %q, want the fallback development", got)
	}
}

// Docs: modules/config.mdx#environment-resolution
// "So the order is: a provider's value, then `GAS_ENV`, then `development`. An invalid value is never reported as an error. Because the environment provider reads every variable, `GASENV=production` is a provider value for the key and takes precedence over `GAS_ENV`."
func TestGasEnvOrder(t *testing.T) {
	unsetGasEnv(t)
	t.Setenv("GAS_ENV", "staging")
	t.Setenv("GASENV", "production")
	ext := gasenv.NewExtension()
	cfg := load(t, config.WithProvider(envProvider()), config.WithExtension(ext))
	if ext.Current() != gasenv.Production || cfg.Get("gasenv") != "production" {
		t.Fatalf("GASENV=production, GAS_ENV=staging: %q / %v, want production", ext.Current(), cfg.Get("gasenv"))
	}
	t.Setenv("GASENV", "invalid")
	t.Setenv("GAS_ENV", "invalid")
	if err := config.New(config.WithProvider(envProvider()), config.WithExtension(gasenv.NewExtension())).Load(); err != nil {
		t.Fatalf("invalid values: Load = %v, want no error", err)
	}
}

// Docs: modules/config.mdx#environments
// "Embed `gas.WithGasEnv` in a config struct and `Bind` fills its `GasEnv` field, which has `IsDevelopment`, `IsTesting`, `IsStaging`, `IsProduction`, `IsDevelopmentLike` (development or testing) and `IsProductionLike` (staging or production)."
func TestGasEnvHelpers(t *testing.T) {
	t.Parallel()
	type row struct{ dev, test, stage, prod, devLike, prodLike bool }
	for env, want := range map[gas.Environment]row{
		gas.EnvDevelopment: {dev: true, devLike: true},
		gas.EnvTesting:     {test: true, devLike: true},
		gas.EnvStaging:     {stage: true, prodLike: true},
		gas.EnvProduction:  {prod: true, prodLike: true},
		"other":            {},
	} {
		got := row{env.IsDevelopment(), env.IsTesting(), env.IsStaging(), env.IsProduction(), env.IsDevelopmentLike(), env.IsProductionLike()}
		if got != want {
			t.Errorf("%q: %+v, want %+v", env, got, want)
		}
	}
}

// Docs: modules/config.mdx#environment-resolution
// "Without the extension, nothing sets the key and `GasEnv` stays empty unless a provider sets `gasenv`."
func TestGasEnvWithoutExtension(t *testing.T) {
	t.Setenv("GAS_ENV", "production")
	var c envConfig
	cfg := load(t, values("m", map[string]any{}))
	if err := cfg.Bind(&c); err != nil {
		t.Fatal(err)
	}
	if c.GasEnv != "" || cfg.Get("gasenv") != nil {
		t.Fatalf("no extension: GasEnv=%q key=%v, want empty", c.GasEnv, cfg.Get("gasenv"))
	}
	if err := load(t, values("m", map[string]any{"gasenv": "staging"})).Bind(&c); err != nil || c.GasEnv != gas.EnvStaging {
		t.Fatalf("no extension, provider sets gasenv: %q, %v; want staging", c.GasEnv, err)
	}
}

// Docs: modules/config.mdx#environments
// "To ask before the app exists, keep the extension; it has the same helpers plus `Current()`"
func TestGasEnvExtensionHelpers(t *testing.T) {
	unsetGasEnv(t)
	_, ext, _ := resolve(t, map[string]any{"gasenv": "staging"})
	if ext.Current() != gasenv.Staging || !ext.IsStaging() || !ext.IsProductionLike() ||
		ext.IsDevelopment() || ext.IsTesting() || ext.IsProduction() || ext.IsDevelopmentLike() {
		t.Fatalf("staging: Current=%q and helpers disagree", ext.Current())
	}
	_, ext, _ = resolve(t, map[string]any{"gasenv": "testing"})
	if !ext.IsTesting() || !ext.IsDevelopmentLike() || ext.IsProductionLike() {
		t.Fatalf("testing: Current=%q and helpers disagree", ext.Current())
	}
}

// Docs: modules/config.mdx#environment-resolution
// "`WithEnvVarName` | Reads another OS variable instead of `GAS_ENV`" and "`WithConfigKey` | Stores the result under another key, and also writes it to `gasenv` so `gas.WithGasEnv` still binds" and "`WithAllowedEnvs` | Narrows the allowed values" and "`WithDefault` | Changes the fallback" and "The fallback should be one of the allowed values, which is not checked."
func TestGasEnvOptions(t *testing.T) {
	unsetGasEnv(t)
	t.Setenv("GAS_ENV", "staging")
	t.Setenv("DOCSCLAIM_ENV", "testing")
	if _, _, got := resolve(t, map[string]any{}, gasenv.WithEnvVarName("DOCSCLAIM_ENV")); got != gas.EnvTesting {
		t.Errorf("WithEnvVarName: %q, want testing from DOCSCLAIM_ENV", got)
	}
	cfg, _, got := resolve(t, map[string]any{}, gasenv.WithConfigKey("app.env"))
	if cfg.Get("app.env") != "staging" || got != gas.EnvStaging {
		t.Errorf("WithConfigKey: app.env=%v GasEnv=%q, want staging in both", cfg.Get("app.env"), got)
	}
	if _, _, got := resolve(t, map[string]any{}, gasenv.WithAllowedEnvs(gasenv.Development, gasenv.Production)); got != gas.EnvDevelopment {
		t.Errorf("WithAllowedEnvs without staging: %q, want the fallback", got)
	}
	if _, _, got := resolve(t, map[string]any{"gasenv": "nope"}, gasenv.WithDefault(gasenv.Testing)); got != gas.EnvTesting {
		t.Errorf("WithDefault(testing): %q, want testing", got)
	}
	_, ext, got := resolve(t, map[string]any{"gasenv": "nope"},
		gasenv.WithAllowedEnvs(gasenv.Production), gasenv.WithDefault("unlisted"))
	if got != "unlisted" || ext.Current() != "unlisted" {
		t.Errorf("fallback outside the allowed values: %q / %q, want it used unchecked", got, ext.Current())
	}
}

// Docs: modules/config.mdx#configuration
// "| `gasenv` | string | `GAS_ENV` when it holds an allowed value, else `development` | The resolved environment, set by the extension during `Load`."
func TestGasEnvKeyDefault(t *testing.T) {
	unsetGasEnv(t)
	ext := gasenv.NewExtension()
	cfg := config.New(values("m", map[string]any{}), config.WithExtension(ext))
	if cfg.Get("gasenv") != nil {
		t.Fatalf("gasenv set before Load: %v", cfg.Get("gasenv"))
	}
	if err := cfg.Load(); err != nil {
		t.Fatal(err)
	}
	if got, ok := cfg.Get("gasenv").(string); !ok || got != "development" {
		t.Fatalf("GAS_ENV unset: gasenv = %#v, want the string development", cfg.Get("gasenv"))
	}
	t.Setenv("GAS_ENV", "production")
	if got := load(t, values("m", map[string]any{}), config.WithExtension(gasenv.NewExtension())).Get("gasenv"); got != "production" {
		t.Fatalf("GAS_ENV=production: gasenv = %v, want production", got)
	}
}
