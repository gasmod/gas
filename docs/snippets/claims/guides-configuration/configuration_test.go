package guidesconfiguration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-playground/validator/v10"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/configtest"
	gasenv "github.com/gasmod/gas/config/extensions/gasenv"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/config/providers/secretsmanager"
	"github.com/gasmod/gas/template/memory"
	"github.com/gasmod/gas/ui"
)

// mapProvider is a custom provider built from Name and Load alone.
type mapProvider struct {
	name   string
	values map[string]any
}

func (p mapProvider) Name() string                  { return p.name }
func (p mapProvider) Load() (map[string]any, error) { return p.values, nil }

func load(t *testing.T, opts ...config.Option) *config.Config {
	t.Helper()
	cfg := config.New(opts...)
	if err := cfg.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

// Docs: guides/configuration.mdx#_top
// "`gas/config` is the one module that is not a service."
func TestConfigIsNotAService(t *testing.T) {
	t.Parallel()
	if reflect.TypeFor[*config.Config]().Implements(reflect.TypeFor[gas.Service]()) {
		t.Fatal("*config.Config implements gas.Service")
	}
}

// Docs: guides/configuration.mdx#loading
// "Later providers override earlier ones, so the order above means environment variables are the base and the JSON file wins."
func TestLaterProvidersOverride(t *testing.T) {
	t.Setenv("DOCSCLAIM__KEY", "from-env")
	t.Setenv("DOCSCLAIM__ENVONLY", "env")
	fsys := fstest.MapFS{"config.json": {Data: []byte(`{"docsclaim":{"key":"from-json"}}`)}}
	cfg := load(t,
		config.WithProvider(providers.NewEnvProvider()),
		config.WithProvider(providers.NewJSONProvider(providers.WithJSONFileFS(fsys), providers.WithJSONFilePath("config.json"))),
	)
	if got := cfg.Get("docsclaim.key"); got != "from-json" {
		t.Fatalf("docsclaim.key = %v, want the later JSON value", got)
	}
	if got := cfg.Get("docsclaim.envonly"); got != "env" {
		t.Fatalf("docsclaim.envonly = %v, want the env base to remain", got)
	}

	cfg = load(t,
		config.WithProvider(mapProvider{"first", map[string]any{"k": "first"}}),
		config.WithProvider(mapProvider{"second", map[string]any{"k": "second"}}),
	)
	if got := cfg.Get("k"); got != "second" {
		t.Fatalf("k = %v, want the later provider's value", got)
	}
}

// Docs: guides/configuration.mdx#loading
// "The environment provider is registered automatically **only when you register no provider at all**. As soon as you pass one explicitly, add `providers.NewEnvProvider()` yourself if you still want environment variables."
func TestEnvProviderOnlyByDefault(t *testing.T) {
	t.Setenv("DOCSCLAIM_AUTO", "present")
	if got := load(t).Get("docsclaim_auto"); got != "present" {
		t.Fatalf("with no provider, docsclaim_auto = %v, want the env value", got)
	}
	if got := load(t, config.WithProvider(mapProvider{"m", map[string]any{}})).Get("docsclaim_auto"); got != nil {
		t.Fatalf("with an explicit provider, docsclaim_auto = %v, want nil", got)
	}
}

// Docs: guides/configuration.mdx#key-naming
// "Keys are lowercased. By default they stay flat, so `DATABASE_HOST` becomes `database_host`. Use a double underscore to nest ... DATABASE__DSN=postgres://localhost/app # database.dsn / STORAGE__BUCKET=uploads # storage.bucket / JWT__SIGNINGKEY=... # jwt.signingkey"
func TestEnvKeyNaming(t *testing.T) {
	t.Setenv("DATABASE_HOST", "flat-host")
	t.Setenv("DATABASE__DSN", "postgres://localhost/app")
	t.Setenv("STORAGE__BUCKET", "uploads")
	t.Setenv("JWT__SIGNINGKEY", "sk")
	cfg := load(t, config.WithProvider(providers.NewEnvProvider()))
	for key, want := range map[string]string{
		"database_host":  "flat-host",
		"database.dsn":   "postgres://localhost/app",
		"storage.bucket": "uploads",
		"jwt.signingkey": "sk",
	} {
		if got := cfg.Get(key); got != want {
			t.Errorf("%s = %v, want %q", key, got, want)
		}
	}
	if _, ok := cfg.Values()["DATABASE_HOST"]; ok {
		t.Error("key kept its upper case")
	}
}

// Docs: guides/configuration.mdx#key-naming
// "A prefix is stripped literally, so include the trailing separator: `WithEnvPrefix("APP_")` turns `APP_HOST` into `host`, while `WithEnvPrefix("APP")` leaves you with `_host`."
func TestEnvPrefixStrippedLiterally(t *testing.T) {
	t.Setenv("APP_HOST", "h")
	if got := load(t, config.WithProvider(providers.NewEnvProvider(providers.WithEnvPrefix("APP_")))).Get("host"); got != "h" {
		t.Fatalf(`WithEnvPrefix("APP_"): host = %v, want h`, got)
	}
	if got := load(t, config.WithProvider(providers.NewEnvProvider(providers.WithEnvPrefix("APP")))).Get("_host"); got != "h" {
		t.Fatalf(`WithEnvPrefix("APP"): _host = %v, want h`, got)
	}
}

// Docs: guides/configuration.mdx#key-naming
// "Environment variables are lowercased and flat by default. Use a double underscore to nest: DATABASE__HOST becomes database.host. ... providers.WithEnvPrefix("APP_"), providers.WithEnvSeparator("__")"
func TestEnvPrefixWithSeparator(t *testing.T) {
	t.Setenv("APP_DATABASE__HOST", "db.internal")
	cfg := load(t, config.WithProvider(providers.NewEnvProvider(providers.WithEnvPrefix("APP_"), providers.WithEnvSeparator("__"))))
	if got := cfg.Get("database.host"); got != "db.internal" {
		t.Fatalf("database.host = %v, want db.internal", got)
	}
}

// Docs: guides/configuration.mdx#key-naming
// "A fixed denylist of names drops session and security-sensitive variables before they are read: `PATH`, `HOME`, `SHELL`, `USER`, the loader variables `LD_PRELOAD`, `LD_LIBRARY_PATH`, `DYLD_INSERT_LIBRARIES`, and `DYLD_LIBRARY_PATH`, session names such as `SSH_AUTH_SOCK` and `SUDO_USER`, `TERM`, `LANG`, `TMPDIR`, `EDITOR`, `HOST`, `HOSTNAME`, `CI`, `GITHUB_TOKEN`, and similar. It lists exact names, not prefixes, so a variable such as `LD_AUDIT` or `SSH_ASKPASS` is read like any other. ... Matching happens on the full variable name before any prefix is stripped, so prefixed variables are never filtered."
func TestEnvDenylist(t *testing.T) {
	denied := []string{"PATH", "HOME", "SHELL", "USER", "LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES",
		"DYLD_LIBRARY_PATH", "SSH_AUTH_SOCK", "SUDO_USER", "TERM", "LANG", "TMPDIR", "EDITOR", "HOST", "HOSTNAME",
		"CI", "GITHUB_TOKEN"}
	allowed := []string{"LD_AUDIT", "SSH_ASKPASS"}
	for _, name := range append(denied, allowed...) {
		t.Setenv(name, "value-"+name)
	}
	values := load(t, config.WithProvider(providers.NewEnvProvider())).Values()
	for _, name := range denied {
		if v, ok := values[strings.ToLower(name)]; ok {
			t.Errorf("%s reached the config as %v", name, v)
		}
	}
	for _, name := range allowed {
		if got := values[strings.ToLower(name)]; got != "value-"+name {
			t.Errorf("%s = %v, want it read like any other variable", name, got)
		}
	}

	t.Setenv("APP_PATH", "p")
	t.Setenv("APP_HOST", "h")
	cfg := load(t, config.WithProvider(providers.NewEnvProvider(providers.WithEnvPrefix("APP_"))))
	if cfg.Get("path") != "p" || cfg.Get("host") != "h" {
		t.Fatalf("prefixed APP_PATH/APP_HOST = %v/%v, want p/h (never filtered)", cfg.Get("path"), cfg.Get("host"))
	}
}

// Docs: guides/configuration.mdx#providers
// "| `providers.NewJSONProvider()` | A JSON file, from disk or an `fs.FS` | | `providers.NewDotEnvProvider()` | A `.env` file |"
func TestFileProviders(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"from":{"disk":"yes"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DOTENV__KEY=dot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{"c.json": {Data: []byte(`{"from":{"fs":"yes"}}`)}}
	cfg := load(t,
		config.WithProvider(providers.NewJSONProvider(providers.WithJSONFilePath("config.json"))),
		config.WithProvider(providers.NewJSONProvider(providers.WithJSONFileFS(fsys), providers.WithJSONFilePath("c.json"))),
		config.WithProvider(providers.NewDotEnvProvider()),
	)
	for key, want := range map[string]string{"from.disk": "yes", "from.fs": "yes", "dotenv.key": "dot"} {
		if got := cfg.Get(key); got != want {
			t.Errorf("%s = %v, want %q", key, got, want)
		}
	}
}

// Docs: guides/configuration.mdx#providers
// "| `secretsmanager.NewProvider()` | AWS Secrets Manager, fetched eagerly at load |"
func TestSecretsManagerFetchedAtLoad(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if !strings.HasSuffix(r.Header.Get("X-Amz-Target"), "GetSecretValue") {
			http.Error(w, "unexpected target", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"ARN":          "arn:aws:secretsmanager:us-east-1:000000000000:secret:app",
			"Name":         "app",
			"SecretString": `{"db":{"password":"s3cret"}}`,
			"VersionId":    "v1",
		})
	}))
	t.Cleanup(srv.Close)
	cfg := config.New(config.WithProvider(secretsmanager.NewProvider(
		secretsmanager.WithEndpoint(srv.URL),
		secretsmanager.WithRegion("us-east-1"),
		secretsmanager.WithStaticCredentials("test", "test"),
		secretsmanager.WithSecret("app"),
	)))
	if requests.Load() != 0 {
		t.Fatal("secret fetched before Load")
	}
	if err := cfg.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("Load made %d requests, want 1", got)
	}
	if got := cfg.Get("db.password"); got != "s3cret" {
		t.Fatalf("db.password = %v, want s3cret", got)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("Get made another request (%d total); want the value fetched at load", got)
	}
}

type ctxKey struct{}

type ctxProvider struct{ got *any }

func (p ctxProvider) Name() string                  { return "ctx" }
func (p ctxProvider) Load() (map[string]any, error) { return map[string]any{}, nil }
func (p ctxProvider) LoadContext(ctx context.Context) (map[string]any, error) {
	*p.got = ctx.Value(ctxKey{})
	return map[string]any{"via": "context"}, nil
}

// Docs: guides/configuration.mdx#providers
// "Write your own by implementing `Name() string` and `Load() (map[string]any, error)`. Add `LoadContext(ctx)` and `LoadWithContext` will pass its context through"
func TestCustomProviders(t *testing.T) {
	t.Parallel()
	if got := load(t, config.WithProvider(mapProvider{"mine", map[string]any{"custom": "yes"}})).Get("custom"); got != "yes" {
		t.Fatalf("custom = %v, want yes", got)
	}
	var got any
	cfg := config.New(config.WithProvider(ctxProvider{got: &got}))
	if err := cfg.LoadWithContext(context.WithValue(t.Context(), ctxKey{}, "passed")); err != nil {
		t.Fatalf("LoadWithContext: %v", err)
	}
	if got != "passed" || cfg.Get("via") != "context" {
		t.Fatalf("LoadContext saw %v and produced via=%v, want the caller's context", got, cfg.Get("via"))
	}
}

// Docs: guides/configuration.mdx#providers
// "Despite its name, `WithDotEnvFileNotFoundPanic(true)`, the default, makes a missing file fail `Load()` with an error. It does not panic. `WithDotEnvFileAppendToOSEnv` defaults to `false`"
func TestDotEnvDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("missing .env panicked: %v", r)
			}
		}()
		if _, err := providers.NewDotEnvProvider().Load(); err == nil {
			t.Fatal("missing .env loaded without an error")
		}
	}()

	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DOCSCLAIM_DOTENV_EXPORT=leaked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCSCLAIM_DOTENV_EXPORT", "")
	if err := os.Unsetenv("DOCSCLAIM_DOTENV_EXPORT"); err != nil {
		t.Fatal(err)
	}
	if _, err := providers.NewDotEnvProvider().Load(); err != nil {
		t.Fatalf("load .env: %v", err)
	}
	if v, ok := os.LookupEnv("DOCSCLAIM_DOTENV_EXPORT"); ok {
		t.Fatalf(".env entry exported to the process environment as %q by default", v)
	}
}

type inner struct {
	Value string
}

type Embedded struct {
	EmbeddedField string
}

type bound struct {
	Embedded
	Tagged    string `json:"alpha"`
	MixedCase string

	I   int
	I8  int8
	I16 int16
	I32 int32
	I64 int64
	U   uint
	U8  uint8
	U16 uint16
	U32 uint32
	U64 uint64
	F32 float32
	F64 float64
	B   bool
	D   time.Duration

	Slice  []string
	Commas []string
	Array  [2]string
	Map    map[string]string
	Nested inner
}

// Docs: guides/configuration.mdx#binding-to-structs
// "Fields match on `json` tags first, then case-insensitively by name. Supported: every int, uint, and float width, bool, string, `time.Duration`, slices, fixed-size arrays, maps, nested structs, and embedded structs. Comma-separated strings bind to slices"
// Assumption: scalars arrive as strings, the way environment variables do.
func TestBindSupportedTypes(t *testing.T) {
	t.Parallel()
	cfg := load(t, config.WithProvider(mapProvider{"m", map[string]any{
		"embeddedfield": "emb",
		"alpha":         "tagged",
		"MIXEDCASE":     "mixed",
		"i":             "-1", "i8": "-8", "i16": "-16", "i32": "-32", "i64": "-64",
		"u": "1", "u8": "8", "u16": "16", "u32": "32", "u64": "64",
		"f32": "1.5", "f64": "2.5", "b": "true", "d": "5s",
		"slice":  []any{"a", "b"},
		"commas": "x,y,z",
		"array":  []any{"p", "q"},
		"map":    map[string]any{"k": "v"},
		"nested": map[string]any{"value": "deep"},
	}}))
	var got bound
	if err := cfg.Bind(&got); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	want := bound{
		Embedded: Embedded{EmbeddedField: "emb"}, Tagged: "tagged", MixedCase: "mixed",
		I: -1, I8: -8, I16: -16, I32: -32, I64: -64, U: 1, U8: 8, U16: 16, U32: 32, U64: 64,
		F32: 1.5, F64: 2.5, B: true, D: 5 * time.Second,
		Slice: []string{"a", "b"}, Commas: []string{"x", "y", "z"}, Array: [2]string{"p", "q"},
		Map: map[string]string{"k": "v"}, Nested: inner{Value: "deep"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Bind =\n%+v\nwant\n%+v", got, want)
	}
}

// Docs: guides/configuration.mdx#binding-to-structs
// "Fields match on `json` tags first, then case-insensitively by name."
// When a source has both the tag key and the field-name key, the tag wins,
// on every Bind.
func TestBindTagBeatsName(t *testing.T) {
	t.Parallel()
	type prec struct {
		Precedent string `json:"other"`
	}
	cfg := load(t, config.WithProvider(mapProvider{"m", map[string]any{"precedent": "by-name", "other": "by-tag"}}))
	counts := map[string]int{}
	for range 50 {
		var got prec
		if err := cfg.Bind(&got); err != nil {
			t.Fatalf("Bind: %v", err)
		}
		counts[got.Precedent]++
	}
	if counts["by-tag"] != 50 {
		t.Fatalf("over 50 binds the field got %v, want by-tag every time", counts)
	}
}

// Docs: guides/configuration.mdx#binding-to-structs
// "a fixed-size array needs a real list, and a source with more elements than fit is an error rather than a silent truncation."
// Assumption: "needs a real list" means a comma-separated string is an error
// for an array field.
func TestBindFixedArrays(t *testing.T) {
	t.Parallel()
	type arr struct{ Array [2]string }
	for name, value := range map[string]any{
		"too many elements":      []any{"a", "b", "c"},
		"comma-separated string": "a,b",
	} {
		var got arr
		err := load(t, config.WithProvider(mapProvider{"m", map[string]any{"array": value}})).Bind(&got)
		if err == nil {
			t.Errorf("%s: Bind = %v with nil error, want an error", name, got.Array)
		}
	}
}

type validated struct {
	Port int    `json:"port" validate:"required"`
	Code string `json:"code" validate:"docsclaim"`
}

// Docs: guides/configuration.mdx#binding-to-structs
// "Validation runs through [go-playground/validator] on `validate` tags. Pass `config.WithValidate(false)` to skip it, or `config.WithValidator(v)` to supply an instance with your own custom rules registered."
func TestBindValidation(t *testing.T) {
	t.Parallel()
	v := validator.New()
	if err := v.RegisterValidation("docsclaim", func(fl validator.FieldLevel) bool {
		return fl.Field().String() == "ok"
	}); err != nil {
		t.Fatal(err)
	}
	mk := func(values map[string]any) *config.Config {
		return load(t, config.WithValidator(v), config.WithProvider(mapProvider{"m", values}))
	}
	var out validated
	if err := mk(map[string]any{"code": "ok"}).Bind(&out); err == nil {
		t.Fatal("missing required port passed validation")
	}
	if err := mk(map[string]any{"code": "ok"}).Bind(&out, config.WithValidate(false)); err != nil {
		t.Fatalf("WithValidate(false) still validated: %v", err)
	}
	if err := mk(map[string]any{"port": 1, "code": "bad"}).Bind(&out); err == nil {
		t.Fatal("custom rule not applied")
	}
	if err := mk(map[string]any{"port": 1, "code": "ok"}).Bind(&out); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

type envConfig struct {
	gas.WithGasEnv
}

// Docs: guides/configuration.mdx#environments
// "The `gasenv` extension resolves the current environment from config providers, then `GAS_ENV`, then a default of `Development`. Embed `gas.WithGasEnv` in your config struct and `Bind` populates it, giving you `IsProduction()`, `IsDevelopmentLike()`, and friends."
func TestGasEnvResolution(t *testing.T) {
	resolve := func(t *testing.T, provider map[string]any) gas.Environment {
		t.Helper()
		cfg := load(t, config.WithProvider(mapProvider{"m", provider}), config.WithExtension(gasenv.NewExtension()))
		var c envConfig
		if err := cfg.Bind(&c); err != nil {
			t.Fatalf("Bind: %v", err)
		}
		return c.GasEnv
	}
	t.Setenv("GAS_ENV", "")
	if err := os.Unsetenv("GAS_ENV"); err != nil {
		t.Fatal(err)
	}
	if got := resolve(t, map[string]any{}); got != gas.EnvDevelopment || !got.IsDevelopmentLike() {
		t.Fatalf("default = %q, want development", got)
	}
	t.Setenv("GAS_ENV", "staging")
	if got := resolve(t, map[string]any{}); got != gas.EnvStaging {
		t.Fatalf("with GAS_ENV=staging = %q, want staging", got)
	}
	if got := resolve(t, map[string]any{"GasEnv": "production"}); got != gas.EnvProduction || !got.IsProduction() {
		t.Fatalf("provider production over GAS_ENV staging = %q, want production", got)
	}
}

// Docs: guides/configuration.mdx#environments
// "Gas uses this itself, which is how `gas/ui` knows to rebuild templates on every request in development."
func TestUIRebuildsTemplatesInDevelopment(t *testing.T) {
	t.Parallel()
	render := func(t *testing.T, env string) (string, string) {
		t.Helper()
		store := memory.NewStore()
		ctx := t.Context()
		_ = store.Register(ctx, "layouts/base.html", []byte(`{{define "base"}}[{{block "content" .}}{{end}}]{{end}}`))
		_ = store.Register(ctx, "home.html", []byte(`{{define "content"}}v1{{end}}`))
		cfg, err := configtest.NewMockConfigWithValues(map[string]any{"GasEnv": env})
		if err != nil {
			t.Fatal(err)
		}
		app := gas.NewApp(
			gas.WithServiceInstance[gas.ConfigProvider](cfg),
			gas.WithServiceInstance[gas.Logger](&gas.NopLogger{}),
			gas.WithServiceInstance[gas.TemplateProvider](store),
			gas.WithSingletonService[gas.UIProvider](ui.New[gas.TemplateProvider](ui.WithStaticFS(fstest.MapFS{}))),
		)
		if err := app.Start(ctx); err != nil {
			t.Fatalf("start: %v", err)
		}
		t.Cleanup(func() { _ = app.Stop(context.Background()) })
		uiSvc := app.ServiceContainer().MustResolve[gas.UIProvider]()
		first := httptest.NewRecorder()
		if err := uiSvc.Render(first, "home", nil); err != nil {
			t.Fatalf("render: %v", err)
		}
		_ = store.Register(ctx, "home.html", []byte(`{{define "content"}}v2{{end}}`))
		second := httptest.NewRecorder()
		if err := uiSvc.Render(second, "home", nil); err != nil {
			t.Fatalf("render: %v", err)
		}
		return first.Body.String(), second.Body.String()
	}
	if a, b := render(t, "development"); a != "[v1]" || b != "[v2]" {
		t.Fatalf("development renders %q then %q, want [v1] then [v2]", a, b)
	}
	if a, b := render(t, "production"); a != "[v1]" || b != "[v1]" {
		t.Fatalf("production renders %q then %q, want the cached [v1] twice", a, b)
	}
}

// Docs: guides/configuration.mdx#server-settings
// "| `Server.Host` | `0.0.0.0` | | `Server.Port` | `8080` | | `Server.ReadTimeout` | `5s` | | `Server.ReadHeaderTimeout` | `5s`; passed to `http.Server`, where `0` means `ReadTimeout` applies | | `Server.WriteTimeout` | `10s` | | `Server.IdleTimeout` | `2m` | | `Server.ShutdownTimeout` | `30s` | | `Server.MaxJSONBodyBytes` | `1048576` (1 MiB); `0` or negative means no limit |"
func TestServerDefaults(t *testing.T) {
	t.Parallel()
	got := gas.DefaultConfig().Server
	want := gas.ServerSettings{
		Host: "0.0.0.0", Port: 8080, ReadTimeout: 5 * time.Second, ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, ShutdownTimeout: 30 * time.Second,
		MaxJSONBodyBytes: 1 << 20,
	}
	if got != want {
		t.Fatalf("DefaultConfig().Server = %+v, want %+v", got, want)
	}
}

func appWith(t *testing.T, server map[string]any) *gas.App {
	t.Helper()
	cfg, err := configtest.NewMockConfigWithValues(map[string]any{"server": server})
	if err != nil {
		t.Fatal(err)
	}
	app := gas.NewApp(gas.WithServiceInstance[gas.ConfigProvider](cfg))
	app.Router().Handle(nil, http.MethodPost, "/bind", func(ctx gas.Context) error {
		var v struct {
			K string `json:"k"`
		}
		if err := ctx.BindJSON(&v); err != nil {
			return err
		}
		return ctx.NoContent()
	})
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	return app
}

// Docs: guides/configuration.mdx#server-settings
// "| `Server.ReadHeaderTimeout` | `5s`; passed to `http.Server`, where `0` means `ReadTimeout` applies |"
// The "0 means ReadTimeout applies" half is net/http's behavior; Gas's part is
// passing the value through unchanged.
func TestReadHeaderTimeoutPassedThrough(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]time.Duration{"0s": 0, "3s": 3 * time.Second} {
		srv := appWith(t, map[string]any{"readtimeout": "7s", "readheadertimeout": value}).Server()
		if srv.ReadTimeout != 7*time.Second || srv.ReadHeaderTimeout != want {
			t.Errorf("readheadertimeout %s: server ReadTimeout=%v ReadHeaderTimeout=%v, want 7s and %v",
				value, srv.ReadTimeout, srv.ReadHeaderTimeout, want)
		}
	}
	if got := appWith(t, map[string]any{}).Server().ReadHeaderTimeout; got != 5*time.Second {
		t.Errorf("default server ReadHeaderTimeout = %v, want 5s", got)
	}
}

// Docs: guides/configuration.mdx#server-settings
// "`App.Server()` may be called before `Start`, for example from a service's `Init` to set a `ReadTimeout` of zero for streaming uploads. When `Start` binds the config, it applies the bound address and timeouts to every field the caller has not changed and keeps those it did."
func TestServerBeforeStartKeepsCallerChanges(t *testing.T) {
	t.Parallel()
	cfg, err := configtest.NewMockConfigWithValues(map[string]any{"server": map[string]any{
		"host": "127.0.0.1", "port": 9494, "readtimeout": "7s", "writetimeout": "11s", "idletimeout": "3m",
	}})
	if err != nil {
		t.Fatal(err)
	}
	var app *gas.App
	app = gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[*uploads](func() *uploads {
			return &uploads{init: func() { app.Server().ReadTimeout = 0 }}
		}),
	)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	srv := app.Server()
	if srv.ReadTimeout != 0 {
		t.Errorf("ReadTimeout = %v, want the caller's 0 kept", srv.ReadTimeout)
	}
	if srv.Addr != "127.0.0.1:9494" || srv.WriteTimeout != 11*time.Second || srv.IdleTimeout != 3*time.Minute {
		t.Errorf("server Addr=%q WriteTimeout=%v IdleTimeout=%v, want the bound 127.0.0.1:9494, 11s, 3m",
			srv.Addr, srv.WriteTimeout, srv.IdleTimeout)
	}
}

type uploads struct{ init func() }

func (u *uploads) Name() string                { return "uploads" }
func (u *uploads) Init(context.Context) error  { u.init(); return nil }
func (u *uploads) Close(context.Context) error { return nil }

// Docs: guides/configuration.mdx#server-settings
// "| `Server.MaxJSONBodyBytes` | `1048576` (1 MiB); `0` or negative means no limit |"
func TestMaxJSONBodyBytesNoLimit(t *testing.T) {
	t.Parallel()
	body := `{"k":"` + strings.Repeat("a", 2<<20) + `"}`
	for _, limit := range []int{0, -1} {
		app := appWith(t, map[string]any{"maxjsonbodybytes": limit})
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/bind", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		app.Handler().ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Errorf("limit %d: 2 MiB body = %d, want 204 (no limit)", limit, rr.Code)
		}
	}
}

// Docs: guides/configuration.mdx#testing
// "`configtest.MockConfig` records calls and takes per-method `Fn` overrides."
func TestMockConfigRecordsAndOverrides(t *testing.T) {
	t.Parallel()
	m := &configtest.MockConfig{GetFn: func(key string) any { return "v:" + key }}
	if got := m.Get("a"); got != "v:a" || m.CallCount("Get") != 1 {
		t.Fatalf("Get = %v, CallCount = %d", got, m.CallCount("Get"))
	}
}

// Docs: guides/configuration.mdx#testing
// "use `configtest.NewMockConfigWithValues`, which delegates to a real config and avoids leaking the machine's environment into the test. Seed it with nested maps, not dotted keys: values go through `SetDefaults`, which does not split on `.`, so `"database.host"` would become one flat key that `Get("database.host")` cannot reach."
func TestMockConfigWithValues(t *testing.T) {
	t.Setenv("DOCSCLAIM_LEAK", "from-machine")
	m, err := configtest.NewMockConfigWithValues(map[string]any{
		"database":  map[string]any{"host": "nested"},
		"cache.ttl": "dotted",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Get("database.host"); got != "nested" {
		t.Errorf("Get(database.host) = %v, want nested", got)
	}
	if got := m.Get("docsclaim_leak"); got != nil {
		t.Errorf("machine environment leaked: docsclaim_leak = %v", got)
	}
	if got := m.Get("cache.ttl"); got != nil {
		t.Errorf("Get(cache.ttl) reached the dotted key: %v", got)
	}
	var out struct {
		Database struct{ Host string }
	}
	if err := m.Bind(&out); err != nil || out.Database.Host != "nested" {
		t.Errorf("Bind = %+v, %v; want real binding", out, err)
	}
}
