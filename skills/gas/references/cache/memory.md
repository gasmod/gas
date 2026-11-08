# github.com/gasmod/gas/cache/memory

```go
import "github.com/gasmod/gas/cache/memory"
```

Package memory is a cache held in the process's memory, as a
gas.CacheProvider. It is local to one process and lost when the process
stops, so it suits development, tests and single-instance apps.

`Service` implements gas.Service and gas.CacheProvider, and keeps the
contract of the cache package.

## Usage

In an app, the container builds the store from `New` and calls its
Init. By hand, with a configuration given in code:

```go
cfg := memory.DefaultConfig()
cfg.Cache.MaxEntries = 10000

c, err := memory.New(memory.WithConfig(cfg))(nil, logger)
if err != nil {
	return err
}
if err := c.Init(ctx); err != nil {
	return err
}
defer c.Close(ctx)

err = c.Set(ctx, "greeting", []byte("hello"), time.Minute)
```

After this c.Get(ctx, "greeting") returns `hello` for a minute, and
c.Len() is 1.

## Configuration

The settings live in the `cache` section, in `Settings`:

- `cache.maxentries`: the most entries the store holds, 0 or more; 0
is no limit. Default: `DefaultMaxEntries`, no limit.
- `cache.cleanupinterval`: how often the janitor removes expired
entries, at least `MinCleanupInterval` (1s). Default:
`DefaultCleanupInterval` (1m).
- `cache.defaultttl`: the TTL of a Set with a zero TTL, 0 or more; 0
is no expiry. Default: `DefaultTTL`, no expiry.

With the defaults the store has no limit: entries without a TTL stay
until deleted, and the store grows with the keys it is given. Set
MaxEntries, DefaultTTL, or both, for keys that come from requests.

`Service.Init` resolves the configuration each time it runs, in one of
two ways:

- Without `WithConfig`, it binds `DefaultConfig` from the
gas.ConfigProvider, which checks the `validate` tags. A nil
ConfigProvider is the error cache.ErrNoConfigProvider.
- With WithConfig, it takes a copy of the given Config, never changing
the caller's struct, and checks the same tags. A nil Config is the
error cache.ErrNilConfig.

A failure wraps cache.ErrInvalidConfig. The resolved configuration
belongs to the entries Init opens, so a restart with another
configuration is safe while calls run.

`Service.Config` returns a copy of the configuration the last successful
Init resolved, defaults included, or the zero Config before one. A
failed Init and Close leave it as it was.

## Eviction

The store keeps its entries in order of use. Get and Set make an entry
the most recently used; Exists and Delete do not.

When MaxEntries is set and a Set adds a new key to a full store, the
store first removes one entry:

- the one that expired first, if any has expired;
- else the least recently used.

Replacing a key's value never evicts. A full store never refuses a Set.

## Expiry

A TTL is counted from the Set, on the monotonic clock, so a change of
the wall clock does not move it. Expired entries are removed in two
ways:

- Get and Exists treat an expired entry as a miss and remove it.
- The janitor, a goroutine that each Init starts, removes every
expired entry each `cleanupinterval`, holding the store's lock while
it does.

An expired entry that nothing reads is held until the next sweep, or
until eviction picks it, as under Eviction. `Service.Len` counts it
until then.

## Lifecycle

A successful Init opens an empty store, starts the janitor and logs
`service initialized` at info level, with `max_entries`,
`cleanup_interval` and `default_ttl`. Init on an initialized store
returns cache.ErrAlreadyInitialized and changes nothing.

`Service.Close` runs these steps, in order:

1. It drops every entry.
2. It stops the janitor, and waits for it to return.
3. It logs `service closed` at info level.

Close on a store that is not initialized returns nil and logs nothing.
Close ignores its context. Init after Close starts again with an empty
store and a configuration resolved afresh.

Outside a successful Init, every method but Init, Close, Name, Len and
Config returns an error wrapping:

- cache.ErrNotInitialized, before the first successful Init.
- cache.ErrClosed, after Close, until Init succeeds.

Len returns 0 then.

`New`'s constructor fails with cache.ErrNilLogger for a nil logger. A
nil ConfigProvider is accepted, and Init then requires WithConfig.

## Context

The store does no I/O, so a call never waits: each method returns the
context's error when the context has ended before the call, and runs to
the end otherwise.

## Concurrency

A Service is safe for concurrent use. One mutex guards the entries and
the state:

- Each call holds it for the whole call, which is short and does no
I/O.
- The janitor holds it for each sweep.
- Close releases it before waiting for the janitor.

A call that runs beside Close either completes before it or returns
cache.ErrClosed.

## Errors

Every error starts with `ServiceName` and the operation, and wraps a
sentinel of the cache package, as the cache package describes.

A configuration that fails its tags gets the validator's error after
`gas/cache/memory: init: invalid config: validate config: `, such as:

```go
Key: 'Config.Cache.CleanupInterval' Error:Field validation for 'CleanupInterval' failed on the 'min' tag
```

The other errors, as returned, with `get` standing for each operation
that can fail the same way:

```go
gas/cache/memory: logger is nil
gas/cache/memory: init: config is nil
gas/cache/memory: init: no config provider
gas/cache/memory: init: service already initialized
gas/cache/memory: init: context canceled
gas/cache/memory: get: invalid key: empty
gas/cache/memory: get: key not found
gas/cache/memory: get: service not initialized
gas/cache/memory: get: service closed
gas/cache/memory: get: context canceled
gas/cache/memory: set: invalid TTL: negative
```

## API

Run `go doc -all github.com/gasmod/gas/cache/memory` for every declaration, or `go doc github.com/gasmod/gas/cache/memory.<Name>` for one.
