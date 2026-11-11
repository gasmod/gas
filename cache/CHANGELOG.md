# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.9.0] - 2026-11-11

### Changed

- The internal modules are required at v0.9.0.

## [0.8.0] - 2026-11-11

### Changed

- **Go 1.27.0 or later is required**, down from 1.27.3, as for the core:
  build apps with Go 1.27.4 or later, for the `net/http` fix of
  [GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859).
- Dependencies updated, and the internal modules required at v0.8.0.

## [0.7.0] - 2026-11-11

### Changed

- The internal modules are required at v0.7.0.

## [0.6.3] - 2026-11-10

### Changed

- The internal modules are required at v0.6.3.

## [0.6.2] - 2026-11-10

### Changed

- The internal modules are required at v0.6.2.

## [0.6.1] - 2026-11-10

### Changed

- Dependencies updated, and the internal modules required at v0.6.1.

### Fixed

- `valkey` works with a Valkey or Redis Cluster again, as in v0.5.6.
  v0.6.0 forced valkey-go's single-server client, so a store whose
  `Addr` is a cluster node talked to that node alone, and a key in a
  slot another node serves failed with `MOVED`, such as
  `gas/cache/valkey: get: MOVED`. valkey-go again detects a cluster
  from `Addr`, a single seed, and sends each command to the node that
  serves its key. A `DB` other than 0 fails `Init` on a valkey-server 8
  cluster, which refuses `SELECT`, and a user that may not run
  `CLUSTER` talks to the node at `Addr` alone. Sentinel is still not
  supported.

## [0.6.0] - 2026-11-08

v0.6.0 rewrites the module around one contract that both stores keep,
written in the `cache` package's `doc.go` and checked by a shared
conformance suite. Each package's `doc.go` is its behavioral contract
and lists its errors verbatim; read it before upgrading.

### Added

- **One store contract.** The `cache` package documents what every
  store promises, and `memory` and `valkey` run the same conformance
  suite: an empty key is `ErrInvalidKey`; values are copied in and out;
  a nil value is stored as empty and read back as an empty, non-nil
  slice; a TTL above zero is rounded up to a whole millisecond; a zero
  TTL takes the store's `DefaultTTL` setting; an expired value is a
  miss for `Get` and `Exists`.
- `cache.CheckKey`, `cache.CheckTTL` and `cache.EffectiveTTL`, the
  checks and the TTL rounding the stores share.
- Sentinel errors `ErrInvalidKey`, `ErrInvalidTTL`, `ErrNotInitialized`,
  `ErrAlreadyInitialized`, `ErrNilLogger`, `ErrNilConfig`,
  `ErrNoConfigProvider` and `ErrInvalidConfig` in `cache`.
- `memory.ServiceName` and `valkey.ServiceName`, which start every error
  the store returns.
- Exported defaults: `memory.DefaultMaxEntries`,
  `DefaultCleanupInterval`, `DefaultTTL` and `MinCleanupInterval`;
  `valkey.DefaultDB`, `DefaultTTL`, `DefaultDialTimeout`,
  `DefaultOpTimeout`, `DefaultConnRetries`, `DefaultConnRetryInterval`
  and `MaxConnRetryInterval`.
- `Service.Config` on both stores returns a copy of the configuration
  the last successful `Init` resolved, defaults included, or the zero
  `Config` before one; `Close` and a failed `Init` keep it. The valkey
  password stays a `Secret` in the copy.
- `memory.Service.Len` returns the number of entries the store holds.
- `valkey` settings: `Username` (ACL user), `KeyPrefix` (prepended to
  every key the store's methods send), `DefaultTTL`, `OpTimeout` (the
  bound of each command, default 3s), `TLS` and `TLSServerName`; the
  `WithTLSConfig` option for a private CA or a client certificate.
- `valkey.ErrConnect` and `valkey.ErrCommand`: a failed connect or
  command carries a fixed reason (`authentication failed`, `timed out`,
  `connection refused`, `unreachable`, `failed`) instead of valkey-go's
  error, and a server reply keeps its text only for codes that never
  echo the command.

### Changed

- **BREAKING: the sentinel texts change.** `cache.ErrKeyNotFound` is
  `key not found` (was `cache: key not found`) and `cache.ErrClosed` is
  `service closed` (was `cache: service is closed`). Every error a store
  returns starts with its service name and the operation, such as
  `gas/cache/memory: get: key not found`, and wraps a sentinel.
- **BREAKING: lifecycle sentinels.** Before the first successful `Init`
  every method returns `cache.ErrNotInitialized` (it was
  `cache.ErrClosed`); `Init` on an initialized store returns
  `cache.ErrAlreadyInitialized` and changes nothing; after `Close`,
  until `Init` succeeds again, `cache.ErrClosed`. `Close` then `Init`
  restarts a store.
- **BREAKING: a negative TTL is `cache.ErrInvalidTTL`** and stores
  nothing, and the empty key is `cache.ErrInvalidKey`, from every method.
- **BREAKING: every method honors its context.** It returns the
  context's error, wrapped, when the context has ended before it starts.
- **BREAKING: the constructors check the logger.** A nil logger fails
  with `cache.ErrNilLogger`. A nil `ConfigProvider` is accepted, and
  `Init` then requires `WithConfig` (`cache.ErrNoConfigProvider`).
- **BREAKING: a configuration given to `WithConfig` is validated.** `Init`
  checks a copy of it against the `validate` tags, as a bound one is, and
  never changes the caller's struct; a nil one is `cache.ErrNilConfig`.
  `Init` resolves the configuration afresh each time it runs. A failure
  wraps `cache.ErrInvalidConfig`.
- **BREAKING: `memory` evicts instead of refusing.** When `MaxEntries` is
  set and a `Set` adds a key to a full store, the store removes the entry
  that expired first, or else the least recently used one. It used to
  evict only an expired entry and else refuse the `Set` with
  `max entries reached`. `Get` and `Set` make an entry the most recently
  used.
- **BREAKING: `memory.Settings.CleanupInterval` is at least 1s**
  (`validate:"min=1s"`); 0 no longer turns the janitor off. `MaxEntries`
  and `DefaultTTL` must not be negative.
- `memory` counts a TTL on the monotonic clock, `Close` drops every entry
  and waits for the janitor to return, and `Init` after `Close` starts
  with an empty store.
- **BREAKING: `valkey.Settings.Addr` has no default** and is required;
  `DefaultConfig` no longer sets `localhost:6379`.
- **BREAKING: `valkey.Settings.WriteTimeout` is replaced by
  `OpTimeout`**, which bounds every command, the connect handshake and
  `PING` included, so a server that stops answering cannot hold a call
  past it, and is also valkey-go's `ConnWriteTimeout`.
- **BREAKING: `valkey.Settings.Password` is a `valkey.Secret`**, an alias
  of `gas.Secret`. Configuration binds into it as into a string, and
  `string(s)` is the value.
- `valkey` validates `DialTimeout` and `OpTimeout` (above 0), `DefaultTTL`
  (0 or more), `ConnRetries` (0 to 10) and, when retries are on,
  `ConnRetryInterval` (100ms to 30s); `TLSServerName` requires `TLS`
  unless `WithTLSConfig` is given. The wait between connect retries
  doubles up to 30s.
- `valkey.Service.Close` cancels an `Init` still connecting or waiting to
  retry, and waits for it; that `Init` fails with `cache.ErrClosed`. A
  command that `Close` interrupts returns `cache.ErrClosed`.
- `valkey.Service.CheckHealth` does no I/O and fails only when the store
  is not initialized or its context has ended; `CheckReady` sends `PING`
  within its context and `OpTimeout`.
- `valkey.Service.Client` returns nil when the store is not initialized;
  after `Close` it returned the closed client. The client is built with
  valkey-go's `DisableCache`, so its connections send no
  `CLIENT TRACKING`, which an ACL user may not be allowed. `DoCache` and
  `DoMultiCache` on it send every command to the server, as `Do` and
  `DoMulti` do, and never answer from a client-side cache, as they did in
  v0.5.6. For client-side caching, build a valkey-go client of your own.
- The logs change: `service initialized` (with `max_entries`,
  `cleanup_interval` and `default_ttl` for `memory`, `db` and `tls` for
  `valkey`) and `service closed`, at info level. The valkey log no
  longer carries the address, and the retry warning carries `attempt`,
  `retries`, `wait` and `reason` instead of `error`, `max_retries` and
  `next_retry_in`.
- **BREAKING: `cachetest.MockCache.Calls` is a method.** The exported
  `Calls` field is gone; `Calls()` returns a copy. A `Get` with no
  `GetFn` returns an error wrapping `cache.ErrKeyNotFound` (it returned
  nil, nil), and `Set` records a copy of its value.

### Removed

- **BREAKING:** `memory.Config` and `valkey.Config` no longer embed
  `gas.WithGasEnv`, which core removes.
- The `claude-skill/gas-cache` directory.

### Fixed

- `valkey.Service.Init`'s context bounds every connect attempt, the dial
  included; an attempt never outlives it (KI-052).
- `memory` no longer races on a restart, and `Close` no longer returns
  before its janitor stops.
- `memory.Service.Get` returns a copy of the value; it returned the slice
  the store kept, which a caller could change.

### Security

- No error the stores make holds a key or a value, and no error or log
  holds the server's address, a user name or the password. The valkey
  errors quoted the key and wrapped valkey-go's errors, which hold the
  address.
- The valkey password no longer prints through fmt, encoding/json, YAML
  encoders or log/slog, which write it as `[redacted]`, and refuses gob
  encoding.
- **Requires Go 1.27.3.** The `go` directive moves from 1.27 to 1.27.3,
  the first 1.27 release with the fix for GO-2026-6809 (CVE-2026-111943):
  `crypto/x509` now rejects intermediate certificates known to be
  compromised. A build with an older toolchain switches to 1.27.3 under
  `GOTOOLCHAIN=auto` and fails under `GOTOOLCHAIN=local`.

## [0.5.6] - 2026-10-19

The tags `cache/v0.4.1` to `cache/v0.5.6` shipped their changes under
`[Unreleased]`. This section holds them, and covers every change since
0.3.0, which was released from the separate
`github.com/gasmod/gas-cache` repository.

### Changed

- **BREAKING: import path.** The module moved from
  `github.com/gasmod/gas-cache` to `github.com/gasmod/gas/cache`, in
  cache/v0.4.1.
- **BREAKING: Go 1.27 is required.** The `go` directive moves from 1.26.1
  to 1.27, in cache/v0.5.0, as core now uses generic methods.
- **BREAKING: `Config.Validate` is removed from both backends; the rules are
  validate tags.** In `valkey`, `Settings.Addr` carries `validate:"required"`
  and `Settings.DB` `validate:"min=0"`. `ConfigProvider.Bind` already
  validates the struct it binds, so `Init` no longer runs a check of its own.
  A config passed with `WithConfig` is used as given, as `gas.Config` is when
  an App has no `ConfigProvider`. The error for an invalid bound config is now
  the validator's, under `gas/cache/valkey: config binding:`, instead of the
  hand-written message.
  `memory.Config.Validate` checked nothing and is removed with its call
  (gas#116).
- **BREAKING: the constructors return an error.** `memory.New` and
  `valkey.New` now return `func(gas.ConfigProvider, gas.Logger) (*Service,
  error)`, the `CONVENTIONS.md` shape. They cannot fail yet, so they return
  nil; the change is the shape. Registering them with
  `gas.WithSingletonService` is unchanged, since the container accepts a
  trailing error. Code that calls the returned function directly must take
  the second result (gas#106).
- `memory.Service` and `valkey.Service` no longer log an error they also
  return: an invalid configuration and valkey's client, ping, and
  out-of-retries failures. The caller that handles the error logs it once.
  An invalid configuration is now returned as
  `<service>: invalid configuration: <cause>`, so it still names the service.
  The per-attempt retry warning stays.
- **BREAKING:** `memory.Service` and `valkey.Service` implement the
  context-aware `gas.Service` (`Init(ctx context.Context) error`,
  `Close(ctx context.Context) error`). In `valkey.Service`, the connection
  ping derives from the Init context, and connection retries stop when that
  context is canceled or its deadline passes; the returned error wraps
  `ctx.Err()`. The memory backend's cleanup goroutine keeps its own lifetime
  and is still stopped by `Close`, not by the Init context.
- `memory.Service` `Get`, `Delete`, and `Exists` return `cache.ErrClosed`
  before the service has started, as `Set` now does. They used to report a
  missing key, `nil`, and `false`.
- `valkey.Service` `CheckHealth` returns `cache.ErrClosed` before the
  service has started; it used to report healthy. A `Get`, `Set`, `Delete`,
  `Exists`, or `CheckReady` that fails because `Close` or a restart happened
  while it ran returns `cache.ErrClosed` instead of the wrapped valkey-go
  error.

### Fixed

- `memory.Service.Set` no longer panics with `assignment to entry in nil
  map` when it runs before the service has started or races `Close`; it
  returns `cache.ErrClosed`. Every operation now checks that the service is
  open under its mutex.
- `valkey.Service` `Get`, `Set`, `Delete`, `Exists`, and `CheckReady` no
  longer panic with a nil pointer dereference before the service has
  started; they return `cache.ErrClosed`. The client is now held in an
  atomic pointer, so restarting the service while operations run is no
  longer a data race.

## 0.3.0 - 2026-07-03

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

[Unreleased]: https://github.com/gasmod/gas/compare/cache/v0.8.0...HEAD
[0.8.0]: https://github.com/gasmod/gas/releases/tag/cache/v0.8.0
[0.7.0]: https://github.com/gasmod/gas/releases/tag/cache/v0.7.0
[0.6.3]: https://github.com/gasmod/gas/releases/tag/cache/v0.6.3
[0.6.2]: https://github.com/gasmod/gas/releases/tag/cache/v0.6.2
[0.6.1]: https://github.com/gasmod/gas/releases/tag/cache/v0.6.1
[0.6.0]: https://github.com/gasmod/gas/releases/tag/cache/v0.6.0
[0.5.6]: https://github.com/gasmod/gas/releases/tag/cache/v0.5.6
