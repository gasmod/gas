---
name: gas-config
description: >
  Reference documentation for the gas/config Go package
  (github.com/gasmod/gas/config), the configuration service for the Gas
  ecosystem. Use this skill when writing, reviewing, or debugging Go code
  involving configuration loading, providers (env, JSON, .env, AWS Secrets
  Manager), struct binding, hierarchical dot-notation access, the
  ConfigProvider interface, extensions with pre/post-load hooks, the
  EnvExtension (GAS_ENV environment detection), Set/SetDefault/SetDefaults,
  the configtest mock, or wiring config into Gas apps via
  gas.WithServiceInstance. Make sure to use this skill whenever working with
  configuration in Gas applications, even if the user doesn't explicitly
  mention gas/config.
---

# Gas Config Package Reference

Configuration service for the Gas ecosystem. Loads from providers
(environment variables, JSON files, `.env` files, AWS Secrets Manager),
merges them into one tree of values, and binds that tree into Go structs.

```go
import "github.com/gasmod/gas/config"
import "github.com/gasmod/gas/config/secretsmanager" // optional, AWS
import "github.com/gasmod/gas/config/configtest"     // tests
```

Since v0.6.0 the providers, the environment extension and `Environment` all
live in package `config` itself. There is no `config/providers` or
`config/extensions/gasenv` package any more.

## Constructor

```go
// Option configures a Config.
type Option func(*Config)

func New(opts ...Option) *Config

func WithProvider(p Provider) Option    // providers load in order added, later wins
func WithExtension(ext Extension) Option // extensions run in order added
func WithValidator(v Validator) Option  // nil keeps validator.New()

type Validator interface {
    StructCtx(ctx context.Context, s any) error
}
```

`New` returns a `*Config` that holds nothing until it is loaded. Create and
load config before the app starts; it is not built through the DI container.
If no provider is passed, `New` adds an `EnvProvider` with the default
separator and no prefix. `*validator.Validate` from
`github.com/go-playground/validator/v10` implements `Validator`.

## Config

`*Config` implements `config.ConfigProvider` and `gas.ConfigProvider` (same
method set; neither module imports the other):

```go
type ConfigProvider interface {
    SetDefault(key string, value any) error
    SetDefaults(values any) error
    Set(key string, value any) error
    Bind(dest any) error
    Get(key string) any
    Find(key string) (value any, exist bool)
    Values() map[string]any
}
```

`*Config` is safe for concurrent use: load, read, write and bind from several
goroutines at once. Provider I/O runs outside the lock.

### Loading

```go
func (c *Config) Load() error // LoadWithContext(context.Background())
func (c *Config) LoadWithContext(ctx context.Context) error

func (c *Config) LoadProvider(p Provider) error // LoadProviderContext(context.Background(), p)
func (c *Config) LoadProviderContext(ctx context.Context, p Provider) error
```

`Load`/`LoadWithContext` run each extension's `PreLoad`, load every provider
in the order added, merge their values over the configuration in that
order, then run each extension's `PostLoad`. The first error stops the load.

- Every provider loads before anything is merged, so a reader never sees part
  of a load, and a failing provider merges none of the providers' values
  (what a `PreLoad` stored stays).
- A failing `PreLoad` stops before any provider loads; a failing `PostLoad`
  stops after the values are merged, and they stay.
- A load never removes a key. Loading again reloads every provider: a key a
  provider no longer returns keeps its value, and a value stored with `Set`
  keeps its value unless a provider sets that key.

Merging: each provider's keys are trimmed and lowercased at every depth
(including maps inside `[]any` lists) before merging. Two keys of one provider
that become the same (`Host` and `host`) are an error; across providers the
later one wins. Where both sides hold a map under one key, maps merge key by
key. Anywhere else the provider's value replaces what is there: a list
replaces a list as a whole, and `nil` replaces anything.

`LoadProvider`/`LoadProviderContext` load one more provider, merge its values
over the configuration, and append it to the providers later loads reload.
A provider that fails to load is not added, so the call can be retried.
Extensions do **not** run, so an `EnvExtension` will not re-resolve the
environment from a provider added this way; call `Load()` afterwards if that
provider can set the environment key.

### Keys

`Get`, `Find`, `Set` and `SetDefault` take a dot-separated key:
`database.host` is `host` in the map under `database`. Each level is trimmed
and lowercased, so `Database.HOST` is the same key. A key that is empty or has
an empty level (`a..b`, `a. .b`) is no key: `Get`/`Find` find nothing, and
`Set`/`SetDefault` return an error wrapping `ErrInvalidKey`.

A key in the tree that contains a dot, such as `{"database.host": "db"}` from a
JSON file, cannot be reached, because the dot splits it. Use nested objects.

### Reading values

```go
func (c *Config) Get(key string) any                      // nil when absent
func (c *Config) Find(key string) (value any, exist bool) // tells nil from missing
func (c *Config) Values() map[string]any                  // whole tree
```

All three return a copy: maps, slices and pointers are copied at every depth,
so mutating the result is safe. A struct value is copied the way Go copies
it, so a map inside one stays shared.

### Setting values

```go
func (c *Config) Set(key string, value any) error        // replaces
func (c *Config) SetDefault(key string, value any) error // only if key holds none
func (c *Config) SetDefaults(values any) error           // map or struct, only missing keys
```

All three return errors now; check them.

- `Set` stores a value, adding missing maps on the path. A level on the path
  that holds a non-map blocks it: after `Set("a", 1)`, `Set("a.b", 2)` returns
  an error wrapping `ErrKeyPathBlocked` and changes nothing.
- `SetDefault` stores only at a key that holds no value. A key holding `nil`
  holds a value. A key under a level holding a non-map is left alone, without
  an error.
- `Set` and `SetDefault` store a normalized copy: map keys inside `value` are
  trimmed and lowercased at every depth (any map key type becomes
  `map[string]any`). Two keys that collide are an error wrapping
  `ErrKeyConflict`, and nothing is stored.
- `SetDefaults` takes `map[string]any`, `*map[string]any`, a struct or
  `*struct`, and adds the keys the configuration does not hold, merging maps
  key by key. Struct fields are named as `Bind` names them. Map keys are
  **not** split on `.`; pass nested maps. An untyped `nil` or a nil pointer to
  a map is `ErrNilValues`; a nil map adds nothing.

Defaults set before a load are a base the providers load over.

### Struct binding

```go
func (c *Config) Bind(dest any) error
```

`Bind` takes no options in v0.6.0 (`BindOption`/`WithValidate` are gone);
validation always runs, after filling. `dest` is a pointer to a struct.

- Fields are named as `encoding/json` names them: the `json` tag name, else
  the Go name. `json:"-"` and unexported fields are skipped; embedded struct
  fields bind at the embedding struct's level. A key matches a field exactly,
  else case-insensitively; two keys matching one field are an error.
- A field with no key keeps its value, so pre-filled defaults survive. Keys
  without a field are ignored.
- Assignable values are copied. Otherwise: struct fields take a map; map
  fields take a map with string keys; slice fields take a list or a
  comma-separated string (elements trimmed), replacing the field as a whole;
  bool/int/float fields take that kind or a parseable string (`"8080"`), and
  an out-of-range number is an error; string fields take strings, bools and
  numbers (`fmt.Sprint`, floats without exponent).
- `time.Duration` takes a string `time.ParseDuration` accepts, so it needs a
  unit: `"30s"` works, `"30"` is an error, `"0"` is zero.
- `nil` zeroes the field; nil pointer fields are allocated.

Validation uses the `Validator` (default `validator.New()`, reading `validate`
tags). A struct that fails to fill is not validated, and fields set before
the error keep their new values. The validator's error is wrapped, so
`errors.As(err, &validator.ValidationErrors{})` works.

## Providers

### Provider interfaces

```go
type Provider interface {
    Name() string                  // names it in errors
    Load() (map[string]any, error) // called on every load
}

// Optional: Config calls LoadContext instead of Load when implemented.
type ContextProvider interface {
    Provider
    LoadContext(ctx context.Context) (map[string]any, error)
}
```

The Config keeps a copy of the returned map, so a provider may keep and
mutate it. The built-in providers keep no state between loads.

### Provider ordering

Later providers override earlier ones. When you pass providers explicitly, no
`EnvProvider` is added; include it yourself if you want OS env vars:

```
provider1 (lowest) -> provider2 -> provider3 (highest, wins on conflict)
```

`LoadProvider` appends to the same list, so a provider added that way wins
over everything before it. Names are not unique keys: two providers with the
same `Name()` are allowed.

### Environment variables

```go
func NewEnvProvider(opts ...EnvProviderOption) *EnvProvider
```

| Option | Signature | Default |
|--------|-----------|---------|
| Prefix | `WithEnvPrefix(prefix string)` | none |
| Separator | `WithEnvSeparator(sep string)` | `"__"`; `""` keeps every name one level |

`Name()` returns `EnvProviderName = "Environment Variables"`.

```
DATABASE_HOST  -> database_host   (flat)
DATABASE__HOST -> database.host   (nested via __)
```

Names are lowercased. The prefix is matched case-insensitively and removed
literally, so include the trailing separator: `WithEnvPrefix("APP_")` maps
`APP_HOST` to `host`, while `WithEnvPrefix("APP")` maps it to `_host`. A name
with an empty level (`A____B`, `__A`) is skipped.

Two names that become the same key (`Foo` and `FOO`), or where one needs the
other's key as a map (`A` and `A__B`), fail `Load` with an error wrapping
`ErrProviderLoadFailed`. This is intended, not a bug.

Unsafe list: variables that describe the user, shell or session never reach
the configuration, including `PATH`, `HOME`, `USER`, `SHELL`, `LD_*`/`DYLD_*`
loader vars, `SSH_*` and `SUDO_*` session vars, `TERM`, `LANG`, `TMPDIR`,
`EDITOR`, `HOST`, `HOSTNAME`, `MAIL`, `PWD`, `GITHUB_TOKEN` and `CI`
(`internal/env/util.go` has the full list of exact names). `HOST` and `CI` are
the common surprises. The check is on the full name before the prefix is
removed, so `APP_PATH` with prefix `APP_` is kept as `path`.

### JSON files

```go
func NewJSONProvider(opts ...JSONOption) *JSONProvider
```

| Option | Signature | Description |
|--------|-----------|-------------|
| File path | `WithJSONFilePath(filePath string)` | **Required**, no default |
| Custom FS | `WithJSONFileFS(fileFS fs.FS)` | e.g. `embed.FS`; nil reads the working directory |

`Name()` returns `"JSON"`. The file must hold an object. Values have
`encoding/json` types except numbers: an integer is `int64` (or `uint64` if
too large), other numbers `float64`; a number too large for `float64`
(`1e400`) is an error.

Errors: `ErrJSONFilePathNotSet`, `ErrJSONFileReadFailed`, `ErrJSONDecodeFailed`.

### Files and file systems

Without a custom FS, the JSON and `.env` providers read from the working
directory: the path (relative or absolute) must lie inside it, symlinks
included, be a regular file, and be at most 1 MiB. A symlink on the path must
be relative. An absolute path such as `/etc/app/config.json` outside the
working directory fails; pass `WithJSONFileFS(os.DirFS("/etc/app"))` with a
relative path instead. A custom `fs.FS` skips those checks and uses its own
path rules (for `embed.FS`: slash-separated, unrooted, no `.`/`..`). A
missing file wraps `fs.ErrNotExist`.

### .env files

```go
func NewDotEnvProvider(opts ...DotEnvOption) *DotEnvProvider
```

| Option | Signature | Default |
|--------|-----------|---------|
| File path | `WithDotEnvFilePath(filePath string)` | `".env"` |
| Separator | `WithDotEnvSeparator(sep string)` | `"__"` |
| Custom FS | `WithDotEnvFileFS(fileFS fs.FS)` | working directory |
| Fail if missing | `WithDotEnvFileNotFoundPanic(bool)` | `true` |
| Copy into OS env | `WithDotEnvFileAppendToOSEnv(bool)` | `false` |

`Name()` returns `"DotEnv"`. Despite its name,
`WithDotEnvFileNotFoundPanic(true)` makes a missing file a `Load` error
wrapping `ErrDotEnvFileReadFailed`; nothing panics. With `false`, a missing
file loads no values. `WithDotEnvFileAppendToOSEnv(true)` copies the file's
variables (minus the unsafe list) into the process environment after the
whole file loads; child processes inherit them, secrets included, so it is
opt-in.

Names become keys as for `EnvProvider` (lowercased, `__` nests, unsafe names
skipped, no prefix). The format is small and strict; anything outside it is
an error:

```
# a comment
DB__HOST=localhost
DB__PORT = 5432            # spaces around "=" are allowed
DB__NAME="app db"          # quotes keep spaces and "#", and may span lines
DB__URL=postgres://${DB__HOST}:${DB__PORT}/app
DB__PASSWORD='p@ss${not-a-ref}'
```

- A name is `[A-Za-z_][A-Za-z0-9_]*`, defined once, not differing from
  another only in case. No `export` keyword.
- An unquoted value ends at a space, tab or `#`. Nothing is escaped: a
  backslash is text, and a double-quoted value cannot hold `"`.
- `${NAME}` in unquoted or double-quoted values expands from the same file,
  never the process environment; single quotes do not expand. An undefined
  reference or a cycle is an error.
- The file must be valid UTF-8.

Errors: `ErrDotEnvFilePathNotSet`, `ErrDotEnvFileReadFailed`,
`ErrDotEnvParseFailed` (format errors and conflicting names; the message
gives a position and key, never the value), `ErrSetEnv`.

### AWS Secrets Manager

```go
import "github.com/gasmod/gas/config/secretsmanager"

func NewProvider(opts ...Option) *Provider
```

| Option | Signature | Description |
|--------|-----------|-------------|
| Secret | `WithSecret(name string)` | Value must be a JSON object, merged at the root; repeatable, later wins |
| Secret at key | `WithSecretAtKey(name, key string)` | JSON object nested at dot key; any other value stored as its raw text |
| Region | `WithRegion(region string)` | Empty leaves it to the default AWS config |
| Static credentials | `WithStaticCredentials(id, secret string)` | Default AWS chain unless both are set |
| Endpoint | `WithEndpoint(url string)` | e.g. LocalStack |
| Client | `WithClient(client API)` | Inject client/fake; region, creds, endpoint then ignored |
| Timeout | `WithTimeout(d time.Duration)` | Bounds each load (default `DefaultTimeout`, 10s); `<= 0` means no limit of its own |

Implements `config.ContextProvider`; `Config.LoadWithContext` passes its
context through, and the load ends at the caller's deadline or the timeout,
whichever is first. Every load re-fetches every secret (AWSCURRENT), one at a
time in registration order, so a reload picks up rotations. No caching. The
client is built on first load and reused.

Constants: `ProviderName = "AWS SecretsManager"`, `DefaultTimeout = 10s`.
Errors: `ErrNoSecretsConfigured`, `ErrInvalidKey` (empty level in a
`WithSecretAtKey` key), `ErrClientInitFailed`, `ErrSecretFetchFailed`
(AWS error wrapped, reachable with `errors.As`), `ErrSecretDecodeFailed`.

## Extensions

```go
type Extension interface {
    Name() string                                    // names it in errors
    PreLoad(ctx context.Context, cfg *Config) error  // before any provider loads
    PostLoad(ctx context.Context, cfg *Config) error // after values are merged
}
```

Hooks run in registration order around provider loading. They run outside the
load ordering and may call `cfg.LoadProvider`.

### EnvExtension (application environment)

```go
func NewExtension(opts ...EnvOption) *EnvExtension
```

Resolves the environment during each load and stores it at the key `GasEnv`
(read case-insensitively, so `gasenv` too).

| Option | Signature | Default |
|--------|-----------|---------|
| OS var name | `WithEnvVarName(name string)` | `DefaultEnvVarName = "GAS_ENV"` |
| Fallback | `WithDefault(env Environment)` | `DefaultEnvironment = EnvDevelopment` |
| Config key | `WithConfigKey(key string)` | `DefaultConfigKey = "GasEnv"` |
| Allowed envs | `WithAllowedEnvs(envs ...Environment)` | all four below |

```go
type Environment string

const (
    EnvDevelopment Environment = "development"
    EnvTesting     Environment = "testing"
    EnvStaging     Environment = "staging"
    EnvProduction  Environment = "production"
)
```

Methods on `Environment`: `IsDevelopment()`, `IsTesting()`, `IsStaging()`,
`IsProduction()`, `IsDevelopmentLike()` (development or testing),
`IsProductionLike()` (production or staging), `String()`.

`*EnvExtension` methods: `Name()` (`ExtensionName = "GasEnv"`), `Current()`,
`Is(env)`, and the same six `Is*` predicates. `Current()` returns the
environment the last load resolved, or `""` before the first load. It is safe
for concurrent use.

Resolution:

1. A value a provider sets at the key (checked in `PostLoad`).
2. Else the `GAS_ENV` variable (stored at the key in `PreLoad`, unless the key
   already holds a value).
3. Else, when the variable is unset or empty, the fallback.

Invalid values are **errors**, not fallbacks. Values compare case-sensitively;
the fallback is always allowed. A disallowed value in the variable fails
`PreLoad`; one from a provider, or a non-string at the key, fails `PostLoad`.
Both wrap `ErrEnvironmentNotAllowed` (plus the hook-failed sentinel), so a
typo like `prod` stops the app. The variable is checked on every load even if
a provider sets the key. Once a load stored the key, a later change to the
variable only takes effect through a provider. With a custom key, the
environment is also stored at `GasEnv`.

### Reading the environment in structs

`gas.WithGasEnv` is gone. Bind a field of type `config.Environment` or
`gas.Environment` from the `gasenv` key. A field named `Env` needs the tag,
since its own name is the key `env`:

```go
type AppConfig struct {
    Env      config.Environment `json:"gasenv"`
    Database struct {
        Host string
        Port int
    }
}

var appCfg AppConfig
if err := cfg.Bind(&appCfg); err != nil {
    return err
}
appCfg.Env.IsProduction()
```

Core's `gas.Config.Env` binds from `gasenv` the same way at `App.Start`, and
Start fails with `gas.ErrInvalidConfig` unless it is one of the App's allowed
environments (`gas.WithAllowedEnvironments`, which always includes
development). If you widen `config.WithAllowedEnvs`, widen the App's list too.

## Testing

```go
import "github.com/gasmod/gas/config/configtest"
```

`MockConfig` satisfies `config.ConfigProvider` (and so `gas.ConfigProvider`).
Every method appends a `Call` and delegates to its `Fn` field when set,
otherwise returns zero values (nil, false, nil error). Recording,
`CallCount` and `Reset` take an internal lock; reading `Calls` directly does
not, so read it after the calls under test finish, or use `CallCount`.
Setting an `Fn` field while a method runs is a data race.

```go
type MockConfig struct {
    SetDefaultFn          func(key string, value any) error
    SetDefaultsFn         func(values any) error
    SetFn                 func(key string, value any) error
    BindFn                func(dest any) error
    GetFn                 func(key string) any
    FindFn                func(key string) (any, bool)
    ValuesFn              func() map[string]any
    LoadProviderFn        func(p config.Provider) error
    LoadProviderContextFn func(ctx context.Context, p config.Provider) error
    Calls                 []Call
}

type Call struct {
    Method string
    Args   []any
}

func (m *MockConfig) Reset()                     // clears Calls
func (m *MockConfig) CallCount(method string) int
```

Recorded method names match the Go method names (`"Get"`, `"Bind"`,
`"LoadProviderContext"`, ...). `LoadProviderContext` records the provider but
not the context.

```go
mock := &configtest.MockConfig{
    GetFn: func(key string) any {
        if key == "database.host" {
            return "localhost"
        }
        return nil
    },
}
svc := NewService(mock)
_ = svc
_ = mock.CallCount("Get") // assert on usage
```

### Seeded mock

```go
func NewMockConfigWithValues(values map[string]any) (*MockConfig, error)
```

Wires every `Fn` field to a real `*config.Config` holding `values`, so `Get`,
`Find`, `Bind` and the rest behave as in an app while calls are recorded. No
provider loads, so the OS environment never leaks in. Nil `values` gives an
empty configuration. Any `Fn` field can be replaced afterwards.

```go
mock, err := configtest.NewMockConfigWithValues(map[string]any{
    "database": map[string]any{
        "host": "localhost",
        "port": 5432,
    },
})
```

Seed nested maps, not dotted keys: values go through `SetDefaults`, which
does not split `.` and fails on keys that collide once lowercased.

## Errors

```text
var (
    ErrProviderLoadFailed          // a provider failed, or its keys conflict
    ErrExtensionPreLoadHookFailed
    ErrExtensionPostLoadHookFailed
    ErrEnvironmentNotAllowed       // wrapped with one of the two above
    ErrNilValues                   // SetDefaults(nil)
    ErrInvalidKey                  // Set/SetDefault with empty key or level
    ErrKeyPathBlocked              // Set under a non-map level
    ErrKeyConflict                 // two keys collide once trimmed+lowercased
    // provider sentinels: ErrJSON*, ErrDotEnv*, ErrSetEnv (see above)
)
```

Test with `errors.Is`. A provider's own error is wrapped together with
`ErrProviderLoadFailed`. Errors name the provider, file, extension, key or
variable, never a configuration value, so secrets do not leak into logs.
Sample messages:

```
failed to load from provider Environment Variables: conflicting variable names: "A" and "A__B"
failed to execute extension pre-load hook one: boom
bind config: server.port: cannot convert string to int: invalid syntax
validate config: Key: 'AppConfig.Database.Host' Error:Field validation for 'Host' failed on the 'required' tag
```

## Complete Example

Multiple providers, the environment extension, and DI wiring into a Gas app:

```go
package main

import (
    "fmt"
    "log"

    "github.com/gasmod/gas"
    "github.com/gasmod/gas/config"
)

type AppConfig struct {
    Env      config.Environment `json:"gasenv"`
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
    // 1. The environment extension (reads GAS_ENV).
    envExt := config.NewExtension()

    // 2. Providers in order; later ones override earlier ones.
    cfg := config.New(
        config.WithProvider(config.NewJSONProvider(
            config.WithJSONFilePath("config.json"), // base config
        )),
        config.WithProvider(config.NewDotEnvProvider(
            config.WithDotEnvFileNotFoundPanic(false), // optional .env
        )),
        config.WithProvider(config.NewEnvProvider(
            config.WithEnvPrefix("APP_"), // only APP_* vars, prefix stripped
        )),
        config.WithExtension(envExt),
    )

    // 3. Defaults before loading (providers load over them).
    if err := cfg.SetDefaults(map[string]any{
        "server": map[string]any{"host": "0.0.0.0", "port": 8080},
    }); err != nil {
        log.Fatal(err)
    }

    // 4. Load from all providers.
    if err := cfg.Load(); err != nil {
        log.Fatal(err)
    }

    // 5. Bind to a typed struct; validation always runs.
    var appCfg AppConfig
    if err := cfg.Bind(&appCfg); err != nil {
        log.Fatal(err)
    }

    // 6. Environment checks.
    if envExt.IsDevelopmentLike() {
        fmt.Println("running in", envExt.Current())
    }

    // 7. Register as the gas.ConfigProvider singleton.
    app := gas.NewApp(
        gas.WithServiceInstance[gas.ConfigProvider](cfg),
    )

    if err := app.Run(); err != nil {
        log.Fatal(err)
    }
}
```

### Consuming via gas.ConfigProvider

Other services receive config through the core interface without importing
gas/config:

```go
type Service struct {
    cfg gas.ConfigProvider // the instance registered above
}

func New(cfg gas.ConfigProvider) *Service {
    return &Service{cfg: cfg}
}

func (s *Service) Init(ctx context.Context) error {
    var myCfg MyConfig
    return s.cfg.Bind(&myCfg)
}
```

### Auto-binding in other Gas packages

Packages such as `gas/database` bind their own config from the injected
`gas.ConfigProvider` when not given an explicit `WithConfig`, so one `.env`
or JSON file can drive every service. Check each module's docs for its keys;
`gas/database`, for example, fails `Init` with `ErrNoConfigProvider` when it
has neither.

## Choosing a Provider

| Use case | Provider | Notes |
|----------|----------|-------|
| 12-factor apps, containers | `EnvProvider` | Zero files, works everywhere |
| Structured config with nesting | `JSONProvider` | Best for complex hierarchies |
| Local dev, secrets not in git | `DotEnvProvider` | Strict KEY=VALUE format |
| Secrets in AWS | `secretsmanager.Provider` | Re-fetched on every load |
| All of the above | Stack them | Later providers win on conflicts |

A common pattern is `JSONProvider` (structured base) + `DotEnvProvider`
(local overrides) + `EnvProvider` (deployment overrides, highest). Remember:
when passing any provider explicitly, include `EnvProvider` yourself if you
want OS env vars.
