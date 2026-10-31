package cachetest

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/cache"
)

// mockName is the prefix of the errors the mock makes itself.
const mockName = "cachetest"

// Call is one call of the mock's method.
type Call struct {
	// Method is the name of the method called.
	Method string
	// Args are the method's arguments after the context, in order.
	Args []any
}

// MockCache is a gas.CacheProvider, gas.HealthReporter and
// gas.ReadyReporter that records its calls, as described in the package
// documentation.
type MockCache struct {
	// GetFn is what Get returns. When it is nil, Get returns an error
	// wrapping cache.ErrKeyNotFound.
	GetFn func(ctx context.Context, key string) ([]byte, error)
	// SetFn is what Set returns. When it is nil, Set returns nil.
	SetFn func(ctx context.Context, key string, value []byte, ttl time.Duration) error
	// DeleteFn is what Delete returns. When it is nil, Delete returns nil.
	DeleteFn func(ctx context.Context, key string) error
	// ExistsFn is what Exists returns. When it is nil, Exists returns
	// false and nil.
	ExistsFn func(ctx context.Context, key string) (bool, error)
	// CheckHealthFn is what CheckHealth returns. When it is nil,
	// CheckHealth returns nil.
	CheckHealthFn func(ctx context.Context) error
	// CheckReadyFn is what CheckReady returns. When it is nil, CheckReady
	// returns nil.
	CheckReadyFn func(ctx context.Context) error

	// calls holds the calls in the order they were made.
	calls []Call
	// mu guards calls, since the mock may be called from several
	// goroutines.
	mu sync.Mutex
}

var (
	_ gas.CacheProvider  = (*MockCache)(nil)
	_ gas.HealthReporter = (*MockCache)(nil)
	_ gas.ReadyReporter  = (*MockCache)(nil)
)

// Get records the call and returns GetFn's result.
func (m *MockCache) Get(ctx context.Context, key string) ([]byte, error) {
	m.record("Get", key)

	if m.GetFn != nil {
		return m.GetFn(ctx, key)
	}

	return nil, fmt.Errorf("%s: get: %w", mockName, cache.ErrKeyNotFound)
}

// Set records the call and returns SetFn's result.
func (m *MockCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	m.record("Set", key, bytes.Clone(value), ttl)

	if m.SetFn != nil {
		return m.SetFn(ctx, key, value, ttl)
	}

	return nil
}

// Delete records the call and returns DeleteFn's result.
func (m *MockCache) Delete(ctx context.Context, key string) error {
	m.record("Delete", key)

	if m.DeleteFn != nil {
		return m.DeleteFn(ctx, key)
	}

	return nil
}

// Exists records the call and returns ExistsFn's result.
func (m *MockCache) Exists(ctx context.Context, key string) (bool, error) {
	m.record("Exists", key)

	if m.ExistsFn != nil {
		return m.ExistsFn(ctx, key)
	}

	return false, nil
}

// CheckHealth records the call and returns CheckHealthFn's result.
func (m *MockCache) CheckHealth(ctx context.Context) error {
	m.record("CheckHealth")

	if m.CheckHealthFn != nil {
		return m.CheckHealthFn(ctx)
	}

	return nil
}

// CheckReady records the call and returns CheckReadyFn's result.
func (m *MockCache) CheckReady(ctx context.Context) error {
	m.record("CheckReady")

	if m.CheckReadyFn != nil {
		return m.CheckReadyFn(ctx)
	}

	return nil
}

// Calls returns a copy of the calls, in the order they were made.
func (m *MockCache) Calls() []Call {
	m.mu.Lock()
	defer m.mu.Unlock()

	return slices.Clone(m.calls)
}

// CallCount returns how many calls were made to method.
func (m *MockCache) CallCount(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	n := 0

	for _, c := range m.calls {
		if c.Method == method {
			n++
		}
	}

	return n
}

// Reset forgets every call.
func (m *MockCache) Reset() {
	m.mu.Lock()

	m.calls = nil
	m.mu.Unlock()
}

// record appends a call.
func (m *MockCache) record(method string, args ...any) {
	m.mu.Lock()

	m.calls = append(m.calls, Call{Method: method, Args: args})
	m.mu.Unlock()
}
