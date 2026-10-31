---
name: gas-cache
description: >
  Reference documentation for the gas/cache Go package
  (github.com/gasmod/gas/cache), the caching service for the Gas ecosystem.
  Use this skill when writing, reviewing, or debugging Go code that uses
  gas/cache for key-value caching with in-memory or Valkey (Redis-compatible)
  backends. Covers the memory and valkey sub-packages, cachetest mock,
  gas.CacheProvider implementation, sentinel errors, DI wiring, configuration
  binding, background cleanup, TTL handling, max entries enforcement, connection
  retry with exponential backoff, and direct Valkey client access. Make sure to
  use this skill whenever working with caching in the Gas ecosystem, even if the
  user doesn't explicitly mention gas/cache; any code that imports
  gasmod/gas/cache or references gas.CacheProvider should trigger this skill.
---

# Gas Cache Package Reference

Cache service for the Gas ecosystem. Provides two `gas.CacheProvider`
implementations: an in-memory backend and a Valkey (Redis-compatible) backend.

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

## Contract (both backends)

- **Keys:** any non-empty string, compared byte for byte. The empty key is
  `ErrInvalidKey` from every method.
- **Values** are copied both ways. A nil value is stored as empty; Get of an
  empty value returns an empty, non-nil slice.
- **TTL:** `ttl > 0` expires after that time, rounded up to a whole
  millisecond. `ttl == 0` takes the store's `DefaultTTL` setting (0 means no
  expiry). A **negative TTL is `ErrInvalidTTL`** and stores nothing.
  `cache.EffectiveTTL(ttl, defaultTTL)` computes the rounded TTL.
- An expired value is a miss for Get and Exists. Delete of a missing key is
  not an error.
- A context that ended before the call returns its error, wrapped.
- **Lifecycle:** before the first successful Init every method but Init and
  Close returns `ErrNotInitialized`; Init on an open store returns
  `ErrAlreadyInitialized`; after Close, until Init succeeds again,
  `ErrClosed`. Close on a store that is not initialized returns nil, and
  Close then Init restarts the store.
- A method checks the key, then the TTL, then the state, then the context.

## Sentinel Errors

The root `cache` package defines (test with `errors.Is`):

```go
cache.ErrKeyNotFound        // no live value under the key
cache.ErrInvalidKey         // empty key
cache.ErrInvalidTTL         // negative TTL
cache.ErrNotInitialized     // used before the first successful Init
cache.ErrAlreadyInitialized // Init on an open store
cache.ErrClosed             // used after Close, or Close ran during the call
cache.ErrNilLogger          // constructor got a nil logger
cache.ErrNilConfig          // WithConfig(nil)
cache.ErrNoConfigProvider   // no WithConfig and a nil ConfigProvider
cache.ErrInvalidConfig      // config failed to bind, validate, or a store rule
```

Checks: `cache.CheckKey(key)`, `cache.CheckTTL(ttl)`. Every store error starts
with the service name and the operation, such as
`gas/cache/memory: get: key not found`. No error or log holds a key, value,
address, user name or password.

## In-Memory Backend

### Constructor

```go
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Service, error)
```

A nil logger is `cache.ErrNilLogger`; a nil ConfigProvider is accepted, and
Init then requires `WithConfig`.

### Options

| Option                    | Description                                                    |
|---------------------------|----------------------------------------------------------------|
| `WithConfig(cfg *Config)` | Use this config (a copy, checked against the same tags) instead of binding |

### Lifecycle (gas.Service)

| Method  | Description                                                                 |
|---------|-----------------------------------------------------------------------------|
| `Name`  | Returns `cachemem.ServiceName`, `"gas/cache/memory"`                        |
| `Init`  | Resolves config, opens an empty store, starts the janitor, logs `service initialized` |
| `Close` | Drops every entry, stops the janitor and waits for it; ignores its context  |
| `Len`   | Entries held, expired ones not yet swept included; 0 when not open          |

### Behavior

- **Eviction:** entries are kept in order of use; Get and Set mark an entry
  used, Exists and Delete do not. When `MaxEntries` is set and a Set adds a
  new key to a full store, it first removes the entry that expired first,
  else the least recently used one. **A full store never refuses a Set.**
  Replacing a key's value never evicts.
- **Expiry** is counted on the monotonic clock. Get and Exists remove an
  expired entry they see; the janitor sweeps every `CleanupInterval`.
- With the defaults there is no limit: set `MaxEntries`, `DefaultTTL` or
  both for keys that come from requests.
- No I/O: a call never waits.

### Config

Section `cache`; keys `cache.maxentries`, `cache.cleanupinterval`,
`cache.defaultttl`.

```go
type Config struct {
    Cache Settings
}

type Settings struct {
    MaxEntries      int           `validate:"min=0"` // 0 = no limit (DefaultMaxEntries)
    CleanupInterval time.Duration `validate:"min=1s"` // default 1m; at least MinCleanupInterval (1s)
    DefaultTTL      time.Duration `validate:"min=0"` // default 0 (no expiry)
}

func DefaultConfig() *Config
```

A `CleanupInterval` of 0 is invalid.

## Valkey Backend

### Constructor

```go
func New(opts ...Option) func(gas.ConfigProvider, gas.Logger) (*Service, error)
```

### Options

| Option                         | Description                                                    |
|--------------------------------|----------------------------------------------------------------|
| `WithConfig(cfg *Config)`      | Use this config (a copy, checked against the same tags) instead of binding |
| `WithTLSConfig(cfg *tls.Config)` | Turns TLS on with this config (cloned shallowly each Init; do not change it after); replaces `tls` and `tlsservername` |

### Lifecycle (gas.Service)

| Method  | Description                                                                  |
|---------|------------------------------------------------------------------------------|
| `Name`  | Returns `cachevk.ServiceName`, `"gas/cache/valkey"`                          |
| `Init`  | Resolves config, connects with retries, PINGs, logs `service initialized` with `db` and `tls`. A failed Init closes what it built |
| `Close` | Closes the client (waits for commands in flight); ignores its context. Close during Init cancels it, and Init fails with `ErrClosed` |

### Behavior

- **Single server:** one standalone server at `addr`; cluster and sentinel
  are not supported.
- **Keys** are sent as `keyprefix` + key, with nothing in between. Stores
  sharing a server and database stay apart only when no prefix is a prefix
  of another.
- **Connecting:** each attempt dials, authenticates, selects `db` and PINGs,
  bounded by Init's context and `dialtimeout` (and each step by
  `optimeout`). A failed attempt is retried up to `connretries` times,
  waiting `connretryinterval` and doubling up to `MaxConnRetryInterval`
  (30s), logging `valkey connection failed, retrying` at warn. When attempts
  run out, Init fails with `cachevk.ErrConnect` and a reason:
  `authentication failed`, `timed out`, `connection refused`, `unreachable`
  or `failed`. valkey-go's error is not kept (it holds the address).
- **Timeouts:** every command runs within its context and `optimeout`. A
  connection-level failure or server error reply wraps `cachevk.ErrCommand`;
  a command's own ended context returns the context's error without
  ErrCommand. Server reply text is kept only for codes that hold no key
  (`WRONGTYPE`, `OOM`, `READONLY`, `LOADING`, `MASTERDOWN`, `MISCONF`,
  `NOREPLICAS`); other replies are cut to their code.
- **TLS:** with `tls` on, TLS 1.2+ against the system roots, checking
  `tlsservername` or the host of `addr`. Without TLS the password and values
  cross the network in plain text.

### Health and Readiness

| Method        | Behavior                                                                 |
|---------------|--------------------------------------------------------------------------|
| `CheckHealth` | No I/O: fails only when the store is not initialized (valkey-go reconnects by itself) |
| `CheckReady`  | Sends `PING` within its context and `optimeout`; errors as a command's, after `ping: ` |

The in-memory backend implements neither interface.

### Direct Client Access

```go
func (s *Service) Client() valkey.Client
```

Returns the valkey-go client, or nil when the store is not initialized.
Commands sent on it are **not** prefixed with `keyprefix`, are bounded only by
their own context, and return valkey-go's errors as they are (which can hold
the address). The store closes the client on Close. Type-assert through a
local interface:

```go
type ValkeyProvider interface {
    Client() valkey.Client
}
```

### Config

Section `cache`; keys are the lowercased field names (`cache.addr`,
`cache.keyprefix`, `cache.optimeout`, ...).

```go
type Config struct {
    Cache Settings
}

type Settings struct {
    Addr              string        `validate:"required"` // host:port; no default
    Username          string        // ACL user; empty = default user
    Password          Secret        // prints, marshals and logs as [redacted]
    KeyPrefix         string        // prepended to every key; default none
    TLSServerName     string        // requires TLS (unless WithTLSConfig)
    DefaultTTL        time.Duration `validate:"min=0"`        // default 0 (no expiry)
    DialTimeout       time.Duration `validate:"gt=0"`         // default 5s
    OpTimeout         time.Duration `validate:"gt=0"`         // default 3s
    ConnRetryInterval time.Duration // default 2s; 100ms to 30s when ConnRetries > 0
    DB                int           `validate:"min=0"`        // default 0
    ConnRetries       int           `validate:"min=0,max=10"` // default 0
    TLS               bool          // default false
}

func DefaultConfig() *Config
```

`Secret` is a `string` type: configuration binds into it as a string, and
`string(s)` is the value. Without `WithConfig`, Init binds `DefaultConfig()`
from the ConfigProvider; with it, Init copies the given Config and checks the
same tags, so start from `DefaultConfig()`. The `WriteTimeout` setting is
gone; use `OpTimeout`.

## Test Mock

```go
type MockCache struct {
    GetFn         func(ctx context.Context, key string) ([]byte, error)
    SetFn         func(ctx context.Context, key string, value []byte, ttl time.Duration) error
    DeleteFn      func(ctx context.Context, key string) error
    ExistsFn      func(ctx context.Context, key string) (bool, error)
    CheckHealthFn func(ctx context.Context) error
    CheckReadyFn  func(ctx context.Context) error
}
```

Implements `gas.CacheProvider`, `gas.HealthReporter`, and `gas.ReadyReporter`.
With a field nil, the method behaves as an empty cache: Get returns an error
wrapping `cache.ErrKeyNotFound`, Exists returns false, the rest return nil.
Each call is recorded (arguments after the context; Set's value copied).

| Method                  | Description                               |
|-------------------------|-------------------------------------------|
| `Calls() []Call`        | Copy of the recorded calls, in order      |
| `CallCount(method) int` | Count calls by method name (e.g. `"Get"`) |
| `Reset()`               | Forget all recorded calls                 |

Methods may run from several goroutines; setting an `Fn` field while a
method runs is a data race.

## DI Wiring Patterns

Register the backend under `gas.CacheProvider`: the container resolves a
dependency by its exact registered type, so a registration under
`*cachemem.Service` does not satisfy a constructor that takes
`gas.CacheProvider`. Both constructors also need a registered
`gas.ConfigProvider` (unless `WithConfig` is given) and `gas.Logger`.

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
cfg.Cache.Password = cachevk.Secret(os.Getenv("VALKEY_PASSWORD"))
cfg.Cache.KeyPrefix = "app:"
cfg.Cache.DB = 1
cfg.Cache.ConnRetries = 3

app := gas.NewApp(
    gas.WithSingletonService[gas.CacheProvider](cachevk.New(cachevk.WithConfig(cfg))),
)
```

### Consuming via gas.CacheProvider

```go
type Service struct {
    cache gas.CacheProvider
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

Both backends satisfy `gas.CacheProvider`, so switching changes only the
registration:

```go
// Development
gas.WithSingletonService[gas.CacheProvider](cachemem.New())

// Production
gas.WithSingletonService[gas.CacheProvider](cachevk.New())
```

### Testing with MockCache

```go
mock := &cachetest.MockCache{
    GetFn: func(ctx context.Context, key string) ([]byte, error) {
        return []byte("hello"), nil
    },
}
// inject mock as gas.CacheProvider; assert with mock.CallCount("Get")
```
