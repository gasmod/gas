---
name: gas-cache
description: >
  Reference documentation for the gas/cache Go package
  (github.com/gasmod/gas/cache) — the caching service for the Gas ecosystem.
  Use this skill when writing, reviewing, or debugging Go code that uses
  gas/cache for key-value caching with in-memory or Valkey (Redis-compatible)
  backends. Covers the memory and valkey sub-packages, cachetest mock,
  gas.CacheProvider implementation, sentinel errors, DI wiring, configuration
  binding, background cleanup, TTL handling, max entries enforcement, connection
  retry with exponential backoff, and direct Valkey client access. Make sure to
  use this skill whenever working with caching in the Gas ecosystem, even if the
  user doesn't explicitly mention gas/cache — any code that imports
  gasmod/gas/cache or references gas.CacheProvider should trigger this skill.
---

# Gas Cache Package Reference

Cache service for the Gas ecosystem. Provides two `gas.CacheProvider`
implementations — an in-memory backend and a Valkey (Redis-compatible) backend.

```
import cache "github.com/gasmod/gas/cache"
import cachemem "github.com/gasmod/gas/cache/memory"
import cachevk "github.com/gasmod/gas/cache/valkey"
import "github.com/gasmod/gas/cache/cachetest"
```

## Backends

| Backend   | Package            | Service name       | Use case                                          |
|-----------|--------------------|--------------------|---------------------------------------------------|
| In-memory | `gas/cache/memory` | `gas/cache/memory` | Development, testing, single-instance deployments |
| Valkey    | `gas/cache/valkey` | `gas/cache/valkey` | Production, multi-instance deployments            |

Both implement `gas.Service` and `gas.CacheProvider`. The Valkey backend additionally
implements `gas.HealthReporter` and `gas.ReadyReporter` (see [Health and Readiness](#health-and-readiness)).

## CacheProvider Interface

Defined in the gas core package:

```go
type CacheProvider interface {
    Get(ctx context.Context, key string) ([]byte, error)
    Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
    Delete(ctx context.Context, key string) error
    Exists(ctx context.Context, key string) (bool, error)
}
```

## Sentinel Errors

The root `cache` package defines sentinel errors used by both backends:

```go
cache.ErrKeyNotFound // Get returns this when the key does not exist or has expired
cache.ErrClosed      // returned when an operation is attempted on a service that is not started or is closed
```

## In-Memory Backend

### Constructor

```go
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Service, error)
```

`New` captures options and returns a DI-injectable constructor. The returned
func receives `gas.ConfigProvider` and `gas.Logger` from the DI container.

### Options

| Option                    | Description                                                 |
|---------------------------|-------------------------------------------------------------|
| `WithConfig(cfg *Config)` | Set configuration explicitly (skips config binding from DI) |

### Lifecycle (gas.Service)

| Method  | Signature   | Description                                           |
|---------|-------------|-------------------------------------------------------|
| `Name`  | `() string` | Returns `"gas/cache/memory"`                          |
| `Init`  | `(context.Context) error` | Binds config (unless `WithConfig`), creates an empty cache, starts background cleanup goroutine (not tied to the Init ctx) |
| `Close` | `(context.Context) error` | Stops cleanup, clears cache |

### Behavior

- **TTL:** `Set` with `ttl == 0` uses the configured `DefaultTTL`. If
  `DefaultTTL` is also 0, the entry never expires. A negative TTL, given or
  from `DefaultTTL`, also stores the entry without expiry.
- **Max entries:** When `MaxEntries > 0` and the limit is reached, `Set` for
  a new key attempts to evict one expired entry first (in map iteration
  order, not by age). If none are expired, the set is rejected with an error. Overwriting an existing key always
  succeeds.
- **Background cleanup:** When `CleanupInterval > 0`, a goroutine
  periodically removes all expired entries.
- **Lazy deletion:** `Get` on an expired key deletes it before returning
  `ErrKeyNotFound`.
- **Not open:** `Get`, `Set`, `Delete`, and `Exists` return `ErrClosed`
  before `Init` and after `Close`, including when `Close` races the call.

### Config

```go
type Config struct {
    gas.WithGasEnv
    Cache Settings
}

type Settings struct {
    MaxEntries      int           // 0 = unlimited
    CleanupInterval time.Duration // default 1m; 0 disables background cleanup
    DefaultTTL      time.Duration // default 0 (never expires)
}

func DefaultConfig() *Config
```

## Valkey Backend

### Constructor

```go
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Service, error)
```

### Options

| Option                    | Description                                                 |
|---------------------------|-------------------------------------------------------------|
| `WithConfig(cfg *Config)` | Set configuration explicitly (skips config binding from DI) |

### Lifecycle (gas.Service)

| Method  | Signature   | Description                                         |
|---------|-------------|-----------------------------------------------------|
| `Name`  | `() string` | Returns `"gas/cache/valkey"`                        |
| `Init`  | `(context.Context) error` | Binds config (unless `WithConfig`), connects with retry, pings server |
| `Close` | `(context.Context) error` | Closes the Valkey client |

### Behavior

- **TTL:** `Set` with `ttl > 0` uses Valkey's `PX` (millisecond precision).
  `ttl <= 0` stores without expiration; there is no `DefaultTTL`.
- **Connection retry:** When `ConnRetries > 0`, the service retries the
  initial connection with exponential backoff. The interval starts at
  `ConnRetryInterval` (default 2s) and doubles after each failed attempt.
  The ping uses the Init context, bounded by `DialTimeout` per attempt, and
  retries stop when that context is canceled or its deadline passes (the
  error wraps `ctx.Err()`).
- **Not open:** `Get`, `Set`, `Delete`, `Exists`, `CheckHealth`, and
  `CheckReady` return `ErrClosed` before `Init` and after `Close`. A command
  that fails because `Close` or a restart happened while it ran also
  returns `ErrClosed`.

### Health and Readiness

The Valkey `*Service` implements `gas.HealthReporter` and `gas.ReadyReporter`:

| Method        | Signature                        | Behavior                                                                 |
|---------------|----------------------------------|--------------------------------------------------------------------------|
| `CheckHealth` | `(ctx context.Context) error`   | Returns `cache.ErrClosed` before `Init` or when closed, `nil` otherwise. Does not probe the server; the valkey-go client reconnects internally, so transient network errors are not liveness failures. |
| `CheckReady`  | `(ctx context.Context) error`   | Returns `cache.ErrClosed` before `Init` or when closed, otherwise issues `PING` against the server using the caller's context and returns its error. |

The in-memory backend does **not** implement these interfaces: with no external
dependency and no warmup, liveness and readiness are fully covered by the gas
service lifecycle.

### Direct Client Access

```go
func (s *Service) Client() valkey.Client
```

`Client` returns nil before `Init` first connects, and the closed client
after `Close`.

For operations beyond `CacheProvider` (pub/sub, Lua, pipelines), define a
local interface and type-assert:

```go
type ValkeyProvider interface {
    Client() valkey.Client
}
```

### Config

```go
type Config struct {
    gas.WithGasEnv
    Cache Settings
}

type Settings struct {
    Addr              string        `validate:"required"` // default "localhost:6379"
    Password          string        // empty = no auth
    DB                int           `validate:"min=0"`    // 0-15, default 0
    DialTimeout       time.Duration // default 5s
    WriteTimeout      time.Duration // default 3s
    ConnRetries       int           // default 0 (no retries)
    ConnRetryInterval time.Duration // default 2s; doubles each attempt
}

func DefaultConfig() *Config
```

The rules are `validate` tags, which `ConfigProvider.Bind` checks when `Init`
binds the config over `DefaultConfig()`. A config passed with `WithConfig` is
used as given, with no defaults filled in, so build it from `DefaultConfig()`:
a zero `DialTimeout` gives every connection ping a deadline that has already
passed. Neither
cache config has a `Validate` method.

## Test Mock

The `cachetest` package provides `MockCache`, a configurable mock of
`gas.CacheProvider` for use in unit tests.

```go
import "github.com/gasmod/gas/cache/cachetest"
```

### MockCache

```go
type MockCache struct {
    GetFn         func(ctx context.Context, key string) ([]byte, error)
    SetFn         func(ctx context.Context, key string, value []byte, ttl time.Duration) error
    DeleteFn      func(ctx context.Context, key string) error
    ExistsFn      func(ctx context.Context, key string) (bool, error)
    CheckHealthFn func(ctx context.Context) error
    CheckReadyFn  func(ctx context.Context) error
    Calls         []Call
}
```

Implements `gas.CacheProvider`, `gas.HealthReporter`, and `gas.ReadyReporter`.
Each method delegates to its `Fn` field if set, otherwise returns zero value.
All calls are recorded in `Calls` for assertions. Thread-safe.

| Method                  | Description                               |
|-------------------------|-------------------------------------------|
| `Reset()`               | Clear all recorded calls                  |
| `CallCount(method) int` | Count calls by method name (e.g. `"Get"`) |

## DI Wiring Patterns

Register the backend under `gas.CacheProvider`: the container resolves a
dependency by its exact registered type, so a registration under
`*cachemem.Service` does not satisfy a constructor that takes
`gas.CacheProvider`. Both constructors also need a registered
`gas.ConfigProvider` and `gas.Logger`.

### Memory backend (dev/test)

```go
app := gas.NewApp(
    gas.WithSingletonService[gas.CacheProvider](cachemem.New()),
)
```

### Valkey backend (production)

```go
app := gas.NewApp(
    gas.WithSingletonService[gas.CacheProvider](cachevk.New()),
)
```

### With explicit config

```go
cfg := cachevk.DefaultConfig()
cfg.Cache.Addr = "valkey.internal:6379"
cfg.Cache.Password = "secret"
cfg.Cache.DB = 1
cfg.Cache.ConnRetries = 3

app := gas.NewApp(
    gas.WithSingletonService[gas.CacheProvider](cachevk.New(cachevk.WithConfig(cfg))),
)
```

### Consuming via gas.CacheProvider

Services receive the cache through the provider interface, never importing
gas/cache backends directly:

```go
type Service struct {
    cache gas.CacheProvider
}

func New(cache gas.CacheProvider) *Service {
    return &Service{cache: cache}
}

func (s *Service) Profile(ctx context.Context, id string) ([]byte, error) {
    b, err := s.cache.Get(ctx, "profile:"+id)
    if errors.Is(err, cache.ErrKeyNotFound) {
        return nil, nil // miss: load from the source of truth
    }
    return b, err
}
```

### Swapping backends

Because both backends satisfy `gas.CacheProvider`, switching from memory to
Valkey requires only changing the service registration — no consumer code
changes:

```go
// Development
gas.WithSingletonService[gas.CacheProvider](cachemem.New())

// Production
gas.WithSingletonService[gas.CacheProvider](cachevk.New())
```

### Testing with MockCache

```go
mock := &cachetest.MockCache{}
mock.GetFn = func(ctx context.Context, key string) ([]byte, error) {
    return []byte("hello"), nil
}

// inject mock as gas.CacheProvider in tests
```
