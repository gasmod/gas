// Package cache defines what every cache store of the module promises:
// the keys it accepts, how it copies values, how a TTL works, how it
// honors a context, its lifecycle, and the errors it returns.
//
// The stores implement gas.CacheProvider and gas.Service, and each lives
// in its own package:
//
//   - gas/cache/memory: a map in the process's memory, with an entry
//     limit and least-recently-used eviction.
//   - gas/cache/valkey: a Valkey server, or any server that speaks its
//     protocol, such as Redis.
//   - gas/cache/cachetest: a mock for tests.
//
// # Usage
//
// Each store's New returns a constructor in the shape the gas container
// expects, so a store is registered as gas.CacheProvider:
//
//	gas.WithSingletonService[gas.CacheProvider](memory.New())
//
// A handler then reads through the cache:
//
//	b, err := c.Get(ctx, "user:42")
//	if errors.Is(err, cache.ErrKeyNotFound) {
//		b = loadUser(ctx, 42)
//		err = c.Set(ctx, "user:42", b, 5*time.Minute)
//	}
//	if err != nil {
//		return err
//	}
//
// After this the cache holds `user:42` for five minutes, unless a store
// evicts it first.
//
// # Contract
//
// Every store, and any gas.CacheProvider that wants to be one, keeps the
// rules of the sections Keys and values, TTL, Context and Lifecycle. The
// module's conformance tests run every store through these rules, with
// identical expectations.
//
// A method checks its arguments first, the key and then the TTL, then the
// store's state, then the context, and returns the first failure.
//
// # Keys and values
//
// The methods read, write and remove values by key:
//
//   - Get returns the value of a key, or an error wrapping
//     [ErrKeyNotFound] when the store holds no live value under it.
//   - Exists reports whether Get would find one.
//   - Delete removes a key. Deleting a key the store does not hold is not
//     an error.
//
// A key is any non-empty string, of any bytes; keys are compared byte for
// byte. The empty key is [ErrInvalidKey], from every method, as
// [CheckKey] reports it.
//
// Values are bytes and are copied: Set keeps a copy of what it is given,
// and Get returns a copy of what the store keeps, so the caller may change
// either slice afterwards. A nil value is stored as empty, and Get returns
// an empty value as an empty, non-nil slice.
//
// # TTL
//
// Set replaces the value of a key and its TTL. The TTL works like this:
//
//   - A TTL above zero makes the value expire once that time has passed,
//     rounded up to a whole millisecond, so `500µs` lives a millisecond,
//     as Valkey counts TTLs. [EffectiveTTL] computes the rounded TTL.
//   - A TTL of zero takes the store's DefaultTTL setting, where zero means
//     no expiry.
//   - A negative TTL is [ErrInvalidTTL], as [CheckTTL] reports it, and
//     stores nothing.
//
// A TTL within a millisecond of the largest time.Duration is rounded down
// to a whole millisecond instead, since rounding up would overflow.
//
// An expired value is a miss for Get and for Exists, whether or not the
// store has removed it yet.
//
// # Context
//
// Every method returns the context's error, wrapped, when its context has
// ended before it starts. A store that does I/O honors the context during
// the call too. Close is the exception: it always closes.
//
// # Lifecycle
//
// A store is initialized with Init and closed with Close:
//
//   - Before its first successful Init, every method but Init and Close
//     returns [ErrNotInitialized].
//   - Init on an initialized store returns [ErrAlreadyInitialized] and
//     changes nothing.
//   - After Close, until Init succeeds again, every method but Init and
//     Close returns [ErrClosed].
//   - Close on a store that is not initialized returns nil.
//
// Close then Init restarts the store. Close waits for the goroutines the
// store started.
//
// # Security
//
// Cache keys often carry user identifiers or session and token hashes,
// and values are application data. So the stores keep them out of what
// they report:
//
//   - No error the stores make holds a key or a value.
//   - No error or log holds a server's address, a user name or a
//     password.
//
// A store's errors name the operation instead.
//
// # Concurrency
//
// Every store is safe for concurrent use, as each store's package
// describes. The mock is too, except for setting its fields while it is
// called.
//
// # Errors
//
// Every error a store returns starts with its service name, such as
// `gas/cache/memory`, then the operation, such as `get`, then what
// failed. It wraps one of these sentinels, to be tested with [errors.Is]:
//
//   - [ErrKeyNotFound]: no live value under the key.
//   - [ErrInvalidKey]: the key is empty.
//   - [ErrInvalidTTL]: the TTL is negative.
//   - [ErrNotInitialized], [ErrAlreadyInitialized], [ErrClosed]: as under
//     Lifecycle.
//   - [ErrNilLogger]: a store's constructor got a nil logger.
//   - [ErrNilConfig], [ErrNoConfigProvider], [ErrInvalidConfig]: Init
//     could not resolve a valid configuration.
//
// The errors of this package's checks, as returned, before a store adds
// its service name and the operation:
//
//	invalid key: empty
//	invalid TTL: negative
package cache
