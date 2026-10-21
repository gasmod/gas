package productiondeployment_test

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"syscall"
	"testing"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config/extensions/gasenv"
)

// boundEnv starts an app on cfg and returns Config().GasEnv.
func boundEnv(t *testing.T, cfg gas.ConfigProvider) gas.Environment {
	t.Helper()
	a := gas.NewApp(
		gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
	)
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Stop(context.Background()) })
	return a.Config().GasEnv
}

// Docs: production/deployment.mdx#environments
// "The `gasenv` extension of gas/config sets the environment from `GAS_ENV`
// or a config provider: `development`, `testing`, `staging`, or `production`,
// falling back to `development` for a missing or unknown value. The app binds
// it into `Config().GasEnv` at `Start`."
func TestGasEnvExtension(t *testing.T) {
	for _, c := range []struct {
		name     string
		env      string // GAS_ENV; empty means unset
		provider any    // GasEnv from the provider; nil means absent
		want     gas.Environment
	}{
		{"development", "development", nil, gas.EnvDevelopment},
		{"testing", "testing", nil, gas.EnvTesting},
		{"staging", "staging", nil, gas.EnvStaging},
		{"production", "production", nil, gas.EnvProduction},
		{"from provider", "", "staging", gas.EnvStaging},
		{"missing", "", nil, gas.EnvDevelopment},
		{"unknown", "qa", nil, gas.EnvDevelopment},
		{"unknown from provider", "", "qa", gas.EnvDevelopment},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("GAS_ENV", c.env)
			values := map[string]any{}
			if c.provider != nil {
				values["GasEnv"] = c.provider
			}
			if got := boundEnv(t, cfgFrom(t, values, gasenv.NewExtension())); got != c.want {
				t.Fatalf("GasEnv = %q, want %q", got, c.want)
			}
		})
	}
}

// Docs: production/deployment.mdx#environments
// "Without the extension, nothing sets it unless a provider sets the `GasEnv`
// key, and it stays empty."
func TestGasEnvWithoutExtension(t *testing.T) {
	t.Setenv("GAS_ENV", "production")
	if got := boundEnv(t, cfgFrom(t, map[string]any{})); got != "" {
		t.Errorf("GasEnv without the extension = %q, want empty even with GAS_ENV set", got)
	}
	if got := boundEnv(t, cfgFrom(t, map[string]any{"GasEnv": "staging"})); got != gas.EnvStaging {
		t.Errorf("GasEnv from a provider = %q, want staging", got)
	}
}

// Docs: production/deployment.mdx#environments
// "Core uses it in two places: `Run` logs the route map as one table in
// `development` and as one line per route otherwise, and `server started`
// carries it in the `environment` field."
func TestRouteMapAndServerStartedByEnvironment(t *testing.T) {
	for _, env := range []string{"development", "production", "staging"} {
		t.Run(env, func(t *testing.T) {
			sink, logOpt := logger()
			port := freePort(t)
			values := serverValues(port, nil)
			values["GasEnv"] = env
			a := gas.NewApp(
				logOpt,
				gas.WithServiceInstance[gas.ConfigProvider](cfgFrom(t, values)),
				gas.WithSingletonService[*svc[kA]](func(r *gas.Router) *svc[kA] {
					s := &svc[kA]{}
					s.onInit = func(context.Context) error {
						ok := func(ctx gas.Context) error { return ctx.NoContent() }
						r.Get(s, "/one", ok)
						r.Post(s, "/two", ok)
						return nil
					}
					return s
				}),
			)
			r := runApp(t, a, port)
			if err := r.stop(syscall.SIGINT); err != nil {
				t.Fatalf("Run: %v", err)
			}
			table := sink.index(t, "route map*", nil) != -1
			lines := sink.find(t, "route registered", map[string]string{"service": "kA"})
			if env == "development" {
				if !table || len(lines) != 0 {
					t.Errorf("development: table=%v, %d route lines; want one table", table, len(lines))
				}
			} else {
				var got []string
				for _, l := range lines {
					got = append(got, l["method"].(string)+" "+l["path"].(string))
				}
				slices.Sort(got)
				if table || !slices.Equal(got, []string{http.MethodGet + " /one", http.MethodPost + " /two"}) {
					t.Errorf("%s: table=%v, route lines %v; want one line per route", env, table, got)
				}
			}
			if len(sink.find(t, "server started", map[string]string{
				"environment": env, "addr": "127.0.0.1:" + strconv.Itoa(port),
			})) != 1 {
				t.Errorf("no server started record with environment=%s", env)
			}
		})
	}
}

// appConfig is a configuration struct of an app's own.
type appConfig struct {
	gas.WithGasEnv
	Feature struct{ Enabled bool }
}

// Docs: production/deployment.mdx#environments
// "Your own configuration struct gets it by embedding `gas.WithGasEnv`, with
// helpers such as `IsProduction()` and `IsProductionLike()` (`staging` or
// `production`)."
func TestWithGasEnvEmbedding(t *testing.T) {
	for env, like := range map[string]bool{
		"development": false, "testing": false, "staging": true, "production": true,
	} {
		t.Setenv("GAS_ENV", env)
		var c appConfig
		cfg := cfgFrom(t, map[string]any{"Feature": map[string]any{"Enabled": true}}, gasenv.NewExtension())
		if err := cfg.Bind(&c); err != nil {
			t.Fatal(err)
		}
		if c.GasEnv.String() != env || !c.Feature.Enabled {
			t.Errorf("bound %+v, want GasEnv %s", c, env)
		}
		if c.GasEnv.IsProduction() != (env == "production") || c.GasEnv.IsProductionLike() != like {
			t.Errorf("%s: IsProduction=%v IsProductionLike=%v", env, c.GasEnv.IsProduction(), c.GasEnv.IsProductionLike())
		}
	}
}
