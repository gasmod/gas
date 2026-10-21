# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

The internal modules this module requires are pinned at 0.6.0.

### Fixed

- **`Bind` no longer picks a field's value at random.** When the source
  held both a key matching a field's `json` tag and one matching its
  lowercased Go name, which value the field got depended on map iteration
  order and could change from one run to the next. The tag key now always
  wins, in nested structs too. In a nested struct, where keys also match
  case-insensitively, an exact key beats a case-folded one of the same
  kind, and among keys that still tie the lexicographically smallest wins,
  so the result is the same on every run.

### Security

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

[Unreleased]: https://github.com/gasmod/gas/compare/config/v0.5.6...HEAD
[0.5.6]: https://github.com/gasmod/gas/releases/tag/config/v0.5.6
