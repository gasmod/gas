// Package cache is the caching module for Gas apps. Its backends implement
// the core's gas.CacheProvider (Get, Set, Delete, Exists over []byte
// values with a TTL), so services depend on that interface and the backend
// is chosen where the app is wired. The backends live in sub-packages:
// memory keeps entries in process, and valkey talks to a Valkey or other
// Redis-compatible server. cachetest provides a recording mock of
// gas.CacheProvider.
//
// This package holds what the backends share: the sentinel errors
// ErrKeyNotFound, returned by Get for a key that is missing or expired, and
// ErrClosed, returned by every operation on a backend that has not started
// or has been stopped.
//
// Guides with usage examples are at https://gasmod.github.io/gas.
//
// SPDX-License-Identifier: MIT
package cache
