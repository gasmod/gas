// Package valkey is the gas.CacheProvider backend for a Valkey or other
// Redis-compatible server, built on the valkey-go client. Its Service also
// implements gas.Service, gas.HealthReporter, and gas.ReadyReporter, and
// Client exposes the underlying valkey.Client for commands the
// gas.CacheProvider interface does not cover.
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
// over DefaultConfig when it starts. The cache settings are Addr (default
// "localhost:6379"), Password (empty for no authentication), DB (default
// 0), DialTimeout (default five seconds, also the bound on the connection
// ping), WriteTimeout (default three seconds), ConnRetries (default 0), and
// ConnRetryInterval (default two seconds). Validate requires a non-empty
// Addr and a DB of 0 or more.
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
// cache.ErrClosed.
//
// # Behavior
//
// Get returns cache.ErrKeyNotFound for a missing key. Set with a positive
// TTL sets a millisecond expiry on the server; a TTL of 0 or less stores the
// key without one. Errors from the server are wrapped with the service name
// and the key. CheckHealth fails only once the service has stopped, since
// the client reconnects on its own; CheckReady pings the server with the
// caller's context. Get, Set, Delete, Exists, and CheckReady panic on a
// service that has not started, because the client does not exist yet
// (KI-017).
//
// SPDX-License-Identifier: MIT
package valkey
