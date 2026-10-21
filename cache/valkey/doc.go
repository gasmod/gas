// Package valkey is the gas.CacheProvider backend for a Valkey or other
// Redis-compatible server, built on the valkey-go client. Its Service also
// implements gas.Service, gas.HealthReporter, and gas.ReadyReporter, and
// Client exposes the underlying valkey.Client for commands the
// gas.CacheProvider interface does not cover.
//
// # Wiring
//
// New returns a DI constructor that takes a gas.ConfigProvider and a
// gas.Logger and returns *Service and an error, which is always nil; register
// it under gas.CacheProvider. WithConfig supplies the configuration directly
// and skips binding it from the ConfigProvider.
//
// # Configuration
//
// Without WithConfig, the service binds a Config from the ConfigProvider
// over DefaultConfig when it starts. The cache settings are Addr (default
// "localhost:6379"), Password (empty for no authentication), DB (default
// 0), DialTimeout (default five seconds, also the bound on the connection
// ping), WriteTimeout (default three seconds), ConnRetries (default 0), and
// ConnRetryInterval (default two seconds). When the config is bound from the
// ConfigProvider, its validate tags require a non-empty Addr and a DB of 0 or
// more; a config given with WithConfig is used as given.
//
// # Lifecycle
//
// Starting the service creates a client and pings the server. If that
// fails and ConnRetries is positive, it retries up to ConnRetries times,
// sleeping ConnRetryInterval before the first retry and doubling the wait
// each time (an interval of 0 or less uses two seconds); starting fails
// once the retries run out. The ping runs under the start context, and when
// that context ends during a wait the retries stop and starting fails with
// an error wrapping both the context's error and the last connection error.
// Stopping the service closes the client and makes every operation return
// cache.ErrClosed, as does every operation before the service has started.
// An operation that is running when the service stops, or when a restart
// replaces the client, also returns cache.ErrClosed. The client is held in
// an atomic pointer, so operations can run while the service restarts.
//
// # Behavior
//
// Get returns cache.ErrKeyNotFound for a missing key. Set with a positive
// TTL sets a millisecond expiry on the server; a TTL of 0 or less stores the
// key without one. Errors from the server are wrapped with the service name
// and the key. CheckHealth fails only before the service has started or
// once it has stopped, since the client reconnects on its own; CheckReady
// pings the server with the caller's context. Client returns nil before the
// service first connects, and the closed client after it stops.
//
// SPDX-License-Identifier: MIT
package valkey
