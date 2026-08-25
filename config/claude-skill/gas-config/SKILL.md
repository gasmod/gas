---
name: gas-config
description: >
  Reference documentation for the gas/config Go package
  (github.com/gasmod/gas/config) — configuration management service for the
  Gas ecosystem. Use this skill when writing, reviewing, or debugging Go code
  involving configuration loading, providers (env, JSON, .env), struct binding,
  hierarchical dot-notation access, ConfigProvider interface, extensions with
  pre/post-load hooks, gasenv environment detection, SetDefault/SetDefaults,
  the configtest mock, or wiring config into Gas apps via
  gas.WithServiceInstance. Make sure to use
  this skill whenever working with configuration in Gas applications, even if
  the user doesn't explicitly mention gas/config.
---

# Gas Config Package Reference

Configuration management service for the Gas ecosystem. Supports loading from
multiple providers (environment variables, JSON files, `.env` files) and
binding to Go structs.

```go
import config "github.com/gasmod/gas/config"
import "github.com/gasmod/gas/config/providers"
```

## Constructor

```go
// Option configures the config service constructor.
type Option func(*Config)

func New(opts ...Option) *Config

func WithProvider(p providers.Provider) Option
func WithExtension(ext Extension) Option
func WithValidator(v *validator.Validate) Option
```

`New` returns a `*Config` directly. Create and load config before the app
starts — it is not wired through the DI container. If no providers are
specified, an `EnvProvider` is added as the sole default provider.

## Config

Implements `gas.ConfigProvider`.

### Loading

```go
func (c *Config) Load() error                          // loads from all providers
func (c *Config) LoadWithContext(ctx context.Context) error
```

### Reading values

```go
func (c *Config) Get(key string) any                       // dot notation: "database.host"
func (c *Config) Find(key string) (value any, exist bool)  // same, with existence check
func (c *Config) Values() map[string]any                   // deep-cloned snapshot
```

`Get` and `Find` support hierarchical dot-notation paths. Keys are
lowercased internally. Returns deep-cloned values (safe for mutation).

### Setting values

```go
func (c *Config) Set(key string, value any)           // overrides existing
func (c *Config) SetDefault(key string, value any)    // won't override existing
func (c *Config) SetDefaults(values any) error        // from struct/map, won't override
```

`Set` and `SetDefault` create nested maps along the dot-path if they don't
exist. Empty keys are ignored. `SetDefaults` accepts a struct, `*struct`,
`map[string]any`, or `*map[string]any`.

Two dot-path caveats:

- `SetDefaults` (plural) does **not** split `.` in map keys — `{"database.host": x}`
  becomes one flat top-level key, unreachable via `Get("database.host")`. Pass
  nested maps instead. `SetDefault` (singular) does split the key.
- `Set`/`SetDefault` silently do nothing when a segment of the path already
  holds a non-map: after `Set("a", 1)`, `Set("a.b", 2)` is a no-op.

### Struct binding

```go
func (c *Config) Bind(dest any, options ...BindOption) error

type BindOption func(*BindOptions)

func WithValidate(validate bool) BindOption
```

Field matching: `json` tags first, then case-insensitive field names.
Validation uses `go-playground/validator` tags (enabled by default). Pass
`config.WithValidator(v)` to `New` to use a caller-owned `*validator.Validate`
(e.g. with custom tags registered); otherwise `validator.New()` is used.

Supported types: all int/uint/float variants, bool, string, `time.Duration`,
slices (including comma-separated strings), fixed-size arrays, maps, nested
structs, embedded structs.

Comma-separated strings bind to slices only. A fixed-size array needs a real
list (JSON array or any `[]any`); a source with more elements than the array
can hold is an error, not a silent truncation. (The sentinel lives in
`internal/maputils`, so callers get the message, not an `errors.Is` target.)

## Providers

### Provider interface

```go
// In package providers
type Provider interface {
    Name() string
    Load() (map[string]any, error)
}

// Optional: providers that call remote services can also implement this;
// Config.LoadWithContext prefers LoadContext over Load when present.
type ContextProvider interface {
    Provider
    LoadContext(ctx context.Context) (map[string]any, error)
}
```

### Provider ordering

Later providers override earlier ones. When you specify providers explicitly,
no `EnvProvider` is auto-added — you must include it yourself if needed:

```
provider1 (lowest) → provider2 → provider3 (highest, wins on conflict)
```

If you specify **no** providers at all, a default `EnvProvider` is created
automatically.

### Environment variables

```go
providers.NewEnvProvider(opts ...EnvOption) *EnvProvider
```

| Option | Signature | Description |
|--------|-----------|-------------|
| Prefix | `WithEnvPrefix(prefix string)` | Filter by prefix; prefix is stripped literally from keys |
| Separator | `WithEnvSeparator(sep string)` | Nesting separator (default `"__"`) |

Key conventions — env vars are lowercased and kept as flat `snake_case` keys:

```
DATABASE_HOST  → database_host       (flat)
DATABASE__HOST → database.host       (nested via __)
```

The prefix is stripped literally, so it must include the trailing separator:
`WithEnvPrefix("APP_")` maps `APP_HOST` → `host`, while `WithEnvPrefix("APP")`
maps it to `_host`.

Denylist: session- and security-sensitive variables never reach the
configuration — `PATH`, `HOME`, `SHELL`, `USER`, `LD_*`/`DYLD_*`, `SSH_*`,
`SUDO_*`, `TERM`, `LANG`, `TMPDIR`, `EDITOR`, `HOST`, `HOSTNAME`, `CI`,
`GITHUB_TOKEN`, and similar (`internal/env/util.go` has the full list).
`HOST` and `CI` are the common surprises. Matching is against the full
variable name before the prefix is stripped, so prefixed variables are never
filtered.

Constant: `providers.EnvProviderName = "Environment Variables"`

### JSON files

```go
providers.NewJSONProvider(opts ...JSONOption) *JSONProvider
```

| Option | Signature | Description |
|--------|-----------|-------------|
| File path | `WithJSONFilePath(filePath string)` | **Required** |
| Custom FS | `WithJSONFileFS(fileFS fs.FS)` | e.g. `embed.FS` |

Errors: `ErrJSONFilePathNotSet`, `ErrJSONFileReadFailed`, `ErrJSONDecodeFailed`

### .env files

```go
providers.NewDotEnvProvider(opts ...DotEnvOption) *DotEnvProvider
```

| Option | Signature | Default |
|--------|-----------|---------|
| File path | `WithDotEnvFilePath(filePath string)` | `".env"` |
| Separator | `WithDotEnvSeparator(sep string)` | `"__"` |
| Custom FS | `WithDotEnvFileFS(fileFS fs.FS)` | OS filesystem |
| Fail if missing | `WithDotEnvFileNotFoundPanic(bool)` | `true` |
| Inject into OS env | `WithDotEnvFileAppendToOSEnv(bool)` | `false` |

Despite its name, `WithDotEnvFileNotFoundPanic(true)` makes a missing file fail
`Load()` with an error; it does not panic. `WithDotEnvFileAppendToOSEnv`
defaults to `false` — mirroring `.env` entries into the live process
environment leaks them to child processes, so it is opt-in.

Same key conventions as `EnvProvider`, including the denylist: flat
`snake_case` by default, `__` for nesting. Denylisted names are skipped both
when building the config map and when exporting to the OS environment.

Errors: `ErrDotEnvFilePathNotSet`, `ErrDotEnvFileReadFailed`,
`ErrDotEnvParseFailed`, `ErrSetEnv`

### AWS SecretsManager

```go
import "github.com/gasmod/gas/config/providers/secretsmanager"

secretsmanager.NewProvider(opts ...Option) *Provider
```

| Option | Signature | Description |
|--------|-----------|-------------|
| Secret | `WithSecret(name string)` | JSON-object secret merged at root; repeatable, later wins |
| Secret at key | `WithSecretAtKey(name, key string)` | Value nested at dot-notation key (object or raw string) |
| Region | `WithRegion(region string)` | AWS region |
| Static credentials | `WithStaticCredentials(id, secret string)` | Default AWS chain when unset |
| Endpoint | `WithEndpoint(url string)` | Custom endpoint (LocalStack) |
| Client | `WithClient(client API)` | Inject client/mock; other client options ignored |
| Timeout | `WithTimeout(d time.Duration)` | Bounds `Load()` without caller context (default 10s) |

Implements `providers.ContextProvider`: `Config.LoadWithContext` passes its
context through. Eager fetch at load time; no caching or rotation.

Constants: `ProviderName = "AWS SecretsManager"`, `DefaultTimeout = 10s`.
Errors: `ErrNoSecretsConfigured`, `ErrSecretFetchFailed`,
`ErrSecretDecodeFailed`, `ErrClientInitFailed`.

## Extensions

```go
type Extension interface {
    Name() string
    PreLoad(ctx context.Context, cfg *Config) error
    PostLoad(ctx context.Context, cfg *Config) error
}
```

Extensions run hooks around provider loading:
1. All `PreLoad` hooks execute (in registration order)
2. All providers load (in registration order, later overrides earlier)
3. All `PostLoad` hooks execute (in registration order)

### gasenv Extension

```go
import gasenv "github.com/gasmod/gas/config/extensions/gasenv"
```

Environment detection and validation. Resolves current environment from
config providers, OS variable, or default.

#### Constructor

```go
gasenv.NewExtension(opts ...EnvOption) *Extension
```

| Option | Signature | Default |
|--------|-----------|---------|
| OS var name | `WithEnvVarName(name string)` | `"GAS_ENV"` |
| Default env | `WithDefault(env Environment)` | `Development` |
| Config key | `WithConfigKey(key string)` | `"GasEnv"` |
| Allowed envs | `WithAllowedEnvs(envs ...Environment)` | all four |

#### Environment type and constants

```go
type Environment string

const (
    Development Environment = "development"
    Testing     Environment = "testing"
    Staging     Environment = "staging"
    Production  Environment = "production"
)
```

Methods on `Environment`: `IsDevelopment()`, `IsTesting()`, `IsStaging()`,
`IsProduction()`, `IsDevelopmentLike()`, `IsProductionLike()`, `String()`.

#### Extension API

| Method | Returns | Description |
|--------|---------|-------------|
| `Name()` | `string` | `"GasEnv"` |
| `Current()` | `Environment` | Resolved environment |
| `Is(env)` | `bool` | Exact match |
| `IsDevelopment()` | `bool` | `== Development` |
| `IsTesting()` | `bool` | `== Testing` |
| `IsStaging()` | `bool` | `== Staging` |
| `IsProduction()` | `bool` | `== Production` |
| `IsDevelopmentLike()` | `bool` | development or testing |
| `IsProductionLike()` | `bool` | production or staging |

#### Resolution priority

1. Config providers (JSON, `.env`, etc.) — checked in `PostLoad`
2. OS environment variable (`GAS_ENV` by default) — checked in `PreLoad`
3. Default environment (`Development`)

Invalid values at any stage fall back to the default.

#### Constants

```go
const ExtensionName    = "GasEnv"
const DefaultConfigKey = "GasEnv"
const DefaultEnvVarName = "GAS_ENV"
const DefaultEnvironment = Development
```

#### Embedding in config structs

```go
type WithGasEnv struct {
    GasEnv Environment
}
```

Embed `gasenv.WithGasEnv` in your config struct. The `GasEnv` field is
auto-populated by `Bind()` from the config key.

```go
type AppConfig struct {
    gasenv.WithGasEnv
    Database struct {
        Host string
        Port int
    }
}

var appCfg AppConfig
cfg.Bind(&appCfg)
appCfg.GasEnv.IsProduction() // false
```

## Testing

```go
import "github.com/gasmod/gas/config/configtest"
```

`MockConfig` structurally satisfies `gas.ConfigProvider`. Every method records
the call and delegates to its `Fn` field when set, otherwise returns the zero
value. `Calls` is guarded by an internal mutex, so the mock is safe to use
from concurrent tests.

```go
type MockConfig struct {
    SetDefaultFn  func(key string, value any)
    SetDefaultsFn func(values any) error
    SetFn         func(key string, value any)
    BindFn        func(dest any, options ...config.BindOption) error
    GetFn         func(key string) any
    FindFn        func(key string) (any, bool)
    ValuesFn      func() map[string]any
    Calls         []Call
}

type Call struct {
    Method string
    Args   []any
}

func (m *MockConfig) Reset()                     // clears Calls
func (m *MockConfig) CallCount(method string) int
```

Recorded method names match the Go method names — `"SetDefault"`,
`"SetDefaults"`, `"Set"`, `"Bind"`, `"Get"`, `"Find"`, `"Values"`. `Bind`
records only `dest`, not its `BindOption`s.

```go
mock := &configtest.MockConfig{}
mock.GetFn = func(key string) any {
    if key == "database.host" {
        return "localhost"
    }
    return nil
}

mock.CallCount("Get") // assert on usage
```

### Seeded mock

```go
func NewMockConfigWithValues(values map[string]any) (*MockConfig, error)
```

Wires every `Fn` field to a real `*config.Config` seeded with `values`, so
`Get`/`Find`/`Bind` keep their real key-normalization and binding semantics
while calls are still recorded. It loads from an internal no-op provider
rather than the default `EnvProvider`, so real OS environment variables never
leak into tests. Individual `Fn` fields can still be overridden afterwards.

```go
mock, err := configtest.NewMockConfigWithValues(map[string]any{
    "database": map[string]any{
        "host": "localhost",
        "port": 5432,
    },
})
```

Seed nested maps, not dotted keys: the values go through `SetDefaults`, which
does not split `.` in keys.

## Errors

```go
var (
    ErrProviderLoadFailed          = errors.New("failed to load from provider")
    ErrExtensionPreLoadHookFailed  = errors.New("failed to execute extension pre-load hook")
    ErrExtensionPostLoadHookFailed = errors.New("failed to execute extension post-load hook")
    ErrNilValues                   = errors.New("values cannot be nil")
)
```

## Complete Example

Full example showing config with multiple providers, gasenv extension, and
DI wiring into a Gas application:

```go
package main

import (
    "fmt"
    "log"

    "github.com/gasmod/gas"
    config "github.com/gasmod/gas/config"
    "github.com/gasmod/gas/config/providers"
    gasenv "github.com/gasmod/gas/config/extensions/gasenv"
)

type AppConfig struct {
    gasenv.WithGasEnv // auto-populated environment field
    Database struct {
        Host     string `json:"host" validate:"required"`
        Port     int    `json:"port"`
        Password string `json:"password" validate:"required"`
    }
    Server struct {
        Host string `json:"host"`
        Port int    `json:"port"`
    }
}

func main() {
    // 1. Create the environment extension
    envExt := gasenv.NewExtension()

    // 2. Create config with providers and extensions
    //    Later providers override earlier ones
    cfg := config.New(
        config.WithProvider(providers.NewEnvProvider(
            providers.WithEnvPrefix("APP_"),    // only APP_* vars, prefix stripped
        )),
        config.WithProvider(providers.NewJSONProvider(
            providers.WithJSONFilePath("config.json"), // base config
        )),
        config.WithProvider(providers.NewDotEnvProvider(
            providers.WithDotEnvFileNotFoundPanic(false), // optional .env
        )),
        config.WithExtension(envExt),
    )

    // 3. Set defaults before loading (won't override provider values)
    cfg.SetDefault("server.host", "0.0.0.0")
    cfg.SetDefault("server.port", 8080)

    // 4. Load from all providers
    if err := cfg.Load(); err != nil {
        log.Fatal(err)
    }

    // 5. Bind to a typed struct with validation
    var appCfg AppConfig
    if err := cfg.Bind(&appCfg); err != nil {
        log.Fatal(err)
    }

    // 6. Use environment checks
    if envExt.IsDevelopmentLike() {
        fmt.Println("Running in dev mode:", envExt.Current())
    }

    // 7. Wire into Gas app as ConfigProvider singleton
    app := gas.NewApp(
        gas.WithServiceInstance[gas.ConfigProvider](cfg),
        // other services receive cfg via constructor injection
    )

    app.Run()
}
```

### Consuming via gas.ConfigProvider

Other services receive config through the provider interface without
importing gas/config:

```go
type Service struct {
    cfg gas.ConfigProvider // the singleton instance registered above
}

func New(cfg gas.ConfigProvider) *Service {
    return &Service{cfg: cfg}
}

func (s *Service) Init() error {
    var myCfg MyConfig
    return s.cfg.Bind(&myCfg)
}
```

### Auto-binding in other Gas packages

When packages like `gas/database` don't receive explicit `WithConfig`, they
automatically bind from the injected `gas.ConfigProvider`. This lets you
drive all service configuration from a single `.env` or JSON file without
manual wiring.

## Choosing a Provider

| Use case | Provider | Notes |
|----------|----------|-------|
| 12-factor apps, containers | `EnvProvider` | Zero files, works everywhere |
| Structured config with nesting | `JSONProvider` | Best for complex hierarchies |
| Local dev, secrets not in git | `DotEnvProvider` | Familiar KEY=VALUE format |
| All of the above | Stack them | Later providers win on conflicts |

A common pattern is `EnvProvider` (base) + `JSONProvider` (structured defaults) +
`DotEnvProvider` (local overrides). Remember: when specifying any provider
explicitly, include `EnvProvider` yourself if you want OS env vars.
