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
cache.ErrClosed      // returned when an operation is attempted on a closed service
```

## In-Memory Backend

### Constructor

```go
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) *Service
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
| `Init`  | `() error`  | Validates config, starts background cleanup goroutine |
| `Close` | `() error`  | Stops cleanup, clears cache                           |

### Behavior

- **TTL:** `Set` with `ttl == 0` uses the configured `DefaultTTL`. If
  `DefaultTTL` is also 0, the entry never expires.
- **Max entries:** When `MaxEntries > 0` and the limit is reached, `Set` for
  a new key attempts to evict one expired entry first. If none are expired,
  the set is rejected with an error. Overwriting an existing key always
  succeeds.
- **Background cleanup:** When `CleanupInterval > 0`, a goroutine
  periodically removes all expired entries.
- **Lazy deletion:** `Get` on an expired key deletes it before returning
  `ErrKeyNotFound`.

### Config

```go
type Config struct {
    env.WithGasEnv
    Cache Settings
}

type Settings struct {
    MaxEntries      int           // 0 = unlimited
    CleanupInterval time.Duration // default 1m; 0 disables background cleanup
    DefaultTTL      time.Duration // default 0 (never expires)
}

func DefaultConfig() *Config
func (c *Config) Validate() error
```

## Valkey Backend

### Constructor

```go
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) *Service
```

### Options

| Option                    | Description                                                 |
|---------------------------|-------------------------------------------------------------|
| `WithConfig(cfg *Config)` | Set configuration explicitly (skips config binding from DI) |

### Lifecycle (gas.Service)

| Method  | Signature   | Description                                         |
|---------|-------------|-----------------------------------------------------|
| `Name`  | `() string` | Returns `"gas/cache/valkey"`                        |
| `Init`  | `() error`  | Validates config, connects with retry, pings server |
| `Close` | `() error`  | Closes the Valkey client                            |

### Behavior

- **TTL:** `Set` with `ttl > 0` uses Valkey's `PX` (millisecond precision).
  `ttl == 0` stores without expiration.
- **Connection retry:** When `ConnRetries > 0`, the service retries the
  initial connection with exponential backoff. The interval starts at
  `ConnRetryInterval` (default 2s) and doubles after each failed attempt.

### Health and Readiness

The Valkey `*Service` implements `gas.HealthReporter` and `gas.ReadyReporter`:

| Method        | Signature                        | Behavior                                                                 |
|---------------|----------------------------------|--------------------------------------------------------------------------|
| `CheckHealth` | `(ctx context.Context) error`   | Returns `cache.ErrClosed` when closed, `nil` otherwise. Does not probe the server — the valkey-go client reconnects internally, so transient network errors are not liveness failures. |
| `CheckReady`  | `(ctx context.Context) error`   | Returns `cache.ErrClosed` when closed, otherwise issues `PING` against the server using the caller's context and returns its error. |

The in-memory backend does **not** implement these interfaces: with no external
dependency and no warmup, liveness and readiness are fully covered by the gas
service lifecycle.

### Direct Client Access

```go
func (s *Service) Client() valkey.Client
```

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
    env.WithGasEnv
    Cache Settings
}

type Settings struct {
    Addr              string        // default "localhost:6379"
    Password          string        // empty = no auth
    DB                int           // 0-15, default 0
    DialTimeout       time.Duration // default 5s
    WriteTimeout      time.Duration // default 3s
    ConnRetries       int           // default 0 (no retries)
    ConnRetryInterval time.Duration // default 2s; doubles each attempt
}

func DefaultConfig() *Config
func (c *Config) Validate() error  // rejects empty Addr
```

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

### Memory backend (dev/test)

```go
app := gas.NewApp(
    gas.WithSingletonService[*cachemem.Service](cachemem.New()),
)
```

### Valkey backend (production)

```go
app := gas.NewApp(
    gas.WithSingletonService[*cachevk.Service](cachevk.New()),
)
```

### With explicit config

```go
app := gas.NewApp(
    gas.WithSingletonService[*cachevk.Service](
        cachevk.New(cachevk.WithConfig(&cachevk.Config{
            Cache: cachevk.Settings{
                Addr:              "valkey.internal:6379",
                Password:          "secret",
                DB:                1,
                ConnRetries:       3,
                ConnRetryInterval: 2 * time.Second,
            },
        })),
    ),
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

func (s *Service) Init() error {
    // use s.cache.Get, Set, Delete, Exists
    return nil
}
```

### Swapping backends

Because both backends satisfy `gas.CacheProvider`, switching from memory to
Valkey requires only changing the service registration — no consumer code
changes:

```go
// Development
gas.WithSingletonService[*cachemem.Service](cachemem.New())

// Production
gas.WithSingletonService[*cachevk.Service](cachevk.New())
```

### Testing with MockCache

```go
mock := &cachetest.MockCache{}
mock.GetFn = func(ctx context.Context, key string) ([]byte, error) {
    return []byte("hello"), nil
}

// inject mock as gas.CacheProvider in tests
```
