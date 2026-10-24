package projectknownissues_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/config/providers/secretsmanager"
)

// silentEndpoint is an HTTP server that accepts every request and answers
// none until the test ends.
func silentEndpoint(t *testing.T) string {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return srv.URL
}

func secretsProvider(endpoint string) *secretsmanager.Provider {
	return secretsmanager.NewProvider(
		secretsmanager.WithSecret("app"),
		secretsmanager.WithRegion("us-east-1"),
		secretsmanager.WithStaticCredentials("AKID", "secret"),
		secretsmanager.WithEndpoint(endpoint),
		secretsmanager.WithTimeout(200*time.Millisecond),
	)
}

// Docs: project/known-issues.mdx#config
// "`Config.Load` loads providers with `context.Background()` through their
// context-aware `LoadContext`, and only the Secrets Manager provider's own
// `Load` applies `DefaultTimeout` or `WithTimeout`. An endpoint that accepts and
// never answers blocks `config.Load` with no deadline from the provider."
func TestKI046LoadIgnoresProviderTimeout(t *testing.T) {
	t.Parallel()
	endpoint := silentEndpoint(t)

	// The provider's own Load honors WithTimeout.
	start := time.Now()
	if _, err := secretsProvider(endpoint).Load(); err == nil {
		t.Fatalf("provider Load against a silent endpoint error = nil")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("provider Load took %v with a 200ms WithTimeout, want it bounded", took)
	}

	// config.Load does not.
	cfg := config.New(config.WithProvider(secretsProvider(endpoint)))
	done := make(chan error, 1)
	go func() { done <- cfg.Load() }()
	select {
	case err := <-done:
		t.Fatalf("config.Load returned (%v) within 2s with a 200ms WithTimeout, want it blocked", err)
	case <-time.After(2 * time.Second):
	}
}

// Docs: project/known-issues.mdx#config
// "**Workaround:** call `cfg.LoadWithContext(ctx)` with a context that has a
// deadline."
func TestKI046Workaround(t *testing.T) {
	t.Parallel()
	cfg := config.New(config.WithProvider(secretsProvider(silentEndpoint(t))))
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := cfg.LoadWithContext(ctx); err == nil {
		t.Fatalf("LoadWithContext against a silent endpoint error = nil")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("LoadWithContext took %v with a 300ms deadline", took)
	}
}

// jsonProvider is a JSON provider reading config.json holding content.
func jsonProvider(content string) *providers.JSONProvider {
	return providers.NewJSONProvider(
		providers.WithJSONFileFS(fstest.MapFS{"config.json": {Data: []byte(content)}}),
		providers.WithJSONFilePath("config.json"),
	)
}

// Docs: project/known-issues.mdx#config
// "A JSON config file whose content is `null` loads as no values instead of
// failing with `ErrJSONDecodeFailed`, as a top-level array, string, number, or
// boolean does."
func TestKI047JSONNull(t *testing.T) {
	t.Parallel()
	values, err := jsonProvider("null").Load()
	if err != nil || len(values) != 0 {
		t.Fatalf("Load(null) = %v, %v, want no values and no error", values, err)
	}
	for _, content := range []string{`[]`, `"s"`, `1`, `true`} {
		_, err := jsonProvider(content).Load()
		if !errors.Is(err, providers.ErrJSONDecodeFailed) {
			t.Errorf("Load(%s) error = %v, want ErrJSONDecodeFailed", content, err)
		}
	}
}

// Docs: project/known-issues.mdx#config
// "**Workaround:** mark required fields with `validate:"required"`, so binding
// fails when the file supplied nothing."
func TestKI047Workaround(t *testing.T) {
	t.Parallel()
	cfg := config.New(config.WithProvider(jsonProvider("null")))
	if err := cfg.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	var dest struct {
		App struct {
			Name string `validate:"required"`
		}
	}
	if err := cfg.Bind(&dest); err == nil {
		t.Fatalf("Bind with a required field after a null file error = nil, want a failure")
	}
}

// Docs: project/known-issues.mdx#config
// "A separator with an uppercase letter, such as `WithEnvSeparator("_X_")`,
// never matches, so `PROBEA_X_B` loads as the flat key `probea_x_b` instead of
// nesting."
// Docs: project/known-issues.mdx#config
// "The environment and `.env` providers lowercase every variable name before
// splitting it on the separator, but keep the separator as given."
// Not parallel: it sets process environment variables.
func TestKI055UppercaseSeparator(t *testing.T) {
	t.Setenv("PROBEA_X_B", "upper")
	t.Setenv("PROBEC_y_D", "lower")
	env, err := providers.NewEnvProvider(providers.WithEnvSeparator("_X_")).Load()
	if err != nil {
		t.Fatal(err)
	}
	if env["probea_x_b"] != "upper" || env["probea"] != nil {
		t.Errorf("env with separator _X_: probea_x_b = %v, probea = %v, want the flat key", env["probea_x_b"], env["probea"])
	}
	lower, err := providers.NewEnvProvider(providers.WithEnvSeparator("_y_")).Load()
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := lower["probec"].(map[string]any); !ok || m["d"] != "lower" {
		t.Errorf("env with separator _y_: probec = %v, want it nested", lower["probec"])
	}

	dot, err := providers.NewDotEnvProvider(
		providers.WithDotEnvFileFS(fstest.MapFS{".env": {Data: []byte("DOTA_X_B=upper\n")}}),
		providers.WithDotEnvFilePath(".env"),
		providers.WithDotEnvSeparator("_X_"),
	).Load()
	if err != nil {
		t.Fatal(err)
	}
	if dot["dota_x_b"] != "upper" || dot["dota"] != nil {
		t.Errorf(".env with separator _X_: %v, want the flat key dota_x_b", dot)
	}
}

// Docs: project/known-issues.mdx#config
// "**Workaround:** use a separator without uppercase letters, such as the
// default `__`."
// Not parallel: it sets process environment variables.
func TestKI055Workaround(t *testing.T) {
	t.Setenv("PROBEE__F", "nested")
	env, err := providers.NewEnvProvider().Load()
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := env["probee"].(map[string]any); !ok || m["f"] != "nested" {
		t.Fatalf("env with the default separator: probee = %v, want it nested", env["probee"])
	}
}
