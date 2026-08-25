# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] - 2026-07-03

First open source release. Versions prior to 0.3.0 were developed in a private
repository; this entry summarizes the package as published.

### Added

- **`gas.CacheProvider` implementations** — `memory.Service` and
  `valkey.Service` both implement `gas.Service` and `gas.CacheProvider`
  (`Get`, `Set`, `Delete`, `Exists`), so consumers can depend on the
  provider interface and swap backends by changing only the DI
  registration.
- **In-memory backend** (`gas/cache/memory`) — a map-based cache for
  development, testing, and single-instance deployments. `Set` honors a
  per-call TTL or falls back to the configured `DefaultTTL` (0 = never
  expires); `Get` lazily deletes expired keys.
- **Max entries enforcement** — when `Settings.MaxEntries > 0` and the
  in-memory cache is full, `Set` for a new key first tries to evict one
  expired entry and rejects the write with an error only if none is
  available. Overwriting an existing key always succeeds.
- **Background cleanup** — the in-memory backend runs a goroutine on
  `Settings.CleanupInterval` (default 1m) that sweeps expired entries;
  set to 0 to disable it.
- **Valkey backend** (`gas/cache/valkey`) — a Redis-compatible backend
  for production and multi-instance deployments, built on
  `valkey-io/valkey-go`. TTLs are set with millisecond precision via
  `PX`; a TTL of 0 stores the key without expiration.
- **Connection retry with backoff** — `Settings.ConnRetries` controls how
  many times the Valkey backend retries its initial connection on
  `Init`, starting at `Settings.ConnRetryInterval` (default 2s) and
  doubling after each failed attempt.
- **Health and readiness** — the Valkey `Service` implements
  `gas.HealthReporter` (`CheckHealth`, liveness only — the valkey-go
  client reconnects internally so transient network errors don't fail
  it) and `gas.ReadyReporter` (`CheckReady`, issues a `PING` against the
  server using the caller's context). The in-memory backend implements
  neither, since it has no external dependency to probe.
- **Direct client access** — `valkey.Service.Client()` exposes the
  underlying `valkey.Client` for operations beyond `CacheProvider`
  (pub/sub, Lua scripts, pipelines).
- **Sentinel errors** — `cache.ErrKeyNotFound` (returned by `Get` for a
  missing or expired key) and `cache.ErrClosed` (returned by any
  operation on a closed service).
- **DI-friendly constructors** — both backends' `New(opts ...Option)`
  return a `func(gas.ConfigProvider, gas.Logger) *Service` for use with
  `gas.WithSingletonService`; `WithConfig` supplies configuration
  explicitly and skips binding from the DI container.
- **Configuration** — `memory.Config`/`memory.Settings` (`MaxEntries`,
  `CleanupInterval`, `DefaultTTL`) and `valkey.Config`/`valkey.Settings`
  (`Addr`, `Password`, `DB`, `DialTimeout`, `WriteTimeout`,
  `ConnRetries`, `ConnRetryInterval`), each with `DefaultConfig()` and
  `Validate()` (the Valkey config rejects an empty `Addr`).
- **`cachetest.MockCache`** — a configurable, thread-safe mock of
  `gas.CacheProvider` (plus `HealthReporter`/`ReadyReporter`) for unit
  tests, with per-method `Fn` overrides, recorded `Calls`, `Reset()`,
  and `CallCount()`.

[Unreleased]: https://github.com/gasmod/gas/cache/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/gasmod/gas/cache/releases/tag/v0.3.0
