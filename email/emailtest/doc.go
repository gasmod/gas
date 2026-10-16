// Package emailtest provides MockEmail, a mock of core's gas.EmailProvider
// for tests. It shows that a caller is wired to send email correctly, not
// that a backend works; the ses backend has its own tests.
//
// # Role in the framework
//
// MockEmail implements gas.EmailProvider and gas.ReadyReporter. Each method
// records the call and delegates to its function field (SendFn,
// SendFromTemplateFn, CheckReadyFn) when set; otherwise it returns nil.
//
//	mock := &emailtest.MockEmail{}
//	mock.SendFn = func(ctx context.Context, msg *gas.Email) error {
//	    return nil
//	}
//
// # Wiring, configuration and lifecycle
//
// A test passes the mock wherever a gas.EmailProvider is expected, in place
// of a real backend. The package reads no configuration, has no lifecycle
// and starts no goroutines.
//
// # Invariants
//
// Recording and the CallCount and Reset methods are safe for concurrent
// use. The Calls field is not guarded for readers: read it directly only
// after the calls under test have finished, or use CallCount.
//
// SPDX-License-Identifier: MIT
package emailtest
