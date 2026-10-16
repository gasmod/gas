// Package queuetest provides MockQueue, a mock of core's gas.JobQueueProvider
// for tests. It shows that a caller is wired to enqueue and consume jobs
// correctly, not that a backend works; the sqs backend has its own tests.
//
// # Role in the framework
//
// MockQueue implements gas.JobQueueProvider. Each method records the call
// and delegates to its function field (EnqueueFn, DequeueFn, AckFn, NackFn)
// when set; otherwise it returns zero values (a nil error, and no jobs from
// Dequeue).
//
//	mock := &queuetest.MockQueue{}
//	mock.EnqueueFn = func(ctx context.Context, queue string, payload []byte, opts ...gas.EnqueueOption) error {
//	    return nil
//	}
//
// # Wiring, configuration and lifecycle
//
// A test passes the mock wherever a gas.JobQueueProvider is expected, in
// place of a real backend. The package reads no configuration, has no
// lifecycle and starts no goroutines.
//
// # Invariants
//
// Recording and the CallCount and Reset methods are safe for concurrent
// use. The Calls field is not guarded for readers: read it directly only
// after the calls under test have finished, or use CallCount.
//
// SPDX-License-Identifier: MIT
package queuetest
