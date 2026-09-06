# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

### Changed

- **`Config.LoadWithContext` now merges atomically.** Every provider is loaded
  before anything is merged, so a failing provider no longer leaves the values
  of the providers ahead of it applied, and a concurrent reader no longer
  observes a state where an earlier provider has landed but a later one that
  overrides it has not. Provider I/O also no longer runs while the config lock
  is held.

## [0.3.0] - 2026-07-02

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

[Unreleased]: https://github.com/gasmod/gas/config/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/gasmod/gas/config/releases/tag/v0.3.0
