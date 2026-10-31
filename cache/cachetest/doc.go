// Package cachetest holds a mock of gas.CacheProvider, [MockCache], for
// tests of code that uses a cache. It also implements gas.HealthReporter
// and gas.ReadyReporter.
//
// # Usage
//
// The mock records each call, and returns what the function in the field
// named after the method returns:
//
//	m := &cachetest.MockCache{
//		GetFn: func(context.Context, string) ([]byte, error) {
//			return []byte("hello"), nil
//		},
//	}
//	b, err := m.Get(ctx, "greeting")
//	// b is hello, err is nil, and m.CallCount("Get") is 1
//
// # Defaults
//
// With a field nil, its method behaves as an empty cache that accepts
// everything: Get returns an error wrapping cache.ErrKeyNotFound,
// `cachetest: get: key not found`; Exists returns false and nil; Set,
// Delete, CheckHealth and CheckReady return nil.
//
// # Calls
//
// Each method appends a [Call] with its name and its arguments after the
// context, before it calls its field: the key for Get, Delete and Exists;
// the key, the value and the TTL for Set; none for CheckHealth and
// CheckReady. The value is recorded as a copy, nil for nil, so that a
// caller that reuses its slice does not change a recorded call. Calls
// returns a copy of the calls in the order they were made, CallCount
// counts those of one method, and Reset forgets them all.
//
// The methods may be called from several goroutines at once, as the calls
// are kept under a lock, which Calls, CallCount and Reset take too.
// Setting a field while a method runs is a data race.
package cachetest
