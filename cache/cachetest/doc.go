// Package cachetest provides a mock implementation of gas.CacheProvider
// for use in tests. The mock records all calls and allows configuring
// per-method behavior via function fields.
//
//	mock := &cachetest.MockCache{}
//	mock.GetFn = func(ctx context.Context, key string) ([]byte, error) {
//	    return []byte("value"), nil
//	}
package cachetest
