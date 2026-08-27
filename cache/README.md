# gas/cache

[![Test](https://github.com/gasmod/gas/actions/workflows/test.yml/badge.svg)](https://github.com/gasmod/gas/actions/workflows/test.yml) [![Go Reference](https://pkg.go.dev/badge/github.com/gasmod/gas/cache.svg)](https://pkg.go.dev/github.com/gasmod/gas/cache) ![Go Version](https://img.shields.io/github/go-mod/go-version/gasmod/gas?filename=cache/go.mod) [![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Part of the [Gas](../README.md) monorepo · [All modules](../README.md#modules) · [Examples](../example/README.md)

Cache service for the [Gas](../README.md) framework. Provides two `gas.CacheProvider` implementations —
an in-memory backend for development and testing, and a Valkey (Redis-compatible) backend for production.

## Install

```bash
go get github.com/gasmod/gas/cache
```

Both backends' `New()` returns a DI constructor that takes `gas.ConfigProvider` and `gas.Logger`, so register both alongside the cache (see [gas/config](../config/README.md) and [gas/log](../log/README.md)).

## Backends

| Backend        | Package                              | Use case                                          |
|----------------|--------------------------------------|---------------------------------------------------|
| In-memory      | `github.com/gasmod/gas/cache/memory` | Development, testing, single-instance deployments |
| Valkey (Redis) | `github.com/gasmod/gas/cache/valkey` | Production, multi-instance deployments            |

Both backends implement `gas.Service` and `gas.CacheProvider`. The Valkey backend also
implements `gas.HealthReporter` and `gas.ReadyReporter` for Kubernetes-style liveness
and readiness probes.

## Usage

### In-memory backend

```go
package main

import (
	"log"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	cachemem "github.com/gasmod/gas/cache/memory"
	gaslog "github.com/gasmod/gas/log"
)

func main() {
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

		// Registered under gas.CacheProvider, the interface consumers inject.
		gas.WithSingletonService[gas.CacheProvider](cachemem.New()),
	)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
```

With custom configuration:

```go
cfg := cachemem.DefaultConfig()
cfg.Cache.MaxEntries = 10000
cfg.Cache.DefaultTTL = 5 * time.Minute
cfg.Cache.CleanupInterval = 2 * time.Minute

cachemem.New(cachemem.WithConfig(cfg))
```

### Valkey backend

```go
package main

import (
	"log"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/config/providers"
	cachevk "github.com/gasmod/gas/cache/valkey"
	gaslog "github.com/gasmod/gas/log"
)

func main() {
	cfg := config.New(config.WithProvider(providers.NewEnvProvider()))
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	app := gas.NewApp(
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),

		gas.WithSingletonService[gas.CacheProvider](cachevk.New()),
	)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
```

With custom configuration:

```go
cfg := cachevk.DefaultConfig()
cfg.Cache.Addr = "valkey.internal:6379"
cfg.Cache.Password = "secret"
cfg.Cache.DB = 1
cfg.Cache.ConnRetries = 3

cachevk.New(cachevk.WithConfig(cfg))
```

### Dependency injection

Services receive the cache through `gas.CacheProvider` via constructor injection:

```go
type Service struct {
	cache gas.CacheProvider
}

func New(cache gas.CacheProvider) *Service {
	return &Service{cache: cache}
}

func (s *Service) Init() error {
	ctx := context.Background()
	_ = s.cache.Set(ctx, "hello", []byte("world"), 5*time.Minute)
	return nil
}
```

### Direct Valkey client access

For advanced Valkey operations beyond the `CacheProvider` interface, type-assert to access the underlying client:

```go
type ValkeyProvider interface {
	Client() valkey.Client
}

func (s *Service) Init() error {
	if vp, ok := s.cache.(ValkeyProvider); ok {
		client := vp.Client()
		// use client for pub/sub, Lua scripts, etc.
	}
	return nil
}
```

## Config

If `WithConfig` is not provided, both backends automatically bind configuration from the `gas.ConfigProvider` injected
via DI. This lets you drive cache settings from environment variables or a config file without any explicit wiring.

### Memory config

| Field                   | Default | Description                                                   |
|-------------------------|---------|---------------------------------------------------------------|
| `Cache.MaxEntries`      | `0`     | Max entries (0 = unlimited)                                   |
| `Cache.CleanupInterval` | `1m`    | How often expired entries are evicted (0 = disabled)          |
| `Cache.DefaultTTL`      | `0`     | Default TTL when Set is called with ttl=0 (0 = never expires) |

### Valkey config

| Field                     | Default          | Description                                              |
|---------------------------|------------------|----------------------------------------------------------|
| `Cache.Addr`              | `localhost:6379` | Valkey server address (host:port)                        |
| `Cache.Password`          |                  | Authentication password (empty = no auth)                |
| `Cache.DB`                | `0`              | Database number (0-15)                                   |
| `Cache.DialTimeout`       | `5s`             | Timeout for establishing new connections                 |
| `Cache.WriteTimeout`      | `3s`             | Timeout for write operations and periodic PINGs          |
| `Cache.ConnRetries`       | `0`              | Number of connection retry attempts (0 = no retries)     |
| `Cache.ConnRetryInterval` | `2s`             | Base retry interval; doubles each attempt (exp. backoff) |

## Testing

The `cachetest` package provides a mock implementation of `gas.CacheProvider`:

```go
import "github.com/gasmod/gas/cache/cachetest"

mock := &cachetest.MockCache{}
mock.GetFn = func(ctx context.Context, key string) ([]byte, error) {
	return []byte("hello"), nil
}

// pass mock as gas.CacheProvider
// assert calls:
if mock.CallCount("Get") != 1 {
	t.Error("expected one Get call")
}
```

## Health and Readiness

The Valkey backend implements `gas.HealthReporter` and `gas.ReadyReporter`:

- `CheckHealth(ctx)` — liveness. Returns `cache.ErrClosed` once the service is closed and
  `nil` otherwise. The valkey-go client reconnects internally, so a transient network
  failure is *not* a liveness failure — a process restart would not help.
- `CheckReady(ctx)` — readiness. Issues a `PING` against the Valkey server using the
  caller's context. Returns an error while the dependency is unreachable so traffic
  can be drained until it recovers.

The in-memory backend intentionally does not implement these interfaces: it has no
external dependency and no warmup, so liveness and readiness track the service
lifecycle that the gas framework already manages.

## Sentinel Errors

The root `cache` package defines two sentinel errors used by both backends:

```go
cache.ErrKeyNotFound // returned by Get when the key does not exist or has expired
cache.ErrClosed      // returned when an operation is attempted on a closed service
```
