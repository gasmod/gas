// Package conformance holds the tests every store of the module runs: the
// contract of the cache package, checked with identical expectations
// against a store built by the caller.
//
// # Usage
//
// A store's tests call [Run] with a [Subject] that builds a new,
// uninitialized store with the DefaultTTL the suite asks for:
//
//	conformance.Run(t, conformance.Subject{
//		Name: memory.ServiceName,
//		New: func(t *testing.T, defaultTTL time.Duration) conformance.Store {
//			cfg := memory.DefaultConfig()
//			cfg.Cache.DefaultTTL = defaultTTL
//			s, err := memory.New(memory.WithConfig(cfg))(nil, &gas.NopLogger{})
//			require.NoError(t, err)
//			return s
//		},
//	})
//
// A store that shares a server between tests gives each store a key
// space of its own, such as a key prefix unique to the test.
//
// # Suite
//
// Run runs each rule as a parallel subtest, on a new store each: misses,
// a round trip, replacing, the empty key, keys of any bytes, copies in
// and out, nil and empty values, binary values, expiry, TTLs under a
// millisecond, negative TTLs, DefaultTTL, a zero TTL clearing a TTL, an
// ended context, the lifecycle sentinels and a restart, Init with an
// ended context, concurrent calls for -race, and errors that never quote
// a key. Every error is checked with errors.Is and its full text, which
// starts with the Subject's Name.
package conformance
