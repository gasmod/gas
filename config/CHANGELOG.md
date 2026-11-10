# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.6.1] - 2026-11-10

### Changed

- **BREAKING: `NewExtension` is `NewEnvExtension`.** The constructor of
  `EnvExtension` is named for the extension it returns, beside
  `NewEnvProvider`.
- **`Environment` is `gas.Environment` again.** `Environment` and the
  `Env*` constants are aliases of core's, so a `config.Environment` passes
  for a `gas.Environment` without a conversion, as in
  `gas.WithAllowedEnvironments(config.EnvProduction)`. For this, config
  requires core again.
- Dependencies updated, and the internal modules required at v0.6.1.

### Fixed

- `WithValidator` given a nil pointer, such as a nil `*validator.Validate`,
  keeps the default validator, as a nil `Validator` does. `Bind` panicked.

## [0.6.0] - 2026-11-08

v0.6.0 rewrites the module around the contract in its package
documentation (`doc.go`), which lists every error verbatim. Read it before
upgrading: most of the API moved or changed.

### Added

- **`EnvExtension`, `Environment` and the `Env*` constants in `config`.**
  The environment extension and its type live in the root package;
  `Environment` is the module's own string type with `IsDevelopment`,
  `IsProductionLike` and the other helpers, and `EnvExtension` is safe for
  concurrent use.
- **`ConfigProvider`**, the interface services read configuration through,
  which `*Config` implements.
- **`Validator`**, the interface `Bind` validates with. `WithValidator`
  takes any value with `StructCtx(ctx, any) error`, so a
  `*validator.Validate` still fits; a nil one keeps the default.
- **`WithoutEnvPrefix`**, which lets an `EnvProvider` load every variable
  it keeps, without a prefix.
- **New sentinel errors:** `ErrEnvPrefixRequired`,
  `ErrEnvironmentNotAllowed`, `ErrInvalidKey`, `ErrKeyPathBlocked` and
  `ErrKeyConflict`; and `secretsmanager.ErrInvalidKey`.
- **`.env` references.** `${NAME}` in an unquoted or double-quoted value is
  the value of `NAME` defined in the same file; a single-quoted value is
  not expanded. A reference to a name the file does not define, or a
  cycle, is an error.
- **Array fields.** `Bind` fills an array from a list no longer than the
  array; a longer list is an error.

### Changed

- **BREAKING: one package.** `providers` and `extensions/gasenv` are merged
  into `config`: `providers.NewEnvProvider` is `config.NewEnvProvider`,
  `providers.Provider` is `config.Provider`, `gasenv.NewExtension` is
  `config.NewExtension`, `gasenv.Extension` is `config.EnvExtension`,
  `gasenv.Development` and the rest are `config.EnvDevelopment` and the
  rest, and so on. The environment provider's option type is
  `EnvProviderOption`; `EnvOption` now configures the `EnvExtension`.
  `providers/secretsmanager` moves to `config/secretsmanager`.
- **BREAKING: config no longer depends on core.** `BindOption`,
  `BindOptions`, `WithValidate`, `gasenv.WithGasEnv` and the aliases of
  core types are removed. `Bind` takes only the destination and always
  validates; to skip validation, pass `WithValidator` a `Validator` that
  returns nil. An app reads the environment by binding a field of type
  `Environment` (or `gas.Environment`) from the key `gasenv`.
- **BREAKING: an `EnvProvider` needs a prefix.** Without `WithEnvPrefix`
  or `WithoutEnvPrefix`, `Load` fails with `ErrEnvPrefixRequired`, wrapped
  with `ErrProviderLoadFailed`, and reads nothing, so the process
  environment, other tools' tokens included, no longer becomes
  configuration by default. `New` without a provider still adds an
  `EnvProvider` with no prefix, which therefore fails to load: give `New`
  the providers to load from.
- **BREAKING: `Set` and `SetDefault` return an error.** A key that is
  empty or has an empty level, such as `a..b`, is `ErrInvalidKey`; a level
  on the path that holds a value that is not a map makes `Set` fail with
  `ErrKeyPathBlocked` instead of doing nothing; a map value with two keys
  that differ only in case is `ErrKeyConflict`. Both store a copy of the
  value with its map keys trimmed and lowercased, so every key in it can
  be reached; they stored it as given.
- **BREAKING: conflicting keys are errors.** Two keys of one provider, one
  secret, or one value given to `Set`, `SetDefault` or `SetDefaults` that
  become the same once trimmed and lowercased, such as `Host` and `host`,
  fail with `ErrKeyConflict`. Two environment variable names that become
  the same key, such as `Foo` and `FOO`, or where one needs the other's
  key as a map, such as `A` and `A__B`, fail the load. A variable name
  with an empty level, such as `A____B`, is left out.
- **BREAKING: `Bind` matches fields as `encoding/json` names them.** A key
  matches a field with exactly its name (the `json` tag, or else the Go
  name), or else without regard to case, at every depth. Two keys that
  match one field, such as `Host` and `host`, are an error; the tag key
  used to win. Two kinds of field that used to bind now keep their value
  without an error, as a key without a field is ignored:
  - A field tagged `json:"-"` binds from no key. The field
    ``Password string `json:"-"` `` was set from `password`; drop the
    tag, or tag it with its key, such as `json:"password"`.
  - A tagged field binds only from its tag. The field
    ``ReadTimeout time.Duration `json:"read_timeout"` `` was also set from
    `readtimeout`, its Go name; rename the key to `read_timeout`, or drop
    the tag.
- **BREAKING: a duration needs a unit.** `Bind` parses every string for a
  `time.Duration` field with `time.ParseDuration`. A string without a unit
  was read as nanoseconds, so `SERVER__READTIMEOUT=30` set 30ns; it is now
  an error that names the key and shows the fix:
  `server.readtimeout: duration has no unit; add one such as "s" or "ms"`.
  A number for a duration field, a JSON number or an int, uint or float
  set with `Set` or `SetDefault`, is the same error unless it is 0, which
  binds as zero. `"0"` and a `time.Duration` value bind as before.
- **BREAKING: JSON numbers keep their digits.** The JSON provider and the
  Secrets Manager provider decode an integer to an `int64`, or a `uint64`
  when it is too large for one, and any other number to a `float64`; every
  number was a `float64`, so an integer past 2^53 lost digits. A number
  too large for a `float64`, such as `1e400`, is an error. A float bound
  to a string field is written without an exponent, so 1234567.0 is
  `1234567`.
- **BREAKING: a float binds to an integer field only when it is whole.**
  `Bind` dropped the fractional part, so `8080.7`, from a JSON file or
  `Set`, set an `int` or `uint` field to 8080. It is now an error that
  names the key, such as
  `bind config: port: cannot convert float64 to int: not a whole number`,
  and so are NaN and an infinity. A whole float such as `8080.0` binds as
  before. Write the value as an integer, or bind it to a float field and
  convert it in code.
- **BREAKING: an environment that is not allowed fails the load.** A
  `GAS_ENV` value, or a value a provider sets at the extension's key, that
  is not one of the allowed environments fails `Load` with an error
  wrapping `ErrEnvironmentNotAllowed`, which names the variable or key and
  the allowed values but not the value. It used to be replaced with the
  fallback without a word, so `prod` ran as development. The fallback is
  always allowed. Core's `App.Start` now checks the environment as well,
  against `gas.WithAllowedEnvironments`, which defaults to the four
  standard environments: an environment added with `WithAllowedEnvs` or
  `WithDefault`, such as `local`, loads but fails `Start` with
  `gas: invalid config: gasenv must be one of development, testing,
  staging, production`. Give core the same list, as in
  `gas.WithAllowedEnvironments("local", gas.EnvProduction)`.
- **BREAKING: the `.env` format is strict.** A name is
  `[A-Za-z_][A-Za-z0-9_]*`, defined once and not differing from another
  only in case; there is no `export`; nothing is escaped; an unquoted
  value ends at a space, tab or `#`; the file must be valid UTF-8.
  Anything outside the format fails with `ErrDotEnvParseFailed`, giving a
  line and column and the name, never a character of a value.
- **BREAKING: symlinks in file paths.** The default file system opens the
  JSON and `.env` files through an `os.Root` on the working directory. A
  symlink, in the directory part or as the last element, is followed when
  its target is relative and stays inside the working directory; an
  absolute target is refused even when it lies inside. The file must be a
  regular file: a directory, FIFO, socket or device is refused.
- **Loads are serialized.** `Load`, `LoadWithContext`, `LoadProvider` and
  `LoadProviderContext` load and merge their providers one at a time, so a
  provider added during a load is merged after the providers it follows.
- **`secretsmanager.Provider.LoadContext` applies the timeout.** It
  derives its context from the caller's and ends at the caller's deadline
  or the provider's timeout, whichever comes first; it used to use the
  caller's context as is.
- **`configtest.MockConfig`** follows the new signatures: `SetDefaultFn`
  and `SetFn` return an error, `BindFn` takes only the destination, and
  the provider fields take a `config.Provider`.

### Removed

- **BREAKING:** the `providers` and `extensions/gasenv` packages,
  `BindOption`, `BindOptions`, `WithValidate`, `WithGasEnv` and the
  `examples` directory.

### Security

- **Printing a `*Config` shows counts only.** With fmt, any verb, and
  log/slog, a `*Config` prints how many providers, extensions and
  top-level keys it has, such as
  `config.Config{providers: 1, extensions: 0, keys: 3}`. It printed every
  value, by default the whole process environment.
- **A `secretsmanager.Provider` never prints its secrets.** It prints its
  region, secret count, timeout, and whether it has an endpoint and static
  credentials, never the credentials, the endpoint or the secrets' names.
- **Errors carry no configuration values.** `.env` syntax errors no longer
  quote a character of the file, and `Bind` and decode errors name the
  key, type or position instead of the value.
- **Requires Go 1.27.3.** The `go` directive moves from 1.27 to 1.27.3,
  the first 1.27 release with the fix for GO-2026-6809 (CVE-2026-111943):
  `crypto/x509` now rejects intermediate certificates known to be
  compromised. A build with an older toolchain switches to 1.27.3 under
  `GOTOOLCHAIN=auto` and fails under `GOTOOLCHAIN=local`.

## [0.5.6] - 2026-10-19

The tags `config/v0.4.1` to `config/v0.5.6` shipped their changes under
`[Unreleased]`. This section holds them, and covers every change since
0.3.0, which was released from the separate
`github.com/gasmod/gas-config` repository.

### Added

- **AWS SecretsManager provider** — `providers/secretsmanager` loads secrets
  eagerly at `Load()` time. `WithSecret` merges JSON-object secrets at the
  root; `WithSecretAtKey` nests a value at a dot-notation key. Client options
  mirror the rest of the Gas ecosystem (`WithRegion`, `WithStaticCredentials`,
  `WithEndpoint`, `WithClient`, `WithTimeout`).
- **`providers.ContextProvider`** — optional interface (`LoadContext(ctx)`);
  `Config.LoadWithContext` now passes its context to providers that
  implement it.
- **`Config.LoadProvider` / `Config.LoadProviderContext`** — register a
  provider after construction and load it immediately, merging its values over
  what is already loaded. Same override rule as `WithProvider`: the provider
  wins on the keys it defines and leaves the rest alone, and reusing a
  `Name()` is allowed with the later values winning. The provider is
  registered only once its values are in hand, so a failed load leaves nothing
  registered and the call can be retried. Extension `PreLoad`/`PostLoad` hooks
  are not run — call `Load()` afterwards if an extension such as `gasenv`
  needs to observe the new values.
- **`configtest.MockConfig` implements `LoadProvider` and
  `LoadProviderContext`**, recording each call and delegating to the new
  `LoadProviderFn` and `LoadProviderContextFn` fields when set; unset, they
  return nil (config/v0.5.2).

### Changed

- **BREAKING: import path.** The module moved from
  `github.com/gasmod/gas-config` to `github.com/gasmod/gas/config`, in
  config/v0.4.1.
- **BREAKING: Go 1.27 is required.** The `go` directive moves from 1.26.1
  to 1.27, in config/v0.5.0, as core now uses generic methods.
- **BREAKING: config depends on core, and core no longer depends on
  config.** `BindOption` and `BindOptions` are aliases of `gas.BindOption` and
  `gas.BindOptions`; `BindOptions`' unexported `validate` field is now
  `gas.BindOptions.Validate`, and `WithValidate` sets it. In `gasenv`,
  `Environment` and `WithGasEnv` are aliases of `gas.Environment` and
  `gas.WithGasEnv`, and `Development`, `Testing`, `Staging` and `Production`
  equal `gas.EnvDevelopment` and the rest, so existing code compiles
  unchanged; new code should embed `gas.WithGasEnv`. `Config.LoadProvider`
  and `Config.LoadProviderContext` remain on `*Config` but are no longer part
  of `gas.ConfigProvider` (gas#105).
- **`Config.LoadWithContext` now merges atomically.** Every provider is loaded
  before anything is merged, so a failing provider no longer leaves the values
  of the providers ahead of it applied, and a concurrent reader no longer
  observes a state where an earlier provider has landed but a later one that
  overrides it has not. Provider I/O also no longer runs while the config lock
  is held.
- **`DotEnvProvider.Load` fails on an unclosed quote.** A quoted value whose
  quote is never closed now returns an error wrapping `ErrDotEnvParseFailed`
  that names the key and the line it starts on. It used to absorb every
  following line and drop the value without an error, so a typo silently
  removed the configuration below it. A `.env` file with this mistake that
  loaded before now fails to load.

### Fixed

- **`secretsmanager.WithTimeout` with zero or a negative duration means no
  timeout.** `Load` passed the value to `context.WithTimeout` as is, so a
  zero or negative timeout gave an already-expired context and every `Load`
  failed with `context.DeadlineExceeded`. `Load` now adds no deadline when the
  timeout is not positive. `LoadContext` is unchanged.
- **`.env` values with non-ASCII text are no longer corrupted.** The
  inline-comment scan re-encoded every byte above 0x7F as a separate
  character, so `café` loaded as `cafÃ©` and any non-ASCII password or
  secret was silently changed. Found by the new fuzz target.
- **Config files load when the working directory is `/`.** The default file
  system checks that a file lies inside the working directory by matching
  the directory plus a separator as a prefix, which for `/` became `//`, so
  `JSONProvider` and `DotEnvProvider` refused every file with
  `unsafe file path: outside allowed directory`. Running from `/` is common
  in containers. A root working directory is now matched as is.

### Security

- **The default file system no longer follows a symlinked directory out of
  the working directory.** `JSONProvider` and `DotEnvProvider` checked the
  path only as written, so `link/secret.json`, with `link` a symlink to a
  directory outside the working directory, was read. The check now runs
  again after resolving symlinked directories, and such a path fails with
  `ErrJSONFileReadFailed` or `ErrDotEnvFileReadFailed`. A symlinked
  directory that stays inside the working directory still works.

## 0.3.0 - 2026-07-02

First open source release. Versions prior to 0.3.0 were developed in a private
repository; this entry summarizes the package as published.

### Added

- **Early initialization** — `config.New()` returns `*Config` directly so
  configuration can be created and loaded before the rest of the app starts.
- **Providers** — `EnvProvider` (auto-registered by default), `JSONProvider`
  (with optional `fs.FS` support for embedded files), `DotEnvProvider`, and a
  `Provider` interface for custom sources. Later providers override earlier
  ones.
- **Hierarchical access** — dot-notation `Get`/`Find`/`Set`/`SetDefault` over
  nested configuration values, with `__`-separated env/`.env` keys mapping to
  nested maps.
- **Struct binding** via `Bind()`, matching `json` tags then case-insensitive
  field names, with support for all int/uint/float variants, bool, string,
  `time.Duration`, slices (including comma-separated strings), maps, and
  nested/embedded structs.
- **Validation** — `Bind()` integrates `go-playground/validator`, with
  `WithValidator` to supply a caller-owned instance (e.g. with custom tags
  registered) and `WithValidate(false)` to opt out per call.
- **Extensions** — `Extension` interface with `PreLoad`/`PostLoad` hooks, and
  a bundled `gasenv` extension for application environment detection
  (`Development`, `Testing`, `Staging`, `Production`) resolved from config
  providers, the `GAS_ENV` OS variable, or a configurable default.
- **`gas.ConfigProvider` implementation** for direct use in Gas apps via
  `gas.WithServiceInstance`.
- **`configtest` package** with `MockConfig` (structurally satisfies
  `gas.ConfigProvider`) and `NewMockConfigWithValues` for tests that need real
  `Get`/`Find`/`Bind` semantics seeded with known values.

### Fixed

- `DotEnvProvider` no longer mirrors `.env` entries into the live process
  environment by default, and when mirroring is explicitly enabled it skips
  security-relevant variables (`PATH`, `LD_PRELOAD`, `LD_LIBRARY_PATH`,
  `DYLD_INSERT_LIBRARIES`, `DYLD_LIBRARY_PATH`, and others), preventing a
  `.env` file from leaking secrets into child processes or overwriting
  dynamic-linker variables.

[Unreleased]: https://github.com/gasmod/gas/compare/config/v0.6.1...HEAD
[0.6.1]: https://github.com/gasmod/gas/releases/tag/config/v0.6.1
[0.6.0]: https://github.com/gasmod/gas/releases/tag/config/v0.6.0
[0.5.6]: https://github.com/gasmod/gas/releases/tag/config/v0.5.6
