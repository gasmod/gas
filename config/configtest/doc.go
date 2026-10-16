// Package configtest provides MockConfig, a mock of core's gas.ConfigProvider
// for tests. It shows that a caller reads configuration correctly, not that a
// configuration source works; gas/config and its providers have their own
// tests.
//
// # Role in the framework
//
// MockConfig has every gas.ConfigProvider method and satisfies the interface
// structurally. Each method records the call and delegates to its function
// field (GetFn, BindFn, FindFn, ...) when set; otherwise it returns zero
// values.
//
//	mock := &configtest.MockConfig{}
//	mock.GetFn = func(key string) any {
//	    if key == "database.host" {
//	        return "localhost"
//	    }
//	    return nil
//	}
//
// For a test that just needs working configuration with known values,
// NewMockConfigWithValues returns a MockConfig whose function fields delegate
// to a real *config.Config seeded with those values, so Get, Find and Bind
// keep their normal key and binding behavior while calls are still recorded.
//
// # Wiring, configuration and lifecycle
//
// A test registers the mock in place of the real configuration, for example
// as the gas.ConfigProvider instance. The package reads no configuration, has
// no lifecycle and starts no goroutines.
//
// # Invariants
//
// Recording and the CallCount and Reset methods are safe for concurrent use.
// The Calls field is not guarded for readers: read it directly only after
// the calls under test have finished, or use CallCount.
//
// SPDX-License-Identifier: MIT
package configtest
