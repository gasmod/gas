// Package memory is the in-process gas.CacheProvider backend. Its Service
// keeps entries in a map guarded by a mutex, so the cache is local to one
// process and is lost when it stops. It also implements gas.Service.
//
// # Wiring
//
// New returns a DI constructor that takes a gas.ConfigProvider and a
// gas.Logger and returns *Service; register it under gas.CacheProvider. The
// constructor does not return an error, so it is not yet on the shape in
// CONVENTIONS.md (KI-004 in KNOWN_ISSUES.md). WithConfig supplies the
// configuration directly and skips binding it from the ConfigProvider.
//
// # Configuration
//
// Without WithConfig, the service binds a Config from the ConfigProvider
// over DefaultConfig when it starts. The cache settings are MaxEntries
// (default 0, unlimited), CleanupInterval (default one minute; 0 or less
// disables background cleanup), and DefaultTTL (default 0, no expiry).
// Validate accepts any values.
//
// # Lifecycle
//
// Starting the service binds the configuration, creates an empty map, and,
// when CleanupInterval is positive, starts a goroutine that removes expired
// entries on that interval until the service stops. Stopping it cancels
// that goroutine, drops every entry, and makes every operation return
// cache.ErrClosed. Starting it again begins with an empty cache. Before
// the service has started, every operation also returns cache.ErrClosed.
// Each operation checks this under the mutex, so a stop that races an
// operation makes it return cache.ErrClosed rather than touch the dropped
// map.
//
// # Behavior
//
// A Set with a TTL of 0 uses DefaultTTL; a positive TTL, from either
// source, sets an expiry, and a negative one stores the entry without
// expiry. Expired entries are invisible to Get and Exists, and Get deletes
// the expired entry it finds. When MaxEntries is reached, a Set of a new
// key removes one expired entry, chosen in map iteration order rather than
// by age, and fails with an error if there is none; overwriting an existing
// key always succeeds. Values are stored and returned
// as the same slice, not copies, so a caller that modifies a slice it
// passed to Set or got from Get changes the cached value.
//
// SPDX-License-Identifier: MIT
package memory
